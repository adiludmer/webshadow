package mitm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/recording"
)

// harness runs a recording proxy whose upstream transport trusts origin.
type harness struct {
	t      *testing.T
	ca     *CA
	store  *recording.Store
	proxy  *Proxy
	addr   string
	closed bool
}

func newHarness(t *testing.T, trust *httptest.Server) *harness {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := recording.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTransport()
	tr.Proxy = nil
	if trust != nil {
		pool := x509.NewCertPool()
		pool.AddCert(trust.Certificate())
		tr.TLSClientConfig.RootCAs = pool
	}
	ln, err := Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ca: ca, store: store, addr: ln.Addr().String(),
		proxy: &Proxy{CA: ca, Store: store, Transport: tr, Logf: t.Logf}}
	go h.proxy.Serve(ln)
	t.Cleanup(func() { h.stop() })
	return h
}

// client returns an HTTP client that goes through the proxy and trusts
// only the Webshadow CA, like the recording browser.
func (h *harness) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(h.ca.Cert)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: h.addr}),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

// stop shuts the proxy, closes the store and loads the recording.
func (h *harness) stop() *recording.Recording {
	h.t.Helper()
	if !h.closed {
		h.closed = true
		h.proxy.Close()
		if err := h.store.Close(); err != nil {
			h.t.Fatal(err)
		}
	}
	r, err := recording.Load(h.store.Dir())
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func body(t *testing.T, r *recording.Recording, b *recording.Body) string {
	t.Helper()
	rc, err := r.OpenBody(b)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return string(data)
}

func TestProxyRecordsDecryptedHTTPS(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=abc")
		fmt.Fprintf(w, `{"path":%q,"got":%q}`, r.URL.RequestURI(), in)
	}))
	defer origin.Close()
	h := newHarness(t, origin)
	c := h.client()

	for _, q := range []string{"laptop", "phone"} {
		resp, err := c.Post(origin.URL+"/s?k="+q, "application/json", strings.NewReader(`{"q":"`+q+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if want := `{"path":"/s?k=` + q + `","got":"{\"q\":\"` + q + `\"}"}`; string(got) != want {
			t.Fatalf("browser got %s, want %s", got, want)
		}
	}

	r := h.stop()
	if len(r.Exchanges) != 2 {
		t.Fatalf("recorded %d exchanges, want 2", len(r.Exchanges))
	}
	ex := r.Exchanges[0]
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "https://"))
	if ex.Request.Scheme != "https" || ex.Request.Method != "POST" || ex.Request.Host != host ||
		ex.Request.URL != origin.URL+"/s?k=laptop" || ex.Request.Protocol != "HTTP/1.1" {
		t.Errorf("request = %+v", ex.Request)
	}
	if got := body(t, r, ex.Request.Body); got != `{"q":"laptop"}` {
		t.Errorf("request body = %q", got)
	}
	if ex.Request.Body.ContentType != "application/json" {
		t.Errorf("request body content type = %q", ex.Request.Body.ContentType)
	}
	if ex.Response == nil || ex.Response.Status != 200 || headerValue(ex.Response.Headers, "Set-Cookie") != "session=abc" {
		t.Fatalf("response = %+v", ex.Response)
	}
	if got := body(t, r, ex.Response.Body); !strings.Contains(got, `"path":"/s?k=laptop"`) {
		t.Errorf("response body = %q", got)
	}
	tm := ex.Timing
	if !(tm.RequestStart > 0 && tm.RequestStart <= tm.ResponseStart && tm.ResponseStart <= tm.ResponseEnd) {
		t.Errorf("timing out of order: %+v", tm)
	}
	if ex.Error != "" {
		t.Errorf("error = %q", ex.Error)
	}
	// Both requests went over one kept-alive tunnel.
	if r.Exchanges[0].ConnectionID != r.Exchanges[1].ConnectionID || r.Exchanges[0].ID == r.Exchanges[1].ID {
		t.Errorf("connection ids %s, %s; exchange ids %s, %s", r.Exchanges[0].ConnectionID, r.Exchanges[1].ConnectionID, r.Exchanges[0].ID, r.Exchanges[1].ID)
	}
}

func TestProxyRecordsPlainHTTP(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "echo:%s", in)
	}))
	defer origin.Close()
	h := newHarness(t, nil)
	resp, err := h.client().Post(origin.URL+"/form", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "echo:hello" {
		t.Fatalf("browser got %q", got)
	}
	r := h.stop()
	if len(r.Exchanges) != 1 {
		t.Fatalf("recorded %d exchanges", len(r.Exchanges))
	}
	ex := r.Exchanges[0]
	if ex.Request.Scheme != "http" || ex.Request.URL != origin.URL+"/form" || body(t, r, ex.Request.Body) != "hello" || body(t, r, ex.Response.Body) != "echo:hello" {
		t.Errorf("exchange = %+v", ex)
	}
}

func TestProxyKeepsContentCoding(t *testing.T) {
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	io.WriteString(zw, strings.Repeat("compressed page ", 100))
	zw.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		w.Write(zipped.Bytes())
	}))
	defer origin.Close()
	h := newHarness(t, origin)
	resp, err := h.client().Get(origin.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(got), "compressed page") {
		t.Fatalf("browser could not decode the body: %q", got[:min(len(got), 20)])
	}
	r := h.stop()
	b := r.Exchanges[0].Response.Body
	if body(t, r, b) != zipped.String() || b.ContentEncoding != "gzip" || b.ContentType != "text/html" {
		t.Errorf("recorded body is not the gzip bytes as sent: %+v", b)
	}
}

func TestProxyKeepsUpstreamVerification(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "should not be reached")
	}))
	defer origin.Close()
	h := newHarness(t, nil) // the proxy does not trust the origin's self-signed certificate
	resp, err := h.client().Get(origin.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(got), "certificate") {
		t.Errorf("browser got %d %q, want 502 naming the certificate", resp.StatusCode, got)
	}
	r := h.stop()
	if len(r.Exchanges) != 1 || r.Exchanges[0].Response != nil || !strings.Contains(r.Exchanges[0].Error, "certificate") {
		t.Errorf("exchanges = %+v", r.Exchanges)
	}
}

func TestProxyRelaysUpgrades(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "upgrade header lost", http.StatusBadRequest)
			return
		}
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		line, _ := rw.ReadString('\n')
		rw.WriteString("echo " + line)
		rw.Flush()
	}))
	defer origin.Close()
	h := newHarness(t, nil)

	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", origin.URL, strings.TrimPrefix(origin.URL, "http://"))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response = %v, %v", resp, err)
	}
	io.WriteString(conn, "ping\n")
	if line, _ := br.ReadString('\n'); line != "echo ping\n" {
		t.Errorf("relayed %q", line)
	}
	conn.Close()

	r := h.stop()
	if len(r.Exchanges) != 1 || r.Exchanges[0].Response.Status != 101 {
		t.Errorf("exchanges = %+v", r.Exchanges)
	}
}

func TestListenFallsBack(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if _, err := Listen(busy.Addr().String(), false); err == nil {
		t.Error("Listen without fallback took a busy port")
	}
	ln, err := Listen(busy.Addr().String(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() == busy.Addr().String() {
		t.Error("fallback listener reused the busy address")
	}
}
