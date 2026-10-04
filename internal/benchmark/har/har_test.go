package har

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/fixture.har")
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
	if doc.Cookies != 1 {
		t.Errorf("cookies = %d, want 1", doc.Cookies)
	}
	if doc.RequestHeader("user-agent") != "test" {
		t.Errorf("case-insensitive header lookup failed")
	}
	if doc.Duration != 12500*time.Microsecond {
		t.Errorf("duration = %v, want 12.5ms", doc.Duration)
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
	if trace.Entries[5].ResponseBody != nil {
		t.Errorf("missing body should be nil, got %q", trace.Entries[5].ResponseBody)
	}
	if png := trace.Entries[6].ResponseBody; !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Errorf("base64 body not decoded: %q", png)
	}
}

func TestParseErrors(t *testing.T) {
	for name, input := range map[string]string{
		"not json":        "nope",
		"no log":          `{}`,
		"no entries":      `{"log": {}}`,
		"bad url":         `{"log": {"entries": [{"request": {"url": "http://a b/%zz"}}]}}`,
		"bad base64 body": `{"log": {"entries": [{"request": {"url": "https://a.test/"}, "response": {"content": {"text": "***", "encoding": "base64"}}}]}}`,
	} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestClassify(t *testing.T) {
	want := []Class{
		ClassDocument,  // _resourceType document
		ClassAPI,       // xhr
		ClassDocument,  // form POST navigation
		ClassAPI,       // fetch
		ClassAPI,       // xhr with a 500
		ClassAPI,       // fetch with no body
		ClassMedia,     // image
		ClassTelemetry, // analytics host
		ClassScript,    // no _resourceType: from MIME
		ClassFont,      // no _resourceType or MIME: from extension
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
		{Entry{Host: "a.test", Path: "/cdn-cgi/rum", ResourceType: "xhr"}, ClassTelemetry},
		{Entry{Host: "sub.doubleclick.net:443", Path: "/x", ResourceType: "script"}, ClassTelemetry},
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
			t.Errorf("sanitized HAR still contains %q", secret)
		}
	}
	if report.Entries != 10 || report.Cookies != 1 || report.Headers != 4 {
		t.Errorf("unexpected report: %+v", report)
	}

	trace, err := Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("sanitized HAR does not parse: %v", err)
	}
	if found := Unsanitized(trace); len(found) != 0 {
		t.Errorf("sanitized HAR fails the check: %v", found)
	}
	if found := Unsanitized(parseFixture(t)); len(found) != 4 {
		t.Errorf("raw fixture should report cookie list, Cookie, Set-Cookie and Authorization; got %v", found)
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
	if !bytes.Contains(out, []byte(`"_initiator"`)) || !bytes.Contains(out, []byte(`"callFrames"`)) {
		t.Errorf("unknown HAR fields should be kept by default")
	}
}

func TestSanitizeIsIdempotent(t *testing.T) {
	once, _ := sanitizeFixture(t, DefaultSanitizeConfig())
	var twice bytes.Buffer
	report, err := Sanitize(bytes.NewReader(once), &twice, DefaultSanitizeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if report.Headers+report.Cookies+report.Params+report.BodyFields != 0 {
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
	cfg.TrimInitiators = true
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
		if (e.Class == ClassScript || e.Class == ClassMedia) && e.ResponseBody != nil {
			t.Errorf("body of %s should have been stripped", e.URL)
		}
	}
	if trace.Entries[1].ResponseBody == nil {
		t.Errorf("API bodies must be kept")
	}
	if bytes.Contains(out, []byte("callFrames")) || !bytes.Contains(out, []byte(`"_initiator"`)) {
		t.Errorf("initiator stacks should be gone but the initiator kept")
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
	input := `{"log": {"entries": [{
		"request": {
			"method": "GET",
			"url": "https://feed.test/json?llvl=2&data=` + enc + `",
			"headers": [
				{"name": ":path", "value": "/json?llvl=2&data=` + enc + `"},
				{"name": "Referer", "value": "https://shop.test/p?uid=visitor-1&page=2"}
			],
			"queryString": [{"name": "data", "value": "` + enc + `"}]
		},
		"response": {"status": 200, "headers": [], "content": {
			"mimeType": "application/javascript",
			"text": "trc_json_response =\n{\"items\":[{\"title\":\"Hello\"}],\"sd\":\"visitor-1\"};"
		}}
	}, {
		"request": {"method": "GET", "url": "https://shop.test/cb?x=1", "headers": []},
		"response": {"status": 200, "headers": [], "content": {
			"mimeType": "text/javascript",
			"text": "cb({\"user_id\":\"visitor-1\",\"ok\":true});"
		}}
	}]}}`
	var out bytes.Buffer
	if _, err := Sanitize(strings.NewReader(input), &out, DefaultSanitizeConfig()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "visitor-1") {
		t.Fatalf("identifier survived:\n%s", out.String())
	}
	trace, err := Parse(&out)
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
}

func TestClassifyThirdPartyNoise(t *testing.T) {
	for _, e := range []Entry{
		{Host: "www.google.com", Path: "/recaptcha/api2/reload", ResourceType: "xhr"},
		{Host: "jnn-pa.googleapis.com", Path: "/$rpc/x", ResourceType: "fetch"},
	} {
		if got := Classify(&e); got != ClassTelemetry {
			t.Errorf("Classify(%s%s) = %s, want telemetry", e.Host, e.Path, got)
		}
	}
}
