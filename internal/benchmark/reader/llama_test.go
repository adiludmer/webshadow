//go:build llama

package reader

import (
	"context"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/llama/llamatest"
)

// TestRunWithEmbeddedLlama drives the reader with the in-process llama
// adapter. The tiny model replies with gibberish, so the run cannot
// succeed, but it must reach a limit or a scored answer, not an error.
func TestRunWithEmbeddedLlama(t *testing.T) {
	dir := t.TempDir()
	path, err := llamatest.WriteTinyModel(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := model.ParseRegistry([]byte("models:\n  - {id: tiny, adapter: llama, model_path: '" + path + "', context_size: 4096, max_tokens: 24, temperature: 0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := reg.Open("tiny", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	s := ensoScenario(t)
	s.Limits.Answer.MaxSteps = 3
	root := ensoTree()
	defer root.Close()
	res, err := Run(context.Background(), m, s, root.FS(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status == Error || res.Status == Success {
		t.Fatalf("status %s, err %v", res.Status, res.Agent.Err)
	}
	if res.Agent.Usage.InputTokens == 0 || len(res.Agent.Steps) == 0 {
		t.Fatalf("no model calls recorded: %+v", res.Agent)
	}
	t.Logf("status %s after %d steps; first reply %q", res.Status, len(res.Agent.Steps), res.Agent.Steps[0].Reply)
}
