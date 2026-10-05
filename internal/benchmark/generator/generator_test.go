package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

func ensoScenario(t *testing.T) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Load(filepath.Join("..", "testdata", "enso"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var script = []string{
	`{"action": "capture_index", "args": {}}`,
	`{"action": "capture_entry", "args": {"seq": 0}}`,
	`{"action": "write", "args": {"path": "index.md", "content": "# Geektime\n\n- [Enso](companies/enso.md)\n"}}`,
	`{"action": "write", "args": {"path": "companies/enso.md", "content": "# Enso\n\nRaised $15M.\n"}}`,
	`{"action": "list", "args": {"recursive": true}}`,
	`{"action": "finish", "answer": "2 files"}`,
}

func TestRunWritesAndFreezesTree(t *testing.T) {
	s := ensoScenario(t)
	dir := filepath.Join(t.TempDir(), "tree-01")
	m := model.NewFake("fake", script...)
	res, err := Run(context.Background(), m, s, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Generated || res.Stats.Files != 2 || !strings.HasPrefix(res.Digest, "sha256:") {
		t.Fatalf("result: %+v", res)
	}
	if res.Agent.Steps[4].Result != "companies/\ncompanies/enso.md (21 bytes)\nindex.md (40 bytes)" {
		t.Errorf("list of the tree: %q", res.Agent.Steps[4].Result)
	}
	info, err := os.Stat(filepath.Join(dir, "companies", "enso.md"))
	if err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Errorf("file not frozen: %v, %v", info.Mode(), err)
	}
	root, _ := os.OpenRoot(dir)
	defer root.Close()
	if d, _, _ := shadow.Digest(root.FS()); d != res.Digest {
		t.Errorf("digest %s does not match the tree (%s)", res.Digest, d)
	}

	// The generator must never see the goal or the expected answer.
	goal, _ := os.ReadFile(s.GoalPath())
	reqs := m.Requests()
	sys := reqs[0].Messages[0].Content
	for _, msg := range reqs[len(reqs)-1].Messages {
		if strings.Contains(msg.Content, "How much money") || strings.Contains(msg.Content, strings.TrimSpace(string(goal))) {
			t.Fatalf("goal leaked into the generator's prompt: %q", msg.Content)
		}
	}
	for _, tool := range []string{"documents", "document", "place", "capture_index", "capture_entry", "write", "list", "read"} {
		if !strings.Contains(sys, "- "+tool+":") {
			t.Errorf("system prompt lacks tool %s", tool)
		}
	}
	if strings.Contains(sys, "- search:") {
		t.Error("generator got the reader's search tool")
	}
	if task := reqs[0].Messages[1].Content; !strings.Contains(task, "The session has 12 requests") || !strings.Contains(task, "www.geektime.co.il") ||
		!strings.Contains(task, "no documents to place") ||
		!strings.Contains(task, "Other responses with the most content, out of 7 that capture_index lists:\n- seq 8, 90936 bytes: https://www.geektime.co.il/wp-content/uploads/hp.json\n") {
		t.Errorf("task: %q", task)
	}
}

func TestRunStatuses(t *testing.T) {
	s := ensoScenario(t)
	res, err := Run(context.Background(), model.NewFake("fake", `{"action": "finish", "answer": "nothing"}`), s, filepath.Join(t.TempDir(), "t"), Options{})
	if err != nil || res.Status != Failed || res.Stats.Files != 0 {
		t.Errorf("empty tree: %s, %+v, %v", res.Status, res.Stats, err)
	}

	s.Limits.Generate.MaxSteps = 3
	res, err = Run(context.Background(), model.NewFake("fake", script...), s, filepath.Join(t.TempDir(), "t"), Options{})
	if err != nil || res.Status != Generated || res.Agent.Outcome != "step_limit" || res.Stats.Files != 1 {
		t.Errorf("stopped at the step limit with one file: %s, %s, %+v, %v", res.Status, res.Agent.Outcome, res.Stats, err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "old.md"), []byte("x"), 0o644)
	if _, err := Run(context.Background(), model.NewFake("fake", script...), s, dir, Options{}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("non-empty dir: %v", err)
	}
}
