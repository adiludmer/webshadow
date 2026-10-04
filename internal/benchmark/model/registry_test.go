package model

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const validRegistry = `
models:
  - id: fake
    adapter: fake
    replies: ["hi"]
  - id: local
    adapter: llama
    model_path: ${MODELS_DIR}/tiny.gguf
    context_size: 2048
`

func TestParseRegistry(t *testing.T) {
	r, err := ParseRegistry([]byte(validRegistry))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.IDs(), ","); got != "fake,local" {
		t.Errorf("IDs = %s", got)
	}
	c, ok := r.Get("local")
	if !ok || c.ContextSize != 2048 || c.ModelPath != "${MODELS_DIR}/tiny.gguf" {
		t.Errorf("Get(local) = %+v, %v", c, ok)
	}
}

func TestParseRegistryErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"empty file", "", "empty models file"},
		{"no models", "models: []", "at least one model"},
		{"unknown key", "models:\n  - id: a\n    adapter: fake\n    replies: [x]\n    colour: red\n", "field colour not found"},
		{"missing id", "models:\n  - adapter: fake\n    replies: [x]\n", "model #1: id is required"},
		{"bad id", "models:\n  - id: My Model\n    adapter: fake\n    replies: [x]\n", "id must be lowercase"},
		{"duplicate id", "models:\n  - {id: a, adapter: fake, replies: [x]}\n  - {id: a, adapter: fake, replies: [y]}\n", "id is used twice"},
		{"missing adapter", "models:\n  - id: a\n", "adapter is required"},
		{"unknown adapter", "models:\n  - {id: a, adapter: magic}\n", `adapter "magic" is not known`},
		{"fake without replies", "models:\n  - {id: a, adapter: fake}\n", "at least one entry in replies"},
		{"replies on llama", "models:\n  - {id: a, adapter: llama, model_path: m.gguf, replies: [x]}\n", "only used by the fake adapter"},
		{"negative context", "models:\n  - {id: a, adapter: llama, model_path: m.gguf, context_size: -1}\n", "context_size and threads must not be negative"},
		{"llama without path", "models:\n  - {id: a, adapter: llama}\n", "model_path is required"},
		{"llama fields on fake", "models:\n  - {id: a, adapter: fake, replies: [x], context_size: 10}\n", "only used by the llama adapter"},
		{"negative tokens", "models:\n  - {id: a, adapter: fake, replies: [x], max_tokens: -1}\n", "max_tokens must not be negative"},
		{"temperature too high", "models:\n  - {id: a, adapter: fake, replies: [x], temperature: 3}\n", "temperature must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRegistry([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseRegistryReportsAllProblems(t *testing.T) {
	_, err := ParseRegistry([]byte("models:\n  - {id: a}\n  - {id: b, adapter: fake}\n"))
	if err == nil || !strings.Contains(err.Error(), "model a: adapter is required") || !strings.Contains(err.Error(), "model b: the fake adapter") {
		t.Fatalf("err = %v", err)
	}
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestOpen(t *testing.T) {
	r, err := ParseRegistry([]byte(validRegistry))
	if err != nil {
		t.Fatal(err)
	}

	m, err := r.Open("fake", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := m.Complete(context.Background(), Request{})
	if err != nil || resp.Text != "hi" || m.ID() != "fake" {
		t.Fatalf("fake: %+v, %v", resp, err)
	}
	m.Close()

	if _, err := r.Open("local", env(nil)); err == nil || !strings.Contains(err.Error(), "MODELS_DIR is not set") {
		t.Errorf("unset model_path variable: err = %v", err)
	}
	if _, err := r.Open("nope", env(nil)); err == nil || !strings.Contains(err.Error(), "known: fake, local") {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestExpand(t *testing.T) {
	got, err := expand("$HOME/models/${NAME}.gguf", env(map[string]string{"HOME": "/h", "NAME": "q"}))
	if err != nil || got != "/h/models/q.gguf" {
		t.Errorf("expand = %q, %v", got, err)
	}
	if _, err := expand("${A}/${B}", env(nil)); err == nil || !strings.Contains(err.Error(), "A, B") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckedInRegistryIsValid(t *testing.T) {
	if _, err := LoadRegistry(filepath.Join("..", "..", "..", "models.yaml")); err != nil {
		t.Fatal(err)
	}
}
