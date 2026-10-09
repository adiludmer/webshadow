package semantic

import (
	"context"
	"fmt"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Checks an operation hypothesis must pass.
const (
	CheckRolesSupported  = "roles_supported"
	CheckSlotsExist      = "slots_exist"
	CheckTypesCompatible = "types_compatible"
	CheckMerges          = "merges_recorded"
)

// opOutcome is what operation synthesis produced, besides its ledgers.
type opOutcome struct {
	outcome
	operations []ir.Operation
	// merges records every merge and every pair kept apart.
	merges []MergeLine
}

// MergeLine is one line of the merges ledger.
type MergeLine struct {
	Operation string `json:"operation"`
	A         string `json:"a"`
	B         string `json:"b"`
	Merged    bool   `json:"merged"`
	Rule      string `json:"rule,omitempty"`
	Detail    string `json:"detail"`
}

// synthesizeOperations assembles operations from the families whose role
// stands as a read or a mutation, with the entities and prerequisites the
// earlier stages kept. Go checks every operation; a model only names it.
func synthesizeOperations(ctx context.Context, in *input.Input, d *decide.Decider, roles []ir.Hypothesis, ents entityOutcome, prereqs prereqOutcome) (opOutcome, error) {
	var out opOutcome
	standing := map[string]string{}
	for _, h := range roles {
		if h.Status == ir.StatusMechanicallySupported {
			for _, s := range h.SubjectRefs {
				_, id, _ := ir.SplitRef(s)
				standing[id] = h.CandidateID
			}
		}
	}
	slots := map[string]model.Slot{}
	for _, f := range in.Families {
		for _, s := range f.Slots {
			slots[ir.Ref(ir.RefFamily, f.ID)+"#request."+s.Location] = s
		}
	}
	for _, c := range candidates.Operations(in, standing, ents.forOperations, prereqs.fed) {
		subjects := make([]string, len(c.Families))
		for i, f := range c.Families {
			subjects[i] = ir.Ref(ir.RefFamily, f)
		}
		for _, m := range c.Merges {
			out.merges = append(out.merges, MergeLine{Operation: c.ID, A: m.A, B: m.B, Merged: true, Rule: m.Rule, Detail: m.Detail})
		}
		for _, m := range c.Rejected {
			out.merges = append(out.merges, MergeLine{Operation: c.ID, A: m.A, B: m.B, Detail: m.Detail})
		}

		h := ir.Hypothesis{
			Kind: ir.KindOperation, SubjectRefs: subjects, CandidateID: c.Role,
			EvidenceRefs: c.Evidence, ModelRunID: "rule:operation",
			Checks:     operationChecks(c, slots),
			Confidence: ir.ConfidenceRecord{Supporting: len(c.Families)},
		}
		h.Status = statusOf(h.Checks, ir.StatusRejected)
		h = out.add(h)
		if h.Status == ir.StatusRejected {
			continue
		}

		name, dec := "", decide.Decision{}
		if len(c.Names) > 0 {
			// With no derivable name there is nothing to choose among.
			name = c.Names[0]
			out.tasks++
			var err error
			if dec, err = d.Decide(ctx, c.Name); err != nil {
				return out, err
			}
			out.decisions = append(out.decisions, dec)
		}
		if dec.Status == decide.StatusDecided && dec.ChoiceID != decide.ChoiceUnknown {
			nh := ir.Hypothesis{
				Kind: ir.KindOperationName, SubjectRefs: subjects, CandidateID: dec.ChoiceID,
				EvidenceRefs: dec.EvidenceRefs, ModelRunID: dec.Model.ID + "@" + dec.Prompt,
				Confidence: ir.ConfidenceRecord{Supporting: len(dec.EvidenceRefs), Agreement: 1},
			}
			about := map[string]bool{}
			for _, e := range c.Name.Evidence {
				about[e.Ref] = true
			}
			nh.Checks = roleChecks(in, c.Name, dec, about)
			nh.Status = statusOf(nh.Checks, ir.StatusRejected)
			nh = out.add(nh)
			if nh.Status == ir.StatusMechanicallySupported {
				name = dec.ChoiceID
			}
		}
		if name == "" {
			name = strings.TrimPrefix(c.ID, ir.RefOperation+":")
		}

		op := ir.Operation{
			ID: c.ID, Name: name, Inputs: c.Inputs, Outputs: c.Outputs,
			SourceFamilies: subjects, EvidenceRefs: c.Evidence, Status: h.Status, Hypothesis: h.ID,
		}
		member := map[string]bool{}
		for _, s := range subjects {
			member[s] = true
		}
		for _, p := range prereqs.preconditions {
			if member[p.ConsumerFamily] && p.Status != ir.StatusRejected {
				op.Preconditions = append(op.Preconditions, p)
			}
		}
		out.operations = append(out.operations, op)
	}
	return out, nil
}

// operationChecks verifies an operation against the clustering output:
// its families' roles stand, every input slot exists, the slots an input
// gathers have compatible types, and every merge has a recorded rule.
func operationChecks(c candidates.OperationCandidate, slots map[string]model.Slot) []ir.CheckResult {
	checks := []ir.CheckResult{{
		Check: CheckRolesSupported, Passed: true,
		Detail: fmt.Sprintf("%d families whose %s role is mechanically supported", len(c.Families), c.Role),
	}}
	var missing, mixed []string
	for _, in := range c.Inputs {
		kinds := map[string]bool{}
		for _, s := range in.Slots {
			slot, ok := slots[s.Ref]
			if !ok {
				missing = append(missing, s.Ref)
				continue
			}
			kinds[typeKind(slot.Type)] = true
		}
		if len(kinds) > 1 {
			mixed = append(mixed, in.Name)
		}
	}
	exist := ir.CheckResult{Check: CheckSlotsExist, Passed: len(missing) == 0, EvidenceRefs: missing}
	if len(missing) > 0 {
		exist.Detail = "input slots missing from the families"
	}
	types := ir.CheckResult{Check: CheckTypesCompatible, Passed: len(mixed) == 0}
	if len(mixed) > 0 {
		types.Detail = "inputs gather slots of different types: " + strings.Join(mixed, ", ")
	}
	merges := ir.CheckResult{Check: CheckMerges, Passed: len(c.Merges) >= len(c.Families)-1}
	var rules []string
	for _, m := range c.Merges {
		rules = append(rules, m.Rule)
	}
	merges.Detail = fmt.Sprintf("%d families, merges: %s; %d pairs kept apart", len(c.Families), strings.Join(rules, ", "), len(c.Rejected))
	return append(checks, exist, types, merges)
}

// typeKind folds slot types into what an agent would pass: path character
// classes and strings are text, integers and numbers are numbers.
func typeKind(t string) string {
	switch t {
	case model.TypeInteger, model.TypeNumber, "int":
		return "number"
	case model.TypeBool:
		return "bool"
	}
	return "text"
}

// patchOperations merges a run's operations into the IR. Operations built
// only from this evidence's families are replaced by what this run found,
// unless a person reviewed them; operations from other evidence stay.
func patchOperations(x *ir.InterfaceIR, in *input.Input, found []ir.Operation) {
	status := map[string]ir.HypothesisStatus{}
	for _, h := range x.Hypotheses {
		status[h.ID] = h.Status
	}
	ours := map[string]bool{}
	for _, f := range in.Families {
		ours[ir.Ref(ir.RefFamily, f.ID)] = true
	}
	byID := map[string]ir.Operation{}
	for _, op := range x.Operations {
		local := true
		for _, f := range op.SourceFamilies {
			local = local && ours[f]
		}
		if !local || status[op.Hypothesis] == ir.StatusReviewed {
			byID[op.ID] = op
		}
	}
	for _, op := range found {
		if old, ok := byID[op.ID]; ok && status[old.Hypothesis] == ir.StatusReviewed {
			continue
		}
		byID[op.ID] = op
	}
	x.Operations = make([]ir.Operation, 0, len(byID))
	for _, id := range sortedIDs(byID) {
		x.Operations = append(x.Operations, byID[id])
	}
}
