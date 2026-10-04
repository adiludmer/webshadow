// Package llama runs GGUF models in-process through llama.cpp, linked in
// statically with cgo. It is compiled only with the llama build tag (see
// `make llama`); without it, Load reports ErrNotBuilt.
package llama

import (
	"errors"
	"sync"
)

// ErrNotBuilt is returned by Load in binaries built without the llama tag.
var ErrNotBuilt = errors.New("this webshadow binary was built without llama.cpp; rebuild with `make llama`")

// Options configure model loading. Zero values keep llama.cpp's defaults;
// a zero ContextSize means the model's training context.
type Options struct {
	ContextSize int
	// GPULayers is the number of layers to offload to a GPU backend, when
	// one is built in; negative means all, nil keeps the default.
	GPULayers *int
	Threads   int
}

// Message is one chat turn.
type Message struct {
	Role    string
	Content string
}

// Stop reasons a Result can carry.
const (
	StopEOG     = "stop"    // the model ended its turn
	StopLength  = "length"  // the token budget or the context ran out
	StopAborted = "aborted" // the context was cancelled mid-generation
)

// Result is one completion.
type Result struct {
	Text         string
	PromptTokens int
	OutputTokens int
	StopReason   string
}

// Model is a loaded GGUF model with one context. Chat calls are
// serialized, since a context runs one sequence at a time.
type Model struct {
	mu     sync.Mutex
	handle handle
}
