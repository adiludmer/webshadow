package normalize

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/recording"
)

func TestCanonicalizesHostPortMethodAndQueryOrder(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "get", scheme: "HTTPS", host: "Data.Example.COM", port: 443,
			url: "https://Data.Example.COM/api/products/B0ABC123?marketplace=US&ref=nav",
		})
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "data.example.com",
			url: "https://data.example.com/api/products/B0ABC123?ref=nav&marketplace=US",
		})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations", len(obs))
	}
	a, c := obs[0], obs[1]
	if a.Method != "GET" || a.Host != "data.example.com" || a.Scheme != "https" {
		t.Errorf("not canonicalized: %s %s://%s", a.Method, a.Scheme, a.Host)
	}
	if got, want := a.PathSegments, []string{"api", "products", "B0ABC123"}; !equal(got, want) {
		t.Errorf("segments = %v, want %v", got, want)
	}
	if a.QueryShape.Fingerprint() != c.QueryShape.Fingerprint() {
		t.Error("query order changed the shape fingerprint")
	}
	if got, want := a.QueryShape.Canonical(), "{marketplace:string,ref:string}"; got != want {
		t.Errorf("query shape = %s, want %s", got, want)
	}
	// An identifier in the path is a value at a position, nothing more; no
	// pass in this package decides it is variable.
	if !hasValue(a.Values, model.SideRequest, model.PartPath, "2", "B0ABC123") {
		t.Errorf("path value missing: %+v", a.Values)
	}
}

func TestNonDefaultPortIsKept(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{method: "GET", scheme: "https", host: "shop.test", port: 8443, url: "https://shop.test:8443/a"})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := obs[0].Host; got != "shop.test:8443" {
		t.Errorf("host = %q, want shop.test:8443", got)
	}
}

func TestBodyShapesIgnoreKeyOrderAndValues(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "POST", scheme: "https", host: "shop.test", url: "https://shop.test/search",
			reqBody: payload{data: []byte(`{"query":"ring camera","page":1}`), contentType: "application/json"},
			resBody: payload{data: []byte(`{"products":[{"asin":"B0ABC"}],"nextIndex":24}`), contentType: "application/json"},
		})
		b.exchange(exchange{
			method: "POST", scheme: "https", host: "shop.test", url: "https://shop.test/search",
			reqBody: payload{data: []byte(`{"page":2,"query":"mechanical keyboard"}`), contentType: "application/json"},
			resBody: payload{data: []byte(`{"nextIndex":48,"products":[{"asin":"B0XYZ"}]}`), contentType: "application/json"},
		})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	a, c := obs[0], obs[1]
	if a.RequestBody.Shape.Fingerprint() != c.RequestBody.Shape.Fingerprint() {
		t.Errorf("request shapes differ: %s vs %s", a.RequestBody.Shape.Canonical(), c.RequestBody.Shape.Canonical())
	}
	if a.ResponseBody.Shape.Fingerprint() != c.ResponseBody.Shape.Fingerprint() {
		t.Errorf("response shapes differ: %s vs %s", a.ResponseBody.Shape.Canonical(), c.ResponseBody.Shape.Canonical())
	}
	if a.RequestBody.Kind != model.BodyJSON || a.RequestBody.Media != "application/json" {
		t.Errorf("request body = %+v", a.RequestBody)
	}
	if !hasValue(a.Values, model.SideResponse, model.PartBody, "products[].asin", "B0ABC") {
		t.Errorf("response value missing: %+v", a.Values)
	}
	// The search term crossed the wire, so it is evidence; nothing here
	// calls it a search term.
	if !hasValue(a.Values, model.SideRequest, model.PartBody, "query", "ring camera") {
		t.Errorf("request value missing: %+v", a.Values)
	}
}

func TestCompressedBodiesAreDecoded(t *testing.T) {
	raw := []byte(`{"token":"abc123def","page":1}`)
	rec := build(t, func(b *builder) {
		for _, enc := range []string{"gzip", "deflate", "br", "identity"} {
			b.exchange(exchange{
				method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/" + enc,
				resBody: payload{data: compress(t, enc, raw), contentType: "application/json", encoding: enc},
			})
		}
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.ResponseBody.ParseError != "" {
			t.Errorf("%s: parse error %q", o.URL, o.ResponseBody.ParseError)
			continue
		}
		if got, want := o.ResponseBody.Shape.Canonical(), "{page:integer,token:string}"; got != want {
			t.Errorf("%s: canonical = %s, want %s", o.URL, got, want)
		}
		if !hasValue(o.Values, model.SideResponse, model.PartBody, "token", "abc123def") {
			t.Errorf("%s: token value missing", o.URL)
		}
	}
}

func TestUnparsableBodyKeepsItsReasonAndStaysOpaque(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/truncated",
			resBody: payload{data: []byte(`{"products":[`), contentType: "application/json"},
		})
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "img.test", url: "https://img.test/logo.png",
			resBody: payload{data: bytes.Repeat([]byte{0x89}, 3000), contentType: "image/png"},
		})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	broken, image := obs[0], obs[1]
	if broken.ResponseBody.Kind != model.BodyOpaque || broken.ResponseBody.ParseError == "" {
		t.Errorf("broken JSON = %+v, want opaque with a reason", broken.ResponseBody)
	}
	if got, want := image.ResponseBody.Shape.Canonical(), "opaque(image/png,small)"; got != want {
		t.Errorf("image shape = %s, want %s", got, want)
	}
	if !image.Static() {
		t.Error("an image response is not reported as static")
	}
	if broken.Static() {
		t.Error("a JSON response is reported as static")
	}
}

func TestLargeBodyIsNotParsedButIsRecorded(t *testing.T) {
	big := append([]byte(`{"a":"`), append(bytes.Repeat([]byte("x"), 4096), []byte(`"}`)...)...)
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/big",
			resBody: payload{data: big, contentType: "application/json"},
		})
	})
	obs, err := Recording(rec, Options{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	b := obs[0].ResponseBody
	if b.Kind != model.BodyOpaque || b.ParseError == "" || b.Size != int64(len(big)) {
		t.Errorf("body = %+v, want opaque with a reason and the real size", b)
	}
}

func TestCookiesAndRedirectsAreObservedAsValues(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/",
			resHeaders: []recording.Header{
				{Name: "set-cookie", Value: "session=XYZ789abc; Path=/; HttpOnly"},
				{Name: "Location", Value: "https://shop.test/home"},
				{Name: "Date", Value: "Mon, 06 Oct 2026 13:00:00 GMT"},
			},
			status: 302,
		})
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/home",
			reqHeaders: []recording.Header{
				{Name: "cookie", Value: "session=XYZ789abc; ubid=111-222"},
				{Name: "User-Agent", Value: "Chromium/140"},
				{Name: "X-Request-Token", Value: "tok_9f8e7d"},
			},
		})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	first, second := obs[0], obs[1]
	if !hasValue(first.Values, model.SideResponse, model.PartSetCookie, "session", "XYZ789abc") {
		t.Errorf("Set-Cookie value missing: %+v", first.Values)
	}
	if !hasValue(first.Values, model.SideResponse, model.PartLocation, "Location", "https://shop.test/home") {
		t.Errorf("Location value missing: %+v", first.Values)
	}
	if !hasValue(second.Values, model.SideRequest, model.PartCookie, "session", "XYZ789abc") {
		t.Errorf("Cookie value missing: %+v", second.Values)
	}
	// A custom header can carry a value a response handed over; the browser's
	// own headers cannot, and indexing them would link everything to
	// everything.
	if !hasValue(second.Values, model.SideRequest, model.PartHeader, "X-Request-Token", "tok_9f8e7d") {
		t.Errorf("custom header value missing: %+v", second.Values)
	}
	for _, name := range []string{"User-Agent", "Date", "Cookie"} {
		if hasLocation(second.Values, model.PartHeader, name) || hasLocation(first.Values, model.PartHeader, name) {
			t.Errorf("%s was indexed as a value", name)
		}
	}
	// The headers themselves are still part of the observation.
	if len(second.RequestHeaders) != 3 {
		t.Errorf("request headers = %+v, want all three kept", second.RequestHeaders)
	}
}

func TestWeakValuesAreFlaggedNotDropped(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{
			method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/a?page=2",
			resBody: payload{data: []byte(`{"ok":true,"count":7,"type":"application/json","id":"B0FV975MJK","nextIndex":1024}`), contentType: "application/json"},
		})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	weak := map[string]bool{"true": true, "7": true, "application/json": true}
	strong := map[string]bool{"B0FV975MJK": true, "1024": true}
	var seen int
	for _, v := range obs[0].Values {
		switch {
		case weak[v.Value]:
			seen++
			if !v.HasFlag(model.FlagLowInformation) {
				t.Errorf("%q at %s is not flagged low information", v.Value, v.Loc)
			}
		case strong[v.Value]:
			seen++
			if v.HasFlag(model.FlagLowInformation) {
				t.Errorf("%q at %s is flagged low information", v.Value, v.Loc)
			}
		}
	}
	if seen != len(weak)+len(strong) {
		t.Errorf("found %d of the %d values under test: %+v", seen, len(weak)+len(strong), obs[0].Values)
	}
}

func TestObservationOrderFollowsTheRecordedTimeline(t *testing.T) {
	rec := build(t, func(b *builder) {
		b.exchange(exchange{method: "GET", scheme: "https", host: "c.test", url: "https://c.test/3", start: 300})
		b.exchange(exchange{method: "GET", scheme: "https", host: "a.test", url: "https://a.test/1", start: 100})
		b.exchange(exchange{method: "GET", scheme: "https", host: "b.test", url: "https://b.test/2", start: 200})
	})
	obs, err := Recording(rec, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range obs {
		got = append(got, o.URL)
	}
	want := []string{"https://a.test/1", "https://b.test/2", "https://c.test/3"}
	if !equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestValuesAreInDeterministicOrder(t *testing.T) {
	collect := func() []model.ValueRef {
		rec := build(t, func(b *builder) {
			b.exchange(exchange{
				method: "GET", scheme: "https", host: "shop.test", url: "https://shop.test/a/b?z=1&a=2&m=3",
				reqHeaders: []recording.Header{{Name: "X-B", Value: "bbbb"}, {Name: "X-A", Value: "aaaa"}},
				resBody:    payload{data: []byte(`{"z":"zzzz","a":"aaaa","m":{"n":"nnnn"}}`), contentType: "application/json"},
			})
		})
		obs, err := Recording(rec, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		return obs[0].Values
	}
	first := collect()
	for i := 0; i < 3; i++ {
		got := collect()
		if len(got) != len(first) {
			t.Fatalf("value count changed between runs: %d then %d", len(first), len(got))
		}
		for j := range got {
			if !sameValue(got[j], first[j]) {
				t.Fatalf("value %d changed between runs: %+v then %+v", j, first[j], got[j])
			}
		}
	}
}

// --- fixtures -------------------------------------------------------------

type payload struct {
	data        []byte
	contentType string
	encoding    string
}

type exchange struct {
	method, scheme, host, url string
	port                      int
	status                    int
	start                     int64
	reqHeaders, resHeaders    []recording.Header
	reqBody, resBody          payload
}

type builder struct {
	t     *testing.T
	store *recording.Store
	n     int64
}

// build writes a recording with the given exchanges and loads it back, so
// tests run against the same on-disk format the browser produces.
func build(t *testing.T, f func(*builder)) *recording.Recording {
	t.Helper()
	store, err := recording.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &builder{t: t, store: store}
	f(b)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rec, err := recording.Load(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func (b *builder) exchange(e exchange) {
	b.t.Helper()
	b.n++
	start := e.start
	if start == 0 {
		start = b.n * int64(time.Millisecond)
	}
	status := e.status
	if status == 0 {
		status = 200
	}
	ex := &recording.HTTPExchange{
		SessionID:   b.store.ID(),
		StartedAt:   recording.Stamp{T: start},
		CompletedAt: recording.Stamp{T: start + int64(time.Millisecond)},
		Timing:      recording.Timing{RequestStart: start, ResponseStart: start + 1, ResponseEnd: start + int64(time.Millisecond)},
		Request: recording.Request{
			Method: e.method, Scheme: e.scheme, Host: e.host, Port: e.port,
			URL: e.url, Protocol: "HTTP/1.1",
			Headers: append(headers(e.reqBody), e.reqHeaders...),
			Body:    b.body(e.reqBody),
		},
		Response: &recording.Response{
			Status: status, Protocol: "HTTP/1.1",
			Headers: append(headers(e.resBody), e.resHeaders...),
			Body:    b.body(e.resBody),
		},
	}
	if err := b.store.AppendExchange(ex); err != nil {
		b.t.Fatal(err)
	}
}

func headers(bd payload) []recording.Header {
	var out []recording.Header
	if bd.contentType != "" {
		out = append(out, recording.Header{Name: "Content-Type", Value: bd.contentType})
	}
	if bd.encoding != "" && bd.encoding != "identity" {
		out = append(out, recording.Header{Name: "Content-Encoding", Value: bd.encoding})
	}
	return out
}

func (b *builder) body(bd payload) *recording.Body {
	if len(bd.data) == 0 {
		return nil
	}
	stored, err := b.store.WriteBody(bd.data)
	if err != nil {
		b.t.Fatal(err)
	}
	stored.ContentType = bd.contentType
	if bd.encoding != "identity" {
		stored.ContentEncoding = bd.encoding
	}
	return stored
}

func compress(t *testing.T, coding string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch coding {
	case "gzip":
		w := gzip.NewWriter(&buf)
		w.Write(data)
		w.Close()
	case "deflate":
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		w.Write(data)
		w.Close()
	case "br":
		w := brotli.NewWriter(&buf)
		w.Write(data)
		w.Close()
	default:
		return data
	}
	return buf.Bytes()
}

// sameValue compares two value records field by field, flags included.
func sameValue(a, b model.ValueRef) bool {
	if a.Value != b.Value || a.Type != b.Type || a.Loc != b.Loc || len(a.Flags) != len(b.Flags) {
		return false
	}
	for i := range a.Flags {
		if a.Flags[i] != b.Flags[i] {
			return false
		}
	}
	return true
}

func hasValue(values []model.ValueRef, side, part, pattern, value string) bool {
	for _, v := range values {
		if v.Loc.Side == side && v.Loc.Part == part && v.Loc.Pattern == pattern && v.Value == value {
			return true
		}
	}
	return false
}

func hasLocation(values []model.ValueRef, part, pattern string) bool {
	for _, v := range values {
		if v.Loc.Part == part && v.Loc.Pattern == pattern {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
