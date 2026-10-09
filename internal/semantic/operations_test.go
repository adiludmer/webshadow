package semantic

import (
	"context"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/report"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// The three URL forms of the Amazon search page.
var searchForms = []string{"fam:62873a6451a2", "fam:98258e0892fb", "fam:a063cd25be92"}

func operationWith(x *ir.InterfaceIR, family string) *ir.Operation {
	for i := range x.Operations {
		if contains(x.Operations[i].SourceFamilies, family) {
			return &x.Operations[i]
		}
	}
	return nil
}

func TestOperationsOnAmazon(t *testing.T) {
	s := testStore(t)
	res, err := Analyze(context.Background(), fixture, mockOptions(s))
	if err != nil {
		t.Fatal(err)
	}
	x := res.IR
	search := operationWith(x, "fam:"+searchFamily)
	if search == nil {
		t.Fatal("no search operation")
	}
	if strings.Join(search.SourceFamilies, ",") != strings.Join(searchForms, ",") {
		t.Errorf("search families = %v", search.SourceFamilies)
	}
	if h := hypothesis(x, search.Hypothesis); h.CandidateID != "collection_read" || h.Status != ir.StatusMechanicallySupported {
		t.Errorf("search hypothesis = %+v", h)
	}
	// One query input behind three URL forms; the response id the
	// suggestions always supply is state, not input.
	var query *ir.InputField
	for i := range search.Inputs {
		if search.Inputs[i].Name == "k" {
			query = &search.Inputs[i]
		}
		for _, sl := range search.Inputs[i].Slots {
			if sl.Ref == "fam:98258e0892fb#request.query.crid" {
				t.Errorf("input %s holds the crid the suggestions supply", search.Inputs[i].Name)
			}
		}
	}
	if query == nil || len(query.Slots) != 3 || !query.Required {
		t.Fatalf("search query input = %+v", query)
	}
	// Search results name products: the search-result-to-product link.
	product := entityByName(x, "asin")
	found := false
	for _, o := range search.Outputs {
		if o.Entity == product.ID && o.Many {
			found = true
		}
	}
	if !found {
		t.Errorf("search outputs = %+v", search.Outputs)
	}
	session := 0
	for _, p := range search.Preconditions {
		if p.Kind == "session_state" && p.Status == ir.StatusProposed {
			session++
		}
	}
	if session == 0 {
		t.Error("search has no session-state precondition")
	}

	item := operationWith(x, "fam:dea048077b86")
	if item == nil || hypothesis(x, item.Hypothesis).CandidateID != "item_read" {
		t.Fatalf("product page operation = %+v", item)
	}
	takesID := false
	for _, in := range item.Inputs {
		for _, sl := range in.Slots {
			takesID = takesID || (sl.Ref == "fam:dea048077b86#request.path.2" && in.Name == product.Name)
		}
	}
	if !takesID {
		t.Errorf("product page inputs = %+v", item.Inputs)
	}

	merges, err := store.ReadLedger[MergeLine](s, res.Run, "merges.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var samePage, apart int
	for _, m := range merges {
		if m.Merged && m.Rule == "same_page" && m.Operation == search.ID {
			samePage++
		}
		if !m.Merged {
			apart++
		}
	}
	if samePage != 3 || apart == 0 {
		t.Errorf("%d same-page merges into search, %d pairs kept apart", samePage, apart)
	}

	c := res.Manifest.Coverage
	if c.Relevant == 0 || c.Covered == 0 || c.Score < 0.9 || c.Covered+len(c.Uncovered) != c.Relevant {
		t.Errorf("coverage = %+v", c)
	}
}

// countingModel answers like the mock and counts its calls.
type countingModel struct {
	decide.Mock
	calls int
}

func (m *countingModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.calls++
	return m.Mock.Complete(ctx, req)
}

func TestUnchangedEvidenceAsksNothing(t *testing.T) {
	s := testStore(t)
	first, err := Analyze(context.Background(), fixture, mockOptions(s))
	if err != nil {
		t.Fatal(err)
	}
	m := &countingModel{}
	second, err := Analyze(context.Background(), fixture, Options{Store: s, Decider: decide.New(m, decide.ModelInfo{}, decide.DefaultParams()), Reuse: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 0 || second.Changed || second.Manifest.Counts.Reused != first.Manifest.Counts.Tasks || second.Manifest.ReusedFrom != first.Run {
		t.Errorf("%d calls, changed %v, reused %d of %d from %q", m.calls, second.Changed, second.Manifest.Counts.Reused, first.Manifest.Counts.Tasks, second.Manifest.ReusedFrom)
	}
	// A changed answer is asked again: a different model id reuses nothing.
	other := decide.New(&countingModel{}, decide.ModelInfo{ID: "other"}, decide.DefaultParams())
	third, err := Analyze(context.Background(), fixture, Options{Store: s, Decider: other, Reuse: true})
	if err != nil {
		t.Fatal(err)
	}
	if third.Manifest.Counts.Reused != 0 {
		t.Errorf("another model reused %d decisions", third.Manifest.Counts.Reused)
	}
}

func TestPatchRecordsContradictions(t *testing.T) {
	s := testStore(t)
	if _, err := Analyze(context.Background(), fixture, mockOptions(s)); err != nil {
		t.Fatal(err)
	}
	m := &decide.Mock{Script: map[string][]string{productTask: {`{"choice_id":"not_an_object","evidence_refs":["E1"]}`}}}
	res, err := Analyze(context.Background(), fixture, Options{Store: s, Decider: decide.New(m, decide.ModelInfo{}, decide.DefaultParams())})
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.ReadJSON[store.Patch](s, res.Run, "patch.json")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prior != "r0001" || p.Revision != "" && p.Revision != res.Revision {
		t.Errorf("patch %s -> %s", p.Prior, p.Revision)
	}
	if !contains(p.Removed, "ent:5d8dfab6c68e") || len(p.Contradictions) == 0 {
		t.Errorf("removed %v, contradictions %+v", p.Removed, p.Contradictions)
	}
	for _, c := range p.Contradictions {
		if c.To != ir.StatusRejected {
			t.Errorf("contradiction %+v", c)
		}
	}

	r, err := report.Build(s, res.Run)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Patch.Contradictions) != len(p.Contradictions) || len(r.Operations) != len(res.IR.Operations) || len(r.Rejected) == 0 {
		t.Errorf("report: %d contradictions, %d operations, %d rejected", len(r.Patch.Contradictions), len(r.Operations), len(r.Rejected))
	}
	var md strings.Builder
	report.Markdown(&md, r)
	for _, want := range []string{"## Operations", "Coverage:", "## Entities", "## Uncertain", "## Rejected", "contradictions", "## Cost"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("report lacks %q", want)
		}
	}
	path, err := report.Write(s, r)
	if err != nil || !strings.HasSuffix(path, res.Run+".json") {
		t.Errorf("report written to %s: %v", path, err)
	}
}
