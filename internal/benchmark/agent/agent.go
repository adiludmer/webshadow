// Package agent runs a model as a tool-using agent through a plain-text
// JSON action protocol, so any chat model can drive it without native tool
// calling. Each reply must hold one JSON object: either a tool call,
// {"action": "<tool>", "args": {...}}, or the final answer,
// {"action": "finish", "answer": ...}. Tool results go back as the next
// user message. The reader and generator stages are both built on it.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
)

// Tool is an action the agent may take.
type Tool interface {
	Name() string
	// Usage describes the tool and its arguments for the system prompt,
	// for example: `Read a file. args: {"path": string}`.
	Usage() string
	// Run executes the tool. An error is shown to the model as the
	// observation; it does not stop the run.
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// FinishAction is the action name that ends a run.
const FinishAction = "finish"

// Outcome says how a run ended.
type Outcome string

const (
	Finished   Outcome = "finished"    // the model sent a finish action
	StepLimit  Outcome = "step_limit"  // MaxSteps replies without finishing
	TokenLimit Outcome = "token_limit" // MaxTokens used without finishing
	Timeout    Outcome = "timeout"     // the deadline passed
	Failed     Outcome = "error"       // the model call failed
)

// Config describes one run.
type Config struct {
	// Instructions is the stage's role prompt; the protocol and tool list
	// are appended to it.
	Instructions string
	// Task is the first user message.
	Task  string
	Tools []Tool

	MaxSteps int
	Timeout  time.Duration
	// MaxTokens caps input plus output tokens over the run; zero means no
	// cap. It is checked after each reply, so a run can overshoot by one.
	MaxTokens int
	// MaxObservation truncates each tool result to this many bytes so one
	// large file cannot fill the context; zero means DefaultMaxObservation.
	MaxObservation int

	// KeepObservations is how many of the latest tool results stay in the
	// transcript in full; older ones are cut to a short stub so long runs
	// fit a local model's context. Zero means DefaultKeepObservations and a
	// negative value keeps everything.
	KeepObservations int

	// Now is the clock for timestamps; nil means time.Now.
	Now func() time.Time
}

// Defaults for Config fields left at zero.
const (
	DefaultMaxObservation   = 8000
	DefaultKeepObservations = 6
)

// elidedSize is how much of an old tool result stays visible.
const elidedSize = 200

// Step is one model reply and what came of it.
type Step struct {
	Index    int             `json:"index"`
	Reply    string          `json:"reply"`
	Action   string          `json:"action,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   string          `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	Usage    model.Usage     `json:"usage"`
	Duration time.Duration   `json:"duration_ns"`
}

// Result is a finished run.
type Result struct {
	Outcome Outcome
	// Answer is the finish action's answer, raw; nil unless Finished.
	Answer json.RawMessage
	Steps  []Step
	Usage  model.Usage
	// Err explains an error or timeout outcome.
	Err       error
	StartedAt time.Time
	Duration  time.Duration
	// System is the full system prompt the model received.
	System string
}

// Run drives m until it finishes or hits a limit.
func Run(ctx context.Context, m model.Model, cfg Config) (res Result) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	maxObs := cfg.MaxObservation
	if maxObs <= 0 {
		maxObs = DefaultMaxObservation
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	keep := cfg.KeepObservations
	if keep == 0 {
		keep = DefaultKeepObservations
	}
	tools := map[string]Tool{}
	for _, t := range cfg.Tools {
		tools[t.Name()] = t
	}

	res = Result{StartedAt: now(), System: SystemPrompt(cfg.Instructions, cfg.Tools)}
	defer func() { res.Duration = now().Sub(res.StartedAt) }()
	msgs := []model.Message{
		{Role: model.RoleSystem, Content: res.System},
		{Role: model.RoleUser, Content: cfg.Task},
	}
	var observations []int // indexes of tool-result messages in msgs

	for i := 1; i <= cfg.MaxSteps; i++ {
		started := now()
		resp, err := m.Complete(ctx, model.Request{Messages: msgs})
		if err != nil {
			res.Outcome, res.Err = Failed, err
			if ctx.Err() != nil {
				res.Outcome, res.Err = Timeout, ctx.Err()
			}
			return res
		}
		step := Step{Index: i, Reply: resp.Text, Usage: resp.Usage}
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		msgs = append(msgs, model.Message{Role: model.RoleAssistant, Content: resp.Text})

		var feedback string
		act, perr := ParseAction(resp.Text)
		switch {
		case perr != nil:
			step.Error = perr.Error()
			feedback = "Your reply was not a valid action: " + perr.Error() +
				"\nReply with exactly one JSON object, for example {\"action\": \"finish\", \"answer\": {...}}."
		case act.Action == FinishAction:
			step.Action = act.Action
			step.Duration = now().Sub(started)
			res.Steps = append(res.Steps, step)
			res.Outcome, res.Answer = Finished, act.Answer
			return res
		default:
			step.Action, step.Args = act.Action, act.Args
			tool, ok := tools[act.Action]
			if !ok {
				step.Error = fmt.Sprintf("unknown action %q", act.Action)
				feedback = fmt.Sprintf("Error: unknown action %q. Available actions: %s, %s.", act.Action, strings.Join(names(cfg.Tools), ", "), FinishAction)
				break
			}
			out, err := tool.Run(ctx, act.Args)
			if err != nil {
				step.Error = err.Error()
				feedback = fmt.Sprintf("Error from %s: %v", act.Action, err)
			} else {
				out = truncate(out, maxObs)
				step.Result = out
				feedback = fmt.Sprintf("Result of %s:\n%s", act.Action, out)
			}
		}
		step.Duration = now().Sub(started)
		res.Steps = append(res.Steps, step)

		if ctx.Err() != nil {
			res.Outcome, res.Err = Timeout, ctx.Err()
			return res
		}
		if cfg.MaxTokens > 0 && res.Usage.InputTokens+res.Usage.OutputTokens >= cfg.MaxTokens {
			res.Outcome = TokenLimit
			return res
		}
		msgs = append(msgs, model.Message{Role: model.RoleUser, Content: feedback})
		observations = append(observations, len(msgs)-1)
		if keep > 0 && len(observations) > keep {
			i := observations[len(observations)-keep-1]
			msgs[i].Content = elide(msgs[i].Content)
		}
	}
	res.Outcome = StepLimit
	return res
}

// SystemPrompt appends the action protocol and tool list to instructions.
func SystemPrompt(instructions string, tools []Tool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(instructions))
	b.WriteString(`

## How to act

Reply with exactly one JSON object and nothing else.
To use a tool: {"action": "<tool name>", "args": {...}}
To give your final answer: {"action": "finish", "answer": <answer>}
After each tool call you will receive its result.

## Tools
`)
	for _, t := range tools {
		fmt.Fprintf(&b, "\n- %s: %s", t.Name(), t.Usage())
	}
	b.WriteString("\n")
	return b.String()
}

// Action is a parsed reply.
type Action struct {
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
	Answer json.RawMessage `json:"answer"`
}

// ParseAction extracts the first JSON object from a reply. It tolerates
// prose and code fences around the object, since small models add them.
func ParseAction(reply string) (Action, error) {
	for start := strings.IndexByte(reply, '{'); start >= 0; {
		dec := json.NewDecoder(strings.NewReader(reply[start:]))
		dec.UseNumber()
		var a Action
		if err := dec.Decode(&a); err == nil {
			if a.Action == "" {
				return Action{}, errors.New(`the JSON object has no "action" field`)
			}
			if a.Action == FinishAction && len(a.Answer) == 0 {
				return Action{}, errors.New(`a finish action needs an "answer" field`)
			}
			return a, nil
		}
		next := strings.IndexByte(reply[start+1:], '{')
		if next < 0 {
			break
		}
		start += 1 + next
	}
	return Action{}, errors.New("no JSON object found")
}

func names(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name()
	}
	return out
}

// truncate cuts s to at most n bytes on a rune boundary and says so.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[truncated: showing %d of %d bytes]", cut, len(s))
}

// elide shortens an old tool result to its start.
func elide(s string) string {
	if len(s) <= elidedSize {
		return s
	}
	cut := elidedSize
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[older result shortened; call the tool again if you need it]"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
