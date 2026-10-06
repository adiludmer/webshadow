package markdown

import (
	"strings"
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	got := strings.Join(Tokens("The [Enso](https://x.test/enso-slug) startup raised 15 million, של OpenAI ב-AI!"), " ")
	if got != "enso startup raised million openai ai" {
		t.Errorf("tokens: %q", got)
	}
}

func TestKeywords(t *testing.T) {
	docs := [][]string{
		Tokens("menu menu enso enso enso security security"),
		Tokens("menu menu jev jev model model model"),
		Tokens("menu menu קפה קפה הקפה ראיון"),
	}
	got := Keywords(docs, 3)
	if strings.Join(got[0], ",") != "enso,security" {
		t.Errorf("doc 0: %v (menu is in every doc and must score zero)", got[0])
	}
	if strings.Join(got[1], ",") != "model,jev" {
		t.Errorf("doc 1: %v", got[1])
	}
	if strings.Join(got[2], ",") != "קפה" {
		t.Errorf("doc 2: %v (הקפה should fold into קפה)", got[2])
	}
	single := Keywords([][]string{Tokens("alpha alpha beta beta beta gamma")}, 5)
	if strings.Join(single[0], ",") != "beta,alpha" {
		t.Errorf("single doc: %v", single[0])
	}
}

func TestRender(t *testing.T) {
	d := &Document{
		URL:         "https://a.test/enso/",
		Title:       "Enso: raises $15M",
		Description: "A Series A.",
		Captured:    time.Date(2026, 10, 5, 7, 14, 42, 0, time.FixedZone("IDT", 3*3600)),
		Tags:        []string{"Funding", "Series A"},
		Body:        "# Enso\n\nText.\n\n",
	}
	docs := []*Document{d, {URL: "https://a.test/other/", Body: "other words"}}
	d.Body += "series series enso enso funding funding"
	AddKeywords(docs, 3)
	want := "---\nurl: https://a.test/enso/\ntitle: 'Enso: raises $15M'\ndescription: A Series A.\ncaptured: 2026-10-05 07:14:42 IDT\n" +
		"tags: [Funding, Series A]\nkeywords: [enso, series]\n---\n\n# Enso\n\nText.\n\nseries series enso enso funding funding\n"
	if got := d.Render(); got != want {
		t.Errorf("render:\n%s\nwant:\n%s", got, want)
	}
}
