package recording

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Recording is a session read back from disk.
type Recording struct {
	Dir       string
	Session   Session
	Exchanges []HTTPExchange
	Events    []BrowserEvent
	// Truncated is set when a stream ended in a partial line, which happens
	// when the recorder was killed mid-write. The partial line is dropped.
	Truncated bool
}

// Load reads the recording in dir.
func Load(dir string) (*Recording, error) {
	r := &Recording{Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, SessionFile))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &r.Session); err != nil {
		return nil, fmt.Errorf("%s: %w", SessionFile, err)
	}
	cut, err := readLines(filepath.Join(dir, HTTPFile), func(line []byte) error {
		var ex HTTPExchange
		if err := json.Unmarshal(line, &ex); err != nil {
			return err
		}
		r.Exchanges = append(r.Exchanges, ex)
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.Truncated = cut
	cut, err = readLines(filepath.Join(dir, BrowserFile), func(line []byte) error {
		var ev BrowserEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return err
		}
		r.Events = append(r.Events, ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.Truncated = r.Truncated || cut
	return r, nil
}

// readLines calls f for every JSON line in path. A final line without a
// newline that does not parse is reported as cut rather than as an error.
func readLines(path string, f func([]byte) error) (cut bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for n := 1; len(data) > 0; n++ {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		data = rest
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := f(line); err != nil {
			if !complete {
				return true, nil
			}
			return false, fmt.Errorf("%s:%d: %w", filepath.Base(path), n, err)
		}
	}
	return false, nil
}

// OpenBody opens a stored body.
func (r *Recording) OpenBody(b *Body) (io.ReadCloser, error) {
	if b == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if !bodyRef.MatchString(b.Ref) {
		return nil, fmt.Errorf("recording: bad body ref %q", b.Ref)
	}
	return os.Open(filepath.Join(r.Dir, filepath.FromSlash(b.Ref)))
}

var (
	bodyRef   = regexp.MustCompile(`^bodies/[0-9a-f]{64}\.bin$`)
	sessionID = regexp.MustCompile(`^rec_[0-9]{8}_[0-9]{3,4}$`)
)

// List returns the sessions under root, oldest first. Directories that are
// not recordings are skipped.
func List(root string) ([]Session, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if !e.IsDir() || !sessionID.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), SessionFile))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(data, &s) == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Dir returns the directory of session id under root.
func Dir(root, id string) (string, error) {
	if !sessionID.MatchString(id) {
		return "", fmt.Errorf("recording: %q is not a session id", id)
	}
	return filepath.Join(root, id), nil
}

// Delete removes session id under root, bodies included.
func Delete(root, id string) error {
	dir, err := Dir(root, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, SessionFile)); err != nil {
		return fmt.Errorf("recording: no session %s under %s", id, root)
	}
	return os.RemoveAll(dir)
}
