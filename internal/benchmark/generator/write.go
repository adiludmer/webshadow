package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

// Write limits keep one generator from filling the disk.
const (
	MaxFileBytes = 64 << 10
	MaxFiles     = 500
	MaxTreeBytes = 8 << 20
)

// writeTool creates or replaces Markdown files under the tree root.
// os.Root keeps every write inside the tree, symlinks included.
type writeTool struct {
	root *os.Root

	mu     sync.Mutex
	sizes  map[string]int    // bytes per file written so far
	placed map[string]string // path to the id of the document placed there
}

func newWriteTool(root *os.Root) *writeTool {
	return &writeTool{root: root, sizes: map[string]int{}, placed: map[string]string{}}
}

func (*writeTool) Name() string { return "write" }
func (*writeTool) Usage() string {
	return fmt.Sprintf(`Create or replace a Markdown file in the shadow tree; parent directories are created. `+
		`args: {"path": relative path ending in .md, "content": string (max %d KB)}`, MaxFileBytes>>10)
}

func (t *writeTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(raw, &args); err != nil {
		return "", err
	}
	p, err := checkWritePath(args.Path)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id := t.placed[p]; id != "" {
		return "", fmt.Errorf("%s holds document %s; write somewhere else or place %s at another path first", p, id, id)
	}
	if len(args.Content) > MaxFileBytes {
		return "", fmt.Errorf("content is %d bytes; files are limited to %d", len(args.Content), MaxFileBytes)
	}
	return t.save(p, args.Content)
}

// save writes content to the checked path p within the file count and tree
// size limits. The caller holds t.mu and checks the per-file limit, which
// applies to what the model writes, not to converted documents.
func (t *writeTool) save(p, content string) (string, error) {
	prev, exists := t.sizes[p]
	if !exists && len(t.sizes) >= MaxFiles {
		return "", fmt.Errorf("the tree already has %d files, the limit", MaxFiles)
	}
	total := len(content) - prev
	for _, n := range t.sizes {
		total += n
	}
	if total > MaxTreeBytes {
		return "", fmt.Errorf("this write would bring the tree to %d bytes; the limit is %d", total, MaxTreeBytes)
	}

	if i := strings.LastIndexByte(p, '/'); i > 0 {
		if err := mkdirAll(t.root, p[:i]); err != nil {
			return "", err
		}
	}
	f, err := t.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	t.sizes[p] = len(content)
	verb := "wrote"
	if exists {
		verb = "replaced"
	}
	return fmt.Sprintf("%s %s (%d bytes); the tree has %d files", verb, p, len(content), len(t.sizes)), nil
}

// remove deletes the file at p, if the tree has one. The caller holds t.mu.
func (t *writeTool) remove(p string) error {
	if _, ok := t.sizes[p]; !ok {
		return nil
	}
	if err := t.root.Remove(p); err != nil {
		return err
	}
	delete(t.sizes, p)
	delete(t.placed, p)
	return nil
}

// checkWritePath accepts relative paths ending in .md that stay inside
// the tree.
func checkWritePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("path must be relative to the tree root")
	}
	p, err := shadow.CleanPath(raw)
	if err != nil {
		return "", err
	}
	if p == "." || !strings.HasSuffix(strings.ToLower(p), ".md") {
		return "", errors.New("only Markdown files (.md) can be written")
	}
	return p, nil
}

// mkdirAll creates dir and its parents inside root.
func mkdirAll(root *os.Root, dir string) error {
	parts := strings.Split(dir, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		if err := root.Mkdir(sub, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}
