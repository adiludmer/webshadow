package reader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

func ensoScenario(t *testing.T) *scenario.Scenario {
	t.Helper()
	s, err := scenario.Load(filepath.Join("..", "testdata", "enso"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ensoTree() *os.Root {
	root, err := os.OpenRoot(filepath.Join("testdata", "enso-tree"))
	if err != nil {
		panic(err)
	}
	return root
}

func TestRunStatuses(t *testing.T) {
	s := ensoScenario(t)
	root := ensoTree()
	defer root.Close()

	search := `{"action": "search", "args": {"query": "enso"}}`
	tests := []struct {
		name    string
		replies []string
		want    Status
	}{
		{"correct", []string{search, `{"action": "read", "args": {"path": "articles/enso-funding.md"}}`, `{"action": "finish", "answer": {"amount": "$15,000,000", "currency": " usd"}}`}, Success},
		{"wrong amount", []string{`{"action": "finish", "answer": {"amount": 1500000, "currency": "USD"}}`}, WrongAnswer},
		{"missing field", []string{`{"action": "finish", "answer": {"amount": 15000000}}`}, WrongAnswer},
		{"not an object", []string{`{"action": "finish", "answer": "15 million dollars"}`}, InvalidOutput},
		{"model gives up", nil, Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := model.NewFake("fake", tt.replies...)
			res, err := Run(context.Background(), m, s, root.FS(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.want {
				t.Fatalf("status = %s, want %s (score %+v, err %v)", res.Status, tt.want, res.Score, res.Agent.Err)
			}
			if len(m.Requests()) == 0 {
				return
			}
			first := m.Requests()[0].Messages
			if !strings.Contains(first[0].Content, "exactly these keys: amount, currency") || !strings.Contains(first[1].Content, "How much money did the startup Enso raise") {
				t.Errorf("prompt does not carry the keys and goal:\n%s\n---\n%s", first[0].Content, first[1].Content)
			}
		})
	}
}

func TestRunStepLimit(t *testing.T) {
	s := ensoScenario(t)
	s.Limits.Answer.MaxSteps = 2
	root := ensoTree()
	defer root.Close()
	list := `{"action": "list", "args": {}}`
	res, err := Run(context.Background(), model.NewFake("fake", list, list, list), s, root.FS(), Options{})
	if err != nil || res.Status != StepLimit || res.Answer != nil {
		t.Fatalf("status %s, answer %v, err %v", res.Status, res.Answer, err)
	}
}
