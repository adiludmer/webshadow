package generator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/capture"
)

// Capture tool limits.
const (
	defaultIndexPage = 50
	maxIndexPage     = 100
	maxIndexURL      = 200
	defaultBodyPage  = 4000
	maxBodyPage      = 8000
	maxRepeatSeqs    = 5
)

// CaptureTools returns the read-only tools over a parsed capture.
func CaptureTools(t *capture.Trace) []agent.Tool {
	return []agent.Tool{indexTool{t}, entryTool{t}}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid args: %v", err)
	}
	return nil
}

type indexTool struct{ trace *capture.Trace }

func (indexTool) Name() string { return "capture_index" }
func (indexTool) Usage() string {
	return fmt.Sprintf(`List the session's requests, one line each: seq, method, status, class, response size (readable text size for HTML pages), URL. `+
		`Requests with no body are hidden, and repeats of the same URL with the same body are folded into one line. `+
		`args: {"page": number (default 1), "page_size": number (default %d, max %d), `+
		`"classes": list of document|api|script|stylesheet|media|font|telemetry|unknown or ["all"] (default document, api, unknown), "host": substring, `+
		`"include_empty": bool (default false)}`,
		defaultIndexPage, maxIndexPage)
}

func (t indexTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Page         int      `json:"page"`
		PageSize     int      `json:"page_size"`
		Classes      []string `json:"classes"`
		Host         string   `json:"host"`
		IncludeEmpty bool     `json:"include_empty"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	f := capture.Filter{Host: args.Host}
	if slices.Contains(args.Classes, "all") {
		f.Classes = capture.Classes
	} else {
		for _, name := range args.Classes {
			c, ok := capture.ParseClass(strings.ToLower(strings.TrimSpace(name)))
			if !ok {
				return "", fmt.Errorf("unknown class %q", name)
			}
			f.Classes = append(f.Classes, c)
		}
	}
	rows, hidden, folded := indexRows(t.trace.Select(f), args.IncludeEmpty)
	size := args.PageSize
	if size <= 0 {
		size = defaultIndexPage
	}
	size = min(size, maxIndexPage)
	page := max(args.Page, 1)
	start := (page - 1) * size
	if len(rows) == 0 {
		if hidden > 0 {
			return fmt.Sprintf("no requests with a body match; %d without one are hidden (include_empty: true shows them)", hidden), nil
		}
		return "no requests match", nil
	}
	if start >= len(rows) {
		return fmt.Sprintf("page %d is past the end: %d matching requests, %d pages", page, len(rows), (len(rows)+size-1)/size), nil
	}
	end := min(start+size, len(rows))
	classes := f.Classes
	if len(classes) == 0 {
		classes = capture.DefaultIndexClasses
	}
	var b strings.Builder
	fmt.Fprintf(&b, "requests %d-%d of %d (classes: %s", start+1, end, len(rows), joinClasses(classes))
	if hidden > 0 {
		fmt.Fprintf(&b, "; %d with no body hidden", hidden)
	}
	if folded > 0 {
		fmt.Fprintf(&b, "; %d repeats folded", folded)
	}
	b.WriteString(")\n")
	for _, r := range rows[start:end] {
		e := r.entry
		u := capture.NormalizeURL(e.URL, capture.DefaultVolatileKeys)
		if len(u) > maxIndexURL {
			u = u[:maxIndexURL] + "..."
		}
		fmt.Fprintf(&b, "%d %s %d %s %d %s", e.Sequence, e.Method, e.Status, e.Class, len(e.Readable()), u)
		if n := len(r.repeats); n > 0 {
			fmt.Fprintf(&b, " (x%d, also %s)", n+1, joinSeqs(r.repeats))
		}
		b.WriteByte('\n')
	}
	if end < len(rows) {
		fmt.Fprintf(&b, "[next: page %d]", page+1)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// indexRow is one line of capture_index: an entry and the later entries that
// repeat it.
type indexRow struct {
	entry   capture.Entry
	repeats []int
}

// indexRows drops entries with no request or response body, other than
// redirects, unless includeEmpty is set, and folds entries with the same
// method, host, path, request body and response body into the first of
// them. The query is left out of the comparison because sites often add a
// cache-busting timestamp to it; with identical bodies the folded entries
// show nothing new.
func indexRows(entries []capture.Entry, includeEmpty bool) (rows []indexRow, hidden, folded int) {
	seen := map[[sha256.Size]byte]int{}
	for _, e := range entries {
		redirect := e.Status >= 300 && e.Status < 400
		if !includeEmpty && !redirect && len(e.ResponseBody) == 0 && len(e.RequestBody) == 0 {
			hidden++
			continue
		}
		h := sha256.New()
		fmt.Fprintf(h, "%s %s %s\x00", e.Method, strings.ToLower(e.Host), e.Path)
		h.Write(e.RequestBody)
		h.Write([]byte{0})
		h.Write(e.ResponseBody)
		var key [sha256.Size]byte
		h.Sum(key[:0])
		if i, ok := seen[key]; ok {
			rows[i].repeats = append(rows[i].repeats, e.Sequence)
			folded++
			continue
		}
		seen[key] = len(rows)
		rows = append(rows, indexRow{entry: e})
	}
	return rows, hidden, folded
}

func joinSeqs(seqs []int) string {
	parts := make([]string, 0, min(len(seqs), maxRepeatSeqs)+1)
	for _, s := range seqs[:min(len(seqs), maxRepeatSeqs)] {
		parts = append(parts, fmt.Sprint(s))
	}
	if len(seqs) > maxRepeatSeqs {
		parts = append(parts, fmt.Sprintf("and %d more", len(seqs)-maxRepeatSeqs))
	}
	return strings.Join(parts, ", ")
}

func joinClasses(cs []capture.Class) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ", ")
}

type entryTool struct{ trace *capture.Trace }

func (entryTool) Name() string { return "capture_entry" }
func (entryTool) Usage() string {
	return fmt.Sprintf(`Show one request: URL, status, content types and a page of a body. HTML pages are shown as readable text with links. `+
		`args: {"seq": number from capture_index, "part": "response" (default) or "request", "offset": byte offset into the body (default 0), "limit": bytes (default %d, max %d), "raw": bool, true shows HTML source (default false)}`,
		defaultBodyPage, maxBodyPage)
}

func (t entryTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	args := struct {
		Seq    *int   `json:"seq"`
		Part   string `json:"part"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
		Raw    bool   `json:"raw"`
	}{}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	if args.Seq == nil {
		return "", errors.New("seq is required")
	}
	if *args.Seq < 0 || *args.Seq >= len(t.trace.Entries) {
		return "", fmt.Errorf("seq %d does not exist; the session has requests 0-%d", *args.Seq, len(t.trace.Entries)-1)
	}
	e := t.trace.Entries[*args.Seq]

	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %s\nstatus %d, class %s\n", e.Sequence, e.Method, e.URL, e.Status, e.Class)
	var body []byte
	var mime string
	text := false
	switch args.Part {
	case "", "response":
		body, mime = e.ResponseBody, e.ResponseMIME
		text = e.Page != nil && !args.Raw
		if loc := e.ResponseHeader("Location"); loc != "" {
			fmt.Fprintf(&b, "location: %s\n", loc)
		}
	case "request":
		body, mime = e.RequestBody, e.RequestMIME
	default:
		return "", fmt.Errorf("part must be response or request, not %q", args.Part)
	}
	fmt.Fprintf(&b, "%s body: %s, %d bytes", partName(args.Part), orNone(mime), len(body))
	if text {
		body = []byte(e.Page.Body)
		fmt.Fprintf(&b, ", shown as %d bytes of readable text (raw: true for the HTML)", len(body))
	}
	b.WriteByte('\n')
	if len(body) == 0 {
		if args.Part != "request" && e.Status == 0 {
			b.WriteString("the capture has no response for this request")
			return b.String(), nil
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	if !utf8.Valid(body) {
		b.WriteString("[binary body not shown]")
		return b.String(), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = defaultBodyPage
	}
	limit = min(limit, maxBodyPage)
	offset := max(args.Offset, 0)
	if offset >= len(body) {
		fmt.Fprintf(&b, "offset %d is past the end of the body", offset)
		return b.String(), nil
	}
	// Keep the page on rune boundaries so multi-byte text is not split.
	for offset > 0 && !utf8.RuneStart(body[offset]) {
		offset--
	}
	end := min(offset+limit, len(body))
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	fmt.Fprintf(&b, "bytes %d-%d:\n%s", offset, end, body[offset:end])
	if end < len(body) {
		fmt.Fprintf(&b, "\n[more: offset %d]", end)
	}
	return b.String(), nil
}

func partName(p string) string {
	if p == "request" {
		return "request"
	}
	return "response"
}

func orNone(s string) string {
	if s == "" {
		return "no content type"
	}
	return s
}
