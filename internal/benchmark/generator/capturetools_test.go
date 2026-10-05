package generator

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/capture"
)

func fixture(t *testing.T) *capture.Trace {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "capture", "testdata", "fixture.xml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	trace, err := capture.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func runTool(t *testing.T, trace *capture.Trace, name, args string) (string, error) {
	t.Helper()
	for _, tool := range CaptureTools(trace) {
		if tool.Name() == name {
			return tool.Run(context.Background(), []byte(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return "", nil
}

func TestCaptureIndex(t *testing.T) {
	trace := fixture(t)
	out, err := runTool(t, trace, "capture_index", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "requests 1-") || !strings.Contains(out, "(classes: document, api, unknown; 1 with no body hidden)") {
		t.Errorf("header: %q", out)
	}
	for _, want := range []string{"0 GET 200 document", "1 GET 200 api 81 https://shop.test/api/items?access_token=tok-SECRET&tag=a&tag=b"} {
		if !strings.Contains(out, want) {
			t.Errorf("index lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"logo.png", "google-analytics", "app.js", "woff2"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("default index shows %s:\n%s", unwanted, out)
		}
	}

	out, _ = runTool(t, trace, "capture_index", `{"classes": ["all"]}`)
	if !strings.HasPrefix(out, "requests 1-8 of 8") || !strings.Contains(out, "2 with no body hidden") {
		t.Errorf("all classes: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"classes": ["all"], "include_empty": true}`)
	if !strings.HasPrefix(out, "requests 1-10 of 10 (classes: document, api, script, stylesheet, media, font, telemetry, unknown)") {
		t.Errorf("include_empty: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"classes": ["Media", "font"]}`)
	if !strings.HasPrefix(out, "requests 1-2 of 2 (classes: media, font)") {
		t.Errorf("chosen classes: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"classes": ["all"], "host": "google"}`)
	if !strings.HasPrefix(out, "no requests with a body match; 1 without one are hidden") {
		t.Errorf("host filter: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"classes": ["all"], "host": "google", "include_empty": true}`)
	if !strings.HasPrefix(out, "requests 1-1 of 1") {
		t.Errorf("host filter with empty: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"classes": ["all"], "page_size": 4, "page": 2, "include_empty": true}`)
	if !strings.HasPrefix(out, "requests 5-8 of 10") || !strings.HasSuffix(out, "[next: page 3]") {
		t.Errorf("paging: %q", out)
	}
	out, _ = runTool(t, trace, "capture_index", `{"page": 9}`)
	if !strings.Contains(out, "past the end") {
		t.Errorf("past end: %q", out)
	}
	if _, err := runTool(t, trace, "capture_index", `{"classes": ["pictures"]}`); err == nil {
		t.Error("unknown class accepted")
	}
}

func TestCaptureIndexFoldsRepeats(t *testing.T) {
	entry := func(seq int, method, rawURL, req, resp string, status int) capture.Entry {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		return capture.Entry{Sequence: seq, Method: method, URL: rawURL, Host: u.Host, Path: u.Path, Status: status, Class: capture.ClassAPI,
			RequestBody: []byte(req), ResponseBody: []byte(resp)}
	}
	trace := &capture.Trace{Entries: []capture.Entry{
		entry(0, "GET", "https://a.test/hp.json?ver=1", "", `{"n":1}`, 200),
		entry(1, "GET", "https://a.test/hp.json?ver=1", "", "", 200),
		entry(2, "GET", "https://a.test/hp.json?ver=1", "", `{"n":1}`, 200),
		entry(3, "GET", "https://a.test/hp.json?ver=1", "", `{"n":2}`, 200),
		entry(4, "POST", "https://a.test/q", `{"q":"a"}`, `[]`, 200),
		entry(5, "POST", "https://a.test/q", `{"q":"b"}`, `[]`, 200),
		entry(6, "GET", "https://a.test/old", "", "", 301),
	}}
	for i := 7; i < 14; i++ {
		trace.Entries = append(trace.Entries, entry(i, "GET", fmt.Sprintf("https://a.test/hp.json?ver=%d", i), "", `{"n":1}`, 200))
	}
	out, err := runTool(t, trace, "capture_index", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `requests 1-5 of 5 (classes: document, api, unknown; 1 with no body hidden; 8 repeats folded)
0 GET 200 api 7 https://a.test/hp.json?ver=1 (x9, also 2, 7, 8, 9, 10, and 3 more)
3 GET 200 api 7 https://a.test/hp.json?ver=1
4 POST 200 api 2 https://a.test/q
5 POST 200 api 2 https://a.test/q
6 GET 301 api 0 https://a.test/old`
	if out != want {
		t.Errorf("index:\n%s\nwant:\n%s", out, want)
	}
}

func TestCaptureEntry(t *testing.T) {
	trace := fixture(t)
	out, err := runTool(t, trace, "capture_entry", `{"seq": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 GET https://shop.test/api/items") || !strings.Contains(out, "response body: application/json, 81 bytes") || !strings.Contains(out, "bytes 0-81:\n{") {
		t.Errorf("entry 1:\n%s", out)
	}
	out, _ = runTool(t, trace, "capture_entry", `{"seq": 1, "offset": 10, "limit": 20}`)
	if !strings.Contains(out, "bytes 10-30:") || !strings.HasSuffix(out, "[more: offset 30]") {
		t.Errorf("paged entry:\n%s", out)
	}
	out, _ = runTool(t, trace, "capture_entry", `{"seq": 3, "part": "request"}`)
	if !strings.Contains(out, "request body: application/json") {
		t.Errorf("request part:\n%s", out)
	}
	out, _ = runTool(t, trace, "capture_entry", `{"seq": 2}`)
	if !strings.Contains(out, "status 302") || !strings.Contains(out, "0 bytes") {
		t.Errorf("redirect:\n%s", out)
	}
	out, _ = runTool(t, trace, "capture_entry", `{"seq": 6}`)
	if !strings.Contains(out, "[binary body not shown]") {
		t.Errorf("binary:\n%s", out)
	}
	for args, want := range map[string]string{`{}`: "seq is required", `{"seq": 99}`: "requests 0-9", `{"seq": 1, "part": "headers"}`: "part must be"} {
		if _, err := runTool(t, trace, "capture_entry", args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

func TestCaptureEntryKeepsRunesWhole(t *testing.T) {
	trace := &capture.Trace{Entries: []capture.Entry{{URL: "https://x.test/", ResponseMIME: "text/plain", ResponseBody: []byte("שלום עולם")}}}
	out, err := runTool(t, trace, "capture_entry", `{"seq": 0, "offset": 1, "limit": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bytes 0-2:\nש") {
		t.Errorf("split a rune:\n%s", out)
	}
}
