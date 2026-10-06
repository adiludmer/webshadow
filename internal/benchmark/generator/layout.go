package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
)

// layout holds the converted documents and where the generator put them.
// The generator decides paths only; a document's content is fixed.
type layout struct {
	docs  []*doc
	byID  map[string]*doc
	paths map[string]string // doc id to tree path
	w     *writeTool
}

func newLayout(docs []*doc, w *writeTool) *layout {
	l := &layout{docs: docs, byID: map[string]*doc{}, paths: map[string]string{}, w: w}
	for _, d := range docs {
		l.byID[d.ID] = d
	}
	return l
}

func (l *layout) tools() []agent.Tool {
	return []agent.Tool{documentsTool{l}, documentTool{l}, placeTool{l}}
}

func (l *layout) doc(id string) (*doc, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("id is required")
	}
	d, ok := l.byID[id]
	if !ok {
		return nil, fmt.Errorf("no document %q; ids run d1-d%d", id, len(l.docs))
	}
	return d, nil
}

// Layout limits.
const (
	docsPageSize   = 30
	maxDocListTags = 6
)

type documentsTool struct{ l *layout }

func (documentsTool) Name() string { return "documents" }
func (documentsTool) Usage() string {
	return `List the converted pages, one line each: id, size, where it is placed (or "unplaced"), URL, title, tags and keywords. ` +
		fmt.Sprintf(`args: {"page": number (default 1, %d per page)}`, docsPageSize)
}

func (t documentsTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Page int `json:"page"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	if len(t.l.docs) == 0 {
		return "the capture has no HTML pages to place", nil
	}
	page := max(args.Page, 1)
	start := (page - 1) * docsPageSize
	if start >= len(t.l.docs) {
		return "", fmt.Errorf("page %d is past the end; there are %d pages of documents", page, (len(t.l.docs)+docsPageSize-1)/docsPageSize)
	}
	end := min(start+docsPageSize, len(t.l.docs))
	t.l.w.mu.Lock()
	defer t.l.w.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "documents %d-%d of %d\n", start+1, end, len(t.l.docs))
	for _, d := range t.l.docs[start:end] {
		b.WriteString(t.l.line(d))
		b.WriteByte('\n')
	}
	if end < len(t.l.docs) {
		fmt.Fprintf(&b, "[next: page %d]", page+1)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// line describes one doc for the documents tool and the task message.
func (l *layout) line(d *doc) string {
	where := "unplaced"
	if p, ok := l.paths[d.ID]; ok {
		where = "at " + p
	}
	s := fmt.Sprintf("%s %s %s %s | %s", d.ID, size(len(d.Body)), where, displayURL(d.URL), d.Title)
	if len(d.Tags) > 0 {
		s += " | tags: " + strings.Join(d.Tags[:min(len(d.Tags), maxDocListTags)], ", ")
	}
	if len(d.Keywords) > 0 {
		s += " | keywords: " + strings.Join(d.Keywords, ", ")
	}
	return s
}

func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

// displayURL unescapes the path so non-Latin slugs are readable.
func displayURL(raw string) string {
	if s, err := url.PathUnescape(raw); err == nil && utf8.ValidString(s) {
		return s
	}
	return raw
}

type documentTool struct{ l *layout }

func (documentTool) Name() string { return "document" }
func (documentTool) Usage() string {
	return fmt.Sprintf(`Read a converted page as it will appear in the tree: a YAML header (url, title, description, tags, keywords), then the Markdown body. `+
		`args: {"id": document id, "offset": byte offset (default 0), "limit": bytes (default %d, max %d)}`, defaultBodyPage, maxBodyPage)
}

func (t documentTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID     string `json:"id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	d, err := t.l.doc(args.ID)
	if err != nil {
		return "", err
	}
	text := d.Render()
	limit := args.Limit
	if limit <= 0 {
		limit = defaultBodyPage
	}
	limit = min(limit, maxBodyPage)
	offset := max(args.Offset, 0)
	if offset >= len(text) {
		return fmt.Sprintf("offset %d is past the end of %s (%d bytes)", offset, d.ID, len(text)), nil
	}
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	end := min(offset+limit, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	out := fmt.Sprintf("%s, bytes %d-%d of %d:\n%s", d.ID, offset, end, len(text), text[offset:end])
	if end < len(text) {
		out += fmt.Sprintf("\n[more: offset %d]", end)
	}
	return out, nil
}

type placeTool struct{ l *layout }

func (placeTool) Name() string { return "place" }
func (placeTool) Usage() string {
	return `Put a converted page into the tree at a path, unchanged. Placing it again moves it. ` +
		`Pages you do not place are filed under pages/ when you finish. args: {"id": document id, "path": relative path ending in .md}`
}

func (t placeTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	d, err := t.l.doc(args.ID)
	if err != nil {
		return "", err
	}
	p, err := checkWritePath(args.Path)
	if err != nil {
		return "", err
	}
	return t.l.place(d, p)
}

func (l *layout) place(d *doc, p string) (string, error) {
	w := l.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if id := w.placed[p]; id != "" && id != d.ID {
		return "", fmt.Errorf("%s already holds document %s", p, id)
	}
	if _, ok := w.sizes[p]; ok && w.placed[p] == "" {
		return "", fmt.Errorf("%s is a file you wrote; pick another path", p)
	}
	old, moved := l.paths[d.ID]
	if moved && old != p {
		if err := w.remove(old); err != nil {
			return "", err
		}
	}
	if _, err := w.save(p, d.Render()); err != nil {
		return "", err
	}
	w.placed[p] = d.ID
	l.paths[d.ID] = p
	if moved && old != p {
		return fmt.Sprintf("moved %s from %s to %s; the tree has %d files", d.ID, old, p, len(w.sizes)), nil
	}
	return fmt.Sprintf("placed %s at %s (%d bytes); the tree has %d files", d.ID, p, len(d.Render()), len(w.sizes)), nil
}

// DocumentStats counts what happened to the converted pages.
type DocumentStats struct {
	Total      int // pages converted from the capture
	Placed     int // placed by the generator
	AutoPlaced int // filed under pages/ at the end
	Skipped    int // not filed because the tree hit a limit
}

// finish files every unplaced doc under pages/, then points links between
// captured pages, in placed docs and in written files alike, at their
// files in the tree.
func (l *layout) finish() (DocumentStats, error) {
	w := l.w
	w.mu.Lock()
	defer w.mu.Unlock()
	stats := DocumentStats{Total: len(l.docs), Placed: len(l.paths)}

	hosts := map[string]bool{}
	for _, d := range l.docs {
		if u, err := url.Parse(d.URL); err == nil {
			hosts[strings.ToLower(u.Hostname())] = true
		}
	}
	taken := func(p string) bool { _, ok := w.sizes[p]; return ok }
	for _, d := range l.docs {
		if _, ok := l.paths[d.ID]; ok {
			continue
		}
		p := slugPath(d, len(hosts) > 1, taken)
		if _, err := w.save(p, d.Render()); err != nil {
			stats.Skipped++
			continue
		}
		w.placed[p] = d.ID
		l.paths[d.ID] = p
		stats.AutoPlaced++
	}

	targets := map[string]string{}
	for id, p := range l.paths {
		targets[urlKey(l.byID[id].URL)] = p
	}
	for p := range w.sizes {
		var content string
		if id := w.placed[p]; id != "" {
			d := l.byID[id]
			content = d.RenderBody(rewriteLinks(d.Body, p, targets))
		} else {
			old, err := readFile(w, p)
			if err != nil {
				return stats, err
			}
			content = rewriteLinks(old, p, targets)
		}
		if err := overwrite(w, p, content); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func readFile(w *writeTool, p string) (string, error) {
	f, err := w.root.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

// overwrite replaces a file's content without the write limits, which the
// file already passed before its links were rewritten.
func overwrite(w *writeTool, p, content string) error {
	f, err := w.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	w.sizes[p] = len(content)
	return f.Close()
}
