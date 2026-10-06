package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

// The Burp browse scenario has four whole pages: the home page (d1) and
// three articles (d2-d4).
func burpScenario(t *testing.T) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Load(filepath.Join("..", "..", "..", "benchmarks", "geektime-burp-browse"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLayoutRun(t *testing.T) {
	s := burpScenario(t)
	dir := filepath.Join(t.TempDir(), "tree-01")
	m := model.NewFake("fake",
		`{"action": "documents", "args": {}}`,
		`{"action": "document", "args": {"id": "d2", "limit": 300}}`,
		`{"action": "place", "args": {"id": "d2", "path": "articles/openai.md"}}`,
		`{"action": "place", "args": {"id": "d2", "path": "articles/ai/openai-safety.md"}}`,
		`{"action": "place", "args": {"id": "d3", "path": "articles/ai/openai-safety.md"}}`,
		`{"action": "write", "args": {"path": "articles/ai/openai-safety.md", "content": "x"}}`,
		`{"action": "write", "args": {"path": "index.md", "content": "# Geektime\n\n- [OpenAI safety](https://www.geektime.co.il/former-openai-researcher-says-company-culture-is-broken/)\n"}}`,
		`{"action": "place", "args": {"id": "d4", "path": "index.md"}}`,
		`{"action": "finish", "answer": "1 article placed"}`,
	)
	res, err := Run(context.Background(), m, s, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	steps := res.Agent.Steps
	if !strings.HasPrefix(steps[0].Result, "documents 1-4 of 4\nd1 ") || !strings.Contains(steps[0].Result, "d2 7.3KB unplaced https://www.geektime.co.il/former-openai-researcher-says-company-culture-is-broken/ | ") {
		t.Errorf("documents:\n%s", steps[0].Result)
	}
	if !strings.HasPrefix(steps[1].Result, "d2, bytes 0-") || !strings.Contains(steps[1].Result, "---\nurl: https://www.geektime.co.il/former-openai") {
		t.Errorf("document:\n%s", steps[1].Result)
	}
	if !strings.HasPrefix(steps[3].Result, "moved d2 from articles/openai.md to articles/ai/openai-safety.md") {
		t.Errorf("move: %q", steps[3].Result)
	}
	for i, want := range map[int]string{4: "already holds document d2", 5: "holds document d2", 7: "is a file you wrote"} {
		if !strings.Contains(steps[i].Error, want) {
			t.Errorf("step %d: error %q, want %q", i+1, steps[i].Error, want)
		}
	}

	if res.Documents != (DocumentStats{Total: 4, Placed: 1, AutoPlaced: 3}) {
		t.Errorf("documents: %+v", res.Documents)
	}
	if _, err := os.Stat(filepath.Join(dir, "articles", "openai.md")); !os.IsNotExist(err) {
		t.Errorf("moved document left its old file: %v", err)
	}
	for _, p := range []string{"pages/home.md", "pages/ice-coffee-job-interview-debate.md", "pages/jev-is-the-new-ai-buzzword.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("unplaced document not filed: %v", err)
		}
	}
	index, _ := os.ReadFile(filepath.Join(dir, "index.md"))
	if !strings.Contains(string(index), "[OpenAI safety](articles/ai/openai-safety.md)") {
		t.Errorf("index.md links not rewritten:\n%s", index)
	}
	home, _ := os.ReadFile(filepath.Join(dir, "pages", "home.md"))
	if !strings.Contains(string(home), "](../articles/ai/openai-safety.md)") {
		t.Errorf("home page links not rewritten")
	}
	if !strings.HasPrefix(string(home), "---\nurl: https://www.geektime.co.il/\n") {
		t.Errorf("home page header:\n%.300s", home)
	}
	if res.Stats.Files != 5 {
		t.Errorf("tree has %d files, want 5", res.Stats.Files)
	}
}

func TestDocsSkipFragmentsAndRepeats(t *testing.T) {
	s := burpScenario(t)
	docs := buildDocs(loadTrace(t, s.CapturePath()))
	var urls []string
	for _, d := range docs {
		urls = append(urls, d.ID+" "+d.URL)
	}
	want := "d1 https://www.geektime.co.il/,d2 https://www.geektime.co.il/former-openai-researcher-says-company-culture-is-broken/," +
		"d3 https://www.geektime.co.il/ice-coffee-job-interview-debate/,d4 https://www.geektime.co.il/jev-is-the-new-ai-buzzword/"
	if got := strings.Join(urls, ","); got != want {
		t.Errorf("docs:\n%s\nwant\n%s", got, want)
	}
	if len(docs[1].Keywords) == 0 || len(docs[1].Tags) == 0 {
		t.Errorf("d2 has no tags or keywords: %+v", docs[1].Document)
	}
}
