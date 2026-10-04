package model

import (
	"context"

	"github.com/adiludmer/webshadow/internal/llama"
)

// Llama runs a GGUF model in-process through the llama.cpp linked into
// this binary (see `make llama`). Binaries built without it fail to open
// llama entries with llama.ErrNotBuilt.
type Llama struct {
	cfg   Config
	model *llama.Model
}

// OpenLlama loads the entry's model. modelPath is already resolved.
func OpenLlama(cfg Config, modelPath string) (*Llama, error) {
	m, err := llama.Load(modelPath, llama.Options{ContextSize: cfg.ContextSize, GPULayers: cfg.GPULayers, Threads: cfg.Threads})
	if err != nil {
		return nil, err
	}
	return &Llama{cfg: cfg, model: m}, nil
}

func (l *Llama) ID() string { return l.cfg.ID }

// Complete runs one chat completion. The model is shared, so concurrent
// calls queue behind each other.
func (l *Llama) Complete(ctx context.Context, req Request) (Response, error) {
	msgs := make([]llama.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = llama.Message{Role: m.Role, Content: m.Content}
	}
	maxTokens := l.cfg.MaxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	var temp float64
	if l.cfg.Temperature != nil {
		temp = *l.cfg.Temperature
	}
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	res, err := l.model.Chat(ctx, msgs, maxTokens, float32(temp), l.cfg.Seed)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Text:       res.Text,
		StopReason: res.StopReason,
		Usage:      Usage{InputTokens: res.PromptTokens, OutputTokens: res.OutputTokens},
	}, nil
}

// Close frees the model.
func (l *Llama) Close() error { return l.model.Close() }
