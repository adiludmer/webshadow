package semantic

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const fixture = "testdata/amazon-search"

func mockOptions(s *store.Store) Options {
	return Options{Store: s, Decider: decide.New(&decide.Mock{}, decide.ModelInfo{}, decide.DefaultParams())}
}

func testStore(t *testing.T) *store.Store {
	s := store.Open(t.TempDir())
	s.Now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return s
}

// The end-to-end contract: the Amazon fixture produces a valid revision
// and a complete run, the same evidence analysed again changes nothing, and
// two independent stores get byte-identical revisions.
func TestAnalyzeIsReproducible(t *testing.T) {
	a := testStore(t)
	res, err := Analyze(context.Background(), fixture, mockOptions(a))
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != "r0001" || !res.Changed || res.Prior != "" {
		t.Fatalf("first run: %+v", res)
	}
	m := res.Manifest
	if m.Evidence != "cl_a6df5fb6df42" || m.Counts.Families != 294 || m.Counts.StaticFamilies != 151 || len(m.EvidenceFiles) != 7 {
		t.Errorf("manifest = %+v", m)
	}
	if got, err := a.LoadRun(res.Run); err != nil || got.Revision != "r0001" {
		t.Errorf("stored manifest = %+v, %v", got, err)
	}
	for _, name := range append([]string{"manifest.json"}, store.RunFiles...) {
		if _, err := os.Stat(filepath.Join(a.Root, "runs", res.Run, name)); err != nil {
			t.Error(err)
		}
	}

	again, err := Analyze(context.Background(), fixture, mockOptions(a))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != "r0001" || again.Changed || again.Prior != "r0001" {
		t.Errorf("second run: revision %s, changed %v, prior %s", again.Revision, again.Changed, again.Prior)
	}

	b := testStore(t)
	if _, err := Analyze(context.Background(), fixture, mockOptions(b)); err != nil {
		t.Fatal(err)
	}
	ra, _ := os.ReadFile(filepath.Join(a.Root, "ir", "revisions", "r0001.json"))
	rb, _ := os.ReadFile(filepath.Join(b.Root, "ir", "revisions", "r0001.json"))
	if len(ra) == 0 || !bytes.Equal(ra, rb) {
		t.Errorf("revisions differ:\n%s\n---\n%s", ra, rb)
	}
	x, err := a.Load(store.Latest)
	if err != nil {
		t.Fatal(err)
	}
	if x.SchemaVersion != ir.SchemaVersion || len(x.Evidence) != 1 || x.Evidence[0] != "cl_a6df5fb6df42" {
		t.Errorf("revision = %+v", x)
	}
}

func TestAnalyzeRefusesInvalidPrior(t *testing.T) {
	s := testStore(t)
	bad := ir.New("cl_a6df5fb6df42")
	bad.Hypotheses = []ir.HypothesisRef{{ID: "hyp:1", Kind: ir.KindFamilyRole, Status: ir.StatusProposed}}
	bad.Operations = []ir.Operation{{
		ID: "op:1", Name: "invented", SourceFamilies: []string{"fam:000000000000"},
		Status: ir.StatusProposed, Hypothesis: "hyp:1",
	}}
	if _, _, err := s.Commit(bad, "run_0"); err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(context.Background(), fixture, mockOptions(s)); err == nil {
		t.Fatal("an IR naming an unknown family was committed")
	}
	idx, _ := s.Index()
	if len(idx) != 1 {
		t.Errorf("index has %d revisions; a rejected run must leave it unchanged", len(idx))
	}
}

const searchFamily = "98258e0892fb" // GET www.amazon.com/s

func ledger(t *testing.T, s *store.Store, run, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.Root, "runs", run, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRoleLedgersAreReproducible(t *testing.T) {
	a, b := testStore(t), testStore(t)
	ra, err := Analyze(context.Background(), fixture, mockOptions(a))
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Analyze(context.Background(), fixture, mockOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	c := ra.Manifest.Counts
	if c.Settled+c.Tasks != 294 || c.Decided != c.Tasks || c.Hypotheses != 294 {
		t.Errorf("counts = %+v", c)
	}
	for _, name := range []string{"hypotheses.jsonl", "verification.jsonl"} {
		la, lb := ledger(t, a, ra.Run, name), ledger(t, b, rb.Run, name)
		if lines := bytes.Count(la, []byte("\n")); lines != 294 {
			t.Errorf("%s has %d lines", name, lines)
		}
		if !bytes.Equal(la, lb) {
			t.Errorf("%s differs between identical runs", name)
		}
	}
	if lines := bytes.Count(ledger(t, a, ra.Run, "decisions.jsonl"), []byte("\n")); lines != 294 {
		t.Errorf("decisions.jsonl has %d lines", lines)
	}
	for _, h := range ra.IR.Hypotheses {
		if h.Kind != ir.KindFamilyRole || h.Status != ir.StatusMechanicallySupported {
			t.Errorf("hypothesis %+v", h)
		}
	}
}

func scripted(s *store.Store, replies ...string) Options {
	m := &decide.Mock{Script: map[string][]string{"role:" + searchFamily + ":v1": replies}}
	return Options{Store: s, Decider: decide.New(m, decide.ModelInfo{}, decide.DefaultParams())}
}

func searchRoles(x *ir.InterfaceIR) []ir.HypothesisRef {
	var out []ir.HypothesisRef
	for _, h := range x.Hypotheses {
		if len(h.SubjectRefs) == 1 && h.SubjectRefs[0] == "fam:"+searchFamily {
			out = append(out, h)
		}
	}
	return out
}

func TestInvalidAnswersNeverReachTheIR(t *testing.T) {
	s := testStore(t)
	res, err := Analyze(context.Background(), fixture, scripted(s,
		`{"choice_id":"search_api","evidence_refs":["E1"]}`,
		`{"choice_id":"collection_read","evidence_refs":["fam:000000000000"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Counts.Unresolved != 1 || res.Manifest.Counts.Hypotheses != 293 {
		t.Errorf("counts = %+v", res.Manifest.Counts)
	}
	if got := searchRoles(res.IR); len(got) != 0 {
		t.Errorf("an invalid answer produced %+v", got)
	}
	if bytes.Contains(ledger(t, s, res.Run, "hypotheses.jsonl"), []byte("search_api")) {
		t.Error("an invented choice reached the hypothesis ledger")
	}
}

func TestNewRoleSupersedesOld(t *testing.T) {
	s := testStore(t)
	first, err := Analyze(context.Background(), fixture, mockOptions(s))
	if err != nil {
		t.Fatal(err)
	}
	old := searchRoles(first.IR)
	if len(old) != 1 || old[0].CandidateID != "collection_read" {
		t.Fatalf("first run: %+v", old)
	}
	second, err := Analyze(context.Background(), fixture, scripted(s, `{"choice_id":"presentation","evidence_refs":["E2"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != "r0002" || second.Prior != "r0001" {
		t.Fatalf("second run: %s from %s", second.Revision, second.Prior)
	}
	got := searchRoles(second.IR)
	if len(got) != 2 {
		t.Fatalf("search hypotheses = %+v", got)
	}
	for _, h := range got {
		switch h.CandidateID {
		case "collection_read":
			if h.Status != ir.StatusSuperseded {
				t.Errorf("old claim = %+v", h)
			}
		case "presentation":
			if h.Status != ir.StatusMechanicallySupported || len(h.Supersedes) != 1 || h.Supersedes[0] != old[0].ID {
				t.Errorf("new claim = %+v", h)
			}
		default:
			t.Errorf("unexpected %+v", h)
		}
	}
	// An abstention later leaves the standing claim alone.
	third, err := Analyze(context.Background(), fixture, scripted(s, `{"choice_id":"unknown","evidence_refs":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if third.Revision != "r0002" || third.Changed || third.Manifest.Counts.Unknown != 1 {
		t.Errorf("third run: %s changed %v counts %+v", third.Revision, third.Changed, third.Manifest.Counts)
	}
}
