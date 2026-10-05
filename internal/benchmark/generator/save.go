package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/adiludmer/webshadow/internal/benchmark/capture"
)

// saveTool copies a response into the tree verbatim, so the full text of
// an article lands in the shadow without the model retyping it. HTML pages
// are saved as their readable text.
type saveTool struct {
	trace *capture.Trace
	w     *writeTool
}

// maxSaveText leaves room in a file for the heading and the note.
const maxSaveText = MaxFileBytes - 4<<10

func (saveTool) Name() string { return "save_entry" }
func (saveTool) Usage() string {
	return `Save a response's full text into a Markdown file, exactly as captured (HTML pages as readable text). ` +
		`Use it to keep whole articles and pages instead of retyping them. ` +
		`args: {"seq": number from capture_index, "path": relative path ending in .md, "title": heading for the file, ` +
		`"note": optional Markdown written above the text, "offset": byte offset into the text for a continuation file (default 0)}`
}

func (t saveTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Seq    *int   `json:"seq"`
		Path   string `json:"path"`
		Title  string `json:"title"`
		Note   string `json:"note"`
		Offset int    `json:"offset"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	if args.Seq == nil {
		return "", errors.New("seq is required")
	}
	if *args.Seq < 0 || *args.Seq >= len(t.trace.Entries) {
		return "", fmt.Errorf("seq %d does not exist; the session has requests 0-%d", *args.Seq, len(t.trace.Entries)-1)
	}
	p, err := checkWritePath(args.Path)
	if err != nil {
		return "", err
	}
	e := t.trace.Entries[*args.Seq]
	text := e.Readable()
	if len(text) == 0 {
		return "", fmt.Errorf("seq %d has no response body to save", e.Sequence)
	}
	if !utf8.Valid(text) {
		return "", fmt.Errorf("seq %d has a binary response, which cannot be saved", e.Sequence)
	}
	offset := max(args.Offset, 0)
	if offset >= len(text) {
		return "", fmt.Errorf("offset %d is past the end of the %d-byte text", offset, len(text))
	}
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	end := min(offset+maxSaveText, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}

	body := string(text[offset:end])
	if e.ResponseText == "" && strings.Contains(strings.ToLower(e.ResponseMIME), "json") {
		body = "```json\n" + body + "\n```"
	}
	var b strings.Builder
	if title := strings.TrimSpace(args.Title); title != "" {
		if offset > 0 {
			title += " (continued)"
		}
		b.WriteString("# " + title + "\n\n")
	}
	fmt.Fprintf(&b, "Source: %s\n\n", e.URL)
	if note := strings.TrimSpace(args.Note); note != "" && offset == 0 {
		b.WriteString(note + "\n\n")
	}
	b.WriteString(body + "\n")

	out, err := t.w.save(p, b.String())
	if err != nil {
		return "", err
	}
	out = fmt.Sprintf("saved text bytes %d-%d of %d from seq %d: %s", offset, end, len(text), e.Sequence, out)
	if end < len(text) {
		out += fmt.Sprintf("; the rest did not fit, save it to another file with offset %d", end)
	}
	return out, nil
}
