package capture

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// IsHTML reports whether a body with this content type is an HTML page.
func IsHTML(mime string) bool {
	mime = strings.ToLower(mime)
	return strings.Contains(mime, "text/html") || strings.Contains(mime, "application/xhtml")
}

// minMainText is the least text a <main> element must hold to stand in for
// the whole page; below it the page is rendered from <body>.
const minMainText = 500

// HTMLText renders an HTML page as Markdown-style text: the title, the
// meta description, then headings, paragraphs, list items, table rows and
// links, with scripts, styles and other non-content markup removed. When
// the page has a <main> element with real content, only that is rendered.
// Relative links are resolved against base.
func HTMLText(body []byte, base string) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	baseURL, _ := url.Parse(base)
	r := &renderer{base: baseURL}

	var head strings.Builder
	if t := find(doc, atom.Title); t != nil {
		if s := collapse(textOf(t)); s != "" {
			head.WriteString("# " + s + "\n\n")
		}
	}
	if d := metaDescription(doc); d != "" {
		head.WriteString(d + "\n\n")
	}

	root := find(doc, atom.Body)
	if m := find(doc, atom.Main); m != nil && len(collapse(textOf(m))) >= minMainText {
		root = m
	}
	if root == nil {
		root = doc
	}
	r.walk(root)
	return strings.TrimSpace(head.String() + tidy(r.b.String()))
}

// skipped elements hold no readable content.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true,
	atom.Template: true, atom.Iframe: true, atom.Canvas: true, atom.Head: true,
	atom.Button: true, atom.Select: true, atom.Input: true, atom.Textarea: true,
}

// blocks start on a new line.
var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Nav: true, atom.Aside: true,
	atom.Main: true, atom.Ul: true, atom.Ol: true, atom.Table: true,
	atom.Blockquote: true, atom.Figure: true, atom.Figcaption: true,
	atom.Form: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Pre: true, atom.Address: true, atom.Time: false,
}

type renderer struct {
	b    strings.Builder
	base *url.URL
}

func (r *renderer) newline() { r.b.WriteString("\n") }

func (r *renderer) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		r.b.WriteString(collapseKeepEdges(n.Data))
		return
	case html.ElementNode:
	default:
		r.children(n)
		return
	}
	if skipped[n.DataAtom] || hidden(n) {
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if s := collapse(textOf(n)); s != "" {
			level := int(n.Data[1] - '0')
			r.b.WriteString("\n\n" + strings.Repeat("#", level) + " " + s + "\n\n")
		}
	case atom.Br:
		r.newline()
	case atom.Li:
		r.b.WriteString("\n- ")
		r.children(n)
		r.newline()
	case atom.Tr:
		var cells []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Td || c.DataAtom == atom.Th {
				cells = append(cells, collapse(textOf(c)))
			}
		}
		r.b.WriteString("\n| " + strings.Join(cells, " | ") + " |\n")
	case atom.A:
		r.link(n)
	case atom.Img:
		if alt := collapse(attr(n, "alt")); alt != "" {
			r.b.WriteString(" [image: " + alt + "] ")
		}
	default:
		if blocks[n.DataAtom] {
			r.b.WriteString("\n\n")
			r.children(n)
			r.b.WriteString("\n\n")
			return
		}
		r.children(n)
	}
}

func (r *renderer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.walk(c)
	}
}

// link writes an anchor as [text](url). Anchors that hold block content,
// such as whole article cards, are rendered as their content followed by
// the link, so headings inside them keep their line.
func (r *renderer) link(n *html.Node) {
	href := r.resolve(attr(n, "href"))
	text := collapse(textOf(n))
	if href == "" || text == "" {
		r.children(n)
		return
	}
	if hasBlock(n) {
		r.children(n)
		r.b.WriteString("\n(link: " + href + ")\n")
		return
	}
	r.b.WriteString("[" + text + "](" + href + ")")
}

func (r *renderer) resolve(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if r.base != nil {
		u = r.base.ResolveReference(u)
	}
	return u.String()
}

func hasBlock(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			switch c.DataAtom {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Li, atom.Tr:
				return true
			}
			if blocks[c.DataAtom] || hasBlock(c) {
				return true
			}
		}
	}
	return false
}

func hidden(n *html.Node) bool {
	if _, ok := attrOK(n, "hidden"); ok {
		return true
	}
	if strings.EqualFold(attr(n, "aria-hidden"), "true") {
		return true
	}
	style := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", "")
	return strings.Contains(style, "display:none")
}

func metaDescription(doc *html.Node) string {
	var desc string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if desc != "" {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta {
			name := strings.ToLower(attr(n, "name") + attr(n, "property"))
			if name == "description" || name == "og:description" {
				desc = collapse(attr(n, "content"))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return desc
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

// textOf returns the readable text under n, skipping non-content elements.
func textOf(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteString(" ")
			return
		}
		if n.Type == html.ElementNode && (skipped[n.DataAtom] || hidden(n)) {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// collapse joins whitespace runs into single spaces and trims the ends.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// collapseKeepEdges collapses whitespace but keeps one space at either end
// if there was any, so adjacent inline text stays separated.
func collapseKeepEdges(s string) string {
	c := collapse(s)
	if c == "" {
		if s != "" {
			return " "
		}
		return ""
	}
	if s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r' {
		c = " " + c
	}
	if last := s[len(s)-1]; last == ' ' || last == '\n' || last == '\t' || last == '\r' {
		c += " "
	}
	return c
}

// tidy trims each line and collapses spaces in it, drops a line that repeats the one before it, and
// keeps at most one blank line between paragraphs.
func tidy(s string) string {
	var out []string
	prev, blank := "", false
	for _, line := range strings.Split(s, "\n") {
		line = collapse(line)
		if line == "" || line == "-" {
			blank = len(out) > 0
			continue
		}
		if line == prev {
			continue
		}
		if blank && !sameRun(prev, line) {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
		prev = line
	}
	return strings.Join(out, "\n")
}

// sameRun reports whether two lines are items of one list or rows of one
// table, which stay on consecutive lines.
func sameRun(a, b string) bool {
	return strings.HasPrefix(a, "- ") && strings.HasPrefix(b, "- ") ||
		strings.HasPrefix(a, "|") && strings.HasPrefix(b, "|")
}
