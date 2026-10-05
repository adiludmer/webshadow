package capture

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// rawItem is one request/response pair for building a Burp export in tests.
type rawItem struct {
	url, method, request, response string
}

// burpXML builds a Burp XML export from raw HTTP messages.
func burpXML(items ...rawItem) string {
	var b strings.Builder
	b.WriteString(exportHead + `<items burpVersion="2026.8" exportTime="Mon Oct 05 07:16:08 UTC 2026">` + "\n")
	for _, it := range items {
		status := ""
		if f := strings.Fields(it.response); len(f) > 1 {
			status = f[1]
		}
		fmt.Fprintf(&b, `  <item>
    <time>Mon Oct 05 07:14:42 UTC 2026</time>
    <url><![CDATA[%s]]></url>
    <host ip="">x</host>
    <port>443</port>
    <protocol>https</protocol>
    <method><![CDATA[%s]]></method>
    <path><![CDATA[/]]></path>
    <extension>null</extension>
    <request base64="true"><![CDATA[%s]]></request>
    <status>%s</status>
    <responselength>%d</responselength>
    <mimetype></mimetype>
    <response base64="true"><![CDATA[%s]]></response>
    <comment></comment>
  </item>
`, it.url, it.method, base64.StdEncoding.EncodeToString([]byte(it.request)), status, len(it.response), base64.StdEncoding.EncodeToString([]byte(it.response)))
	}
	b.WriteString("</items>\n")
	return b.String()
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/fixture.xml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parseFixture(t *testing.T) *Trace {
	t.Helper()
	trace, err := Parse(bytes.NewReader(readFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func TestParse(t *testing.T) {
	trace := parseFixture(t)
	if len(trace.Entries) != 10 {
		t.Fatalf("got %d entries, want 10", len(trace.Entries))
	}

	doc := trace.Entries[0]
	if doc.Sequence != 0 || doc.Method != "GET" || doc.Host != "shop.test" || doc.Path != "/" || doc.Status != 200 {
		t.Errorf("document entry parsed wrong: %+v", doc)
	}
	if doc.RequestHeader("user-agent") != "test" || doc.ResponseMIME != "text/html" {
		t.Errorf("headers parsed wrong: %v / %v", doc.RequestHeaders, doc.ResponseHeaders)
	}
	if want := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC); !doc.StartedAt.Equal(want) {
		t.Errorf("started = %v, want %v", doc.StartedAt, want)
	}

	api := trace.Entries[1]
	if got := api.Query["tag"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("duplicate query keys = %v, want [a b]", got)
	}
	if !strings.Contains(string(api.ResponseBody), `"price": 10`) {
		t.Errorf("JSON body not kept: %s", api.ResponseBody)
	}

	login := trace.Entries[2]
	if login.Status != 302 || login.ResponseHeader("location") != "https://shop.test/account" {
		t.Errorf("redirect parsed wrong: %+v", login)
	}
	if login.RequestMIME != "application/x-www-form-urlencoded" || !strings.Contains(string(login.RequestBody), "password=") {
		t.Errorf("post body parsed wrong: %q %q", login.RequestMIME, login.RequestBody)
	}

	if trace.Entries[4].Status != 500 {
		t.Errorf("error status not kept")
	}
	if len(trace.Entries[5].ResponseBody) != 0 {
		t.Errorf("missing body should be empty, got %q", trace.Entries[5].ResponseBody)
	}
	if png := trace.Entries[6].ResponseBody; !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Errorf("binary body not kept: %q", png)
	}
}

func TestParseDecodesBodies(t *testing.T) {
	page := "<html><body>שלום, Enso raised $15M</body></html>"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(page))
	zw.Close()
	input := burpXML(
		rawItem{"https://news.test/a", "GET", "GET /a HTTP/2\r\nHost: news.test\r\n\r\n",
			"HTTP/2 200 OK\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Encoding: gzip\r\n\r\n" + gz.String()},
		rawItem{"https://news.test/api", "POST", "POST /api HTTP/1.1\r\nContent-Type: application/json\r\n\r\n{\"q\":1}",
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json\r\n\r\n4\r\n{\"a\"\r\n3\r\n:1}\r\n0\r\n\r\n"},
		rawItem{"https://news.test/br", "GET", "GET /br HTTP/1.1\n\n", "HTTP/1.1 200 OK\r\nContent-Encoding: br\r\n\r\n\x8b\x01"},
		rawItem{"https://news.test/none", "GET", "GET /none HTTP/1.1\r\n\r\n", ""},
	)
	trace, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if e := trace.Entries[0]; string(e.ResponseBody) != page || e.Class != ClassDocument || e.StartedAt.IsZero() {
		t.Errorf("gzip page: %q, %s, %v", e.ResponseBody, e.Class, e.StartedAt)
	}
	if e := trace.Entries[1]; string(e.ResponseBody) != `{"a":1}` || string(e.RequestBody) != `{"q":1}` || e.RequestMIME != "application/json" {
		t.Errorf("chunked: %q, request %q (%s)", e.ResponseBody, e.RequestBody, e.RequestMIME)
	}
	if e := trace.Entries[2]; string(e.ResponseBody) != "\x8b\x01" {
		t.Errorf("brotli body should be kept as recorded: %q", e.ResponseBody)
	}
	if e := trace.Entries[3]; e.Status != 0 || e.ResponseBody != nil {
		t.Errorf("item with no response: status %d, body %q", e.Status, e.ResponseBody)
	}

	// Sanitizing stores bodies decoded and drops the coding headers.
	var out bytes.Buffer
	if _, err := Sanitize(strings.NewReader(input), &out, DefaultSanitizeConfig()); err != nil {
		t.Fatal(err)
	}
	clean, err := Parse(&out)
	if err != nil {
		t.Fatal(err)
	}
	if e := clean.Entries[0]; string(e.ResponseBody) != page || e.ResponseHeader("Content-Encoding") != "" {
		t.Errorf("sanitized gzip page: %q, coding %q", e.ResponseBody, e.ResponseHeader("Content-Encoding"))
	}
	if e := clean.Entries[1]; string(e.ResponseBody) != `{"a":1}` || e.ResponseHeader("Transfer-Encoding") != "" {
		t.Errorf("sanitized chunked body: %q", e.ResponseBody)
	}
	if e := clean.Entries[2]; e.ResponseHeader("Content-Encoding") != "br" || string(e.ResponseBody) != "\x8b\x01" {
		t.Errorf("sanitized brotli: %q %q", e.ResponseHeader("Content-Encoding"), e.ResponseBody)
	}
}

func TestParseErrors(t *testing.T) {
	for name, input := range map[string]string{
		"not xml":         "nope",
		"no items":        `<?xml version="1.0"?><other/>`,
		"bad url":         `<items><item><url>http://a b/%zz</url></item></items>`,
		"bad base64 body": `<items><item><url>https://a.test/</url><response base64="true">***</response></item></items>`,
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestClassify(t *testing.T) {
	want := []Class{
		ClassDocument,  // text/html
		ClassAPI,       // JSON
		ClassDocument,  // form POST navigation: no MIME or extension, Sec-Fetch-Dest document
		ClassAPI,       // JSON
		ClassAPI,       // JSON with a 500
		ClassAPI,       // JSON with no body
		ClassMedia,     // image/png
		ClassTelemetry, // analytics host
		ClassScript,    // from MIME
		ClassFont,      // no MIME: from extension
	}
	for i, e := range parseFixture(t).Entries {
		if e.Class != want[i] {
			t.Errorf("entry %d (%s): class %s, want %s", i, e.URL, e.Class, want[i])
		}
	}
}

func TestClassifyFallbacks(t *testing.T) {
	tests := []struct {
		entry Entry
		want  Class
	}{
		{Entry{Host: "a.test", Path: "/x", ResponseMIME: "application/ld+json; charset=utf-8"}, ClassAPI},
		{Entry{Host: "a.test", Path: "/x", ResponseMIME: "text/css"}, ClassStylesheet},
		{Entry{Host: "a.test", Path: "/x", ResponseMIME: "video/mp4"}, ClassMedia},
		{Entry{Host: "a.test", Path: "/page.html"}, ClassDocument},
		{Entry{Host: "a.test", Path: "/cdn-cgi/rum", ResponseMIME: "application/json"}, ClassTelemetry},
		{Entry{Host: "sub.doubleclick.net:443", Path: "/x", ResponseMIME: "text/javascript"}, ClassTelemetry},
		{Entry{Host: "a.test", Path: "/x", RequestHeaders: []Header{{"Sec-Fetch-Dest", "empty"}}}, ClassAPI},
		{Entry{Host: "a.test", Path: "/x", RequestHeaders: []Header{{"sec-fetch-dest", "style"}}}, ClassStylesheet},
		{Entry{Host: "a.test", Path: "/x"}, ClassUnknown},
	}
	for _, tt := range tests {
		if got := Classify(&tt.entry); got != tt.want {
			t.Errorf("Classify(%+v) = %s, want %s", tt.entry, got, tt.want)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := map[string]string{
		"HTTPS://Shop.Test:443/api?b=2&a=1&_=123":       "https://shop.test/api?a=1&b=2",
		"http://shop.test:80":                           "http://shop.test/",
		"https://u:p@shop.test/x?tag=b&tag=a#frag":      "https://shop.test/x?tag=b&tag=a",
		"https://shop.test/x?cb=9&ts=1&timestamp=2&q=a": "https://shop.test/x?q=a",
	}
	for in, want := range tests {
		if got := NormalizeURL(in, DefaultVolatileKeys); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func sanitizeFixture(t *testing.T, cfg SanitizeConfig) ([]byte, SanitizeReport) {
	t.Helper()
	var out bytes.Buffer
	report, err := Sanitize(bytes.NewReader(readFixture(t)), &out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), report
}

func TestSanitizeRemovesSecrets(t *testing.T) {
	out, report := sanitizeFixture(t, DefaultSanitizeConfig())
	for _, secret := range []string{"tok-SECRET", "csrf-SECRET", "r-SECRET", "hunter2", "abc123", "a@b.test", "123.456"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("sanitized capture still contains %q", secret)
		}
	}
	if report.Entries != 10 || report.Headers != 4 {
		t.Errorf("unexpected report: %+v", report)
	}

	trace, err := Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("sanitized capture does not parse: %v", err)
	}
	if found := Unsanitized(trace); len(found) != 0 {
		t.Errorf("sanitized capture fails the check: %v", found)
	}
	if found := Unsanitized(parseFixture(t)); len(found) != 3 {
		t.Errorf("raw fixture should report Cookie, Set-Cookie and Authorization; got %v", found)
	}

	// Structure that agents need is kept.
	api := trace.Entries[1]
	if got := api.Query["tag"]; len(got) != 2 {
		t.Errorf("non-sensitive params lost: %v", api.Query)
	}
	if api.Query.Get("access_token") != Redacted {
		t.Errorf("access_token = %q, want %s", api.Query.Get("access_token"), Redacted)
	}
	if !strings.Contains(api.URL, "access_token=REDACTED") || !strings.Contains(api.URL, "tag=a&tag=b") {
		t.Errorf("URL not redacted in place: %s", api.URL)
	}
	var body struct {
		Items []map[string]any
		User  map[string]any
	}
	if err := json.Unmarshal(api.ResponseBody, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.User["name"] != "Ann" || body.User["email"] != Redacted {
		t.Errorf("JSON body redaction wrong: %s", api.ResponseBody)
	}
	if trace.Entries[0].RequestHeader("User-Agent") != "test" {
		t.Errorf("harmless header was changed")
	}

	if got := string(trace.Entries[2].RequestBody); got != "user=ann&password=REDACTED&remember=1" {
		t.Errorf("form body = %q", got)
	}

	var session map[string]any
	if err := json.Unmarshal(trace.Entries[3].ResponseBody, &session); err != nil {
		t.Fatal(err)
	}
	refresh, _ := session["refresh_token"].(map[string]any)
	if session["token"] != Redacted || session["expires_in"] != float64(3600) || refresh["value"] != Redacted || refresh["ttl"] != float64(0) {
		t.Errorf("JSON placeholders wrong: %v", session)
	}

	if !bytes.Equal(trace.Entries[6].ResponseBody, parseFixture(t).Entries[6].ResponseBody) {
		t.Errorf("binary body changed")
	}
	if !strings.Contains(trace.Entries[1].RequestHeader("Host"), "shop.test") {
		t.Errorf("request headers lost: %v", trace.Entries[1].RequestHeaders)
	}
	if !bytes.Contains(out, []byte(`burpVersion="2026.8"`)) || !bytes.Contains(out, []byte("<!DOCTYPE items")) {
		t.Errorf("export prolog not kept:\n%.400s", out)
	}
}

func TestSanitizeIsIdempotent(t *testing.T) {
	once, _ := sanitizeFixture(t, DefaultSanitizeConfig())
	var twice bytes.Buffer
	report, err := Sanitize(bytes.NewReader(once), &twice, DefaultSanitizeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if report.Headers+report.Params+report.BodyFields != 0 {
		t.Errorf("second pass changed more values: %+v", report)
	}
	if !bytes.Equal(once, twice.Bytes()) {
		t.Errorf("second pass changed the output")
	}
}

func TestSanitizeTrimming(t *testing.T) {
	cfg := DefaultSanitizeConfig()
	cfg.StripBodies = []Class{ClassScript, ClassMedia}
	cfg.DropClasses = []Class{ClassTelemetry, ClassFont}
	out, report := sanitizeFixture(t, cfg)
	if report.Entries != 8 || report.DroppedEntries != 2 || report.StrippedBodies != 2 {
		t.Errorf("unexpected report: %+v", report)
	}
	trace, err := Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range trace.Entries {
		if e.Class == ClassTelemetry || e.Class == ClassFont {
			t.Errorf("entry %s should have been dropped", e.URL)
		}
		if (e.Class == ClassScript || e.Class == ClassMedia) && len(e.ResponseBody) != 0 {
			t.Errorf("body of %s should have been stripped", e.URL)
		}
	}
	if trace.Entries[1].ResponseBody == nil {
		t.Errorf("API bodies must be kept")
	}
	if !bytes.Contains(out, []byte(strippedComment)) {
		t.Errorf("stripped items should say so in their comment")
	}
}

func TestSelectAndReports(t *testing.T) {
	trace := parseFixture(t)
	got := trace.Select(Filter{})
	if len(got) != 6 {
		t.Fatalf("default filter kept %d entries, want 6 documents and API calls", len(got))
	}
	if n := len(trace.Select(Filter{Classes: Classes})); n != 10 {
		t.Errorf("all classes kept %d, want 10", n)
	}
	if n := len(trace.Select(Filter{Classes: Classes, Host: "ANALYTICS"})); n != 1 {
		t.Errorf("host filter kept %d, want 1", n)
	}

	var buf bytes.Buffer
	WriteSummary(&buf, trace)
	WriteIndex(&buf, got)
	WriteKeptNames(&buf, trace)
	out := buf.String()
	for _, want := range []string{"10 entries", "telemetry", "shop.test", "https://shop.test/api/items?access_token=tok-SECRET&tag=a&tag=b", "header names:", "query parameter names:"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestSanitizeNestedIdentifiers(t *testing.T) {
	data := `{"data":{"ui":"visitor-1","q":"laptops"}}`
	enc := url.QueryEscape(data)
	input := burpXML(
		rawItem{"https://feed.test/json?llvl=2&data=" + enc, "GET",
			"GET /json?llvl=2&data=" + enc + " HTTP/2\r\nReferer: https://shop.test/p?uid=visitor-1&page=2\r\n\r\n",
			"HTTP/2 200 OK\r\nContent-Type: application/javascript\r\nContent-Length: 70\r\n\r\n" +
				"trc_json_response =\n{\"items\":[{\"title\":\"Hello\"}],\"sd\":\"visitor-1\"};"},
		rawItem{"https://shop.test/cb?x=1", "GET", "GET /cb?x=1 HTTP/1.1\r\n\r\n",
			"HTTP/1.1 200 OK\r\nContent-Type: text/javascript\r\n\r\ncb({\"user_id\":\"visitor-1\",\"ok\":true});"},
	)
	var out bytes.Buffer
	if _, err := Sanitize(strings.NewReader(input), &out, DefaultSanitizeConfig()); err != nil {
		t.Fatal(err)
	}
	trace, err := Parse(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trace.Entries[0].URL, "llvl=2") || !strings.Contains(trace.Entries[0].RequestHeader("referer"), "page=2") {
		t.Errorf("harmless parts of URLs were lost: %s", trace.Entries[0].URL)
	}
	if body := string(trace.Entries[0].ResponseBody); !strings.HasPrefix(body, "trc_json_response =\n{") || !strings.Contains(body, `"title":"Hello"`) {
		t.Errorf("assignment wrapper not kept: %s", body)
	}
	if body := string(trace.Entries[1].ResponseBody); !strings.HasPrefix(body, "cb({") || !strings.HasSuffix(body, "});") {
		t.Errorf("JSONP wrapper not kept: %s", body)
	}
	for _, e := range trace.Entries {
		for _, part := range [][]byte{[]byte(e.URL), e.ResponseBody, []byte(e.RequestHeader("Referer"))} {
			if bytes.Contains(part, []byte("visitor-1")) {
				t.Errorf("identifier survived in item %d: %s", e.Sequence, part)
			}
		}
	}
	if got, want := trace.Entries[0].ResponseHeader("Content-Length"), fmt.Sprint(len(trace.Entries[0].ResponseBody)); got != want {
		t.Errorf("Content-Length %s, body is %s bytes", got, want)
	}
	raw, _ := readExport(bytes.NewReader(out.Bytes()))
	if req := string(mustRaw(raw.items[0].Request)); strings.Contains(req, "visitor-1") || !strings.HasPrefix(req, "GET /json?llvl=2&data=") {
		t.Errorf("request line not redacted: %.120s", req)
	}
}

func TestClassifyThirdPartyNoise(t *testing.T) {
	for _, e := range []Entry{
		{Host: "www.google.com", Path: "/recaptcha/api2/reload", ResponseMIME: "application/json"},
		{Host: "jnn-pa.googleapis.com", Path: "/$rpc/x", ResponseMIME: "application/json"},
	} {
		if got := Classify(&e); got != ClassTelemetry {
			t.Errorf("Classify(%s%s) = %s, want telemetry", e.Host, e.Path, got)
		}
	}
}
