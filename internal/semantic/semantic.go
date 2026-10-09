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
	"fmt"

	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// Options configure a run.
type Options struct {
	Store *store.Store
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
func Analyze(dir string, opts Options) (*Result, error) {
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

// carryForward starts the next revision from the prior one, adding the
// clustering result being analysed to its evidence. Later stages patch it;
// nothing is regenerated from scratch.
func carryForward(prior *ir.InterfaceIR, evidence string) *ir.InterfaceIR {
	next := *prior
	next.Evidence = append(append([]string{}, prior.Evidence...), evidence)
	return &next
}
