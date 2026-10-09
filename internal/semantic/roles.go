package semantic

import (
	"context"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// StatusSettled marks a decision Go made by rule, without a model.
const StatusSettled = "settled"

// Checks a role hypothesis must pass.
const (
	CheckChoiceOffered   = "choice_offered"
	CheckEvidenceExists  = "evidence_resolves"
	CheckEvidenceSubject = "evidence_about_subject"
	CheckRule            = "settled_by_rule"
)

// Verification is one line of the verification ledger.
type Verification struct {
	Hypothesis string              `json:"hypothesis"`
	Status     ir.HypothesisStatus `json:"status"`
	Checks     []ir.CheckResult    `json:"checks"`
}

// outcome is what one stage produced.
type outcome struct {
	decisions     []decide.Decision
	hypotheses    []ir.Hypothesis
	verifications []Verification
	tasks         int
}

// classifyFamilies asks the role question for every family: settled ones
// by rule, the rest of the model through d.
func classifyFamilies(ctx context.Context, in *input.Input, d *decide.Decider) (outcome, error) {
	var out outcome
	subjects := subjectIndex(in)
	for _, q := range candidates.FamilyRoles(in) {
		subject := ir.Ref(ir.RefFamily, q.Family)
		if q.Task == nil {
			out.decisions = append(out.decisions, decide.Decision{
				TaskID: "role:" + q.Family + ":rule", TaskType: decide.TypeClassifyFamily, Subject: subject,
				Prompt: q.Rule, Model: decide.ModelInfo{ID: "rule"}, Status: StatusSettled,
				ChoiceID: q.Settled, EvidenceRefs: []string{subject}, Attempts: []decide.Attempt{},
			})
			h := ir.Hypothesis{
				Kind: ir.KindFamilyRole, SubjectRefs: []string{subject}, CandidateID: q.Settled,
				EvidenceRefs: []string{subject}, ModelRunID: "rule:" + q.Rule,
				Status: ir.StatusMechanicallySupported,
				Checks: []ir.CheckResult{{Check: CheckRule, Passed: true, Detail: q.Rule + ": " + q.Detail, EvidenceRefs: []string{subject}}},
			}
			out.add(h)
			continue
		}
		out.tasks++
		dec, err := d.Decide(ctx, *q.Task)
		if err != nil {
			return out, err
		}
		out.decisions = append(out.decisions, dec)
		if dec.Status != decide.StatusDecided || dec.ChoiceID == decide.ChoiceUnknown {
			continue
		}
		h := ir.Hypothesis{
			Kind: ir.KindFamilyRole, SubjectRefs: []string{subject}, CandidateID: dec.ChoiceID,
			EvidenceRefs: dec.EvidenceRefs, ModelRunID: dec.Model.ID + "@" + dec.Prompt,
			Confidence: ir.ConfidenceRecord{Supporting: len(dec.EvidenceRefs), Agreement: 1},
		}
		h.Checks = roleChecks(in, *q.Task, dec, subjects[q.Family])
		h.Status = ir.StatusMechanicallySupported
		for _, c := range h.Checks {
			if !c.Passed {
				h.Status = ir.StatusRejected
			}
		}
		out.add(h)
	}
	return out, nil
}

// add records a hypothesis and its checks, and returns it with its id.
func (o *outcome) add(h ir.Hypothesis) ir.Hypothesis {
	h.ID = hypothesisID(h)
	h.Canonicalize()
	o.hypotheses = append(o.hypotheses, h)
	o.verifications = append(o.verifications, Verification{Hypothesis: h.ID, Status: h.Status, Checks: h.Checks})
	return h
}

// hypothesisID is stable for the same claim about the same subject, so the
// same decision in a later run lands on the same hypothesis.
func hypothesisID(h ir.Hypothesis) string {
	return ir.NodeID(ir.RefHypothesis, string(h.Kind)+"\x00"+strings.Join(h.SubjectRefs, ",")+"\x00"+h.CandidateID)
}

// roleChecks are the deterministic checks on a model's answer. They show
// the answer is well formed and grounded in its subject's evidence, not
// that the answer is right.
func roleChecks(in *input.Input, task decide.Task, dec decide.Decision, about map[string]bool) []ir.CheckResult {
	_, offered := task.Choice(dec.ChoiceID)
	checks := []ir.CheckResult{{Check: CheckChoiceOffered, Passed: offered, Detail: dec.ChoiceID}}
	var missing, onSubject []string
	for _, r := range dec.EvidenceRefs {
		// A field reference resolves through the part before its path.
		if base, _, _ := strings.Cut(r, "#"); !in.Has(base) {
			missing = append(missing, r)
		}
		if about[r] {
			onSubject = append(onSubject, r)
		}
	}
	exists := ir.CheckResult{Check: CheckEvidenceExists, Passed: len(missing) == 0, EvidenceRefs: missing}
	if len(missing) > 0 {
		exists.Detail = "cited evidence missing from the clustering output"
	}
	subject := ir.CheckResult{Check: CheckEvidenceSubject, Passed: len(onSubject) > 0, EvidenceRefs: onSubject}
	if len(onSubject) == 0 {
		subject.Detail = "no cited evidence involves the subject"
	}
	return append(checks, exists, subject)
}

// subjectIndex maps each family to the evidence references that involve
// it: itself, its variants and observations, the links that carry its
// values, the sequence edges it is on and the episodes it ran in.
func subjectIndex(in *input.Input) map[string]map[string]bool {
	idx := map[string]map[string]bool{}
	add := func(family, ref string) {
		if idx[family] == nil {
			idx[family] = map[string]bool{}
		}
		idx[family][ref] = true
	}
	for _, f := range in.Families {
		add(f.ID, ir.Ref(ir.RefFamily, f.ID))
		for _, v := range f.ResponseVariants {
			add(f.ID, ir.Ref(ir.RefVariant, f.ID, v.ID))
		}
		for _, o := range f.Observations {
			add(f.ID, ir.Ref(ir.RefObservation, o.SessionID, o.ExchangeID))
		}
	}
	for _, l := range in.Links {
		for _, f := range l.FamilyIDs {
			add(f, ir.Ref(ir.RefValueLink, l.ID))
		}
	}
	for _, s := range in.Sequences {
		add(s.FromFamily, ir.Ref(ir.RefSequence, s.ID))
		add(s.ToFamily, ir.Ref(ir.RefSequence, s.ID))
	}
	for _, e := range in.Episodes {
		for _, f := range e.FamilyIDs {
			add(f, ir.Ref(ir.RefEpisode, e.ID))
		}
	}
	return idx
}

// patchHypotheses adds new hypotheses to the IR. A hypothesis already
// listed keeps its id and takes the new status. A claim of the same kind
// about the same subjects that is still standing is superseded by a new
// one that passed its checks, and stays listed as superseded; a person's
// review is never superseded by a run.
func patchHypotheses(x *ir.InterfaceIR, hs []ir.Hypothesis) []ir.Hypothesis {
	key := func(kind ir.HypothesisKind, subjects []string) string {
		return string(kind) + "\x00" + strings.Join(subjects, ",")
	}
	out := make([]ir.Hypothesis, 0, len(hs))
	for _, h := range hs {
		k := key(h.Kind, h.SubjectRefs)
		existing := -1
		for i := range x.Hypotheses {
			p := &x.Hypotheses[i]
			if p.ID == h.ID {
				existing = i
				continue
			}
			if h.Status == ir.StatusRejected || key(p.Kind, p.SubjectRefs) != k {
				continue
			}
			if p.Status == ir.StatusSuperseded || p.Status == ir.StatusRejected || p.Status == ir.StatusReviewed {
				continue
			}
			p.Status = ir.StatusSuperseded
			h.Supersedes = append(h.Supersedes, p.ID)
		}
		h.Canonicalize()
		if existing >= 0 {
			h.Supersedes = append(append([]string{}, x.Hypotheses[existing].Supersedes...), h.Supersedes...)
			h.Canonicalize()
			ref := h.Ref()
			if x.Hypotheses[existing].Status == ir.StatusReviewed {
				ref.Status = ir.StatusReviewed
			}
			x.Hypotheses[existing] = ref
		} else {
			x.Hypotheses = append(x.Hypotheses, h.Ref())
		}
		out = append(out, h)
	}
	return out
}
