package recording

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recordings hold cookies, tokens and form data, so only the owner can
// read them.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// ErrClosed is returned by writes to a store that has been closed.
var ErrClosed = errors.New("recording: store is closed")

// Store writes one recording session. It is safe for concurrent use.
type Store struct {
	dir   string
	clock *Clock

	mu      sync.Mutex
	session Session
	http    *os.File
	browser *os.File
	httpW   *bufio.Writer
	browW   *bufio.Writer
	seq     uint64
	closed  bool
}

// Create starts a new session under root, named rec_<date>_<NNN> with the
// first free number for that day, and writes its session.json.
func Create(root string) (*Store, error) {
	start := time.Now()
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, err
	}
	id, dir, err := allocate(root, start)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:   dir,
		clock: NewClock(start),
		session: Session{
			FormatVersion: FormatVersion,
			ID:            id,
			Status:        StatusRecording,
			StartedAt:     start.UTC(),
		},
	}
	if err := os.Mkdir(filepath.Join(dir, BodiesDir), dirMode); err != nil {
		return nil, err
	}
	if s.http, err = openAppend(filepath.Join(dir, HTTPFile)); err != nil {
		return nil, err
	}
	if s.browser, err = openAppend(filepath.Join(dir, BrowserFile)); err != nil {
		s.http.Close()
		return nil, err
	}
	s.httpW = bufio.NewWriter(s.http)
	s.browW = bufio.NewWriter(s.browser)
	if err := writeSession(dir, &s.session); err != nil {
		s.http.Close()
		s.browser.Close()
		return nil, err
	}
	return s, nil
}

// allocate creates the session directory. Mkdir fails on an existing name,
// so two sessions started at once never share a directory.
func allocate(root string, start time.Time) (id, dir string, err error) {
	day := start.Format("20060102")
	for n := 1; n < 10000; n++ {
		id = fmt.Sprintf("rec_%s_%03d", day, n)
		dir = filepath.Join(root, id)
		err = os.Mkdir(dir, dirMode)
		if err == nil {
			return id, dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("recording: no free session id for %s under %s", day, root)
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, fileMode)
}

// ID returns the session id.
func (s *Store) ID() string { return s.session.ID }

// Dir returns the recording directory.
func (s *Store) Dir() string { return s.dir }

// Clock returns the session clock.
func (s *Store) Clock() *Clock { return s.clock }

// UpdateSession changes session metadata, such as the browser launch
// configuration, and rewrites session.json.
func (s *Store) UpdateSession(f func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	f(&s.session)
	return writeSession(s.dir, &s.session)
}

// AppendExchange writes a completed exchange to http.jsonl. It fills in
// the session id, the sequence number and, when empty, the exchange id.
func (s *Store) AppendExchange(ex *HTTPExchange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.session.Exchanges++
	if ex.ID == "" {
		ex.ID = fmt.Sprintf("ex_%06d", s.session.Exchanges)
	}
	ex.SessionID = s.session.ID
	s.seq++
	ex.Seq = s.seq
	return writeLine(s.httpW, ex)
}

// AppendBrowserEvent writes an event to browser.jsonl. It fills in the
// session id, the sequence number and, when empty, the event id.
func (s *Store) AppendBrowserEvent(ev *BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.session.BrowserEvents++
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("ev_%06d", s.session.BrowserEvents)
	}
	ev.SessionID = s.session.ID
	s.seq++
	ev.Seq = s.seq
	return writeLine(s.browW, ev)
}

// Flush pushes buffered records to the files, so a crash afterwards loses
// nothing already appended.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return errors.Join(s.httpW.Flush(), s.browW.Flush())
}

// Close flushes and syncs both streams and marks the session complete.
// Closing twice is a no-op.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := errors.Join(
		s.httpW.Flush(), s.http.Sync(), s.http.Close(),
		s.browW.Flush(), s.browser.Sync(), s.browser.Close(),
	)
	end := time.Now().UTC()
	s.session.EndedAt = &end
	s.session.Status = StatusComplete
	return errors.Join(err, writeSession(s.dir, &s.session))
}

func writeLine(w *bufio.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// writeSession replaces session.json atomically.
func writeSession(dir string, sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".session-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if err := errors.Join(werr, tmp.Sync(), tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, SessionFile))
}

// BodyWriter streams one body into the store while hashing it.
type BodyWriter struct {
	dir  string
	f    *os.File
	h    hash.Hash
	size int64
}

// NewBody starts a body. Write the bytes, then call Close to store them,
// or Abort to drop them.
func (s *Store) NewBody() (*BodyWriter, error) {
	dir := filepath.Join(s.dir, BodiesDir)
	f, err := os.CreateTemp(dir, ".body-*")
	if err != nil {
		return nil, err
	}
	return &BodyWriter{dir: dir, f: f, h: sha256.New()}, nil
}

// WriteBody stores a body held in memory. It returns nil for an empty body.
func (s *Store) WriteBody(data []byte) (*Body, error) {
	if len(data) == 0 {
		return nil, nil
	}
	w, err := s.NewBody()
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		w.Abort()
		return nil, err
	}
	return w.Close()
}

func (w *BodyWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.h.Write(p[:n])
	w.size += int64(n)
	return n, err
}

// Close stores the body as bodies/<sha256>.bin, sharing the file with any
// identical body already stored. It returns nil for an empty body.
func (w *BodyWriter) Close() (*Body, error) {
	tmp := w.f.Name()
	if err := w.f.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if w.size == 0 {
		os.Remove(tmp)
		return nil, nil
	}
	sum := hex.EncodeToString(w.h.Sum(nil))
	name := sum + ".bin"
	final := filepath.Join(w.dir, name)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		// Another writer may have stored the same bytes first.
		if _, serr := os.Stat(final); serr != nil {
			return nil, err
		}
	}
	return &Body{Ref: BodiesDir + "/" + name, Size: w.size, SHA256: sum}, nil
}

// Abort drops a body that will not be recorded.
func (w *BodyWriter) Abort() {
	w.f.Close()
	os.Remove(w.f.Name())
}

var _ io.Writer = (*BodyWriter)(nil)
