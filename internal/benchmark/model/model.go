// Package model defines the chat-model interface the benchmark's agents
// talk to, the models.yaml registry that names concrete models, and the
// adapters that implement the interface: llama (a local GGUF model run
// in-process) and fake (scripted replies for tests).
package model

import (
	"context"
	"errors"
)

// Roles a message can have.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn of a chat transcript.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is one completion call. MaxTokens and Temperature override the
// registry entry's defaults when set.
type Request struct {
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	// Grammar, when set, is GBNF with a "root" rule that constrains the
	// reply. Adapters that cannot constrain output ignore it, so callers
	// still validate what comes back.
	Grammar string
}

// Usage counts tokens.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the model's reply.
type Response struct {
	Text string
	// StopReason says why generation ended: "stop" when the model ended
	// its turn, "length" when the token budget ran out.
	StopReason string
	Usage      Usage
}

// Model is a chat model the benchmark can call.
type Model interface {
	// ID is the registry id, used in run records.
	ID() string
	Complete(ctx context.Context, req Request) (Response, error)
	// Close releases what the model holds, such as a local server
	// process. Callers close every model they open.
	Close() error
}

// ErrScriptExhausted is returned by a fake model asked for more replies
// than its script holds.
var ErrScriptExhausted = errors.New("fake model: script exhausted")
