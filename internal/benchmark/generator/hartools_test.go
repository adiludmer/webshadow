package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/har"
)

func fixture(t *testing.T) *har.Trace {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "har", "testdata", "fixture.har"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	trace, err := har.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func runTool(t *testing.T, trace *har.Trace, name, args string) (string, error) {
	t.Helper()
	for _, tool := range HARTools(trace) {
		if tool.Name() == name {
			return tool.Run(context.Background(), []byte(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return "", nil
}

func TestHARIndex(t *testing.T) {
	trace := fixture(t)
	out, err := runTool(t, trace, "har_index", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "requests 1-") || !strings.Contains(out, "(classes: document, api, unknown)") {
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

	out, _ = runTool(t, trace, "har_index", `{"classes": ["all"]}`)
	if !strings.HasPrefix(out, "requests 1-10 of 10") {
		t.Errorf("all classes: %q", out)
	}
	out, _ = runTool(t, trace, "har_index", `{"classes": ["Media", "font"]}`)
	if !strings.HasPrefix(out, "requests 1-2 of 2 (classes: media, font)") {
		t.Errorf("chosen classes: %q", out)
	}
	out, _ = runTool(t, trace, "har_index", `{"classes": ["all"], "host": "google"}`)
	if !strings.HasPrefix(out, "requests 1-1 of 1") {
		t.Errorf("host filter: %q", out)
	}
	out, _ = runTool(t, trace, "har_index", `{"classes": ["all"], "page_size": 4, "page": 2}`)
	if !strings.HasPrefix(out, "requests 5-8 of 10") || !strings.HasSuffix(out, "[next: page 3]") {
		t.Errorf("paging: %q", out)
	}
	out, _ = runTool(t, trace, "har_index", `{"page": 9}`)
	if !strings.Contains(out, "past the end") {
		t.Errorf("past end: %q", out)
	}
	if _, err := runTool(t, trace, "har_index", `{"classes": ["pictures"]}`); err == nil {
		t.Error("unknown class accepted")
	}
}

func TestHAREntry(t *testing.T) {
	trace := fixture(t)
	out, err := runTool(t, trace, "har_entry", `{"seq": 1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 GET https://shop.test/api/items") || !strings.Contains(out, "response body: application/json, 81 bytes") || !strings.Contains(out, "bytes 0-81:\n{") {
		t.Errorf("entry 1:\n%s", out)
	}
	out, _ = runTool(t, trace, "har_entry", `{"seq": 1, "offset": 10, "limit": 20}`)
	if !strings.Contains(out, "bytes 10-30:") || !strings.HasSuffix(out, "[more: offset 30]") {
		t.Errorf("paged entry:\n%s", out)
	}
	out, _ = runTool(t, trace, "har_entry", `{"seq": 3, "part": "request"}`)
	if !strings.Contains(out, "request body: application/json") {
		t.Errorf("request part:\n%s", out)
	}
	out, _ = runTool(t, trace, "har_entry", `{"seq": 2}`)
	if !strings.Contains(out, "status 302") || !strings.Contains(out, "0 bytes") {
		t.Errorf("redirect:\n%s", out)
	}
	out, _ = runTool(t, trace, "har_entry", `{"seq": 6}`)
	if !strings.Contains(out, "[binary body not shown]") {
		t.Errorf("binary:\n%s", out)
	}
	for args, want := range map[string]string{`{}`: "seq is required", `{"seq": 99}`: "requests 0-9", `{"seq": 1, "part": "headers"}`: "part must be"} {
		if _, err := runTool(t, trace, "har_entry", args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
}

func TestHAREntryKeepsRunesWhole(t *testing.T) {
	trace := &har.Trace{Entries: []har.Entry{{URL: "https://x.test/", ResponseMIME: "text/plain", ResponseBody: []byte("שלום עולם")}}}
	out, err := runTool(t, trace, "har_entry", `{"seq": 0, "offset": 1, "limit": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bytes 0-2:\nש") {
		t.Errorf("split a rune:\n%s", out)
	}
}
