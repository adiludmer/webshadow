//go:build llama

package llama

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/llama/llamatest"
)

func loadTiny(t *testing.T, opts Options) *Model {
	t.Helper()
	path, err := llamatest.WriteTinyModel(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

var hello = []Message{{"system", "You are terse."}, {"user", "Say hello."}}

func TestChatGeneratesAndIsDeterministic(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 256})
	if got := m.ContextSize(); got != 256 {
		t.Errorf("ContextSize = %d, want 256", got)
	}
	a, err := m.Chat(context.Background(), hello, 16, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.PromptTokens == 0 || a.OutputTokens == 0 || a.OutputTokens > 16 {
		t.Fatalf("unexpected counts: %+v", a)
	}
	if a.StopReason == StopLength && a.OutputTokens != 16 {
		t.Errorf("length stop after %d tokens, want 16", a.OutputTokens)
	}
	b, err := m.Chat(context.Background(), hello, 16, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("greedy decoding differs between calls:\n%+v\n%+v", a, b)
	}
}

func TestChatSampling(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 256})
	a, err := m.Chat(context.Background(), hello, 16, 0.8, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Chat(context.Background(), hello, 16, 0.8, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != b.Text {
		t.Errorf("same seed gave different text: %q vs %q", a.Text, b.Text)
	}
}

func TestChatPromptTooLong(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 64})
	long := []Message{{"user", strings.Repeat("hello world ", 1000)}}
	if _, err := m.Chat(context.Background(), long, 8, 0, 0, ""); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("context holds %d", m.ContextSize())) {
		t.Fatalf("err = %v", err)
	}
	// The model stays usable after a refused prompt.
	if _, err := m.Chat(context.Background(), hello, 4, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChatFillsContextWithoutLimit(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 128})
	res, err := m.Chat(context.Background(), hello, 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := m.ContextSize(); res.StopReason == StopLength && res.PromptTokens+res.OutputTokens != n {
		t.Errorf("stopped at %d+%d tokens, want a full %d-token context", res.PromptTokens, res.OutputTokens, n)
	}
}

func TestChatCancellation(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 512})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Chat(ctx, hello, 0, 0, 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before start: err = %v", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	if _, err := m.Chat(ctx, hello, 0, 0, 0, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired: err = %v", err)
	}

	// Cancel mid-generation: an unbounded reply into an 8192-token context
	// runs far longer than the 50ms before the cancel.
	big := loadTiny(t, Options{ContextSize: 8192})
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := big.Chat(ctx, hello, 0, 0, 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled mid-generation: err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("abort took %v", d)
	}
	if _, err := big.Chat(context.Background(), hello, 4, 0, 0, ""); err != nil {
		t.Fatalf("model unusable after an abort: %v", err)
	}
}

func TestLoadAndCloseErrors(t *testing.T) {
	if _, err := Load("/nonexistent/model.gguf", Options{}); err == nil || !strings.Contains(err.Error(), "/nonexistent/model.gguf") {
		t.Fatalf("err = %v", err)
	}
	m := loadTiny(t, Options{ContextSize: 128})
	m.Close()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Chat(context.Background(), hello, 4, 0, 0, ""); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("chat after close: err = %v", err)
	}
}

// TestRealModel runs a real GGUF model. It is skipped unless
// WEBSHADOW_LLAMA_MODEL names one.
func TestRealModel(t *testing.T) {
	path := os.Getenv("WEBSHADOW_LLAMA_MODEL")
	if path == "" {
		t.Skip("set WEBSHADOW_LLAMA_MODEL to a GGUF file to run a real model")
	}
	m, err := Load(path, Options{ContextSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	res, err := m.Chat(context.Background(), []Message{{"user", "What is 2+2? Answer with just the number."}}, 16, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reply %q, %+v", res.Text, res)
	if !strings.Contains(res.Text, "4") {
		t.Errorf("reply %q does not contain 4", res.Text)
	}
}

func TestChatGrammarConstrainsOutput(t *testing.T) {
	m := loadTiny(t, Options{ContextSize: 256})
	// The tiny model's weights are random, so without the grammar it
	// produces gibberish; with it, only an allowed answer.
	grammar := `root ::= "{\"choice_id\":\"" ("item_read" | "unknown") "\"}"`
	for seed := uint32(0); seed < 3; seed++ {
		res, err := m.Chat(context.Background(), hello, 64, 0.8, seed, grammar)
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != `{"choice_id":"item_read"}` && res.Text != `{"choice_id":"unknown"}` {
			t.Errorf("seed %d: %q (stop %s)", seed, res.Text, res.StopReason)
		}
	}
	if _, err := m.Chat(context.Background(), hello, 8, 0, 0, `root ::= (`); err == nil || !strings.Contains(err.Error(), "grammar") {
		t.Errorf("a broken grammar gave err = %v", err)
	}
	// The model stays usable after a refused grammar.
	if _, err := m.Chat(context.Background(), hello, 4, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
}
