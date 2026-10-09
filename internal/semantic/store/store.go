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
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
var RunFiles = []string{"decisions.jsonl", "hypotheses.jsonl", "verification.jsonl", "prerequisites.jsonl", "merges.jsonl"}

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
	// Coverage is how much of the recorded browsing the operations cover.
	Coverage Coverage `json:"coverage"`
	// ReusedFrom names the earlier run whose unchanged decisions this run
	// repeated instead of asking the model again.
	ReusedFrom string `json:"reused_from,omitempty"`
}

// Coverage scores operations against the browsing they came from: of the
// episodes whose traffic includes a family with a standing role other than
// background or presentation, how many include an operation's family.
type Coverage struct {
	Episodes int     `json:"episodes"`
	Relevant int     `json:"relevant"`
	Covered  int     `json:"covered"`
	Score    float64 `json:"score"`
	// Uncovered lists the relevant episodes no operation covers.
	Uncovered []string `json:"uncovered"`
}

// Patch is how a run changed the IR: the nodes it added, changed and
// removed, the hypotheses whose status moved, and the contradictions among
// them, so no prior assertion changes silently.
type Patch struct {
	Prior          string         `json:"prior,omitempty"`
	Revision       string         `json:"revision"`
	Added          []string       `json:"added"`
	Changed        []string       `json:"changed"`
	Removed        []string       `json:"removed"`
	StatusChanges  []StatusChange `json:"status_changes"`
	Contradictions []StatusChange `json:"contradictions"`
}

// StatusChange is one hypothesis whose status moved.
type StatusChange struct {
	Hypothesis string              `json:"hypothesis"`
	From       ir.HypothesisStatus `json:"from"`
	To         ir.HypothesisStatus `json:"to"`
}

// Counts sizes a run.
type Counts struct {
	Families       int `json:"families"`
	StaticFamilies int `json:"static_families"`
	Tasks          int `json:"tasks"`
	Settled        int `json:"settled"`
	Decided        int `json:"decided"`
	Unknown        int `json:"unknown"`
	Unresolved     int `json:"unresolved"`
	Hypotheses     int `json:"hypotheses"`
	Entities       int `json:"entities"`
	Operations     int `json:"operations"`
	// Prerequisites counts the state prerequisites the run proposed.
	Prerequisites int `json:"prerequisites"`
	// Reused counts decisions repeated from an earlier run.
	Reused int `json:"reused"`
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

// Append adds records to one of the run's ledgers, one JSON object per
// line.
func (r *Run) Append(ledger string, records ...any) error {
	f, err := os.OpenFile(filepath.Join(r.Dir, ledger), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
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

// Runs returns the ids of the complete runs, oldest first. Run ids start
// with their start time, so name order is time order.
func (s *Store) Runs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(s.Root, "runs", e.Name(), "manifest.json")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadLedger decodes every line of one of a run's ledgers into a new T.
func ReadLedger[T any](s *Store, run, ledger string) ([]T, error) {
	if strings.ContainsAny(run, `/\`) || strings.ContainsAny(ledger, `/\`) {
		return nil, fmt.Errorf("invalid run %q or ledger %q", run, ledger)
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "runs", run, ledger))
	if err != nil {
		return nil, err
	}
	var out []T
	for i, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("%s/%s line %d: %w", run, ledger, i+1, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// ReadJSON decodes a run file such as patch.json.
func ReadJSON[T any](s *Store, run, name string) (*T, error) {
	if strings.ContainsAny(run, `/\`) || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("invalid run %q or file %q", run, name)
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "runs", run, name))
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", run, name, err)
	}
	return &v, nil
}

// WriteJSON stores a run file such as patch.json atomically.
func (r *Run) WriteJSON(name string, v any) error {
	data, err := ir.EncodeJSON(v)
	if err != nil {
		return err
	}
	return input.WriteFileAtomic(filepath.Join(r.Dir, name), data)
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
