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
const PromptVersion = "generator-v7"

const instructions = `You organize a recorded browsing session (a proxy capture of every request
and response) into a shadow tree: a folder of Markdown files that another
agent with no web access will use to answer questions about the site. You
do not know what those questions will be.

Every HTML page in the capture is already converted to a Markdown document
with a header listing its url, title, description, tags and keywords. You
do not rewrite them; your job is the layout of the tree.

- See the documents with documents, and read one with document.
- Put each document where a reader would look for it with place, grouping
  related pages under folders and giving each file a short descriptive
  name. Pages you do not place are filed under pages/ when you finish.
- Use write for the files you compose: index.md with an overview of the
  site that links to each section, and short overview pages for sections or
  topics that link to their documents. Link to a page with its URL or its
  tree path; page URLs are rewritten to tree paths when you finish.
- Some content lives only in API and JSON responses, not in pages.
  capture_index and capture_entry show those; write their facts into files.

Write only facts you read in tool output. Never invent names, dates,
numbers or articles.

A suggested layout is index.md at the root, then one folder per kind of
page or topic. Write index.md last, so it links only to files that exist.
Organize it however serves a reader best.

When the layout is done, finish with a one-line summary as the answer, for
example
{"action": "finish", "answer": "4 articles under articles/ by topic, home page and index.md"}.`

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
	Status    Status
	Digest    string
	Stats     shadow.Stats
	Documents DocumentStats
	Agent     agent.Result
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
	l := newLayout(buildDocs(trace), w)
	tools := append(l.tools(), w)
	tools = append(tools, CaptureTools(trace)...)
	for _, t := range reader.Tools(root.FS()) {
		if t.Name() != "search" {
			tools = append(tools, t) // list and read, to review the tree
		}
	}
	ar := agent.Run(ctx, m, agent.Config{
		Instructions: instructions,
		Task:         task(trace, l),
		Tools:        tools,
		MaxSteps:     s.Limits.Generate.MaxSteps,
		Timeout:      time.Duration(s.Limits.Generate.TimeoutSeconds) * time.Second,
		MaxTokens:    s.Limits.Generate.MaxTokens,
		Now:          opts.Now,
	})

	res := Result{Agent: ar}
	if res.Documents, err = l.finish(); err != nil {
		return res, err
	}
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

// task describes the session so the agent knows where to start: the
// documents to place, then the other responses with content.
func task(t *capture.Trace, l *layout) string {
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
	fmt.Fprintf(&b, "The session has %d requests to %s.", len(t.Entries), strings.Join(names, ", "))
	if len(l.docs) == 0 {
		b.WriteString(" None of them is an HTML page, so there are no documents to place.")
	} else {
		fmt.Fprintf(&b, " %d of them are HTML pages, converted into documents d1-d%d:\n", len(l.docs), len(l.docs))
		for _, d := range l.docs[:min(len(l.docs), docsPageSize)] {
			b.WriteString("\n" + l.line(d))
		}
		if len(l.docs) > docsPageSize {
			b.WriteString("\n[more: documents page 2]")
		}
	}
	if big := largest(t, taskLargest); len(big) > 0 {
		fmt.Fprintf(&b, "\n\nOther responses with the most content, out of %d that capture_index lists:", len(rows))
		for _, e := range big {
			u := capture.NormalizeURL(e.URL, capture.DefaultVolatileKeys)
			if len(u) > maxTaskURL {
				u = u[:maxTaskURL] + "..."
			}
			fmt.Fprintf(&b, "\n- seq %d, %d bytes: %s", e.Sequence, len(e.ResponseBody), u)
		}
	}
	b.WriteString("\n\nLay out the shadow tree.")
	return b.String()
}

const (
	taskLargest = 8   // responses the task message names
	maxTaskURL  = 120 // URL length in the task message
)

// largest returns the biggest text response of each endpoint among the
// entries capture_index lists by default, biggest first, at most n of them.
// HTML responses are left out; they are the documents.
func largest(t *capture.Trace, n int) []capture.Entry {
	rows, _, _ := indexRows(t.Select(capture.Filter{}), false)
	var all []capture.Entry
	for _, r := range rows {
		if r.entry.Page == nil && len(r.entry.ResponseBody) > 0 && utf8.Valid(r.entry.ResponseBody) {
			all = append(all, r.entry)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return len(all[i].ResponseBody) > len(all[j].ResponseBody) })
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
