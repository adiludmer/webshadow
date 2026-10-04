package shadow

import (
	"testing"
	"testing/fstest"
)

func TestDigest(t *testing.T) {
	a := fstest.MapFS{"index.md": {Data: []byte("# Site\n")}, "pages/a.md": {Data: []byte("A")}}
	b := fstest.MapFS{"pages/a.md": {Data: []byte("A")}, "index.md": {Data: []byte("# Site\n")}, "empty": {Mode: 0o755 | 1<<31}}
	da, st, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, _, _ := Digest(b)
	if da != db {
		t.Errorf("same files, different digests: %s vs %s", da, db)
	}
	if st.Files != 2 || st.Bytes != 8 {
		t.Errorf("stats = %+v", st)
	}
	changed := fstest.MapFS{"index.md": {Data: []byte("# Site\n")}, "pages/a.md": {Data: []byte("B")}}
	moved := fstest.MapFS{"index.md": {Data: []byte("# Site\n")}, "pages/b.md": {Data: []byte("A")}}
	for name, fsys := range map[string]fstest.MapFS{"content": changed, "path": moved} {
		if d, _, _ := Digest(fsys); d == da {
			t.Errorf("changing the %s kept the digest", name)
		}
	}
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"": ".", "/": ".", ".": ".", "./a.md": "a.md", "/pages/a.md": "pages/a.md", "pages//b/../a.md": "pages/a.md", " x.md ": "x.md"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"..", "../secret", "pages/../../x"} {
		if got, err := CleanPath(bad); err == nil {
			t.Errorf("CleanPath(%q) = %q, want an error", bad, got)
		}
	}
}
