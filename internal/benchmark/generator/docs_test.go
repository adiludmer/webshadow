package generator

import (
	"testing"

	"github.com/adiludmer/webshadow/internal/markdown"
)

func TestURLKey(t *testing.T) {
	for _, pair := range [][2]string{
		{"HTTPS://WWW.A.test/x/#frag", "https://www.a.test/x"},
		{"https://www.a.test/x", "https://www.a.test/x"},
		{"https://www.a.test/x/?p=2", "https://www.a.test/x?p=2"},
	} {
		if got := urlKey(pair[0]); got != pair[1] {
			t.Errorf("urlKey(%q) = %q, want %q", pair[0], got, pair[1])
		}
	}
}

func TestSlugPath(t *testing.T) {
	taken := map[string]bool{"pages/news-today.md": true}
	isTaken := func(p string) bool { return taken[p] }
	for _, tt := range []struct {
		url       string
		multiHost bool
		want      string
	}{
		{"https://a.test/", false, "pages/home.md"},
		{"https://a.test/Former-OpenAI/researcher/", false, "pages/former-openai-researcher.md"},
		{"https://a.test/news/today", false, "pages/news-today-2.md"},
		{"https://a.test/search?q=laptop&page=2", false, "pages/search-q-laptop-page-2.md"},
		{"https://a.test/%D7%91%D7%98%D7%99%D7%97%D7%95%D7%AA/", false, "pages/בטיחות.md"},
		{"https://shop.a.test/item", true, "pages/shop.a.test/item.md"},
	} {
		d := &doc{Document: markdown.Document{URL: tt.url}}
		if got := slugPath(d, tt.multiHost, isTaken); got != tt.want {
			t.Errorf("slugPath(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestRewriteLinks(t *testing.T) {
	targets := map[string]string{
		urlKey("https://a.test/enso/"): "articles/funding/enso.md",
		urlKey("https://a.test/"):      "home.md",
	}
	text := "[Enso](https://a.test/enso/#top) and [home](https://a.test/) and [ext](https://b.test/) and ![x](https://a.test/enso/)"
	got := rewriteLinks(text, "articles/security/jev.md", targets)
	want := "[Enso](../funding/enso.md) and [home](../../home.md) and [ext](https://b.test/) and ![x](../funding/enso.md)"
	if got != want {
		t.Errorf("rewriteLinks:\n%s\nwant\n%s", got, want)
	}
	if got := rewriteLinks("[self](https://a.test/)", "home.md", targets); got != "[self](https://a.test/)" {
		t.Errorf("a self link should stay: %s", got)
	}
	if got := rewriteLinks("[Enso](https://a.test/enso/)", "index.md", targets); got != "[Enso](articles/funding/enso.md)" {
		t.Errorf("from the root: %s", got)
	}
}
