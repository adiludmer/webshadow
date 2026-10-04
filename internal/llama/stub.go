//go:build !llama

package llama

import "context"

// Available reports whether this binary has llama.cpp linked in.
const Available = false

type handle = struct{}

// Load always fails without the llama build tag.
func Load(path string, opts Options) (*Model, error) { return nil, ErrNotBuilt }

func (m *Model) ContextSize() int { return 0 }

func (m *Model) Chat(ctx context.Context, msgs []Message, maxTokens int, temperature float32, seed uint32) (Result, error) {
	return Result{}, ErrNotBuilt
}

func (m *Model) Close() error { return nil }
