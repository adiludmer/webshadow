package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// cridPrereq is the candidate for the response id the suggestions family
// hands to the search request.
const cridPrereq = "prereq:4788fb527f3e:v1"

func prerequisites(t *testing.T, x *ir.InterfaceIR) map[string]ir.HypothesisRef {
	t.Helper()
	out := map[string]ir.HypothesisRef{}
	for _, h := range x.Hypotheses {
		if h.Kind == ir.KindPrerequisite {
			out[h.ID] = h
		}
	}
	return out
}

func TestPrerequisitesStayProposed(t *testing.T) {
	s := testStore(t)
	res, err := Analyze(context.Background(), fixture, mockOptions(s))
	if err != nil {
		t.Fatal(err)
	}
	hs := prerequisites(t, res.IR)
	if len(hs) == 0 || len(hs) != res.Manifest.Counts.Prerequisites {
		t.Fatalf("%d prerequisite hypotheses, manifest counts %d", len(hs), res.Manifest.Counts.Prerequisites)
	}
	for _, h := range hs {
		if h.Status != ir.StatusProposed {
			t.Errorf("prerequisite %s is %s; correlation never makes it more than proposed", h.ID, h.Status)
		}
	}
	lines := bytes.Split(bytes.TrimSpace(ledger(t, s, res.Run, "prerequisites.jsonl")), []byte("\n"))
	if len(lines) != len(hs) {
		t.Fatalf("%d ledger lines for %d hypotheses", len(lines), len(hs))
	}
	var session int
	for _, line := range lines {
		var p ir.PrerequisiteHypothesis
		if err := json.Unmarshal(line, &p); err != nil {
			t.Fatal(err)
		}
		if _, ok := hs[p.Hypothesis]; !ok {
			t.Errorf("%s names hypothesis %s, which the IR does not list", p.ID, p.Hypothesis)
		}
		if p.Scope == "" || len(p.EvidenceRefs) == 0 || p.ConsumerFamily == "" {
			t.Errorf("incomplete prerequisite %+v", p)
		}
		for _, r := range p.EvidenceRefs {
			if !res.Input.Has(baseRef(r)) {
				t.Errorf("%s cites %s, which is not in the evidence", p.ID, r)
			}
		}
		if p.Kind == "session_state" && p.Carrier == "cookie" && p.ConsumerFamily == "fam:98258e0892fb" {
			session++
		}
	}
	if session == 0 {
		t.Error("no session cookie prerequisite for the search family")
	}
}

func TestUnknownPrerequisiteIsKept(t *testing.T) {
	s := testStore(t)
	m := &decide.Mock{Script: map[string][]string{cridPrereq: {`{"choice_id":"unknown","evidence_refs":[]}`}}}
	res, err := Analyze(context.Background(), fixture, Options{Store: s, Decider: decide.New(m, decide.ModelInfo{}, decide.DefaultParams())})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range prerequisites(t, res.IR) {
		if h.CandidateID == decide.ChoiceUnknown && contains(h.SubjectRefs, "fam:98258e0892fb#request.query.crid") {
			found = h.Status == ir.StatusProposed
		}
	}
	if !found {
		t.Error("an unknown kind dropped the observed flow")
	}
}

func baseRef(r string) string {
	base, _, _ := strings.Cut(r, "#")
	return base
}
