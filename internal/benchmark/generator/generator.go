// Package generator runs the generate stage: an agent that reads a
// sanitized HAR through paged tools and writes a shadow tree of Markdown
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

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/har"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/reader"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

// PromptVersion names the instructions below; records carry it.
const PromptVersion = "generator-v2"

const instructions = `You turn a recorded browsing session (a HAR file) into a shadow tree: a
folder of Markdown files that describes what the site showed and served.
Later, another agent with no web access will answer questions about this
site using only your files. You do not know what those questions will be,
so capture the facts the session revealed: entities, names, numbers,
prices, dates, statuses and links.

Look at the requests with har_index and open the useful ones with
har_entry. Content often lives in API and JSON responses rather than in
HTML pages, so open those too. Write readable Markdown, not raw dumps.

Write only facts you read in har_entry output, and open a response before
writing about it. Never invent placeholder names, dates, numbers or
articles; a short tree of real facts is better than a long made-up one.

A suggested layout is index.md with an overview of the site that links to
the other files, then one file per page or entity. Organize it however
serves a reader best.

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
	f, err := os.Open(s.HARPath())
	if err != nil {
		return Result{}, err
	}
	trace, err := har.Parse(f)
	f.Close()
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", s.HAR, err)
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

	tools := append(HARTools(trace), newWriteTool(root))
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
func task(t *har.Trace) string {
	hosts := map[string]int{}
	for _, e := range t.Entries {
		hosts[e.Host]++
	}
	shown := len(t.Select(har.Filter{}))
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
	return fmt.Sprintf("The session has %d requests; %d are documents, API calls or unclassified, which har_index lists by default. "+
		"Busiest hosts: %s.\n\nBuild the shadow tree. Start with har_index.", len(t.Entries), shown, strings.Join(names, ", "))
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
