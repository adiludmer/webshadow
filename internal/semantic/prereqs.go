package semantic

import (
	"context"
	"fmt"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Checks a prerequisite hypothesis carries.
const (
	CheckFlowObserved = "flow_observed"
	CheckAbsence      = "absence_statistics"
)

// prereqOutcome is what the prerequisite stage produced, besides its
// ledgers: each candidate as an operation would list it.
type prereqOutcome struct {
	outcome
	preconditions []ir.PrerequisiteHypothesis
	// fed lists request slots an earlier response filled on every request
	// with a producer always before it: protocol state, not user input.
	fed map[string]bool
}

// operationRoles are the family roles operations are built on, and so the
// families whose prerequisites matter.
var operationRoles = map[string]bool{
	candidates.RoleCollectionRead: true,
	candidates.RoleItemRead:       true,
	candidates.RoleMutation:       true,
}

// discoverPrerequisites proposes the state each operation-bearing family
// may need. A model labels the kind of state, or says unknown; whatever
// it says, a prerequisite stays proposed, since a recording shows
// correlation and never necessity. One the model's answer fails the checks
// on is rejected.
func discoverPrerequisites(ctx context.Context, in *input.Input, d *decide.Decider, roles []ir.Hypothesis) (prereqOutcome, error) {
	out := prereqOutcome{fed: map[string]bool{}}
	consumers := map[string]bool{}
	for _, h := range roles {
		if h.Status == ir.StatusMechanicallySupported && operationRoles[h.CandidateID] {
			for _, s := range h.SubjectRefs {
				_, id, _ := ir.SplitRef(s)
				consumers[id] = true
			}
		}
	}
	for _, c := range candidates.Prerequisites(in, consumers) {
		out.tasks++
		dec, err := d.Decide(ctx, c.Kind)
		if err != nil {
			return out, err
		}
		out.decisions = append(out.decisions, dec)

		h := ir.Hypothesis{
			Kind: ir.KindPrerequisite, SubjectRefs: prereqSubjects(c), CandidateID: decide.ChoiceUnknown,
			EvidenceRefs: append([]string{ir.Ref(ir.RefSequence, c.Edge)}, c.Examples...),
			ModelRunID:   dec.Model.ID + "@" + dec.Prompt,
			Confidence:   ir.ConfidenceRecord{Supporting: c.Matches, Contradicting: c.Contradicting()},
			Status:       ir.StatusProposed,
		}
		if dec.Status != decide.StatusDecided {
			h.Confidence.Unresolved = 1
		}
		if dec.Status == decide.StatusDecided && dec.ChoiceID != decide.ChoiceUnknown {
			h.CandidateID = dec.ChoiceID
			h.EvidenceRefs = append(h.EvidenceRefs, dec.EvidenceRefs...)
			h.Confidence.Agreement = 1
			h.Checks = roleChecks(in, c.Kind, dec, prereqAbout(c))
			if statusOf(h.Checks, ir.StatusRejected) == ir.StatusRejected {
				h.Status = ir.StatusRejected
			}
		}
		h.Checks = append(h.Checks,
			ir.CheckResult{
				Check: CheckFlowObserved, Passed: c.Matches > 0,
				Detail:       fmt.Sprintf("%s state reached %d of %d consumer requests", c.Carrier, c.Matches, c.Of),
				EvidenceRefs: c.Examples,
			},
			ir.CheckResult{
				Check: CheckAbsence, Passed: true,
				Detail: fmt.Sprintf("without the state: %d; without an earlier producer: %d; alternate producers: %d; caveats: %s",
					c.Of-c.Matches, c.WithoutPredecessor, len(c.Alternates), strings.Join(c.Caveats, "; ")),
			},
		)
		h = out.add(h)
		if c.Carrier == candidates.CarrierScalar && c.WithoutPredecessor == 0 {
			for _, k := range c.Always {
				out.fed[ir.Ref(ir.RefFamily, c.Consumer)+"#"+k] = true
			}
		}
		out.preconditions = append(out.preconditions, c.Precondition(h.CandidateID, h.Status, h.ID))
	}
	return out, nil
}

// prereqSubjects names a candidate by its two families and the request
// locations the state arrived at, so cookie and scalar state between the
// same families are separate claims.
func prereqSubjects(c candidates.PrereqCandidate) []string {
	out := []string{ir.Ref(ir.RefFamily, c.Consumer), ir.Ref(ir.RefFamily, c.Producer)}
	for _, k := range c.Keys {
		out = append(out, ir.Ref(ir.RefFamily, c.Consumer)+"#"+k)
	}
	return out
}

func prereqAbout(c candidates.PrereqCandidate) map[string]bool {
	about := map[string]bool{
		ir.Ref(ir.RefFamily, c.Consumer): true,
		ir.Ref(ir.RefFamily, c.Producer): true,
		ir.Ref(ir.RefSequence, c.Edge):   true,
	}
	for _, e := range c.Examples {
		about[e] = true
	}
	for _, a := range c.Alternates {
		about[ir.Ref(ir.RefFamily, a)] = true
	}
	return about
}
