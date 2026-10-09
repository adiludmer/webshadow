// Package semantic runs semantic discovery: it reads a clustering result,
// proposes and verifies hypotheses about what the site's request families
// mean, and commits the outcome as a new revision of the Agent Interface
// IR.
//
// Go owns every step that changes state. A model only ever picks among
// choices Go built, and nothing it says enters the IR without passing the
// deterministic checks. No step makes a network request.
package semantic

import (
	"context"
	"fmt"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// Options configure a run.
type Options struct {
	Store *store.Store
	// Decider answers the tasks Go cannot settle by rule.
	Decider *decide.Decider
}

// Result is what one run produced.
type Result struct {
	Run      string
	Prior    string
	Revision string
	Changed  bool
	IR       *ir.InterfaceIR
	Input    *input.Input
	Manifest store.RunManifest
}

// Analyze runs semantic discovery over the clustering result in dir and
// commits the resulting IR to the store.
func Analyze(ctx context.Context, dir string, opts Options) (*Result, error) {
	in, err := input.Load(dir)
	if err != nil {
		return nil, err
	}
	run, err := opts.Store.NewRun()
	if err != nil {
		return nil, err
	}
	m := store.RunManifest{
		SchemaVersion: ir.SchemaVersion,
		Run:           run.ID,
		Started:       opts.Store.Now().UTC(),
		Evidence:      in.Manifest.ID,
		EvidenceDir:   in.Dir,
		EvidenceFiles: in.Files,
		Redaction:     in.Redaction,
		Model:         opts.Decider.Info.ID,
	}
	prior, err := opts.Store.Load(store.Latest)
	if err != nil {
		return nil, err
	}
	next := ir.New(in.Manifest.ID)
	if prior != nil {
		m.Prior = prior.Revision
		next = carryForward(prior, in.Manifest.ID)
	}

	roles, err := classifyFamilies(ctx, in, opts.Decider)
	if err != nil {
		return nil, err
	}
	ents, err := discoverEntities(ctx, in, opts.Decider, settledAside(roles.hypotheses))
	if err != nil {
		return nil, err
	}
	stages := []outcome{roles, ents.outcome}
	var hyps []ir.Hypothesis
	for _, st := range stages {
		hyps = append(hyps, patchHypotheses(next, st.hypotheses)...)
	}
	patchEntities(next, ents.entities, ents.dropped)
	if err := writeLedgers(run, stages, hyps); err != nil {
		return nil, err
	}
	for _, st := range stages {
		m.Counts.Tasks += st.tasks
		for _, d := range st.decisions {
			switch {
			case d.Status == StatusSettled:
				m.Counts.Settled++
			case d.Status == decide.StatusUnresolved:
				m.Counts.Unresolved++
			case d.ChoiceID == decide.ChoiceUnknown:
				m.Counts.Unknown++
			default:
				m.Counts.Decided++
			}
		}
	}

	if err := next.Validate(in); err != nil {
		return nil, fmt.Errorf("IR failed validation, nothing committed:\n%w", err)
	}
	rev, changed, err := opts.Store.Commit(next, run.ID)
	if err != nil {
		return nil, err
	}
	m.Revision, m.Changed = rev, changed
	for _, f := range in.Families {
		m.Counts.Families++
		if f.Static {
			m.Counts.StaticFamilies++
		}
	}
	m.Counts.Hypotheses = len(next.Hypotheses)
	m.Counts.Entities = len(next.Entities)
	m.Counts.Operations = len(next.Operations)
	m.Finished = opts.Store.Now().UTC()
	if err := run.WriteManifest(m); err != nil {
		return nil, err
	}
	return &Result{
		Run: run.ID, Prior: m.Prior, Revision: rev, Changed: changed,
		IR: next, Input: in, Manifest: m,
	}, nil
}

// settledAside lists the families whose role hypothesis stands as
// background or presentation: their lists are not entities.
func settledAside(roles []ir.Hypothesis) map[string]bool {
	out := map[string]bool{}
	for _, h := range roles {
		if h.Status != ir.StatusMechanicallySupported {
			continue
		}
		if h.CandidateID == candidates.RoleBackground || h.CandidateID == candidates.RolePresentation {
			for _, s := range h.SubjectRefs {
				_, id, _ := ir.SplitRef(s)
				out[id] = true
			}
		}
	}
	return out
}

// carryForward starts the next revision from the prior one, adding the
// clustering result being analysed to its evidence. Later stages patch it;
// nothing is regenerated from scratch.
func carryForward(prior *ir.InterfaceIR, evidence string) *ir.InterfaceIR {
	next := *prior
	next.Evidence = append(append([]string{}, prior.Evidence...), evidence)
	next.Hypotheses = append([]ir.HypothesisRef{}, prior.Hypotheses...)
	next.Entities = append([]ir.Entity{}, prior.Entities...)
	return &next
}

// writeLedgers appends the run's decisions, the hypotheses it proposed and
// their checks to the run directory. Hypotheses and verifications carry no
// timing or run ids, so the same evidence and model give the same lines.
func writeLedgers(run *store.Run, stages []outcome, hyps []ir.Hypothesis) error {
	for _, st := range stages {
		for _, d := range st.decisions {
			if err := run.Append("decisions.jsonl", d); err != nil {
				return err
			}
		}
	}
	for _, h := range hyps {
		if err := run.Append("hypotheses.jsonl", h); err != nil {
			return err
		}
	}
	for _, st := range stages {
		for _, v := range st.verifications {
			if err := run.Append("verification.jsonl", v); err != nil {
				return err
			}
		}
	}
	return nil
}
