//go:build llama

package generator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/llama/llamatest"
)

// TestRunWithEmbeddedLlama drives the generator with the in-process
// llama adapter. The tiny model writes gibberish, but the run must reach a
// limit rather than fail inside the model.
func TestRunWithEmbeddedLlama(t *testing.T) {
	path, err := llamatest.WriteTinyModel(t.TempDir())
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
	s.Limits.Generate.MaxSteps = 3
	res, err := Run(context.Background(), m, s, filepath.Join(t.TempDir(), "tree"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Agent.Outcome == agent.Failed || len(res.Agent.Steps) == 0 {
		t.Fatalf("outcome %s, err %v", res.Agent.Outcome, res.Agent.Err)
	}
	t.Logf("%s, %s after %d steps", res.Status, res.Agent.Outcome, len(res.Agent.Steps))
}
