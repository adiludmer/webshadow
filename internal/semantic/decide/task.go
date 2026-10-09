// Package decide runs closed-choice decision tasks against a model. Go
// builds every task: a question, a capped list of choices that always
// includes unknown, and the evidence the model may cite. The model answers
// with one choice id and the evidence ids it relied on, nothing else.
// Answers are validated strictly; one invalid answer earns one repair
// prompt, and a second leaves the task unresolved. Nothing the model writes
// beyond those two fields is kept as anything but a log line.
package decide

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Task types.
const (
	TypeClassifyFamily = "classify_family"
	TypeNameEntity     = "name_entity"
	TypeClassifyPrereq = "classify_prereq"
)

// ChoiceUnknown is offered by every task: the model's way to abstain.
const ChoiceUnknown = "unknown"

// MaxEvidenceRefs bounds how many evidence ids an answer may cite.
const MaxEvidenceRefs = 5

// Choice is one answer a task offers.
type Choice struct {
	ID      string `json:"id"`
	Meaning string `json:"meaning"`
	// Why records what in the evidence made the generator offer this
	// choice. It is structural, never a judgement of meaning.
	Why string `json:"why,omitempty"`
}

// Pruned is a choice the generator did not offer, and why.
type Pruned struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Evidence is one item a task shows. ID is the short alias the model cites,
// such as "E3"; Ref is the clustering reference it stands for. Text is
// recorded data, already redacted, and is shown to the model as data only.
type Evidence struct {
	ID   string `json:"id"`
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

// Task is one closed-choice decision.
type Task struct {
	ID              string     `json:"task_id"`
	Type            string     `json:"task_type"`
	Subject         string     `json:"subject"`
	EvidencePackRef string     `json:"evidence_pack_ref"`
	Question        string     `json:"question"`
	Choices         []Choice   `json:"choices"`
	Pruned          []Pruned   `json:"pruned,omitempty"`
	Evidence        []Evidence `json:"evidence"`
}

// Hash identifies the task's content, so a decision records exactly what
// the model was asked.
func (t Task) Hash() string {
	data, _ := json.Marshal(t)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Choice returns the offered choice with id, if any.
func (t Task) Choice(id string) (Choice, bool) {
	for _, c := range t.Choices {
		if c.ID == id {
			return c, true
		}
	}
	return Choice{}, false
}

// Ref returns the reference an evidence alias stands for.
func (t Task) Ref(alias string) (string, bool) {
	for _, e := range t.Evidence {
		if e.ID == alias {
			return e.Ref, true
		}
	}
	return "", false
}

// Answer is the only output a model may give.
type Answer struct {
	ChoiceID     string   `json:"choice_id"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// view is the task as the model sees it: the protocol fields from the
// spec, with no references into the clustering output.
type view struct {
	TaskID          string         `json:"task_id"`
	TaskType        string         `json:"task_type"`
	EvidencePackRef string         `json:"evidence_pack_ref"`
	Question        string         `json:"question"`
	Choices         []Choice       `json:"choices"`
	EvidenceIDs     []string       `json:"evidence_ids"`
	RequiredOutput  map[string]any `json:"required_output"`
}

func (t Task) view() view {
	ids := make([]string, len(t.Evidence))
	for i, e := range t.Evidence {
		ids[i] = e.ID
	}
	return view{
		TaskID: t.ID, TaskType: t.Type, EvidencePackRef: t.EvidencePackRef, Question: t.Question,
		Choices: t.Choices, EvidenceIDs: ids,
		RequiredOutput: map[string]any{"choice_id": "enum", "evidence_refs": "array"},
	}
}
