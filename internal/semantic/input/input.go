// Package input reads a clustering result (the output directory of
// `webshadow cluster`) for semantic discovery. Every value passes through
// the privacy redactor on the way in, so nothing downstream sees a raw
// cookie or token.
package input

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/cluster/evidence"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/privacy"
)

// Files are the clustering outputs semantic discovery reads. Each may also
// be stored gzipped, with a .gz suffix.
var Files = cluster.Files

// File is one clustering output as read: its name and the SHA-256 of its
// bytes on disk, so a run can say exactly what it consumed.
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Raw is a clustering result decoded as generic JSON, file by file.
type Raw struct {
	Dir   string
	Files []File
	Docs  map[string]any
}

// Input is a redacted clustering result.
type Input struct {
	Dir       string
	Files     []File
	Manifest  cluster.Manifest
	Families  []model.RequestFamily
	Links     []model.ValueLink
	Traces    []model.Trace
	Episodes  []model.Episode
	Sequences []model.SequenceEdge
	Evidence  evidence.Pack
	Redaction privacy.Stats

	refs map[string]bool
}

// Load reads, redacts and decodes the clustering result in dir.
func Load(dir string) (*Input, error) {
	raw, err := ReadRaw(dir)
	if err != nil {
		return nil, err
	}
	stats := raw.Redact()
	in, err := raw.Decode()
	if err != nil {
		return nil, err
	}
	in.Redaction = stats
	return in, nil
}

// ReadRaw reads every clustering output in dir without redacting it.
// Callers redact before using any value; Load does.
func ReadRaw(dir string) (*Raw, error) {
	raw := &Raw{Dir: dir, Docs: map[string]any{}}
	for _, name := range Files {
		data, stored, err := readFile(dir, name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		raw.Files = append(raw.Files, File{Name: stored, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))})
		if filepath.Ext(stored) == ".gz" {
			if data, err = gunzip(data); err != nil {
				return nil, fmt.Errorf("%s: %w", stored, err)
			}
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var doc any
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("%s: %w", stored, err)
		}
		raw.Docs[name] = doc
	}
	var m struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
	}
	if err := redecode(raw.Docs["manifest.json"], &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Version != model.Version {
		return nil, fmt.Errorf("%s: clustering output version %d, this build reads version %d; re-run webshadow cluster", dir, m.Version, model.Version)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("%s: manifest.json has no id", dir)
	}
	return raw, nil
}

// Redact replaces every secret value in the documents with its equality
// tag, keyed by the clustering result id.
func (r *Raw) Redact() privacy.Stats {
	id := str(obj(r.Docs["manifest.json"])["id"])
	red := privacy.New(id, r.Docs["links.json"], r.Docs["sequences.json"])
	for _, name := range Files {
		r.Docs[name] = red.Rewrite(r.Docs[name])
	}
	return red.Stats()
}

// Write stores the documents in dir in the form `webshadow cluster`
// writes, gzipped when gz is set. The manifest goes last, so a directory
// with a manifest is complete.
func (r *Raw) Write(dir string, gz bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range Files {
		data, err := ir.EncodeJSON(r.Docs[name])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if gz {
			var b bytes.Buffer
			w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
			w.Write(data)
			if err := w.Close(); err != nil {
				return err
			}
			data, name = b.Bytes(), name+".gz"
		}
		if err := WriteFileAtomic(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// Decode turns the documents into typed clustering output and indexes
// every id they define.
func (r *Raw) Decode() (*Input, error) {
	in := &Input{Dir: r.Dir, Files: r.Files}
	targets := map[string]any{
		"manifest.json":  &in.Manifest,
		"families.json":  &in.Families,
		"links.json":     &in.Links,
		"traces.json":    &in.Traces,
		"episodes.json":  &in.Episodes,
		"sequences.json": &in.Sequences,
		"evidence.json":  &in.Evidence,
	}
	for _, name := range Files {
		if err := redecode(r.Docs[name], targets[name]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	in.index()
	return in, nil
}

func (in *Input) index() {
	in.refs = map[string]bool{}
	add := func(prefix string, parts ...string) { in.refs[ir.Ref(prefix, parts...)] = true }
	for _, f := range in.Families {
		add(ir.RefFamily, f.ID)
		for _, v := range f.ResponseVariants {
			add(ir.RefVariant, f.ID, v.ID)
		}
		for _, o := range f.Observations {
			add(ir.RefObservation, o.SessionID, o.ExchangeID)
		}
	}
	for _, l := range in.Links {
		add(ir.RefValueLink, l.ID)
	}
	for _, t := range in.Traces {
		add(ir.RefTrace, t.ID)
		for _, n := range t.Navigations {
			add(ir.RefNavigation, n.ID)
		}
	}
	for _, e := range in.Episodes {
		add(ir.RefEpisode, e.ID)
	}
	for _, s := range in.Sequences {
		add(ir.RefSequence, s.ID)
	}
}

// Has reports whether ref names something in this clustering result. It
// makes an Input an ir.Resolver.
func (in *Input) Has(ref string) bool { return in.refs[ref] }

// Refs returns every reference the result defines, sorted.
func (in *Input) Refs() []string {
	out := make([]string, 0, len(in.refs))
	for r := range in.refs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// readFile reads name from dir, falling back to name.gz. It returns the
// bytes as stored and the file name they came from.
func readFile(dir, name string) ([]byte, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err == nil {
		return data, name, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	data, gzErr := os.ReadFile(filepath.Join(dir, name+".gz"))
	if gzErr == nil {
		return data, name + ".gz", nil
	}
	if errors.Is(gzErr, os.ErrNotExist) {
		return nil, "", fmt.Errorf("%s: no %s; is this a webshadow cluster output directory?", dir, name)
	}
	return nil, "", gzErr
}

func gunzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// redecode moves a generic document into a typed value through JSON.
func redecode(doc, target any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// WriteFileAtomic writes data to a temporary file beside path and renames
// it into place, so a reader never sees a partial file.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
