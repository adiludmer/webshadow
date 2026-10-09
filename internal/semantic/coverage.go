package semantic

import (
	"sort"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// coverage scores the IR's operations against the browser episodes of
// the clustering result. An episode is relevant when it holds a family
// whose standing role is neither background nor presentation, and covered
// when it holds a family an operation is built from.
func coverage(in *input.Input, x *ir.InterfaceIR) store.Coverage {
	role := map[string]string{}
	for _, h := range x.Hypotheses {
		if h.Kind == ir.KindFamilyRole && (h.Status == ir.StatusMechanicallySupported || h.Status == ir.StatusReviewed) {
			for _, s := range h.SubjectRefs {
				role[s] = h.CandidateID
			}
		}
	}
	status := map[string]ir.HypothesisStatus{}
	for _, h := range x.Hypotheses {
		status[h.ID] = h.Status
	}
	operated := map[string]bool{}
	for _, op := range x.Operations {
		if status[op.Hypothesis] == ir.StatusRejected || status[op.Hypothesis] == ir.StatusSuperseded {
			continue
		}
		for _, f := range op.SourceFamilies {
			operated[f] = true
		}
	}
	c := store.Coverage{Uncovered: []string{}}
	for _, ep := range in.Episodes {
		c.Episodes++
		relevant, covered := false, false
		for _, f := range ep.FamilyIDs {
			ref := ir.Ref(ir.RefFamily, f)
			if r := role[ref]; r != "" && r != candidates.RoleBackground && r != candidates.RolePresentation {
				relevant = true
			}
			covered = covered || operated[ref]
		}
		if !relevant {
			continue
		}
		c.Relevant++
		if covered {
			c.Covered++
		} else {
			c.Uncovered = append(c.Uncovered, ir.Ref(ir.RefEpisode, ep.ID))
		}
	}
	sort.Strings(c.Uncovered)
	if c.Relevant > 0 {
		// Rounded so the manifest encodes the same on every platform.
		c.Score = float64(c.Covered*1000/c.Relevant) / 1000
	}
	return c
}

// planPatch records how next differs from prior: nodes added, changed and
// removed, and hypotheses whose status moved. A move from standing to
// rejected is a contradiction.
func planPatch(prior, next *ir.InterfaceIR) store.Patch {
	p := store.Patch{Added: []string{}, Changed: []string{}, Removed: []string{}, StatusChanges: []store.StatusChange{}, Contradictions: []store.StatusChange{}}
	nodes := func(x *ir.InterfaceIR) map[string]string {
		out := map[string]string{}
		if x == nil {
			return out
		}
		for _, e := range x.Entities {
			data, _ := ir.EncodeJSON(e)
			out[e.ID] = string(data)
		}
		for _, op := range x.Operations {
			data, _ := ir.EncodeJSON(op)
			out[op.ID] = string(data)
		}
		for _, h := range x.Hypotheses {
			data, _ := ir.EncodeJSON(h)
			out[h.ID] = string(data)
		}
		return out
	}
	before, after := nodes(prior), nodes(next)
	for _, id := range sortedIDs(after) {
		old, ok := before[id]
		switch {
		case !ok:
			p.Added = append(p.Added, id)
		case old != after[id]:
			p.Changed = append(p.Changed, id)
		}
	}
	for _, id := range sortedIDs(before) {
		if _, ok := after[id]; !ok {
			p.Removed = append(p.Removed, id)
		}
	}
	if prior != nil {
		p.Prior = prior.Revision
		was := map[string]ir.HypothesisStatus{}
		for _, h := range prior.Hypotheses {
			was[h.ID] = h.Status
		}
		for _, h := range next.Hypotheses {
			from, ok := was[h.ID]
			if !ok || from == h.Status {
				continue
			}
			sc := store.StatusChange{Hypothesis: h.ID, From: from, To: h.Status}
			p.StatusChanges = append(p.StatusChanges, sc)
			if h.Status == ir.StatusRejected && (from == ir.StatusMechanicallySupported || from == ir.StatusReviewed || from == ir.StatusProposed) {
				p.Contradictions = append(p.Contradictions, sc)
			}
		}
	}
	return p
}
