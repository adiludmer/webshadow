// Package model defines the chat-model interface the benchmark's agents
// talk to, the models.yaml registry that names concrete models, and the
// adapters that implement the interface.
//
// Secrets never live in models.yaml: an entry names the environment
// variable that holds its API key, and the key is read when the model is
// opened.
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
}

// Usage counts tokens as the provider reports them.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the model's reply.
type Response struct {
	Text string
	// StopReason is the provider's reason for ending, such as "stop" or
	// "length", passed through unchanged.
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
