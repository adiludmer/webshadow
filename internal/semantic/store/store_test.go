package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

func testStore(t *testing.T) *Store {
	s := Open(t.TempDir())
	s.Now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestCommitWritesRevisionsOnlyOnChange(t *testing.T) {
	s := testStore(t)
	if x, err := s.Load(Latest); x != nil || err != nil {
		t.Fatalf("empty store: %v, %v", x, err)
	}
	rev, changed, err := s.Commit(ir.New("cl_a"), "run_1")
	if err != nil || rev != "r0001" || !changed {
		t.Fatalf("first commit: %s %v %v", rev, changed, err)
	}
	rev, changed, err = s.Commit(ir.New("cl_a"), "run_2")
	if err != nil || rev != "r0001" || changed {
		t.Fatalf("identical commit: %s %v %v", rev, changed, err)
	}
	next := ir.New("cl_a", "cl_b")
	rev, changed, err = s.Commit(next, "run_3")
	if err != nil || rev != "r0002" || !changed || next.Parent != "r0001" {
		t.Fatalf("changed commit: %s %v %v, parent %q", rev, changed, err, next.Parent)
	}
	latest, err := s.Load(Latest)
	if err != nil || latest.Revision != "r0002" || latest.Parent != "r0001" || len(latest.Evidence) != 2 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	idx, err := s.Index()
	if err != nil || len(idx) != 2 || idx[1].Run != "run_3" || idx[1].Parent != "r0001" {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	if _, err := s.Load("r0009"); err == nil {
		t.Error("a missing revision loaded")
	}
}

func TestLoadDetectsTampering(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.Commit(ir.New("cl_a"), "run_1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "ir", "revisions", "r0001.json")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "cl_a", "cl_z", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(Latest); err == nil || !strings.Contains(err.Error(), "does not match the index") {
		t.Errorf("err = %v", err)
	}
}

func TestRunDirectory(t *testing.T) {
	s := testStore(t)
	run, err := s.NewRun()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.ID, "run_20261009T120000Z_") {
		t.Errorf("run id %q", run.ID)
	}
	for _, name := range RunFiles {
		if _, err := os.Stat(filepath.Join(run.Dir, name)); err != nil {
			t.Error(err)
		}
	}
	if err := run.WriteManifest(RunManifest{Run: run.ID, Revision: "r0001"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.LoadRun(run.ID)
	if err != nil || m.Revision != "r0001" {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	if _, err := s.LoadRun("../x"); err == nil {
		t.Error("a path escaped the runs directory")
	}
}

func TestValidRevision(t *testing.T) {
	for name, want := range map[string]bool{"latest": true, "r0001": true, "r12345": true, "r1": false, "../r0001": false} {
		if ValidRevision(name) != want {
			t.Errorf("ValidRevision(%q) != %v", name, want)
		}
	}
}
