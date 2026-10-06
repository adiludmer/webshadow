package generator

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/adiludmer/webshadow/internal/benchmark/capture"
	"github.com/adiludmer/webshadow/internal/markdown"
)

// doc is one captured HTML page converted to Markdown. The conversion is
// deterministic; the generator only decides where each doc goes.
type doc struct {
	ID  string // d1, d2, ... in capture order
	Seq int    // the capture entry it came from
	markdown.Document
}

// buildDocs converts every whole HTML page the capture answered with 200
// into a doc. A URL captured more than once becomes one doc, from the
// response with the most text. Keywords are picked across all docs.
func buildDocs(t *capture.Trace) []*doc {
	var docs []*doc
	byKey := map[string]*doc{}
	for i := range t.Entries {
		e := &t.Entries[i]
		if e.Page == nil || e.Status != 200 || strings.TrimSpace(e.Page.Body) == "" || !markdown.IsPage(e.ResponseBody) {
			continue
		}
		d := &doc{Seq: e.Sequence, Document: markdown.Document{
			URL:         e.URL,
			Title:       e.Page.Title,
			Description: e.Page.Description,
			Captured:    e.StartedAt,
			Tags:        e.Page.Tags,
			Body:        e.Page.Body,
		}}
		key := urlKey(e.URL)
		if prev, ok := byKey[key]; ok {
			if len(d.Body) > len(prev.Body) {
				id := prev.ID
				*prev = *d
				prev.ID = id
			}
			continue
		}
		d.ID = fmt.Sprintf("d%d", len(docs)+1)
		byKey[key] = d
		docs = append(docs, d)
	}
	ds := make([]*markdown.Document, len(docs))
	for i, d := range docs {
		ds[i] = &d.Document
	}
	markdown.AddKeywords(ds, markdown.DefaultKeywords)
	return docs
}

// urlKey identifies a page by URL: lowercase scheme and host, no fragment,
// no trailing slash on the path. The query is kept.
func urlKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = strings.TrimSuffix(u.RawPath, "/")
	return u.String()
}

// maxSlugRunes bounds a file name made from a URL.
const maxSlugRunes = 80

// slugPath names the file an unplaced doc gets: pages/<slug>.md, under a
// host folder when the capture has more than one host. taken reports
// paths already in use; a clash gets a -2, -3, ... suffix.
func slugPath(d *doc, multiHost bool, taken func(string) bool) string {
	u, err := url.Parse(d.URL)
	slug, host := "", ""
	if err == nil {
		host = strings.ToLower(u.Hostname())
		p := u.Path
		if dec, err := url.PathUnescape(u.EscapedPath()); err == nil {
			p = dec
		}
		slug = slugify(strings.Trim(p, "/") + " " + u.RawQuery)
	}
	if slug == "" {
		slug = "home"
	}
	dir := "pages"
	if multiHost && host != "" {
		dir = path.Join(dir, slugify(host))
	}
	p := path.Join(dir, slug+".md")
	for i := 2; taken(p); i++ {
		p = path.Join(dir, fmt.Sprintf("%s-%d.md", slug, i))
	}
	return p
}

// slugify keeps letters, digits, dots and underscores, joins everything
// else into single dashes and lowercases the result.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := strings.Trim(b.String(), ".")
	if r := []rune(out); len(r) > maxSlugRunes {
		out = strings.TrimRight(string(r[:maxSlugRunes]), "-.")
	}
	return out
}

// mdLink matches the target of a Markdown link or image.
var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// rewriteLinks points links to captured pages at their files in the tree.
// from is the path of the file being rewritten; targets maps urlKey of a
// page URL to its tree path. Other links are left alone.
func rewriteLinks(text, from string, targets map[string]string) string {
	return mdLink.ReplaceAllStringFunc(text, func(m string) string {
		target := m[2 : len(m)-1]
		to, ok := targets[urlKey(target)]
		if !ok || to == from {
			return m
		}
		rel := relPath(path.Dir(from), to)
		if strings.ContainsAny(rel, " ()") {
			rel = "<" + rel + ">"
		}
		return "](" + rel + ")"
	})
}

// relPath returns the slash path to target from directory dir, both
// relative to the tree root.
func relPath(dir, target string) string {
	if dir == "." {
		return target
	}
	from := strings.Split(dir, "/")
	to := strings.Split(target, "/")
	i := 0
	for i < len(from) && i < len(to)-1 && from[i] == to[i] {
		i++
	}
	return strings.Repeat("../", len(from)-i) + strings.Join(to[i:], "/")
}
