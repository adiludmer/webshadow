// Package store keeps semantic discovery's files: one directory per
// analysis run and an append-only series of IR revisions.
//
//	<root>/runs/<run-id>/manifest.json
//	<root>/runs/<run-id>/decisions.jsonl
//	<root>/runs/<run-id>/hypotheses.jsonl
//	<root>/runs/<run-id>/verification.jsonl
//	<root>/ir/revisions/<revision>.json
//	<root>/ir/index.json
//
// Revisions are immutable. Committing an IR whose content equals the latest
// revision writes nothing, so re-running identical evidence leaves the
// series as it was. index.json records each revision's parent, the run
// that wrote it and its SHA-256, and loading a revision checks that hash.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/privacy"
)

// Latest names the newest revision wherever a revision is asked for.
const Latest = "latest"

// RunFiles are the ledgers every run directory holds, empty until a stage
// writes to them.
var RunFiles = []string{"decisions.jsonl", "hypotheses.jsonl", "verification.jsonl"}

var revisionPattern = regexp.MustCompile(`^r[0-9]{4,}$`)

// Store is an analysis directory.
type Store struct {
	Root string
	// Now is the clock run ids and manifests use; tests replace it.
	Now func() time.Time
}

// Open returns the store at root. Nothing is created until something is
// written.
func Open(root string) *Store {
	return &Store{Root: root, Now: time.Now}
}

// IndexEntry is one revision in index.json.
type IndexEntry struct {
	Revision string `json:"revision"`
	Parent   string `json:"parent,omitempty"`
	Run      string `json:"run"`
	SHA256   string `json:"sha256"`
}

func (s *Store) revisionsDir() string { return filepath.Join(s.Root, "ir", "revisions") }
func (s *Store) indexPath() string    { return filepath.Join(s.Root, "ir", "index.json") }

// Index returns the revisions in order, oldest first.
func (s *Store) Index() ([]IndexEntry, error) {
	data, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var idx []IndexEntry
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("%s: %w", s.indexPath(), err)
	}
	return idx, nil
}

// Load returns a revision, or the newest one for Latest. It returns nil and
// no error when the store holds no revision yet and Latest was asked for.
func (s *Store) Load(revision string) (*ir.InterfaceIR, error) {
	idx, err := s.Index()
	if err != nil {
		return nil, err
	}
	var entry *IndexEntry
	if revision == Latest {
		if len(idx) == 0 {
			return nil, nil
		}
		entry = &idx[len(idx)-1]
	} else {
		for i := range idx {
			if idx[i].Revision == revision {
				entry = &idx[i]
			}
		}
		if entry == nil {
			return nil, fmt.Errorf("no revision %q in %s", revision, s.Root)
		}
	}
	path := filepath.Join(s.revisionsDir(), entry.Revision+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if got := sha(data); got != entry.SHA256 {
		return nil, fmt.Errorf("%s: SHA-256 %s does not match the index (%s); the file was changed after it was written", path, got, entry.SHA256)
	}
	var x ir.InterfaceIR
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if x.SchemaVersion != ir.SchemaVersion {
		return nil, fmt.Errorf("%s: schema %q, this build reads %q", path, x.SchemaVersion, ir.SchemaVersion)
	}
	return &x, nil
}

// Commit stores x as the next revision, written by run. When x has the
// same content as the latest revision nothing is written and that
// revision is returned with changed false. x gets its Revision and Parent
// set either way.
func (s *Store) Commit(x *ir.InterfaceIR, run string) (revision string, changed bool, err error) {
	idx, err := s.Index()
	if err != nil {
		return "", false, err
	}
	parent := ""
	if len(idx) > 0 {
		last := idx[len(idx)-1]
		prev, err := s.Load(last.Revision)
		if err != nil {
			return "", false, err
		}
		if same, err := sameContent(prev, x); err != nil {
			return "", false, err
		} else if same {
			x.Revision, x.Parent = prev.Revision, prev.Parent
			return last.Revision, false, nil
		}
		parent = last.Revision
	}
	x.Revision = fmt.Sprintf("r%04d", len(idx)+1)
	x.Parent = parent
	data, err := x.Encode()
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(s.revisionsDir(), 0o700); err != nil {
		return "", false, err
	}
	// O_EXCL keeps two writers from both claiming the same revision.
	path := filepath.Join(s.revisionsDir(), x.Revision+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("revision %s: %w", x.Revision, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", false, err
	}
	idx = append(idx, IndexEntry{Revision: x.Revision, Parent: parent, Run: run, SHA256: sha(data)})
	enc, err := ir.EncodeJSON(idx)
	if err != nil {
		return "", false, err
	}
	if err := input.WriteFileAtomic(s.indexPath(), enc); err != nil {
		return "", false, err
	}
	return x.Revision, true, nil
}

// sameContent compares two IRs ignoring their revision and parent.
func sameContent(a, b *ir.InterfaceIR) (bool, error) {
	ca, cb := *a, *b
	ca.Revision, ca.Parent, cb.Revision, cb.Parent = "", "", "", ""
	da, err := ca.Encode()
	if err != nil {
		return false, err
	}
	db, err := cb.Encode()
	if err != nil {
		return false, err
	}
	return string(da) == string(db), nil
}

// ValidRevision reports whether name is Latest or a revision name.
func ValidRevision(name string) bool {
	return name == Latest || revisionPattern.MatchString(name)
}

// RunManifest describes one analysis run. Timestamps and the run id are
// run metadata; everything a run decides lives in its ledgers and in the
// IR revision, which are reproducible.
type RunManifest struct {
	SchemaVersion string    `json:"schema_version"`
	Run           string    `json:"run"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished"`
	// Evidence is the clustering result analysed, with the files read and
	// their hashes as stored.
	Evidence      string        `json:"evidence"`
	EvidenceDir   string        `json:"evidence_dir"`
	EvidenceFiles []input.File  `json:"evidence_files"`
	Redaction     privacy.Stats `json:"redaction"`
	Model         string        `json:"model,omitempty"`
	// Prior is the revision the run started from; Revision is the one it
	// ended at, which equals Prior when nothing changed.
	Prior    string `json:"prior,omitempty"`
	Revision string `json:"revision"`
	Changed  bool   `json:"changed"`
	Counts   Counts `json:"counts"`
}

// Counts sizes a run.
type Counts struct {
	Families       int `json:"families"`
	StaticFamilies int `json:"static_families"`
	Tasks          int `json:"tasks"`
	Hypotheses     int `json:"hypotheses"`
	Entities       int `json:"entities"`
	Operations     int `json:"operations"`
}

// Run is one analysis run's directory.
type Run struct {
	ID  string
	Dir string
}

// NewRun creates a run directory with empty ledgers. Its id is the start
// time plus a random suffix, so two runs never share a directory.
func (s *Store) NewRun() (*Run, error) {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	id := "run_" + s.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(suffix[:])
	dir := filepath.Join(s.Root, "runs", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	for _, name := range RunFiles {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			return nil, err
		}
	}
	return &Run{ID: id, Dir: dir}, nil
}

// WriteManifest stores the run's manifest. It is written last, so a run
// directory with a manifest is complete.
func (r *Run) WriteManifest(m RunManifest) error {
	data, err := ir.EncodeJSON(m)
	if err != nil {
		return err
	}
	return input.WriteFileAtomic(filepath.Join(r.Dir, "manifest.json"), data)
}

// LoadRun reads a run's manifest by id.
func (s *Store) LoadRun(id string) (*RunManifest, error) {
	if strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid run id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "runs", id, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m RunManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
