// Package markdown converts captured HTML pages into Markdown documents
// deterministically: the same page always gives the same bytes, with no
// model involved. A Document carries a YAML header with the page's URL,
// title, description, the site's own tags and keywords picked by TF-IDF
// across a set of documents.
package markdown

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// IsHTML reports whether a body with this content type is an HTML page.
func IsHTML(mime string) bool {
	mime = strings.ToLower(mime)
	return strings.Contains(mime, "text/html") || strings.Contains(mime, "application/xhtml")
}

// IsPage reports whether an HTML body is a whole page rather than a
// fragment, such as a client-side template: it has <html> or <title>.
func IsPage(body []byte) bool {
	head := body[:min(len(body), 4096)]
	lower := bytes.ToLower(head)
	return bytes.Contains(lower, []byte("<html")) || bytes.Contains(lower, []byte("<title"))
}

// Page is a converted HTML page.
type Page struct {
	Title       string
	Description string
	// Tags are the site's own labels for the page: meta keywords,
	// article:tag and tag links in the content.
	Tags []string
	// Body is the content as Markdown, without a header.
	Body string
}

// minMainText is the least text a <main> element must hold to stand in for
// the whole page; below it the page is rendered from <body>.
const minMainText = 500

// Convert renders an HTML page as Markdown. Scripts, styles, forms and
// hidden elements are dropped; headings, paragraphs, lists, tables, quotes,
// code, emphasis and links are kept. When the page has a <main> element
// with real content, only that is rendered. Relative links are resolved
// against pageURL.
func Convert(body []byte, pageURL string) Page {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return Page{}
	}
	base, _ := url.Parse(pageURL)

	root := find(doc, atom.Body)
	if m := find(doc, atom.Main); m != nil && len(collapse(textOf(m))) >= minMainText {
		root = m
	}
	if root == nil {
		root = doc
	}
	r := &renderer{base: base}
	r.walk(root)

	p := Page{
		Title:       meta(doc, "og:title"),
		Description: meta(doc, "description"),
		Body:        tidy(r.b.String()),
	}
	if p.Title == "" {
		if t := find(doc, atom.Title); t != nil {
			p.Title = collapse(textOf(t))
		}
	}
	if p.Description == "" {
		p.Description = meta(doc, "og:description")
	}
	p.Tags = siteTags(doc, root, base)
	return p
}

// skipped elements hold no readable content.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true,
	atom.Template: true, atom.Iframe: true, atom.Canvas: true, atom.Head: true,
	atom.Button: true, atom.Select: true, atom.Input: true, atom.Textarea: true,
}

// blocks start on a new paragraph.
var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Nav: true, atom.Aside: true,
	atom.Main: true, atom.Ul: true, atom.Ol: true, atom.Table: true,
	atom.Figure: true, atom.Figcaption: true, atom.Form: true, atom.Dl: true,
	atom.Dt: true, atom.Dd: true, atom.Address: true, atom.Details: true,
	atom.Summary: true, atom.Hr: true,
}

type renderer struct {
	b    strings.Builder
	base *url.URL
}

func (r *renderer) para() { r.b.WriteString("\n\n") }

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
		if s := r.inline(n); s != "" {
			level := int(n.Data[1] - '0')
			r.b.WriteString("\n\n" + strings.Repeat("#", level) + " " + s + "\n\n")
		}
	case atom.Br:
		r.b.WriteString("\n")
	case atom.Li:
		marker := "- "
		if p := n.Parent; p != nil && p.DataAtom == atom.Ol {
			marker = strconv.Itoa(itemIndex(n)) + ". "
		}
		r.b.WriteString("\n" + marker)
		r.children(n)
		r.b.WriteString("\n")
	case atom.Table:
		r.table(n)
	case atom.Blockquote:
		sub := &renderer{base: r.base}
		sub.children(n)
		if s := tidy(sub.b.String()); s != "" {
			r.b.WriteString("\n\n> " + strings.ReplaceAll(s, "\n", "\n> ") + "\n\n")
		}
	case atom.Pre:
		if s := strings.Trim(rawText(n), "\n"); strings.TrimSpace(s) != "" {
			r.b.WriteString("\n\n```\n" + s + "\n```\n\n")
		}
	case atom.Code:
		if s := collapse(rawText(n)); s != "" {
			r.b.WriteString("`" + s + "`")
		}
	case atom.B, atom.Strong:
		r.emphasis(n, "**")
	case atom.I, atom.Em:
		r.emphasis(n, "*")
	case atom.A:
		r.link(n)
	case atom.Img:
		if alt := collapse(attr(n, "alt")); alt != "" {
			r.b.WriteString(" [image: " + alt + "] ")
		}
	default:
		if blocks[n.DataAtom] {
			r.para()
			r.children(n)
			r.para()
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

// inline renders n's content on one line.
func (r *renderer) inline(n *html.Node) string {
	sub := &renderer{base: r.base}
	sub.children(n)
	return collapse(sub.b.String())
}

func (r *renderer) emphasis(n *html.Node, mark string) {
	if hasBlock(n) {
		r.children(n)
		return
	}
	s := r.inline(n)
	if s == "" {
		return
	}
	text := textOf(n)
	if strings.TrimLeft(text, " \t\r\n") != text {
		r.b.WriteString(" ")
	}
	r.b.WriteString(mark + s + mark)
	if strings.TrimRight(text, " \t\r\n") != text {
		r.b.WriteString(" ")
	}
}

// link writes an anchor as [text](url). An anchor that wraps block
// content, such as a whole article card, is rendered as its content
// followed by a link line named after its first heading.
func (r *renderer) link(n *html.Node) {
	href := r.resolve(attr(n, "href"))
	if href == "" {
		r.children(n)
		return
	}
	if hasBlock(n) {
		r.children(n)
		label := ""
		if h := firstHeading(n); h != nil {
			label = collapse(textOf(h))
		}
		if label == "" {
			label = truncate(collapse(textOf(n)), 80)
		}
		if label != "" {
			r.b.WriteString("\n\n[" + escapeLabel(label) + "](" + href + ")\n\n")
		}
		return
	}
	text := collapse(textOf(n))
	if text == "" {
		return
	}
	r.b.WriteString("[" + escapeLabel(text) + "](" + href + ")")
}

func (r *renderer) resolve(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if r.base != nil {
		u = r.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto" {
		return ""
	}
	return strings.ReplaceAll(u.String(), " ", "%20")
}

// table writes a pipe table. The first row is the header; a table without
// one gets an empty header so Markdown renderers still show it.
func (r *renderer) table(n *html.Node) {
	var rows [][]string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Tr {
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.DataAtom == atom.Td || c.DataAtom == atom.Th {
					cells = append(cells, strings.ReplaceAll(r.inline(c), "|", `\|`))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	if len(rows) == 0 {
		return
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	line := func(cells []string) string {
		for len(cells) < width {
			cells = append(cells, "")
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	sep := make([]string, width)
	for i := range sep {
		sep[i] = "---"
	}
	var b strings.Builder
	b.WriteString("\n\n" + line(rows[0]) + "\n" + line(sep) + "\n")
	for _, row := range rows[1:] {
		b.WriteString(line(row) + "\n")
	}
	b.WriteString("\n")
	r.b.WriteString(b.String())
}

func itemIndex(li *html.Node) int {
	i := 1
	if start, err := strconv.Atoi(attr(li.Parent, "start")); err == nil {
		i = start
	}
	for s := li.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode && s.DataAtom == atom.Li {
			i++
		}
	}
	return i
}

func hasBlock(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Li, atom.Tr, atom.Blockquote, atom.Pre:
			return true
		}
		if blocks[c.DataAtom] || hasBlock(c) {
			return true
		}
	}
	return false
}

func firstHeading(n *html.Node) *html.Node {
	for _, a := range []atom.Atom{atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6} {
		if h := find(n, a); h != nil {
			return h
		}
	}
	return nil
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

// meta returns the content of the first <meta> whose name or property is
// key.
func meta(doc *html.Node, key string) string {
	for _, v := range metas(doc, key) {
		return v
	}
	return ""
}

// metas returns the non-empty contents of every <meta> whose name or
// property is key, compared case-insensitively.
func metas(doc *html.Node, key string) []string {
	var out []string
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta {
			if strings.EqualFold(attr(n, "name"), key) || strings.EqualFold(attr(n, "property"), key) {
				if c := collapse(attr(n, "content")); c != "" {
					out = append(out, c)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return out
}

// maxSiteTags caps the site's tags kept for one page.
const maxSiteTags = 12

// siteTags collects the labels the site gives the page: article:tag and
// keywords meta tags anywhere, and rel=tag or /tag/ links in the rendered
// content (not in sidebars outside it).
func siteTags(doc, root *html.Node, base *url.URL) []string {
	var tags []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.Trim(collapse(t), "#{} ")
		key := strings.ToLower(t)
		if t == "" || seen[key] || len([]rune(t)) > 40 || len(tags) >= maxSiteTags {
			return
		}
		seen[key] = true
		tags = append(tags, t)
	}
	for _, t := range metas(doc, "article:tag") {
		add(t)
	}
	for _, key := range []string{"keywords", "news_keywords"} {
		for _, v := range metas(doc, key) {
			for _, t := range strings.Split(v, ",") {
				add(t)
			}
		}
	}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && (skipped[n.DataAtom] || hidden(n)) {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.A && isTagLink(n, base) {
			add(textOf(n))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return tags
}

func isTagLink(a *html.Node, base *url.URL) bool {
	for _, rel := range strings.Fields(attr(a, "rel")) {
		if strings.EqualFold(rel, "tag") {
			return true
		}
	}
	u, err := url.Parse(strings.TrimSpace(attr(a, "href")))
	if err != nil {
		return false
	}
	if base != nil {
		u = base.ResolveReference(u)
		if !strings.EqualFold(u.Host, base.Host) {
			return false
		}
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(segs) == 2 && (segs[0] == "tag" || segs[0] == "tags")
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
			return
		}
		if n.Type == html.ElementNode && (skipped[n.DataAtom] || hidden(n)) {
			return
		}
		if n.Type == html.ElementNode && (blocks[n.DataAtom] || n.DataAtom == atom.Br) {
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
		if n.Type == html.ElementNode && blocks[n.DataAtom] {
			b.WriteString(" ")
		}
	}
	visit(n)
	return b.String()
}

// rawText returns all text under n with its whitespace, for <pre>.
func rawText(n *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
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
	if strings.TrimLeft(s, " \t\r\n") != s {
		c = " " + c
	}
	if strings.TrimRight(s, " \t\r\n") != s {
		c += " "
	}
	return c
}

func escapeLabel(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// tidy trims and collapses each line outside code fences, drops a line
// that repeats the one before it, keeps list items and table rows on
// consecutive lines, and keeps at most one blank line between paragraphs.
func tidy(s string) string {
	var out []string
	prev, blank, fenced := "", false, false
	for _, line := range strings.Split(s, "\n") {
		if fenced {
			out = append(out, line)
			if strings.TrimSpace(line) == "```" {
				fenced = false
				prev = "```"
			}
			continue
		}
		line = collapse(line)
		if line == "" || line == "-" {
			blank = len(out) > 0
			continue
		}
		if line == prev && line != "```" {
			continue
		}
		if blank && !sameRun(prev, line) {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
		prev = line
		if line == "```" {
			fenced = true
		}
	}
	return strings.Join(out, "\n")
}

// sameRun reports whether two lines are items of one list, rows of one
// table or lines of one quote, which stay on consecutive lines.
func sameRun(a, b string) bool {
	numbered := func(s string) bool {
		i := strings.Index(s, ". ")
		_, err := strconv.Atoi(s[:max(i, 0)])
		return i > 0 && err == nil
	}
	return strings.HasPrefix(a, "- ") && strings.HasPrefix(b, "- ") ||
		numbered(a) && numbered(b) ||
		strings.HasPrefix(a, "|") && strings.HasPrefix(b, "|") ||
		strings.HasPrefix(a, ">") && strings.HasPrefix(b, ">")
}
