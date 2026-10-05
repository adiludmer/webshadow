package generator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/capture"
)

func runSave(t *testing.T, trace *capture.Trace, args string) (string, string, error) {
	t.Helper()
	w, dir := newWriter(t)
	out, err := saveTool{trace: trace, w: w}.Run(context.Background(), []byte(args))
	return out, dir, err
}

func TestSaveEntry(t *testing.T) {
	trace := fixture(t)
	out, dir, err := runSave(t, trace, `{"seq": 0, "path": "pages/home.md", "title": "Home", "note": "The shop's front page."}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote pages/home.md") || !strings.Contains(out, "saved text bytes 0-29 of 29 from seq 0: wrote pages/home.md") {
		t.Errorf("result: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "pages", "home.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Home\n\nSource: https://shop.test/\n\nThe shop's front page.\n\n[Item](https://shop.test/p/1)\n"
	if string(got) != want {
		t.Errorf("file:\n%q\nwant\n%q", got, want)
	}

	_, dir, err = runSave(t, trace, `{"seq": 1, "path": "items.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "items.md"))
	if !strings.HasPrefix(string(got), "Source: https://shop.test/api/items") || !strings.Contains(string(got), "```json\n{") {
		t.Errorf("json file:\n%s", got)
	}

	for args, want := range map[string]string{
		`{"path": "a.md"}`:                         "seq is required",
		`{"seq": 99, "path": "a.md"}`:              "does not exist",
		`{"seq": 0, "path": "a.txt"}`:              "only Markdown files",
		`{"seq": 0, "path": "../a.md"}`:            "",
		`{"seq": 5, "path": "a.md"}`:               "no response body",
		`{"seq": 0, "path": "a.md", "offset": 29}`: "past the end",
	} {
		if _, _, err := runSave(t, trace, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", args, err, want)
		}
	}
}

func TestSaveEntrySplitsLongText(t *testing.T) {
	long := strings.Repeat("שלום עולם ", 8000) // 144,000 bytes, more than two files hold
	trace := &capture.Trace{Entries: []capture.Entry{{URL: "https://a.test/", ResponseMIME: "text/plain", ResponseBody: []byte(long)}}}
	out, _, err := runSave(t, trace, `{"seq": 0, "path": "a.md", "title": "A"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the rest did not fit, save it to another file with offset ") {
		t.Fatalf("result: %s", out)
	}
	next, err := strconv.Atoi(out[strings.LastIndex(out, "offset ")+7:])
	if err != nil || next <= 0 || next > maxSaveText {
		t.Fatalf("next offset %d from %q", next, out)
	}
	out, dir, err := runSave(t, trace, `{"seq": 0, "path": "a2.md", "title": "A", "note": "skipped", "offset": `+strconv.Itoa(next)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a2.md"))
	if !strings.HasPrefix(string(got), "# A (continued)\n\nSource: https://a.test/\n\n") || strings.Contains(string(got), "skipped") {
		t.Errorf("continuation starts %q", got[:80])
	}
	if !strings.Contains(out, "of 144000") {
		t.Errorf("result: %s", out)
	}
}
