package semantic

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const fixture = "testdata/amazon-search"

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
	res, err := Analyze(fixture, Options{Store: a})
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

	again, err := Analyze(fixture, Options{Store: a})
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != "r0001" || again.Changed || again.Prior != "r0001" {
		t.Errorf("second run: revision %s, changed %v, prior %s", again.Revision, again.Changed, again.Prior)
	}

	b := testStore(t)
	if _, err := Analyze(fixture, Options{Store: b}); err != nil {
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
	if _, err := Analyze(fixture, Options{Store: s}); err == nil {
		t.Fatal("an IR naming an unknown family was committed")
	}
	idx, _ := s.Index()
	if len(idx) != 1 {
		t.Errorf("index has %d revisions; a rejected run must leave it unchanged", len(idx))
	}
}
