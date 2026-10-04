package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAICompatible calls a /chat/completions endpoint in the OpenAI format,
// which OpenAI, OpenRouter, vLLM, Ollama and many other servers accept.
type OpenAICompatible struct {
	cfg    Config
	apiKey string
	client *http.Client
	// sleep waits between retries; tests replace it.
	sleep func(context.Context, time.Duration) error
}

// NewOpenAICompatible builds the adapter from a registry entry and its
// resolved API key, which may be empty for local servers.
func NewOpenAICompatible(cfg Config, apiKey string) *OpenAICompatible {
	return &OpenAICompatible{
		cfg:    cfg,
		apiKey: apiKey,
		client: &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
		sleep:  sleepContext,
	}
}

func (m *OpenAICompatible) ID() string { return m.cfg.ID }

// Close releases idle connections.
func (m *OpenAICompatible) Close() error {
	m.client.CloseIdleConnections()
	return nil
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// StatusError is a non-2xx reply from the provider.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d: %s", e.Status, e.Message)
}

// retryable reports whether a status is worth another attempt.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// Complete sends the request, retrying rate limits and server errors up to
// MaxRetries times with exponential backoff or the server's Retry-After.
func (m *OpenAICompatible) Complete(ctx context.Context, req Request) (Response, error) {
	body := chatRequest{
		Model:       m.cfg.Model,
		Messages:    req.Messages,
		MaxTokens:   m.cfg.MaxTokens,
		Temperature: m.cfg.Temperature,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}

	backoff := time.Second
	for attempt := 0; ; attempt++ {
		resp, wait, err := m.send(ctx, payload)
		if err == nil {
			return resp, nil
		}
		var se *StatusError
		if !errors.As(err, &se) || !retryable(se.Status) || attempt >= *m.cfg.MaxRetries {
			return Response{}, err
		}
		if wait == 0 {
			wait = backoff
			backoff *= 2
		}
		if err := m.sleep(ctx, wait); err != nil {
			return Response{}, err
		}
	}
}

// send makes one HTTP attempt. On a non-2xx reply it returns a StatusError
// and any Retry-After delay the server asked for.
func (m *OpenAICompatible) send(ctx context.Context, payload []byte) (Response, time.Duration, error) {
	url := strings.TrimRight(m.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Response{}, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	httpResp, err := m.client.Do(httpReq)
	if err != nil {
		return Response{}, 0, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, 32<<20))
	if err != nil {
		return Response{}, 0, err
	}

	if httpResp.StatusCode/100 != 2 {
		return Response{}, retryAfter(httpResp.Header.Get("Retry-After")), &StatusError{httpResp.StatusCode, errorMessage(data)}
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return Response{}, 0, fmt.Errorf("decoding provider response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return Response{}, 0, errors.New("provider response has no choices")
	}
	return Response{
		Text:       cr.Choices[0].Message.Content,
		StopReason: cr.Choices[0].FinishReason,
		Usage:      Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens},
	}, 0, nil
}

// errorMessage pulls error.message out of an error body, or falls back to
// the start of the body.
func errorMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// retryAfter parses a Retry-After header given in seconds, capped at a
// minute so a misbehaving server cannot stall a run.
func retryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, time.Minute)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
