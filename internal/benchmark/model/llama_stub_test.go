//go:build !llama

package model

import (
	"errors"
	"testing"

	"github.com/adiludmer/webshadow/internal/llama"
)

func TestLlamaNeedsTheLlamaBuild(t *testing.T) {
	r, err := ParseRegistry([]byte("models:\n  - {id: local, adapter: llama, model_path: /models/x.gguf}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open("local", env(nil)); !errors.Is(err, llama.ErrNotBuilt) {
		t.Fatalf("err = %v, want llama.ErrNotBuilt", err)
	}
}
