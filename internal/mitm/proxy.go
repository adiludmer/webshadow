package mitm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adiludmer/webshadow/internal/recording"
)

// Proxy is an explicit HTTP proxy that records every exchange it forwards.
// HTTPS arrives as CONNECT; the proxy terminates the browser's TLS with a
// leaf from CA and sends the decrypted request upstream over its own,
// normally verified, TLS connection. The MVP speaks HTTP/1.1 on both sides.
type Proxy struct {
	CA    *CA
	Store *recording.Store
	// Transport sends requests upstream. It must not decompress bodies or
	// skip certificate verification. Nil means NewTransport().
	Transport http.RoundTripper
	// Logf reports connection-level failures. It never receives headers or
	// bodies. Nil means no logging.
	Logf func(format string, args ...any)

	srv      *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	connSeq  atomic.Uint64
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
	inflight sync.WaitGroup
}

// NewTransport returns the upstream transport the proxy uses by default:
// HTTP/1.1 only, bodies passed through as they arrive, the system's trust
// roots, and any proxy named by the environment.
func NewTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		// The browser negotiates content coding itself; the recording keeps
		// the bytes as the origin sent them.
		DisableCompression: true,
		// A non-nil empty map turns off HTTP/2.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// Listen opens a listener on addr. With fallback, a busy addr falls back
// to a free port on the same host.
func Listen(addr string, fallback bool) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil || !fallback {
		return ln, err
	}
	host, _, serr := net.SplitHostPort(addr)
	if serr != nil {
		return nil, err
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

// Serve accepts browser connections on ln until Close.
func (p *Proxy) Serve(ln net.Listener) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return http.ErrServerClosed
	}
	if p.Transport == nil {
		p.Transport = NewTransport()
	}
	p.conns = map[net.Conn]struct{}{}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	srv := p.srv
	p.mu.Unlock()
	return srv.Serve(ln)
}

// Close stops accepting, closes every browser connection and waits until
// each in-flight exchange has been recorded.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	srv := p.srv
	for c := range p.conns {
		c.Close()
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	var err error
	if srv != nil {
		err = srv.Close()
	}
	p.inflight.Wait()
	if t, ok := p.Transport.(interface{ CloseIdleConnections() }); ok {
		t.CloseIdleConnections()
	}
	return err
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// ServeHTTP takes over the browser connection. Every request after the
// first is read by the proxy itself, so plain HTTP, tunnels and upgrades
// share one loop.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "webshadow: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	// The request body cannot be read after Hijack, so a plain HTTP body is
	// read first.
	if r.Method != http.MethodConnect && r.Body != nil && r.Body != http.NoBody {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "webshadow: reading request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		p.logf("proxy: hijack: %v", err)
		return
	}
	if !p.track(conn) {
		conn.Close()
		return
	}
	p.inflight.Add(1)
	defer p.inflight.Done()
	defer p.untrack(conn)
	defer conn.Close()

	cs := &connState{id: fmt.Sprintf("conn_%06d", p.connSeq.Add(1))}
	if r.Method == http.MethodConnect {
		p.serveTunnel(conn, rw.Reader, r.Host, cs)
		return
	}
	cs.scheme = "http"
	p.serveConn(conn, rw.Reader, r, started, cs)
}

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// connState is what the proxy knows about one browser connection.
type connState struct {
	id        string
	scheme    string // scheme of requests on this connection
	authority string // host:port from CONNECT, empty for plain HTTP
}

// serveTunnel answers a CONNECT and serves the requests inside it,
// decrypting them when the browser starts a TLS handshake.
func (p *Proxy) serveTunnel(conn net.Conn, br *bufio.Reader, authority string, cs *connState) {
	if _, _, err := net.SplitHostPort(authority); err != nil {
		authority = net.JoinHostPort(authority, "443")
	}
	cs.authority = authority
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] != 0x16 { // not a TLS handshake: plain HTTP through the tunnel
		cs.scheme = "http"
		p.serveConn(conn, br, nil, time.Time{}, cs)
		return
	}
	host, _, _ := net.SplitHostPort(authority)
	tconn := tls.Server(&bufferedConn{Conn: conn, r: br}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.CA.Leaf(name)
		},
	})
	tconn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := tconn.Handshake(); err != nil {
		// Typically a client that pins certificates or refuses our CA.
		p.logf("proxy: TLS handshake with browser for %s failed: %v", host, err)
		return
	}
	tconn.SetDeadline(time.Time{})
	cs.scheme = "https"
	p.serveConn(tconn, bufio.NewReader(tconn), nil, time.Time{}, cs)
}

// bufferedConn reads through the bufio.Reader that already holds the
// bytes peeked from the connection.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveConn handles requests on one browser connection until it closes.
// first is a request already read, if any.
func (p *Proxy) serveConn(conn net.Conn, br *bufio.Reader, first *http.Request, firstAt time.Time, cs *connState) {
	for {
		req, at := first, firstAt
		first = nil
		if req == nil {
			var err error
			req, err = http.ReadRequest(br)
			if err != nil {
				return
			}
			at = time.Now()
		}
		if !p.handle(conn, br, req, at, cs) {
			return
		}
	}
}

// Hop-by-hop headers belong to one connection and are not forwarded.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// handle forwards one request, writes the response to the browser and
// records the exchange. It reports whether the connection can carry
// another request.
func (p *Proxy) handle(conn net.Conn, br *bufio.Reader, req *http.Request, at time.Time, cs *connState) bool {
	clock := p.Store.Clock()
	ex := &recording.HTTPExchange{
		ConnectionID: cs.id,
		StartedAt:    recording.Stamp{T: clock.Since(at), Wall: at.UTC()},
		Timing:       recording.Timing{RequestStart: clock.Since(at)},
	}
	target, err := targetURL(req, cs)
	ex.Request = recording.Request{
		Method:   req.Method,
		Scheme:   cs.scheme,
		Protocol: req.Proto,
		Headers:  requestHeaders(req),
	}
	if err != nil {
		ex.Request.URL = req.RequestURI
		ex.Error = err.Error()
		p.finish(ex, nil, nil)
		writeError(conn, http.StatusBadRequest, err)
		return false
	}
	ex.Request.URL = target.String()
	ex.Request.Host = target.Hostname()
	ex.Request.Port = port(target)

	upgrade := isUpgrade(req.Header)
	out, reqBody, err := p.outgoing(req, target, upgrade)
	if err != nil {
		ex.Error = err.Error()
		p.finish(ex, nil, nil)
		writeError(conn, http.StatusBadGateway, err)
		return false
	}

	resp, err := p.Transport.RoundTrip(out)
	// Whatever the upstream did not read must not be taken for the next
	// request on this connection.
	if req.Body != nil {
		io.Copy(io.Discard, req.Body)
	}
	if err != nil {
		ex.Error = "upstream: " + err.Error()
		p.finish(ex, reqBody, nil)
		writeError(conn, http.StatusBadGateway, err)
		return false
	}
	ex.Timing.ResponseStart = clock.Now().T
	ex.Response = &recording.Response{
		Status:   resp.StatusCode,
		Protocol: resp.Proto,
		Headers:  sortedHeaders(resp.Header),
	}

	if resp.StatusCode == http.StatusSwitchingProtocols {
		return p.relayUpgrade(conn, br, ex, reqBody, resp)
	}

	respBody, err := p.Store.NewBody()
	if err != nil {
		resp.Body.Close()
		ex.Error = "record: " + err.Error()
		p.finish(ex, reqBody, nil)
		writeError(conn, http.StatusBadGateway, err)
		return false
	}
	upstream := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(upstream, respBody), upstream}
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	resp.Request = req
	keep := !req.Close && !resp.Close && req.ProtoAtLeast(1, 1)
	resp.Close = !keep
	// The browser connection is HTTP/1.1 whatever the origin spoke. A body
	// of unknown length is chunked so the connection can stay open.
	resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
	resp.TransferEncoding = nil
	if resp.ContentLength < 0 && keep && req.Method != http.MethodHead {
		resp.TransferEncoding = []string{"chunked"}
	}
	bw := bufio.NewWriter(conn)
	werr := resp.Write(bw)
	if werr == nil {
		werr = bw.Flush()
	}
	upstream.Close()
	ex.Timing.ResponseEnd = clock.Now().T
	if werr != nil {
		ex.Error = "browser: " + werr.Error()
		keep = false
	}
	p.finish(ex, reqBody, respBody)
	return keep
}

// outgoing builds the upstream request. The request body is recorded as
// the transport reads it.
func (p *Proxy) outgoing(req *http.Request, target *url.URL, upgrade bool) (*http.Request, *recording.BodyWriter, error) {
	var body io.Reader
	var rec *recording.BodyWriter
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		if rec, err = p.Store.NewBody(); err != nil {
			return nil, nil, err
		}
		body = io.TeeReader(req.Body, rec)
	}
	out, err := http.NewRequestWithContext(p.ctx, req.Method, target.String(), body)
	if err != nil {
		if rec != nil {
			rec.Abort()
		}
		return nil, nil, err
	}
	out.Header = req.Header.Clone()
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	if upgrade {
		out.Header.Set("Connection", "Upgrade")
		out.Header.Set("Upgrade", req.Header.Get("Upgrade"))
	}
	out.Host = req.Host
	out.ContentLength = req.ContentLength
	if body == nil {
		out.ContentLength = 0
	}
	return out, rec, nil
}

// relayUpgrade passes a 101 response to the browser, records the
// handshake, then copies bytes both ways without recording them. WebSocket
// frames are out of scope for the first recorder.
func (p *Proxy) relayUpgrade(conn net.Conn, br *bufio.Reader, ex *recording.HTTPExchange, reqBody *recording.BodyWriter, resp *http.Response) bool {
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		ex.Error = "upstream: 101 response without a connection"
		p.finish(ex, reqBody, nil)
		return false
	}
	defer upstream.Close()
	bw := bufio.NewWriter(conn)
	fmt.Fprintf(bw, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(bw)
	bw.WriteString("\r\n")
	err := bw.Flush()
	ex.Timing.ResponseEnd = p.Store.Clock().Now().T
	if err != nil {
		ex.Error = "browser: " + err.Error()
	}
	p.finish(ex, reqBody, nil)
	if err != nil {
		return false
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, br); done <- struct{}{} }()
	go func() { io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
	return false
}

// finish stores the bodies and appends the exchange.
func (p *Proxy) finish(ex *recording.HTTPExchange, reqBody, respBody *recording.BodyWriter) {
	ex.CompletedAt = p.Store.Clock().Now()
	var errs []error
	if reqBody != nil {
		b, err := reqBody.Close()
		errs = append(errs, err)
		if b != nil {
			b.ContentType = headerValue(ex.Request.Headers, "Content-Type")
			b.ContentEncoding = headerValue(ex.Request.Headers, "Content-Encoding")
			ex.Request.Body = b
		}
	}
	if respBody != nil {
		b, err := respBody.Close()
		errs = append(errs, err)
		if b != nil && ex.Response != nil {
			b.ContentType = headerValue(ex.Response.Headers, "Content-Type")
			b.ContentEncoding = headerValue(ex.Response.Headers, "Content-Encoding")
			ex.Response.Body = b
		}
	}
	if err := errors.Join(errs...); err != nil && ex.Error == "" {
		ex.Error = "record: " + err.Error()
	}
	if err := p.Store.AppendExchange(ex); err != nil {
		p.logf("proxy: record exchange: %v", err)
	}
}

// targetURL resolves the absolute URL a request is for.
func targetURL(req *http.Request, cs *connState) (*url.URL, error) {
	u := *req.URL
	if cs.authority == "" {
		if !u.IsAbs() || u.Host == "" {
			return nil, fmt.Errorf("proxy request without an absolute URL: %q", req.RequestURI)
		}
		if u.Scheme != "http" {
			return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
		return &u, nil
	}
	u.Scheme = cs.scheme
	u.Host = req.Host
	if u.Host == "" {
		u.Host = cs.authority
	}
	// Keep the tunnel's port when the Host header leaves the default out.
	if _, p, err := net.SplitHostPort(cs.authority); err == nil && u.Port() == "" && p != defaultPort(cs.scheme) {
		u.Host = net.JoinHostPort(u.Hostname(), p)
	}
	return &u, nil
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func port(u *url.URL) int {
	p := u.Port()
	if p == "" {
		p = defaultPort(u.Scheme)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func isUpgrade(h http.Header) bool {
	if h.Get("Upgrade") == "" {
		return false
	}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// requestHeaders lists the request headers as the browser sent them, with
// Host first. Go's parser canonicalizes names and does not keep their
// order, so the rest are sorted by name.
func requestHeaders(req *http.Request) []recording.Header {
	hs := []recording.Header{{Name: "Host", Value: req.Host}}
	return append(hs, sortedHeaders(req.Header)...)
}

func sortedHeaders(h http.Header) []recording.Header {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []recording.Header
	for _, name := range names {
		for _, v := range h[name] {
			out = append(out, recording.Header{Name: name, Value: v})
		}
	}
	return out
}

func headerValue(hs []recording.Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// writeError answers the browser with a plain-text error and closes the
// connection.
func writeError(conn net.Conn, status int, err error) {
	msg := "webshadow: " + err.Error() + "\n"
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(msg), msg)
}
