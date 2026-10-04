package reader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

// Tool limits, so one call cannot flood the context.
const (
	maxListEntries  = 300
	defaultReadLine = 200
	maxReadLines    = 400
	maxMatches      = 40
	maxMatchLine    = 240
)

// Tools returns the read-only tools a reader gets over a shadow tree.
func Tools(tree fs.FS) []agent.Tool {
	return []agent.Tool{listTool{tree}, readTool{tree}, searchTool{tree}}
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

type listTool struct{ tree fs.FS }

func (listTool) Name() string { return "list" }
func (listTool) Usage() string {
	return `List a directory of the shadow tree. args: {"path": string (default "."), "recursive": bool (default false)}`
}

func (t listTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	dir, err := shadow.CleanPath(args.Path)
	if err != nil {
		return "", err
	}
	var lines []string
	add := func(p string, d fs.DirEntry) {
		if d.IsDir() {
			lines = append(lines, p+"/")
			return
		}
		if info, err := d.Info(); err == nil {
			lines = append(lines, fmt.Sprintf("%s (%d bytes)", p, info.Size()))
		}
	}
	if args.Recursive {
		err = fs.WalkDir(t.tree, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == dir {
				return nil
			}
			add(p, d)
			return nil
		})
	} else {
		var entries []fs.DirEntry
		entries, err = fs.ReadDir(t.tree, dir)
		for _, d := range entries {
			add(joinPath(dir, d.Name()), d)
		}
	}
	if err != nil {
		return "", notFound(err, args.Path)
	}
	if len(lines) == 0 {
		return "(empty directory)", nil
	}
	more := ""
	if len(lines) > maxListEntries {
		more = fmt.Sprintf("\n[%d more entries not shown]", len(lines)-maxListEntries)
		lines = lines[:maxListEntries]
	}
	return strings.Join(lines, "\n") + more, nil
}

type readTool struct{ tree fs.FS }

func (readTool) Name() string { return "read" }
func (readTool) Usage() string {
	return fmt.Sprintf(`Read lines of a file. args: {"path": string, "offset": first line, 1-based (default 1), "limit": number of lines (default %d, max %d)}`, defaultReadLine, maxReadLines)
}

func (t readTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", errors.New("path is required")
	}
	p, err := shadow.CleanPath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := fs.ReadFile(t.tree, p)
	if err != nil {
		return "", notFound(err, args.Path)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	offset := max(args.Offset, 1)
	limit := args.Limit
	if limit <= 0 {
		limit = defaultReadLine
	}
	limit = min(limit, maxReadLines)
	if offset > len(lines) {
		return fmt.Sprintf("%s has %d lines; offset %d is past the end", p, len(lines), offset), nil
	}
	end := min(offset-1+limit, len(lines))
	header := fmt.Sprintf("%s (lines %d-%d of %d)\n", p, offset, end, len(lines))
	return header + strings.Join(lines[offset-1:end], "\n"), nil
}

type searchTool struct{ tree fs.FS }

func (searchTool) Name() string { return "search" }
func (searchTool) Usage() string {
	return `Find lines containing text, ignoring case, in every file under a directory. args: {"query": string, "path": string (default ".")}`
}

func (t searchTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	if query == "" {
		return "", errors.New("query is required")
	}
	dir, err := shadow.CleanPath(args.Path)
	if err != nil {
		return "", err
	}
	var matches []string
	total := 0
	err = fs.WalkDir(t.tree, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := t.tree.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			total++
			if len(matches) < maxMatches {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", p, n, clip(strings.TrimSpace(line), maxMatchLine)))
			}
		}
		return sc.Err()
	})
	if err != nil {
		return "", notFound(err, args.Path)
	}
	if total == 0 {
		return fmt.Sprintf("no lines contain %q", args.Query), nil
	}
	out := strings.Join(matches, "\n")
	if total > len(matches) {
		out += fmt.Sprintf("\n[%d more matches not shown]", total-len(matches))
	}
	return out, nil
}

func joinPath(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

func notFound(err error, p string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q does not exist", p)
	}
	return err
}
