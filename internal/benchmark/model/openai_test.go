package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func openAIConfig(baseURL string) Config {
	temp := 0.0
	retries := 2
	return Config{ID: "test", Adapter: AdapterOpenAICompatible, Model: "test-model", BaseURL: baseURL, MaxTokens: 256, Temperature: &temp, TimeoutSeconds: 5, MaxRetries: &retries}
}

// newTestAdapter returns an adapter whose retries record their waits
// instead of sleeping.
func newTestAdapter(cfg Config, key string, waits *[]time.Duration) *OpenAICompatible {
	m := NewOpenAICompatible(cfg, key)
	m.sleep = func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
	return m
}

const okReply = `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`

func TestOpenAISendsRequestAndParsesReply(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Write([]byte(okReply))
	}))
	defer srv.Close()

	var waits []time.Duration
	m := newTestAdapter(openAIConfig(srv.URL+"/v1/"), "sk-test", &waits)
	defer m.Close()
	temp := 0.7
	resp, err := m.Complete(context.Background(), Request{
		Messages:    []Message{{RoleSystem, "be brief"}, {RoleUser, "hi"}},
		MaxTokens:   64,
		Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" || resp.StopReason != "stop" || resp.Usage != (Usage{12, 3}) {
		t.Errorf("unexpected response: %+v", resp)
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q", got.path)
	}
	if got.auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.body["model"] != "test-model" || got.body["max_tokens"] != 64.0 || got.body["temperature"] != 0.7 {
		t.Errorf("request overrides not applied: %v", got.body)
	}
	msgs, _ := got.body["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "hi" {
		t.Errorf("messages = %v", got.body["messages"])
	}
}

func TestOpenAIUsesEntryDefaultsAndNoKey(t *testing.T) {
	var auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(okReply))
	}))
	defer srv.Close()

	var waits []time.Duration
	if _, err := newTestAdapter(openAIConfig(srv.URL), "", &waits).Complete(context.Background(), Request{Messages: []Message{{RoleUser, "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("sent Authorization %q without a key", auth)
	}
	if body["max_tokens"] != 256.0 || body["temperature"] != 0.0 {
		t.Errorf("entry defaults not applied: %v", body)
	}
}

func TestOpenAIRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.Write([]byte(okReply))
		}
	}))
	defer srv.Close()

	var waits []time.Duration
	resp, err := newTestAdapter(openAIConfig(srv.URL), "", &waits).Complete(context.Background(), Request{})
	if err != nil || resp.Text != "hello" {
		t.Fatalf("Complete = %+v, %v", resp, err)
	}
	if len(waits) != 2 || waits[0] != 3*time.Second || waits[1] != time.Second {
		t.Errorf("waits = %v, want [3s 1s]", waits)
	}
}

func TestOpenAIErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantCalls int32
		want      string
	}{
		{"client error is not retried", 400, `{"error":{"message":"bad model name"}}`, 1, "HTTP 400: bad model name"},
		{"server errors exhaust retries", 500, `upstream failed`, 3, "HTTP 500: upstream failed"},
		{"no choices", 200, `{"choices":[]}`, 1, "no choices"},
		{"invalid json", 200, `not json`, 1, "decoding provider response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			var waits []time.Duration
			_, err := newTestAdapter(openAIConfig(srv.URL), "", &waits).Complete(context.Background(), Request{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if calls.Load() != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), tt.wantCalls)
			}
		})
	}
}

func TestOpenAIStopsRetryingWhenCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	m := NewOpenAICompatible(openAIConfig(srv.URL), "")
	m.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleepContext(ctx, d)
	}
	if _, err := m.Complete(ctx, Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "2": 2 * time.Second, "-1": 0, "soon": 0, "600": time.Minute} {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
