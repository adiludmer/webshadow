package decide

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
)

// MockID is the model id of the built-in mock.
const MockID = "mock"

// Mock is an offline model for tests and dry runs. It reads the task from
// the prompt and answers with the first choice the generator offered,
// citing the first evidence item, unless Script holds a reply for the
// task id. Generators order choices by structural fit, so the mock's
// answers are the deterministic baseline a real model is compared with.
type Mock struct {
	// Script maps task ids to the exact replies to give, valid or not.
	// A list gives one reply per call, for exercising repairs.
	Script map[string][]string
	calls  map[string]int
}

func (m *Mock) ID() string   { return MockID }
func (m *Mock) Close() error { return nil }

// Complete answers the task in the request's first user message.
func (m *Mock) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	v, err := taskView(req.Messages)
	if err != nil {
		return model.Response{}, err
	}
	if replies, ok := m.Script[v.TaskID]; ok {
		if m.calls == nil {
			m.calls = map[string]int{}
		}
		i := m.calls[v.TaskID]
		m.calls[v.TaskID]++
		if i >= len(replies) {
			return model.Response{}, model.ErrScriptExhausted
		}
		return model.Response{Text: replies[i], StopReason: "stop"}, nil
	}
	a := Answer{ChoiceID: ChoiceUnknown, EvidenceRefs: []string{}}
	for _, c := range v.Choices {
		if c.ID != ChoiceUnknown {
			a.ChoiceID = c.ID
			break
		}
	}
	if a.ChoiceID != ChoiceUnknown && len(v.EvidenceIDs) > 0 {
		a.EvidenceRefs = []string{v.EvidenceIDs[0]}
	}
	data, _ := json.Marshal(a)
	return model.Response{Text: string(data), StopReason: "stop"}, nil
}

// taskView finds the task JSON block the prompt template embeds.
func taskView(msgs []model.Message) (view, error) {
	for _, msg := range msgs {
		if msg.Role != model.RoleUser {
			continue
		}
		_, rest, ok := strings.Cut(msg.Content, "```json\n")
		if !ok {
			continue
		}
		body, _, ok := strings.Cut(rest, "\n```")
		if !ok {
			continue
		}
		var v view
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return view{}, err
		}
		return v, nil
	}
	return view{}, errors.New("mock model: no task in the prompt")
}
