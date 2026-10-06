// Package normalize turns recorded HTTP exchanges into canonical
// observations. It removes differences that are representation rather than
// content, such as host casing, default ports and key order, and it keeps
// every value it finds beside the structure it stripped them from.
//
// What it must not do is destroy evidence. A body it cannot parse stays an
// opaque observation with the reason recorded, and a value it considers weak
// evidence is flagged rather than dropped.
package normalize

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/shape"
	"github.com/adiludmer/webshadow/internal/recording"
)

// Options bound the work normalization may do.
type Options struct {
	Budget shape.Budget
	// MaxBodyBytes is the largest body that is read and parsed. A larger one
	// is recorded as opaque with its media type and size class; its bytes are
	// still in the recording for a later pass that wants them.
	MaxBodyBytes int64
}

// DefaultOptions is what the clustering pass uses.
func DefaultOptions() Options {
	return Options{Budget: shape.DefaultBudget, MaxBodyBytes: 8 << 20}
}

func (o Options) withDefaults() Options {
	if o.Budget.MaxNodes == 0 {
		o.Budget = shape.DefaultBudget
	}
	if o.MaxBodyBytes == 0 {
		o.MaxBodyBytes = DefaultOptions().MaxBodyBytes
	}
	return o
}

// Recording normalizes every exchange of a recording, ordered by request
// start with the record sequence breaking ties. The order is the recorded
// one, so the trace built from it later is the observed protocol order.
func Recording(r *recording.Recording, opts Options) ([]model.Observation, error) {
	out := make([]model.Observation, 0, len(r.Exchanges))
	for i := range r.Exchanges {
		obs, err := Exchange(r, &r.Exchanges[i], opts)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RequestStart != out[j].RequestStart {
			return out[i].RequestStart < out[j].RequestStart
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// Exchange normalizes one exchange. An unparsable URL is the only hard
// error: without it there is no position for anything else.
func Exchange(r *recording.Recording, ex *recording.HTTPExchange, opts Options) (model.Observation, error) {
	opts = opts.withDefaults()
	u, err := url.Parse(ex.Request.URL)
	if err != nil {
		return model.Observation{}, fmt.Errorf("normalize %s: %w", ex.ID, err)
	}
	obs := model.Observation{
		ExchangeID:   ex.ID,
		SessionID:    ex.SessionID,
		ConnectionID: ex.ConnectionID,
		Seq:          ex.Seq,
		RequestStart: firstNonZero(ex.Timing.RequestStart, ex.StartedAt.T),
		End:          firstNonZero(ex.Timing.ResponseEnd, ex.CompletedAt.T),
		Method:       strings.ToUpper(ex.Request.Method),
		Scheme:       canonicalScheme(ex.Request.Scheme, u),
		URL:          ex.Request.URL,
		RawPath:      u.EscapedPath(),
		Error:        ex.Error,
	}
	obs.ResponseStart = ex.Timing.ResponseStart
	obs.Host = canonicalHost(hostOf(ex, u), obs.Scheme, ex.Request.Port)
	obs.PathSegments = pathSegments(u)
	obs.Query = queryPairs(u)
	obs.QueryShape = shape.OfPairs(obs.Query)
	obs.RequestHeaders = headerPairs(ex.Request.Headers)
	obs.RequestCookies = requestCookies(obs.RequestHeaders)

	if ex.Response != nil {
		obs.Status = ex.Response.Status
		obs.ResponseHeaders = headerPairs(ex.Response.Headers)
		obs.SetCookies = setCookies(obs.ResponseHeaders)
	}

	var reqScalars, respScalars []shape.Scalar
	obs.RequestBody, reqScalars = body(r, ex.Request.Body, headerValue(obs.RequestHeaders, "Content-Type"), opts)
	if ex.Response != nil {
		obs.ResponseBody, respScalars = body(r, ex.Response.Body, headerValue(obs.ResponseHeaders, "Content-Type"), opts)
	}

	obs.Values = values(&obs, reqScalars, respScalars)
	return obs, nil
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func canonicalScheme(scheme string, u *url.URL) string {
	if scheme == "" {
		scheme = u.Scheme
	}
	return strings.ToLower(scheme)
}

func hostOf(ex *recording.HTTPExchange, u *url.URL) string {
	if ex.Request.Host != "" {
		return ex.Request.Host
	}
	return u.Hostname()
}

// canonicalHost lowercases the host and drops the port when it is the
// scheme's default, so https://Shop.test:443 and https://shop.test are one
// host.
func canonicalHost(host, scheme string, port int) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, p, err := splitHostPort(host); err == nil {
		host = h
		if port == 0 {
			port, _ = strconv.Atoi(p)
		}
	}
	if port == 0 || isDefaultPort(scheme, port) {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

func splitHostPort(host string) (string, string, error) {
	i := strings.LastIndex(host, ":")
	if i < 0 || strings.Contains(host[i+1:], "]") {
		return "", "", errors.New("no port")
	}
	if strings.HasPrefix(host, "[") && !strings.Contains(host[i:], "]") {
		return "", "", errors.New("no port")
	}
	return host[:i], host[i+1:], nil
}

func isDefaultPort(scheme string, port int) bool {
	return (scheme == "https" && port == 443) || (scheme == "http" && port == 80)
}

// pathSegments splits a path without assigning any meaning to a segment. A
// trailing slash is kept as an empty final segment, since /products/ and
// /products are different routes to many servers.
func pathSegments(u *url.URL) []string {
	p := u.EscapedPath()
	if p == "" || p == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

// queryPairs parses the query as a multimap and sorts it, keeping repeated
// keys. Sorting is what makes ?a=1&b=2 and ?b=2&a=1 one structure.
func queryPairs(u *url.URL) []model.Pair {
	raw := u.RawQuery
	if raw == "" {
		return nil
	}
	var pairs []model.Pair
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		// A key that will not decode is kept as it crossed the wire; the
		// alternative is to lose the parameter.
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if dec, err := url.QueryUnescape(value); err == nil {
			value = dec
		}
		pairs = append(pairs, model.Pair{Name: name, Value: value})
	}
	sortPairs(pairs)
	return pairs
}

// headerPairs canonicalizes header names for comparison and leaves values
// exactly as observed, in the order they arrived.
func headerPairs(headers []recording.Header) []model.Pair {
	if len(headers) == 0 {
		return nil
	}
	out := make([]model.Pair, 0, len(headers))
	for _, h := range headers {
		out = append(out, model.Pair{Name: http.CanonicalHeaderKey(h.Name), Value: h.Value})
	}
	sortPairs(out)
	return out
}

func headerValue(pairs []model.Pair, name string) string {
	for _, p := range pairs {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

func requestCookies(headers []model.Pair) []model.Pair {
	var out []model.Pair
	for _, h := range headers {
		if h.Name != "Cookie" {
			continue
		}
		for _, part := range strings.Split(h.Value, ";") {
			name, value, found := strings.Cut(strings.TrimSpace(part), "=")
			if !found || name == "" {
				continue
			}
			out = append(out, model.Pair{Name: name, Value: value})
		}
	}
	sortPairs(out)
	return out
}

// setCookies takes the name and value of each Set-Cookie, dropping the
// attributes. The attributes say how the browser stores it, not what value
// a later request will carry.
func setCookies(headers []model.Pair) []model.Pair {
	var out []model.Pair
	for _, h := range headers {
		if h.Name != "Set-Cookie" {
			continue
		}
		first, _, _ := strings.Cut(h.Value, ";")
		name, value, found := strings.Cut(strings.TrimSpace(first), "=")
		if !found || name == "" {
			continue
		}
		out = append(out, model.Pair{Name: name, Value: value})
	}
	sortPairs(out)
	return out
}

func sortPairs(p []model.Pair) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].Name != p[j].Name {
			return p[i].Name < p[j].Name
		}
		return p[i].Value < p[j].Value
	})
}

// body derives the structural record of one body. Only bodies that can be
// parsed structurally are read; an image or a video is described by its
// media type and size class without loading it.
func body(r *recording.Recording, b *recording.Body, contentType string, opts Options) (model.Body, []shape.Scalar) {
	if b == nil || b.Size == 0 {
		return model.Body{Kind: model.BodyNone, Shape: model.Shape{Type: model.TypeAbsent}}, nil
	}
	media, params := shape.MediaType(firstNonEmpty(b.ContentType, contentType))
	out := model.Body{
		Kind:      shape.Kind(media),
		Media:     media,
		Size:      b.Size,
		SizeClass: model.SizeClass(b.Size),
	}
	if out.Kind == model.BodyOpaque {
		out.Shape = shape.Opaque(media, b.Size)
		return out, nil
	}
	if b.Size > opts.MaxBodyBytes {
		return opaqueBody(out, media, b.Size, fmt.Sprintf("body of %d bytes over the %d-byte parse limit", b.Size, opts.MaxBodyBytes)), nil
	}
	data, err := readBody(r, b)
	if err != nil {
		return opaqueBody(out, media, b.Size, err.Error()), nil
	}
	var res shape.Result
	switch out.Kind {
	case model.BodyJSON:
		res, err = shape.OfJSON(data, opts.Budget)
	case model.BodyForm:
		res, err = shape.OfForm(data)
	case model.BodyMultipart:
		res, err = shape.OfMultipart(data, params["boundary"])
	}
	if err != nil {
		return opaqueBody(out, media, b.Size, err.Error()), nil
	}
	out.Shape = res.Shape
	out.Truncated = res.Truncated
	return out, res.Scalars
}

func opaqueBody(out model.Body, media string, size int64, reason string) model.Body {
	out.Kind = model.BodyOpaque
	out.Shape = shape.Opaque(media, size)
	out.ParseError = reason
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// readBody reads a stored body and undoes its content coding. Recordings
// keep the bytes as they crossed the wire, coding included.
func readBody(r *recording.Recording, b *recording.Body) ([]byte, error) {
	rc, err := r.OpenBody(b)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var reader io.Reader = rc
	for _, coding := range codings(b.ContentEncoding) {
		switch coding {
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(reader)
			if err != nil {
				return nil, fmt.Errorf("gzip: %w", err)
			}
			defer zr.Close()
			reader = zr
		case "deflate":
			fr := flate.NewReader(reader)
			defer fr.Close()
			reader = fr
		case "br":
			reader = brotli.NewReader(reader)
		case "identity", "":
			// Nothing to undo.
		default:
			return nil, fmt.Errorf("unsupported content coding %q", coding)
		}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// codings returns the codings of a Content-Encoding header in the order they
// must be undone, which is the reverse of the order they were applied.
func codings(header string) []string {
	if header == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(header, ",") {
		if c := strings.ToLower(strings.TrimSpace(part)); c != "" {
			out = append(out, c)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// values collects every scalar of an exchange with its location. Order is
// deterministic: side, part, pattern, path, then value.
func values(obs *model.Observation, reqScalars, respScalars []shape.Scalar) []model.ValueRef {
	var out []model.ValueRef
	add := func(side, part, path, pattern, typ, value string) {
		ref := model.ValueRef{
			Value: value,
			Type:  typ,
			Loc:   model.Location{Side: side, Part: part, Path: path, Pattern: pattern},
		}
		if lowInformation(typ, value) {
			ref.Flags = append(ref.Flags, model.FlagLowInformation)
		}
		out = append(out, ref)
	}

	for i, seg := range obs.PathSegments {
		idx := strconv.Itoa(i)
		add(model.SideRequest, model.PartPath, idx, idx, model.TypeString, seg)
	}
	for _, s := range shape.ScalarsOfPairs(obs.Query) {
		add(model.SideRequest, model.PartQuery, s.Path, s.Pattern, s.Type, s.Value)
	}
	for _, c := range obs.RequestCookies {
		add(model.SideRequest, model.PartCookie, c.Name, c.Name, model.TypeString, c.Value)
	}
	for _, h := range obs.RequestHeaders {
		if !valueBearingHeader(h.Name) {
			continue
		}
		add(model.SideRequest, model.PartHeader, h.Name, h.Name, model.TypeString, h.Value)
	}
	for _, s := range reqScalars {
		add(model.SideRequest, model.PartBody, s.Path, s.Pattern, s.Type, s.Value)
	}

	for _, c := range obs.SetCookies {
		add(model.SideResponse, model.PartSetCookie, c.Name, c.Name, model.TypeString, c.Value)
	}
	for _, h := range obs.ResponseHeaders {
		switch {
		case h.Name == "Location":
			add(model.SideResponse, model.PartLocation, h.Name, h.Name, model.TypeString, h.Value)
		case valueBearingHeader(h.Name):
			add(model.SideResponse, model.PartHeader, h.Name, h.Name, model.TypeString, h.Value)
		}
	}
	for _, s := range respScalars {
		add(model.SideResponse, model.PartBody, s.Path, s.Pattern, s.Type, s.Value)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Loc, out[j].Loc
		switch {
		case a.Side != b.Side:
			return a.Side < b.Side
		case a.Part != b.Part:
			return a.Part < b.Part
		case a.Pattern != b.Pattern:
			return a.Pattern < b.Pattern
		case a.Path != b.Path:
			return a.Path < b.Path
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// boilerplateHeaders are the headers whose values describe the transport or
// the browser build rather than anything a server could hand back. They are
// still part of the observation; they just do not enter the value index,
// where they would link every request to every other.
var boilerplateHeaders = map[string]bool{
	"Accept": true, "Accept-Charset": true, "Accept-Encoding": true,
	"Accept-Language": true, "Accept-Ranges": true, "Age": true,
	"Alt-Svc": true, "Cache-Control": true, "Connection": true,
	"Content-Encoding": true, "Content-Length": true, "Content-Type": true,
	"Cookie": true, "Date": true, "Dnt": true, "Dpr": true,
	"Downlink": true, "Ect": true, "Expires": true, "Host": true,
	"Keep-Alive": true, "Last-Modified": true, "Nel": true, "Origin": true,
	"Permissions-Policy": true, "Pragma": true, "Priority": true,
	"Referer": true, "Referrer-Policy": true, "Report-To": true,
	"Rtt": true, "Save-Data": true, "Server": true, "Set-Cookie": true,
	"Strict-Transport-Security": true, "Te": true, "Timing-Allow-Origin": true,
	"Transfer-Encoding": true, "Upgrade-Insecure-Requests": true,
	"User-Agent": true, "Vary": true, "Via": true,
	"Viewport-Width": true, "X-Content-Type-Options": true,
	"X-Frame-Options": true, "X-Xss-Protection": true,
}

// valueBearingHeader reports whether a header's value goes into the value
// index. Sec-* and Access-Control-* are browser and CORS negotiation, never
// a value a later request echoes.
func valueBearingHeader(name string) bool {
	if boilerplateHeaders[name] {
		return false
	}
	if strings.HasPrefix(name, "Sec-") || strings.HasPrefix(name, "Access-Control-") ||
		strings.HasPrefix(name, "Content-Security-Policy") {
		return false
	}
	return true
}

// lowInformation reports whether a value's repetition is weak evidence of a
// join on its own. Such a value is flagged, never dropped: a response's
// nextIndex coming back as a request's startIndex is a real flow even though
// the value is a small number.
func lowInformation(typ, value string) bool {
	switch typ {
	case model.TypeBool, model.TypeNull:
		return true
	case model.TypeInteger, model.TypeNumber:
		// A number of up to three digits collides by chance across unrelated
		// fields; a long one, such as a timestamp or an id, does not.
		digits := strings.TrimLeft(value, "-+")
		return len(digits) <= 3
	}
	if len(value) < 4 {
		return true
	}
	if mediaTypeLike(value) {
		return true
	}
	switch strings.ToLower(value) {
	case "true", "false", "null", "none", "yes", "no", "undefined", "default", "unknown":
		return true
	}
	return false
}

// mediaTypeLike reports whether a string looks like a media type, such as
// "application/json", which recurs everywhere without connecting anything.
func mediaTypeLike(value string) bool {
	left, right, found := strings.Cut(value, "/")
	if !found || left == "" || right == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	switch left {
	case "application", "text", "image", "audio", "video", "font", "multipart", "message", "model":
		return !strings.Contains(right, "/")
	}
	return false
}
