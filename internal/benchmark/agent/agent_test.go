package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
)

func TestParseAction(t *testing.T) {
	tests := []struct {
		name, reply, action, args, err string
	}{
		{"plain", `{"action": "read", "args": {"path": "a.md"}}`, "read", `{"path": "a.md"}`, ""},
		{"fenced", "```json\n{\"action\": \"list\", \"args\": {}}\n```", "list", `{}`, ""},
		{"prose around", "I'll look first.\n{\"action\": \"list\"}\nThen read.", "list", "", ""},
		{"skips non-action braces", `Using {curly} words. {"action": "search", "args": {"query": "x"}}`, "search", `{"query": "x"}`, ""},
		{"finish", `{"action": "finish", "answer": {"amount": 15000000}}`, "finish", "", ""},
		{"no object", "I think the answer is 15 million.", "", "", "no JSON object"},
		{"no action", `{"answer": 1}`, "", "", `no "action" field`},
		{"finish without answer", `{"action": "finish"}`, "", "", `needs an "answer"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ParseAction(tt.reply)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.Action != tt.action || string(a.Args) != tt.args {
				t.Errorf("got %q %s, want %q %s", a.Action, a.Args, tt.action, tt.args)
			}
		})
	}
}

// echoTool returns its "text" argument, or fails when told to.
type echoTool struct{}

func (echoTool) Name() string  { return "echo" }
func (echoTool) Usage() string { return `Echo text. args: {"text": string}` }
func (echoTool) Run(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Text string `json:"text"`
		Fail bool   `json:"fail"`
	}
	json.Unmarshal(raw, &args)
	if args.Fail {
		return "", errors.New("echo failed")
	}
	return args.Text, nil
}

// fakeClock advances one second per reading.
func fakeClock() func() time.Time {
	t := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func config() Config {
	return Config{Instructions: "You are a test agent.", Task: "Do the thing.", Tools: []Tool{echoTool{}}, MaxSteps: 10, Now: fakeClock()}
}

func TestRunToolThenFinish(t *testing.T) {
	m := model.NewFake("fake",
		`{"action": "echo", "args": {"text": "hello"}}`,
		`{"action": "finish", "answer": {"said": "hello"}}`,
	)
	res := Run(context.Background(), m, config())
	if res.Outcome != Finished || string(res.Answer) != `{"said": "hello"}` {
		t.Fatalf("outcome %s, answer %s, err %v", res.Outcome, res.Answer, res.Err)
	}
	if len(res.Steps) != 2 || res.Steps[0].Result != "hello" || res.Steps[1].Action != FinishAction {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if res.Duration <= 0 || res.Steps[0].Duration != time.Second {
		t.Errorf("durations not taken from the clock: run %v, step %v", res.Duration, res.Steps[0].Duration)
	}

	reqs := m.Requests()
	sys := reqs[0].Messages[0].Content
	if !strings.HasPrefix(sys, "You are a test agent.") || !strings.Contains(sys, "- echo: Echo text.") || !strings.Contains(sys, `"action": "finish"`) {
		t.Errorf("system prompt:\n%s", sys)
	}
	second := reqs[1].Messages
	if last := second[len(second)-1]; last.Role != model.RoleUser || last.Content != "Result of echo:\nhello" {
		t.Errorf("tool result message = %+v", last)
	}
	if second[2].Role != model.RoleAssistant {
		t.Errorf("the model's reply is not in the transcript: %+v", second)
	}
}

func TestRunFeedsBackErrors(t *testing.T) {
	m := model.NewFake("fake",
		`the answer is probably 42`,
		`{"action": "fly"}`,
		`{"action": "echo", "args": {"fail": true}}`,
		`{"action": "finish", "answer": 42}`,
	)
	res := Run(context.Background(), m, config())
	if res.Outcome != Finished || string(res.Answer) != "42" {
		t.Fatalf("outcome %s, answer %s", res.Outcome, res.Answer)
	}
	reqs := m.Requests()
	feedback := func(i int) string { msgs := reqs[i].Messages; return msgs[len(msgs)-1].Content }
	if !strings.Contains(feedback(1), "not a valid action: no JSON object") {
		t.Errorf("parse feedback: %q", feedback(1))
	}
	if !strings.Contains(feedback(2), `unknown action "fly"`) || !strings.Contains(feedback(2), "echo, finish") {
		t.Errorf("unknown action feedback: %q", feedback(2))
	}
	if feedback(3) != "Error from echo: echo failed" {
		t.Errorf("tool error feedback: %q", feedback(3))
	}
	for i, want := range []string{"no JSON object found", `unknown action "fly"`, "echo failed"} {
		if res.Steps[i].Error != want {
			t.Errorf("step %d error = %q, want %q", i+1, res.Steps[i].Error, want)
		}
	}
}

func TestRunLimits(t *testing.T) {
	loop := `{"action": "echo", "args": {"text": "again"}}`

	cfg := config()
	cfg.MaxSteps = 3
	res := Run(context.Background(), model.NewFake("fake", loop, loop, loop, loop), cfg)
	if res.Outcome != StepLimit || len(res.Steps) != 3 {
		t.Errorf("step limit: %s after %d steps", res.Outcome, len(res.Steps))
	}

	cfg = config()
	cfg.MaxTokens = 100 // the fake counts characters; the system prompt alone exceeds this
	res = Run(context.Background(), model.NewFake("fake", loop, loop), cfg)
	if res.Outcome != TokenLimit || len(res.Steps) != 1 {
		t.Errorf("token limit: %s after %d steps", res.Outcome, len(res.Steps))
	}

	res = Run(context.Background(), model.NewFake("fake"), config())
	if res.Outcome != Failed || !errors.Is(res.Err, model.ErrScriptExhausted) {
		t.Errorf("model error: %s, %v", res.Outcome, res.Err)
	}
}

// slowModel blocks until its context ends.
type slowModel struct{}

func (slowModel) ID() string   { return "slow" }
func (slowModel) Close() error { return nil }
func (slowModel) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

func TestRunTimeout(t *testing.T) {
	cfg := config()
	cfg.Now = nil
	cfg.Timeout = 20 * time.Millisecond
	res := Run(context.Background(), slowModel{}, cfg)
	if res.Outcome != Timeout || !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("outcome %s, err %v", res.Outcome, res.Err)
	}
}

func TestRunTruncatesObservations(t *testing.T) {
	big := strings.Repeat("é", 100) // 200 bytes
	m := model.NewFake("fake",
		`{"action": "echo", "args": {"text": "`+big+`"}}`,
		`{"action": "finish", "answer": null}`,
	)
	cfg := config()
	cfg.MaxObservation = 51
	res := Run(context.Background(), m, cfg)
	got := res.Steps[0].Result
	if !strings.HasPrefix(got, strings.Repeat("é", 25)+"\n[truncated: showing 50 of 200 bytes]") {
		t.Fatalf("truncated result = %q", got)
	}
}

func TestRunElidesOldObservations(t *testing.T) {
	long := strings.Repeat("x", 500)
	call := `{"action": "echo", "args": {"text": "` + long + `"}}`
	m := model.NewFake("fake", call, call, call, `{"action": "finish", "answer": 1}`)
	cfg := config()
	cfg.KeepObservations = 2
	if res := Run(context.Background(), m, cfg); res.Outcome != Finished {
		t.Fatalf("outcome %s", res.Outcome)
	}
	last := m.Requests()[3].Messages
	// system, task, then (reply, result) three times.
	results := []string{last[3].Content, last[5].Content, last[7].Content}
	if !strings.Contains(results[0], "older result shortened") || len(results[0]) > 300 {
		t.Errorf("oldest result not shortened: %d bytes", len(results[0]))
	}
	for i, r := range results[1:] {
		if !strings.HasSuffix(r, long) {
			t.Errorf("recent result %d was shortened", i+2)
		}
	}
	res := Run(context.Background(), model.NewFake("fake", call, `{"action": "finish", "answer": 1}`), cfg)
	if !strings.HasSuffix(res.Steps[0].Result, long) {
		t.Error("the recorded step lost its full result")
	}
}
