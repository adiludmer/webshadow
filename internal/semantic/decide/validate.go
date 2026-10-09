package decide

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Grammar returns GBNF that admits exactly the answers a task allows: one
// of its choice ids and up to MaxEvidenceRefs of its evidence ids. A model
// run with it cannot invent a choice or an evidence id; it can still
// repeat an id or cite none, which Validate rejects.
func Grammar(task Task) string {
	var b strings.Builder
	b.WriteString(`root ::= "{" ws "\"choice_id\"" ws ":" ws choice ws "," ws "\"evidence_refs\"" ws ":" ws refs ws "}"` + "\n")
	b.WriteString("choice ::= ")
	for i, c := range task.Choices {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(gbnfString(`"` + c.ID + `"`))
	}
	b.WriteString("\n")
	if len(task.Evidence) == 0 {
		b.WriteString(`refs ::= "[" ws "]"` + "\n")
	} else {
		b.WriteString(`refs ::= "[" ws ( ref ( ws "," ws ref ){0,` + strconv.Itoa(MaxEvidenceRefs-1) + `} )? ws "]"` + "\n")
		b.WriteString("ref ::= ")
		for i, e := range task.Evidence {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(gbnfString(`"` + e.ID + `"`))
		}
		b.WriteString("\n")
	}
	b.WriteString(`ws ::= [ \n]{0,2}` + "\n")
	return b.String()
}

// gbnfString renders s as a GBNF string literal.
func gbnfString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Validate parses a reply strictly and checks it against the task. It
// returns the answer with evidence aliases resolved to clustering
// references.
func Validate(task Task, reply string) (Answer, []string, error) {
	text := strings.TrimSpace(reply)
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var a Answer
	if err := dec.Decode(&a); err != nil {
		return Answer{}, nil, fmt.Errorf("not the required JSON object: %v", err)
	}
	if dec.More() {
		return Answer{}, nil, errors.New("text after the JSON object")
	}
	if !strings.HasPrefix(text, "{") {
		return Answer{}, nil, errors.New("not a JSON object")
	}
	if _, ok := task.Choice(a.ChoiceID); !ok {
		return Answer{}, nil, fmt.Errorf("choice_id %q is not one of the choices", a.ChoiceID)
	}
	if a.EvidenceRefs == nil {
		a.EvidenceRefs = []string{}
	}
	if len(a.EvidenceRefs) > MaxEvidenceRefs {
		return Answer{}, nil, fmt.Errorf("%d evidence_refs, at most %d allowed", len(a.EvidenceRefs), MaxEvidenceRefs)
	}
	if len(a.EvidenceRefs) == 0 && a.ChoiceID != ChoiceUnknown {
		return Answer{}, nil, errors.New("evidence_refs is empty; cite the evidence for the choice")
	}
	seen := map[string]bool{}
	refs := make([]string, 0, len(a.EvidenceRefs))
	for _, id := range a.EvidenceRefs {
		if seen[id] {
			return Answer{}, nil, fmt.Errorf("evidence id %q is cited twice", id)
		}
		seen[id] = true
		ref, ok := task.Ref(id)
		if !ok {
			return Answer{}, nil, fmt.Errorf("evidence id %q is not in the task", id)
		}
		refs = append(refs, ref)
	}
	return a, refs, nil
}
