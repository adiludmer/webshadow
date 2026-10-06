package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWriter(t *testing.T) (*writeTool, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return newWriteTool(root), dir
}

func write(w *writeTool, path, content string) (string, error) {
	args := `{"path": ` + quote(path) + `, "content": ` + quote(content) + `}`
	return w.Run(context.Background(), []byte(args))
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func TestWrite(t *testing.T) {
	w, dir := newWriter(t)
	out, err := write(w, "articles/2026/enso.md", "# Enso\n")
	if err != nil || out != "wrote articles/2026/enso.md (7 bytes); the tree has 1 files" {
		t.Fatalf("write = %q, %v", out, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "articles", "2026", "enso.md")); string(data) != "# Enso\n" {
		t.Errorf("file holds %q", data)
	}
	out, err = write(w, "./articles/2026/enso.md", "# Enso v2\n")
	if err != nil || !strings.HasPrefix(out, "replaced articles/2026/enso.md") || !strings.HasSuffix(out, "1 files") {
		t.Errorf("replace = %q, %v", out, err)
	}

	for path, want := range map[string]string{
		"":                 "path is required",
		"/etc/passwd.md":   "must be relative",
		"../outside.md":    "inside the shadow tree",
		"a/../../b.md":     "inside the shadow tree",
		"notes.txt":        "only Markdown",
		"script.md/x.html": "only Markdown",
	} {
		if _, err := write(w, path, "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("write %q: err = %v, want %q", path, err, want)
		}
	}
	if _, err := write(w, "big.md", strings.Repeat("x", MaxFileBytes+1)); err == nil || !strings.Contains(err.Error(), "limited to") {
		t.Errorf("oversized file: %v", err)
	}
}

func TestWriteRejectsSymlinkEscape(t *testing.T) {
	w, dir := newWriter(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := write(w, "link/escape.md", "x"); err == nil {
		t.Fatal("wrote through a symlink out of the tree")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.md")); !os.IsNotExist(err) {
		t.Fatal("file appeared outside the tree")
	}
}

func TestWriteTreeLimits(t *testing.T) {
	w, _ := newWriter(t)
	for i := range MaxFiles {
		if _, err := write(w, filepathName(i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := write(w, "one-more.md", "x"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Errorf("file limit: %v", err)
	}
	if _, err := write(w, filepathName(0), "replacing is fine"); err != nil {
		t.Errorf("replace at the file limit: %v", err)
	}

	w, _ = newWriter(t)
	chunk := strings.Repeat("x", MaxFileBytes)
	for i := range MaxTreeBytes / MaxFileBytes {
		if _, err := write(w, filepathName(i), chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := write(w, "over.md", "x"); err == nil || !strings.Contains(err.Error(), "the limit is") {
		t.Errorf("byte limit: %v", err)
	}
}

func filepathName(i int) string { return fmt.Sprintf("pages/p%04d.md", i) }
