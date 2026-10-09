// Package report summarizes an analysis run for people: what the run
// concluded and how sure the checks are, what it could not decide, what it
// changed in the IR and what it cost. It reads only the run's files and the
// revision it ended at, so a report can be rebuilt at any time.
package report

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// Report is one run's summary, as written to reports/<run-id>.json.
type Report struct {
	Run      store.RunManifest `json:"run"`
	Revision string            `json:"revision"`
	// Hypotheses counts the revision's hypotheses by kind, then status.
	Hypotheses map[ir.HypothesisKind]map[ir.HypothesisStatus]int `json:"hypotheses"`
	Operations []Operation                                       `json:"operations"`
	Entities   []Entity                                          `json:"entities"`
	// Uncertain lists the tasks the run could not decide: unresolved ones
	// and ones the model answered unknown.
	Uncertain []Uncertain `json:"uncertain"`
	// Rejected lists the hypotheses the run rejected, with the checks they
	// failed.
	Rejected []Rejected     `json:"rejected"`
	Patch    store.Patch    `json:"patch"`
	Coverage store.Coverage `json:"coverage"`
	Cost     Cost           `json:"cost"`
}

// Operation summarizes one operation.
type Operation struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Role          string              `json:"role"`
	Status        ir.HypothesisStatus `json:"status"`
	Families      int                 `json:"families"`
	Inputs        []string            `json:"inputs"`
	Outputs       []string            `json:"outputs"`
	Preconditions map[string]int      `json:"preconditions"`
}

// Entity summarizes one entity.
type Entity struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Status    ir.HypothesisStatus `json:"status"`
	Identity  int                 `json:"identity_locations"`
	Fields    int                 `json:"fields"`
	Relations []string            `json:"relations"`
}

// Uncertain is one task left undecided.
type Uncertain struct {
	Task    string `json:"task"`
	Type    string `json:"type"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// Rejected is one rejected hypothesis.
type Rejected struct {
	Hypothesis string   `json:"hypothesis"`
	Kind       string   `json:"kind"`
	Candidate  string   `json:"candidate"`
	Failed     []string `json:"failed_checks"`
}

// Cost is what the run spent on model calls. Reused decisions cost
// nothing and are counted apart.
type Cost struct {
	Tasks          int   `json:"tasks"`
	Reused         int   `json:"reused"`
	Calls          int   `json:"calls"`
	Repairs        int   `json:"repairs"`
	LatencyMS      int64 `json:"latency_ms"`
	InputTokens    int   `json:"input_tokens"`
	OutputTokens   int   `json:"output_tokens"`
	BudgetFailures int   `json:"budget_failures"`
}

// Latest returns the id of the newest complete run.
func Latest(s *store.Store) (string, error) {
	runs, err := s.Runs()
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("no runs in %s; run webshadow analyze first", s.Root)
	}
	return runs[len(runs)-1], nil
}

// Build assembles the report for a run.
func Build(s *store.Store, runID string) (*Report, error) {
	m, err := s.LoadRun(runID)
	if err != nil {
		return nil, err
	}
	x, err := s.Load(m.Revision)
	if err != nil {
		return nil, err
	}
	decisions, err := store.ReadLedger[decide.Decision](s, runID, "decisions.jsonl")
	if err != nil {
		return nil, err
	}
	hyps, err := store.ReadLedger[ir.Hypothesis](s, runID, "hypotheses.jsonl")
	if err != nil {
		return nil, err
	}
	patch, err := store.ReadJSON[store.Patch](s, runID, "patch.json")
	if err != nil {
		return nil, err
	}
	r := &Report{
		Run: *m, Revision: m.Revision, Patch: *patch, Coverage: m.Coverage,
		Hypotheses: map[ir.HypothesisKind]map[ir.HypothesisStatus]int{},
		Operations: []Operation{}, Entities: []Entity{}, Uncertain: []Uncertain{}, Rejected: []Rejected{},
	}

	refs := map[string]ir.HypothesisRef{}
	for _, h := range x.Hypotheses {
		refs[h.ID] = h
		if r.Hypotheses[h.Kind] == nil {
			r.Hypotheses[h.Kind] = map[ir.HypothesisStatus]int{}
		}
		r.Hypotheses[h.Kind][h.Status]++
	}
	for _, op := range x.Operations {
		o := Operation{
			ID: op.ID, Name: op.Name, Role: refs[op.Hypothesis].CandidateID, Status: refs[op.Hypothesis].Status,
			Families: len(op.SourceFamilies), Inputs: []string{}, Outputs: []string{}, Preconditions: map[string]int{},
		}
		for _, in := range op.Inputs {
			name := in.Name
			if in.Required {
				name += " (required)"
			}
			o.Inputs = append(o.Inputs, name)
		}
		for _, out := range op.Outputs {
			name := out.Name
			if out.Many {
				name = "list of " + name
			}
			o.Outputs = append(o.Outputs, name)
		}
		for _, p := range op.Preconditions {
			o.Preconditions[p.Kind]++
		}
		r.Operations = append(r.Operations, o)
	}
	names := map[string]string{}
	for _, e := range x.Entities {
		names[e.ID] = e.Name
	}
	for _, e := range x.Entities {
		ent := Entity{
			ID: e.ID, Name: e.Name, Status: refs[e.Hypothesis].Status,
			Identity: len(e.Identity), Fields: len(e.Fields), Relations: []string{},
		}
		for _, rel := range e.Relations {
			ent.Relations = append(ent.Relations, fmt.Sprintf("%s %s (%s)", rel.Kind, names[rel.Target], rel.Cardinality))
		}
		r.Entities = append(r.Entities, ent)
	}

	for _, d := range decisions {
		if d.Status == "settled" {
			continue
		}
		r.Cost.Tasks++
		if d.Reused != "" {
			r.Cost.Reused++
		}
		for i, a := range d.Attempts {
			r.Cost.Calls++
			if i > 0 {
				r.Cost.Repairs++
			}
			r.Cost.LatencyMS += a.LatencyMS
			r.Cost.InputTokens += a.InputTokens
			r.Cost.OutputTokens += a.OutputTokens
		}
		switch {
		case d.Status == decide.StatusUnresolved:
			if strings.HasPrefix(d.Unresolved, "prompt budget") {
				r.Cost.BudgetFailures++
			}
			r.Uncertain = append(r.Uncertain, Uncertain{Task: d.TaskID, Type: d.TaskType, Subject: d.Subject, Reason: d.Unresolved})
		case d.ChoiceID == decide.ChoiceUnknown:
			r.Uncertain = append(r.Uncertain, Uncertain{Task: d.TaskID, Type: d.TaskType, Subject: d.Subject, Reason: "the model answered unknown"})
		}
	}
	for _, h := range hyps {
		if h.Status != ir.StatusRejected {
			continue
		}
		rj := Rejected{Hypothesis: h.ID, Kind: string(h.Kind), Candidate: h.CandidateID, Failed: []string{}}
		for _, c := range h.Checks {
			if !c.Passed {
				rj.Failed = append(rj.Failed, c.Check)
			}
		}
		r.Rejected = append(r.Rejected, rj)
	}
	sort.Slice(r.Rejected, func(i, j int) bool { return r.Rejected[i].Hypothesis < r.Rejected[j].Hypothesis })
	return r, nil
}

// Write stores the report as reports/<run-id>.json.
func Write(s *store.Store, r *Report) (string, error) {
	path := filepath.Join(s.Root, "reports", r.Run.Run+".json")
	data, err := ir.EncodeJSON(r)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, input.WriteFileAtomic(path, data)
}

// Markdown renders the report for reading.
func Markdown(w io.Writer, r *Report) {
	m := r.Run
	fmt.Fprintf(w, "# Analysis run %s\n\n", m.Run)
	fmt.Fprintf(w, "Evidence %s, model %s. ", m.Evidence, m.Model)
	switch {
	case !m.Changed:
		fmt.Fprintf(w, "The IR was unchanged at %s.\n\n", m.Revision)
	case m.Prior != "":
		fmt.Fprintf(w, "Revision %s from %s.\n\n", m.Revision, m.Prior)
	default:
		fmt.Fprintf(w, "First revision %s.\n\n", m.Revision)
	}

	fmt.Fprintf(w, "## Operations (%d)\n\n", len(r.Operations))
	if len(r.Operations) > 0 {
		fmt.Fprintln(w, "| Operation | Role | Status | Families | Inputs | Outputs | Preconditions |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
		for _, o := range r.Operations {
			fmt.Fprintf(w, "| %s | %s | %s | %d | %s | %s | %s |\n", o.Name, o.Role, o.Status, o.Families,
				orNone(o.Inputs), orNone(o.Outputs), counts(o.Preconditions))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Coverage: %d of %d browsing episodes with user-facing traffic map to an operation (score %.3f).", r.Coverage.Covered, r.Coverage.Relevant, r.Coverage.Score)
	if len(r.Coverage.Uncovered) > 0 {
		fmt.Fprintf(w, " Uncovered: %s.", strings.Join(r.Coverage.Uncovered, ", "))
	}
	fmt.Fprint(w, "\n\n")

	fmt.Fprintf(w, "## Entities (%d)\n\n", len(r.Entities))
	if len(r.Entities) > 0 {
		fmt.Fprintln(w, "| Entity | Status | Identity locations | Fields | Relations |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, e := range r.Entities {
			fmt.Fprintf(w, "| %s | %s | %d | %d | %s |\n", e.Name, e.Status, e.Identity, e.Fields, orNone(e.Relations))
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "## Hypotheses")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Kind | Proposed | Mechanically supported | Reviewed | Rejected | Superseded |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|")
	for _, k := range ir.Kinds {
		c := r.Hypotheses[k]
		if len(c) == 0 {
			continue
		}
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d |\n", k, c[ir.StatusProposed], c[ir.StatusMechanicallySupported],
			c[ir.StatusReviewed], c[ir.StatusRejected], c[ir.StatusSuperseded])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Mechanically supported means every deterministic check passed, not that a meaning is true.")
	fmt.Fprintln(w)

	fmt.Fprintf(w, "## Uncertain (%d)\n\n", len(r.Uncertain))
	for _, u := range r.Uncertain {
		fmt.Fprintf(w, "- %s %s: %s\n", u.Type, u.Subject, u.Reason)
	}
	if len(r.Uncertain) > 0 {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "## Rejected (%d)\n\n", len(r.Rejected))
	for _, rj := range r.Rejected {
		fmt.Fprintf(w, "- %s %s (%s): failed %s\n", rj.Kind, rj.Hypothesis, rj.Candidate, orNone(rj.Failed))
	}
	if len(r.Rejected) > 0 {
		fmt.Fprintln(w)
	}

	p := r.Patch
	fmt.Fprintln(w, "## Changes")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%d nodes added, %d changed, %d removed; %d hypotheses changed status, %d of them contradictions.\n",
		len(p.Added), len(p.Changed), len(p.Removed), len(p.StatusChanges), len(p.Contradictions))
	for _, c := range p.Contradictions {
		fmt.Fprintf(w, "- %s: %s to %s\n", c.Hypothesis, c.From, c.To)
	}
	fmt.Fprintln(w)

	c := r.Cost
	fmt.Fprintln(w, "## Cost")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%d model tasks (%d reused from %s), %d calls, %d repairs, %d prompt-budget failures; %.1f s, %d input and %d output tokens.\n",
		c.Tasks, c.Reused, orDash(m.ReusedFrom), c.Calls, c.Repairs, c.BudgetFailures, float64(c.LatencyMS)/1000, c.InputTokens, c.OutputTokens)
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "no earlier run"
	}
	return s
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", m[k], k)
	}
	return orNone(parts)
}
