package decide

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
)

// Decision statuses.
const (
	StatusDecided    = "decided"
	StatusUnresolved = "unresolved"
)

// Params bound every model call. They are recorded with each decision.
type Params struct {
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Timeout bounds one model call.
	Timeout time.Duration `json:"timeout"`
	// MaxPromptTokens bounds the rendered prompt, estimated at four
	// characters a token; a task over budget is unresolved without a call.
	MaxPromptTokens int `json:"max_prompt_tokens"`
}

// DefaultParams suit small local models.
func DefaultParams() Params {
	return Params{MaxTokens: 96, Temperature: 0, Timeout: 2 * time.Minute, MaxPromptTokens: 6000}
}

// ModelInfo describes the model a decision came from.
type ModelInfo struct {
	ID string `json:"id"`
	// Adapter, Path and Quantization are what the registry says about it,
	// when known.
	Adapter      string `json:"adapter,omitempty"`
	Path         string `json:"path,omitempty"`
	Quantization string `json:"quantization,omitempty"`
}

// Attempt is one model call.
type Attempt struct {
	// Reply is the model's text, kept as a non-authoritative log line.
	Reply        string `json:"reply"`
	Error        string `json:"error,omitempty"`
	LatencyMS    int64  `json:"latency_ms"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	StopReason   string `json:"stop_reason,omitempty"`
}

// Decision is the record of one task: what was asked, of which model with
// which prompt, every attempt, and the validated outcome.
type Decision struct {
	TaskID    string    `json:"task_id"`
	TaskType  string    `json:"task_type"`
	Subject   string    `json:"subject"`
	InputHash string    `json:"input_hash"`
	Prompt    string    `json:"prompt"`
	Model     ModelInfo `json:"model"`
	Params    Params    `json:"params"`
	Status    string    `json:"status"`
	// ChoiceID and EvidenceRefs are set when Status is decided.
	// EvidenceRefs are clustering references, not the aliases the model
	// cited.
	ChoiceID     string    `json:"choice_id,omitempty"`
	EvidenceRefs []string  `json:"evidence_refs,omitempty"`
	Unresolved   string    `json:"unresolved,omitempty"`
	Attempts     []Attempt `json:"attempts"`
	// Reused names the run whose decision this one repeats: the same task,
	// prompt and model, so the model was not asked again.
	Reused string `json:"reused,omitempty"`
}

// Decider asks a model closed-choice questions.
type Decider struct {
	Model  model.Model
	Info   ModelInfo
	Params Params
	// Prior holds earlier decisions by task id. A decided task whose
	// content, prompt and model are unchanged is answered from it, so a
	// run over mostly unchanged evidence only asks about what changed.
	Prior map[string]Decision
	// PriorRun names the run Prior came from.
	PriorRun string
	// templates caches each task type's prompt.
	templates map[string]*Template
}

// New returns a Decider for m.
func New(m model.Model, info ModelInfo, p Params) *Decider {
	if info.ID == "" {
		info.ID = m.ID()
	}
	return &Decider{Model: m, Info: info, Params: p, templates: map[string]*Template{}}
}

// Decide runs one task: a call, one repair call if the first reply is
// invalid, and a validated outcome or an unresolved record. It returns an
// error only when the task itself cannot be rendered.
func (d *Decider) Decide(ctx context.Context, task Task) (Decision, error) {
	tmpl, ok := d.templates[task.Type]
	if !ok {
		var err error
		if tmpl, err = LoadTemplate(task.Type); err != nil {
			return Decision{}, err
		}
		d.templates[task.Type] = tmpl
	}
	msgs, err := tmpl.Render(task)
	if err != nil {
		return Decision{}, err
	}
	dec := Decision{
		TaskID: task.ID, TaskType: task.Type, Subject: task.Subject, InputHash: task.Hash(),
		Prompt: tmpl.ID, Model: d.Info, Params: d.Params, Status: StatusUnresolved, Attempts: []Attempt{},
	}
	if p, ok := d.Prior[task.ID]; ok && p.Status == StatusDecided && p.InputHash == dec.InputHash &&
		p.Prompt == dec.Prompt && p.Model == d.Info && p.Params == d.Params {
		if p.Reused == "" {
			p.Reused = d.PriorRun
		}
		p.Attempts = []Attempt{}
		return p, nil
	}
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content)
	}
	if d.Params.MaxPromptTokens > 0 && chars/4 > d.Params.MaxPromptTokens {
		dec.Unresolved = fmt.Sprintf("prompt budget: about %d tokens, limit %d", chars/4, d.Params.MaxPromptTokens)
		return dec, nil
	}
	grammar := Grammar(task)
	for attempt := 0; attempt < 2; attempt++ {
		reply, a, err := d.call(ctx, msgs, grammar)
		dec.Attempts = append(dec.Attempts, a)
		if err != nil {
			dec.Unresolved = "model error: " + err.Error()
			if errors.Is(err, context.Canceled) {
				return dec, err
			}
			return dec, nil
		}
		answer, refs, verr := Validate(task, reply)
		if verr == nil {
			dec.Status, dec.ChoiceID, dec.EvidenceRefs, dec.Unresolved = StatusDecided, answer.ChoiceID, refs, ""
			return dec, nil
		}
		dec.Attempts[len(dec.Attempts)-1].Error = verr.Error()
		dec.Unresolved = "invalid answer: " + verr.Error()
		msgs = append(msgs, model.Message{Role: model.RoleAssistant, Content: reply}, repairMessage(task, verr.Error()))
	}
	return dec, nil
}

func (d *Decider) call(ctx context.Context, msgs []model.Message, grammar string) (string, Attempt, error) {
	if d.Params.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Params.Timeout)
		defer cancel()
	}
	temp := d.Params.Temperature
	start := time.Now()
	res, err := d.Model.Complete(ctx, model.Request{
		Messages: msgs, MaxTokens: d.Params.MaxTokens, Temperature: &temp, Grammar: grammar,
	})
	a := Attempt{
		Reply: truncate(res.Text, 2048), LatencyMS: time.Since(start).Milliseconds(),
		InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, StopReason: res.StopReason,
	}
	if err != nil {
		a.Error = err.Error()
	}
	return res.Text, a, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
