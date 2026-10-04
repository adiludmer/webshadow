package model

import (
	"context"
	"sync"
)

// Fake replies with a fixed script, one entry per call, and records every
// request it receives. It lets agent runs be tested without a provider.
type Fake struct {
	id string

	mu       sync.Mutex
	script   []string
	next     int
	requests []Request
}

// NewFake returns a fake model that answers with replies in order.
func NewFake(id string, replies ...string) *Fake {
	return &Fake{id: id, script: replies}
}

func (f *Fake) ID() string { return f.id }

// Close does nothing.
func (f *Fake) Close() error { return nil }

// Complete returns the next scripted reply, or ErrScriptExhausted.
func (f *Fake) Complete(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.next >= len(f.script) {
		return Response{}, ErrScriptExhausted
	}
	text := f.script[f.next]
	f.next++
	return Response{Text: text, StopReason: "stop", Usage: Usage{InputTokens: inputSize(req), OutputTokens: len(text)}}, nil
}

// Requests returns a copy of the requests received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// inputSize stands in for a token count: the total characters sent.
func inputSize(req Request) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	return n
}
