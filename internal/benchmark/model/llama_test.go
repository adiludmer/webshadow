//go:build llama

package model

import (
	"context"
	"testing"

	"github.com/adiludmer/webshadow/internal/llama/llamatest"
)

func TestLlamaAdapter(t *testing.T) {
	dir := t.TempDir()
	if _, err := llamatest.WriteTinyModel(dir); err != nil {
		t.Fatal(err)
	}
	r, err := ParseRegistry([]byte("models:\n  - {id: local, adapter: llama, model_path: '${MODELS}/tiny.gguf', context_size: 512, max_tokens: 8, temperature: 0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.Open("local", env(map[string]string{"MODELS": dir}))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	resp, err := m.Complete(context.Background(), Request{Messages: []Message{{RoleSystem, "Be brief."}, {RoleUser, "Hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 || resp.Usage.OutputTokens > 8 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	again, err := m.Complete(context.Background(), Request{Messages: []Message{{RoleSystem, "Be brief."}, {RoleUser, "Hi"}}})
	if err != nil || again != resp {
		t.Errorf("temperature 0 should repeat: %+v vs %+v (%v)", resp, again, err)
	}
	short, err := m.Complete(context.Background(), Request{Messages: []Message{{RoleUser, "Hi"}}, MaxTokens: 2})
	if err != nil || short.Usage.OutputTokens > 2 {
		t.Errorf("request max_tokens not applied: %+v, %v", short, err)
	}
}
