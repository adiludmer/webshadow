package semantic

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Checks entity, identity and relation hypotheses must pass.
const (
	CheckValuesReproduce   = "values_reproduce"
	CheckHeldOutJoin       = "held_out_join"
	CheckIdentitySupported = "identity_supported"
	CheckObservedTwice     = "observed_twice"
	CheckNamedAsObject     = "named_as_object"
	CheckNested            = "nested_in_response"
	CheckTravelTogether    = "values_travel_together"
)

// Candidate ids of hypotheses Go proposes by itself.
const CandidateSharedIdentifier = "shared_identifier"

// entityOutcome is what the entity stage produced, besides its ledgers.
type entityOutcome struct {
	outcome
	entities []ir.Entity
	// dropped lists entities this run found to be no object at all.
	dropped map[string]bool
}

// discoverEntities proposes the entities, their identities and the
// relations among them. Identities and relations are checked by Go alone;
// a model only names each entity, or says it is not an object. Families in
// skip, settled as background or presentation, contribute no list items.
func discoverEntities(ctx context.Context, in *input.Input, d *decide.Decider, skip map[string]bool) (entityOutcome, error) {
	out := entityOutcome{dropped: map[string]bool{}}
	cands, rels := candidates.Entities(in, candidates.KeyClasses(in), skip)
	kept := map[string]*ir.Entity{}
	for i := range cands {
		e := &cands[i]
		identity := ir.StatusMechanicallySupported
		if e.Kind == candidates.EntityKeyed {
			h := identityHypothesis(e)
			identity = h.Status
			out.add(h)
		}

		out.tasks++
		dec, err := d.Decide(ctx, e.Name)
		if err != nil {
			return out, err
		}
		out.decisions = append(out.decisions, dec)
		name, rejected := "", false
		if dec.Status == decide.StatusDecided && dec.ChoiceID != decide.ChoiceUnknown {
			h := ir.Hypothesis{
				Kind: ir.KindEntityName, SubjectRefs: e.Subjects, CandidateID: dec.ChoiceID,
				EvidenceRefs: dec.EvidenceRefs, ModelRunID: dec.Model.ID + "@" + dec.Prompt,
				Confidence: ir.ConfidenceRecord{Supporting: len(dec.EvidenceRefs), Agreement: 1},
			}
			h.Checks = roleChecks(in, e.Name, dec, entityAbout(e))
			h.Status = statusOf(h.Checks, ir.StatusRejected)
			out.add(h)
			if h.Status == ir.StatusMechanicallySupported {
				if dec.ChoiceID == candidates.ChoiceNotAnObject {
					rejected = true
				} else {
					name = dec.ChoiceID
				}
			}
		}

		h := out.add(entityHypothesis(e, identity, dec, rejected))
		if h.Status == ir.StatusRejected {
			out.dropped[e.ID] = true
			continue
		}
		ent := ir.Entity{
			ID: e.ID, Name: name, Identity: e.Identity, Fields: e.Fields,
			EvidenceRefs: e.Evidence, Hypothesis: h.ID,
		}
		for _, n := range e.Names {
			if name == "" {
				ent.Name = n
				continue
			}
			if n != ent.Name {
				ent.Aliases = append(ent.Aliases, n)
			}
		}
		if ent.Name == "" {
			ent.Name = strings.TrimPrefix(e.ID, ir.RefEntity+":")
		}
		kept[e.ID] = &ent
	}

	for _, r := range rels {
		from, to := kept[r.From], kept[r.To]
		if from == nil || to == nil {
			continue
		}
		h := out.add(relationHypothesis(r))
		from.Relations = append(from.Relations, ir.RelationRef{
			ID: r.ID, Target: r.To, Kind: r.Kind, Cardinality: r.Cardinality,
			FieldPaths: r.FieldPaths, Hypothesis: h.ID,
		})
	}
	for _, id := range sortedIDs(kept) {
		out.entities = append(out.entities, *kept[id])
	}
	return out, nil
}

// identityHypothesis asserts that a key class's slots carry one identifier.
// It is mechanically supported when the values repeat and the join holds
// on held-out values: the slot pairs one half of the values joins are
// joined again by the other half.
func identityHypothesis(e *candidates.EntityCandidate) ir.Hypothesis {
	k := e.Key
	ho := k.HeldOut
	h := ir.Hypothesis{
		Kind: ir.KindIdentity, SubjectRefs: e.Subjects, CandidateID: CandidateSharedIdentifier,
		EvidenceRefs: k.Links, ModelRunID: "rule:key_class",
		Confidence: ir.ConfidenceRecord{Supporting: ho.Reproduced, Contradicting: max(ho.PairsA, ho.PairsB) - ho.Reproduced},
		Checks: []ir.CheckResult{
			{
				Check: CheckValuesReproduce, Passed: k.Values >= 2,
				Detail: fmt.Sprintf("%d distinct values across %d request slots", k.Values, len(k.Consumers)),
			},
			{
				Check: CheckHeldOutJoin, Passed: ho.Passed(),
				Detail: fmt.Sprintf("half A: %d values join %d slot pairs; half B: %d values join %d; %d pairs joined by both",
					ho.HalfA, ho.PairsA, ho.HalfB, ho.PairsB, ho.Reproduced),
			},
		},
	}
	h.Status = statusOf(h.Checks, ir.StatusProposed)
	return h
}

// entityHypothesis asserts that a candidate is an object the site
// exposes. A model naming it not an object rejects it; a keyed entity
// whose identity did not hold, or a list seen in one response only, stays
// proposed.
func entityHypothesis(e *candidates.EntityCandidate, identity ir.HypothesisStatus, dec decide.Decision, rejected bool) ir.Hypothesis {
	h := ir.Hypothesis{
		Kind: ir.KindEntity, SubjectRefs: e.Subjects, CandidateID: e.Kind,
		EvidenceRefs: e.Evidence, ModelRunID: "rule:" + e.Kind,
	}
	if e.Kind == candidates.EntityKeyed {
		h.Checks = append(h.Checks, ir.CheckResult{
			Check: CheckIdentitySupported, Passed: identity == ir.StatusMechanicallySupported,
			Detail: "identity hypothesis is " + string(identity),
		})
		h.Confidence.Supporting = e.Key.Values
	} else {
		h.Checks = append(h.Checks, ir.CheckResult{
			Check: CheckObservedTwice, Passed: e.Observations >= 2,
			Detail: fmt.Sprintf("seen in %d responses of %d families", e.Observations, len(e.Families)),
		})
		h.Confidence.Supporting = e.Observations
	}
	named := ir.CheckResult{Check: CheckNamedAsObject, Passed: !rejected}
	switch {
	case rejected:
		named.Detail = "named " + candidates.ChoiceNotAnObject + " by " + dec.Model.ID
		named.EvidenceRefs = dec.EvidenceRefs
		h.Confidence.Contradicting = 1
	case dec.Status != decide.StatusDecided || dec.ChoiceID == decide.ChoiceUnknown:
		named.Detail = "no name decided"
	default:
		named.Detail = "named " + dec.ChoiceID
	}
	h.Checks = append(h.Checks, named)
	if rejected {
		h.Status = ir.StatusRejected
	} else {
		h.Status = statusOf(h.Checks, ir.StatusProposed)
	}
	return h
}

// relationHypothesis asserts a relation Go derived: nesting inside one
// response, or identifiers requests carry together, one target per
// source value every time.
func relationHypothesis(r candidates.RelationCandidate) ir.Hypothesis {
	h := ir.Hypothesis{
		Kind: ir.KindRelation, SubjectRefs: r.FieldPaths, CandidateID: r.Kind,
		EvidenceRefs: r.Evidence, ModelRunID: "rule:" + r.Kind,
		Confidence: ir.ConfidenceRecord{Supporting: r.Supporting, Contradicting: r.Contradicting},
	}
	switch r.Kind {
	case candidates.RelationContains:
		h.Checks = []ir.CheckResult{{
			Check: CheckNested, Passed: true,
			Detail: fmt.Sprintf("the %s list sits inside the %s elements in %d responses", r.To, r.From, r.Supporting),
		}}
	default:
		h.Checks = []ir.CheckResult{{
			Check: CheckTravelTogether, Passed: r.Supporting > 0 && r.Contradicting == 0,
			Detail: fmt.Sprintf("%d value pairs seen together in at least 2 requests, %d source values seen with two targets, across %d families",
				r.Supporting, r.Contradicting, len(r.Families)),
			EvidenceRefs: r.Families,
		}}
	}
	h.Status = statusOf(h.Checks, ir.StatusProposed)
	return h
}

// statusOf is mechanically supported when every check passed, and failed
// otherwise.
func statusOf(checks []ir.CheckResult, failed ir.HypothesisStatus) ir.HypothesisStatus {
	for _, c := range checks {
		if !c.Passed {
			return failed
		}
	}
	return ir.StatusMechanicallySupported
}

// entityAbout lists the evidence that concerns an entity candidate: its
// slots and arrays, the families involved, and its value links.
func entityAbout(e *candidates.EntityCandidate) map[string]bool {
	about := map[string]bool{}
	for _, s := range e.Subjects {
		about[s] = true
	}
	for _, s := range e.Identity {
		about[s.Ref] = true
	}
	for _, f := range e.Families {
		about[ir.Ref(ir.RefFamily, f)] = true
	}
	for _, r := range e.Evidence {
		about[r] = true
	}
	for _, f := range e.Fields {
		for _, s := range f.Sources {
			about[s.Ref] = true
		}
	}
	return about
}

// patchEntities merges a run's entities into the IR. An entity found again
// replaces its earlier version unless a person reviewed it; one this run
// found to be no object is removed, again unless reviewed. Relations to
// entities no longer listed are dropped.
func patchEntities(x *ir.InterfaceIR, found []ir.Entity, dropped map[string]bool) {
	status := map[string]ir.HypothesisStatus{}
	for _, h := range x.Hypotheses {
		status[h.ID] = h.Status
	}
	byID := map[string]ir.Entity{}
	for _, e := range x.Entities {
		byID[e.ID] = e
	}
	for _, e := range found {
		if old, ok := byID[e.ID]; ok && status[old.Hypothesis] == ir.StatusReviewed {
			continue
		}
		byID[e.ID] = e
	}
	for id := range dropped {
		if old, ok := byID[id]; ok && status[old.Hypothesis] != ir.StatusReviewed {
			delete(byID, id)
		}
	}
	x.Entities = make([]ir.Entity, 0, len(byID))
	for _, id := range sortedIDs(byID) {
		e := byID[id]
		rels := e.Relations[:0:0]
		for _, r := range e.Relations {
			if _, ok := byID[r.Target]; ok {
				rels = append(rels, r)
			}
		}
		e.Relations = rels
		x.Entities = append(x.Entities, e)
	}
}

func sortedIDs[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
