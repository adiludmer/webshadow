package reader

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func testTree() fstest.MapFS {
	var long strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	return fstest.MapFS{
		"index.md":            {Data: []byte("# Shop\n\n- [laptops](products/laptops.md)\n")},
		"products/laptops.md": {Data: []byte("# Laptops\n\nZenBook 14: 32 GB RAM, $1,299, in stock\nAero 15: 16 GB RAM\n")},
		"products/long.md":    {Data: []byte(long.String())},
	}
}

func run(t *testing.T, name, args string) (string, error) {
	t.Helper()
	for _, tool := range Tools(testTree()) {
		if tool.Name() == name {
			return tool.Run(context.Background(), []byte(args))
		}
	}
	t.Fatalf("no tool %q", name)
	return "", nil
}

func TestList(t *testing.T) {
	out, err := run(t, "list", `{}`)
	if err != nil || out != "index.md (41 bytes)\nproducts/" {
		t.Errorf("list root = %q, %v", out, err)
	}
	out, err = run(t, "list", `{"path": "/products", "recursive": true}`)
	if err != nil || !strings.Contains(out, "products/laptops.md (") || !strings.Contains(out, "products/long.md (") {
		t.Errorf("list products = %q, %v", out, err)
	}
	out, err = run(t, "list", `{"recursive": true}`)
	if err != nil || strings.Count(out, "\n") != 3 {
		t.Errorf("recursive list = %q, %v", out, err)
	}
	if _, err := run(t, "list", `{"path": "nope"}`); err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) {
		t.Errorf("missing dir: %v", err)
	}
	if _, err := run(t, "list", `{"path": "../.."}`); err == nil || !strings.Contains(err.Error(), "inside the shadow tree") {
		t.Errorf("escape: %v", err)
	}
	if _, err := run(t, "list", `{"path": 3}`); err == nil || !strings.Contains(err.Error(), "invalid args") {
		t.Errorf("bad args: %v", err)
	}
}

func TestRead(t *testing.T) {
	out, err := run(t, "read", `{"path": "products/laptops.md"}`)
	if err != nil || !strings.HasPrefix(out, "products/laptops.md (lines 1-4 of 4)\n# Laptops") {
		t.Errorf("read = %q, %v", out, err)
	}
	out, err = run(t, "read", `{"path": "products/long.md", "offset": 10, "limit": 3}`)
	if err != nil || out != "products/long.md (lines 10-12 of 500)\nline 10\nline 11\nline 12" {
		t.Errorf("paged read = %q, %v", out, err)
	}
	out, _ = run(t, "read", `{"path": "products/long.md"}`)
	if !strings.HasPrefix(out, "products/long.md (lines 1-200 of 500)") {
		t.Errorf("default page = %q", out[:60])
	}
	out, _ = run(t, "read", `{"path": "products/long.md", "limit": 10000}`)
	if !strings.HasPrefix(out, "products/long.md (lines 1-400 of 500)") {
		t.Errorf("limit not capped: %q", out[:60])
	}
	out, _ = run(t, "read", `{"path": "index.md", "offset": 99}`)
	if !strings.Contains(out, "past the end") {
		t.Errorf("offset past end = %q", out)
	}
	for args, want := range map[string]string{`{}`: "path is required", `{"path": "x.md"}`: "does not exist", `{"path": "../etc/passwd"}`: "inside the shadow tree"} {
		if _, err := run(t, "read", args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("read %s: err = %v, want %q", args, err, want)
		}
	}
}

func TestSearch(t *testing.T) {
	out, err := run(t, "search", `{"query": "32 gb"}`)
	if err != nil || out != "products/laptops.md:3: ZenBook 14: 32 GB RAM, $1,299, in stock" {
		t.Errorf("search = %q, %v", out, err)
	}
	out, _ = run(t, "search", `{"query": "line"}`)
	if strings.Count(out, "\n") != maxMatches || !strings.HasSuffix(out, "[460 more matches not shown]") {
		t.Errorf("capped search ends %q", out[len(out)-60:])
	}
	out, _ = run(t, "search", `{"query": "laptops", "path": "products"}`)
	if strings.Contains(out, "index.md") {
		t.Errorf("search outside path: %q", out)
	}
	out, _ = run(t, "search", `{"query": "tablet"}`)
	if out != `no lines contain "tablet"` {
		t.Errorf("no match = %q", out)
	}
	if _, err := run(t, "search", `{"query": " "}`); err == nil {
		t.Error("empty query accepted")
	}
}
