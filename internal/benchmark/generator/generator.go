// Package generator runs the generate stage: an agent that reads a
// sanitized proxy capture through paged tools and writes a shadow tree of Markdown
// files. It never sees the scenario's goal, so it has to capture whatever
// the session revealed.
package generator

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/capture"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/reader"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

// PromptVersion names the instructions below; records carry it.
const PromptVersion = "generator-v6"

const instructions = `You turn a recorded browsing session (a proxy capture of every request and
response) into a shadow tree: a
folder of Markdown files that describes what the site showed and served.
Later, another agent with no web access will answer questions about this
site using only your files. You do not know what those questions will be,
so capture the facts the session revealed: entities, names, numbers,
prices, dates, statuses and links.

Look at the requests with capture_index and open the useful ones with
capture_entry, which shows HTML pages as readable text. Content also lives
in API and JSON responses, so open those too.

Keep the full content, not summaries. For an article, post or other page
whose text matters, use save_entry to save its whole text into its own
file, with a title and a short note on what it is. Use write for files you
compose yourself, such as index.md and pages about entities, and keep each
write short.

Write only facts you read in capture_entry output, and open a response before
writing about it. Never invent placeholder names, dates, numbers or
articles; a short tree of real facts is better than a long made-up one.

A suggested layout is index.md with an overview of the site that links to
the other files, then one file per page or entity. Write index.md last, so
it links only to files that exist. Organize it however serves a reader best.

When the tree covers what the session showed, finish with a one-line
summary as the answer, for example
{"action": "finish", "answer": "12 files covering the article feed and 3 company pages"}.`

// Status is the outcome of a generation.
type Status string

const (
	// Generated means the tree has at least one file. The agent may still
	// have stopped at a limit; Agent.Outcome says how it ended.
	Generated Status = "generated"
	// Failed means the agent errored or wrote nothing.
	Failed Status = "generation_failed"
)

// Result is one generation.
type Result struct {
	Status Status
	Digest string
	Stats  shadow.Stats
	Agent  agent.Result
}

// Options tune a run beyond the scenario's limits.
type Options struct {
	Now func() time.Time
}

// Run generates a shadow tree for s into treeDir, which must not exist or
// must be empty. On return the tree's files are read-only.
func Run(ctx context.Context, m model.Model, s *scenario.Scenario, treeDir string, opts Options) (Result, error) {
	f, err := os.Open(s.CapturePath())
	if err != nil {
		return Result{}, err
	}
	trace, err := capture.Parse(f)
	f.Close()
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", s.Capture, err)
	}

	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		return Result{}, err
	}
	if entries, err := os.ReadDir(treeDir); err != nil {
		return Result{}, err
	} else if len(entries) > 0 {
		return Result{}, fmt.Errorf("tree directory %s is not empty", treeDir)
	}
	root, err := os.OpenRoot(treeDir)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()

	w := newWriteTool(root)
	tools := append(CaptureTools(trace), w, saveTool{trace: trace, w: w})
	for _, t := range reader.Tools(root.FS()) {
		if t.Name() != "search" {
			tools = append(tools, t) // list and read, to review the tree
		}
	}
	ar := agent.Run(ctx, m, agent.Config{
		Instructions: instructions,
		Task:         task(trace),
		Tools:        tools,
		MaxSteps:     s.Limits.Generate.MaxSteps,
		Timeout:      time.Duration(s.Limits.Generate.TimeoutSeconds) * time.Second,
		MaxTokens:    s.Limits.Generate.MaxTokens,
		Now:          opts.Now,
	})

	res := Result{Agent: ar}
	res.Digest, res.Stats, err = shadow.Digest(root.FS())
	if err != nil {
		return res, err
	}
	if err := freeze(treeDir); err != nil {
		return res, err
	}
	res.Status = Generated
	if ar.Outcome == agent.Failed || res.Stats.Files == 0 {
		res.Status = Failed
	}
	return res, nil
}

// task describes the session so the agent knows where to start.
func task(t *capture.Trace) string {
	hosts := map[string]int{}
	for _, e := range t.Entries {
		hosts[e.Host]++
	}
	rows, _, _ := indexRows(t.Select(capture.Filter{}), false)
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Slice(names, func(i, j int) bool {
		if hosts[names[i]] != hosts[names[j]] {
			return hosts[names[i]] > hosts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 8 {
		names = names[:8]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The session has %d requests. capture_index lists %d of them by default: documents, API calls and unclassified requests "+
		"that have a body, with repeats folded. Busiest hosts: %s.", len(t.Entries), len(rows), strings.Join(names, ", "))
	if big := largest(t, taskLargest); len(big) > 0 {
		b.WriteString("\n\nThe largest responses, which usually hold the most content:")
		for _, e := range big {
			u := capture.NormalizeURL(e.URL, capture.DefaultVolatileKeys)
			if len(u) > maxTaskURL {
				u = u[:maxTaskURL] + "..."
			}
			fmt.Fprintf(&b, "\n- seq %d, %d bytes: %s", e.Sequence, len(e.Readable()), u)
		}
	}
	b.WriteString("\n\nBuild the shadow tree. Start with capture_index.")
	return b.String()
}

const (
	taskLargest = 8   // responses the task message names
	maxTaskURL  = 120 // URL length in the task message
)

// largest returns the biggest text response of each endpoint among the
// entries capture_index lists by default, biggest first, at most n of them.
func largest(t *capture.Trace, n int) []capture.Entry {
	rows, _, _ := indexRows(t.Select(capture.Filter{}), false)
	var all []capture.Entry
	for _, r := range rows {
		if len(r.entry.ResponseBody) > 0 && utf8.Valid(r.entry.ResponseBody) {
			all = append(all, r.entry)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return len(all[i].Readable()) > len(all[j].Readable()) })
	// One per endpoint, so a widget polled with changing results, such as an
	// ad feed, does not fill the list.
	seen := map[string]bool{}
	var out []capture.Entry
	for _, e := range all {
		key := e.Method + " " + strings.ToLower(e.Host) + e.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		if out = append(out, e); len(out) == n {
			break
		}
	}
	return out
}

// freeze makes every file in the tree read-only, so a tree that readers
// were scored on cannot change afterwards.
func freeze(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return os.Chmod(p, 0o444)
		}
		return nil
	})
}
