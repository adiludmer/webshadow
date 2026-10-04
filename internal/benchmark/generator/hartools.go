package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/har"
)

// HAR tool limits.
const (
	defaultIndexPage = 50
	maxIndexPage     = 100
	maxIndexURL      = 200
	defaultBodyPage  = 4000
	maxBodyPage      = 8000
)

// HARTools returns the read-only tools over a parsed HAR.
func HARTools(t *har.Trace) []agent.Tool {
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

type indexTool struct{ trace *har.Trace }

func (indexTool) Name() string { return "har_index" }
func (indexTool) Usage() string {
	return fmt.Sprintf(`List the session's requests, one line each: seq, method, status, class, response body size, URL. `+
		`args: {"page": number (default 1), "page_size": number (default %d, max %d), `+
		`"classes": list of document|api|script|stylesheet|media|font|telemetry|unknown or ["all"] (default document, api, unknown), "host": substring}`,
		defaultIndexPage, maxIndexPage)
}

func (t indexTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Page     int      `json:"page"`
		PageSize int      `json:"page_size"`
		Classes  []string `json:"classes"`
		Host     string   `json:"host"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	f := har.Filter{Host: args.Host}
	if slices.Contains(args.Classes, "all") {
		f.Classes = har.Classes
	} else {
		for _, name := range args.Classes {
			c, ok := har.ParseClass(strings.ToLower(strings.TrimSpace(name)))
			if !ok {
				return "", fmt.Errorf("unknown class %q", name)
			}
			f.Classes = append(f.Classes, c)
		}
	}
	entries := t.trace.Select(f)
	size := args.PageSize
	if size <= 0 {
		size = defaultIndexPage
	}
	size = min(size, maxIndexPage)
	page := max(args.Page, 1)
	start := (page - 1) * size
	if len(entries) == 0 {
		return "no requests match", nil
	}
	if start >= len(entries) {
		return fmt.Sprintf("page %d is past the end: %d matching requests, %d pages", page, len(entries), (len(entries)+size-1)/size), nil
	}
	end := min(start+size, len(entries))
	classes := f.Classes
	if len(classes) == 0 {
		classes = har.DefaultIndexClasses
	}
	var b strings.Builder
	fmt.Fprintf(&b, "requests %d-%d of %d (classes: %s)\n", start+1, end, len(entries), joinClasses(classes))
	for _, e := range entries[start:end] {
		u := har.NormalizeURL(e.URL, har.DefaultVolatileKeys)
		if len(u) > maxIndexURL {
			u = u[:maxIndexURL] + "..."
		}
		fmt.Fprintf(&b, "%d %s %d %s %d %s\n", e.Sequence, e.Method, e.Status, e.Class, len(e.ResponseBody), u)
	}
	if end < len(entries) {
		fmt.Fprintf(&b, "[next: page %d]", page+1)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func joinClasses(cs []har.Class) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ", ")
}

type entryTool struct{ trace *har.Trace }

func (entryTool) Name() string { return "har_entry" }
func (entryTool) Usage() string {
	return fmt.Sprintf(`Show one request: URL, status, content types and a page of a body. `+
		`args: {"seq": number from har_index, "part": "response" (default) or "request", "offset": byte offset into the body (default 0), "limit": bytes (default %d, max %d)}`,
		defaultBodyPage, maxBodyPage)
}

func (t entryTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	args := struct {
		Seq    *int   `json:"seq"`
		Part   string `json:"part"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
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
	switch args.Part {
	case "", "response":
		body, mime = e.ResponseBody, e.ResponseMIME
		if loc := e.ResponseHeader("Location"); loc != "" {
			fmt.Fprintf(&b, "location: %s\n", loc)
		}
	case "request":
		body, mime = e.RequestBody, e.RequestMIME
	default:
		return "", fmt.Errorf("part must be response or request, not %q", args.Part)
	}
	fmt.Fprintf(&b, "%s body: %s, %d bytes\n", partName(args.Part), orNone(mime), len(body))
	if len(body) == 0 {
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
