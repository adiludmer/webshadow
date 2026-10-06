package recording

import (
	"bytes"
	"encoding/json"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateAllocatesSeparateSessions(t *testing.T) {
	root := t.TempDir()
	a := mustCreate(t, root)
	b := mustCreate(t, root)
	if a.ID() == b.ID() || a.Dir() == b.Dir() {
		t.Fatalf("sessions share an id: %s", a.ID())
	}
	if !strings.HasSuffix(a.ID(), "_001") || !strings.HasSuffix(b.ID(), "_002") {
		t.Errorf("ids = %s, %s; want _001 then _002", a.ID(), b.ID())
	}
	if err := a.AppendExchange(&HTTPExchange{Request: Request{Method: "GET", URL: "https://a.test/"}}); err != nil {
		t.Fatal(err)
	}
	ba, _ := a.WriteBody([]byte("only in a"))
	if err := b.AppendBrowserEvent(&BrowserEvent{Type: "Page.loadEventFired"}); err != nil {
		t.Fatal(err)
	}
	closeStore(t, a)
	closeStore(t, b)

	ra, rb := mustLoad(t, a.Dir()), mustLoad(t, b.Dir())
	if len(ra.Exchanges) != 1 || len(ra.Events) != 0 || len(rb.Exchanges) != 0 || len(rb.Events) != 1 {
		t.Errorf("streams mixed: a has %d/%d, b has %d/%d", len(ra.Exchanges), len(ra.Events), len(rb.Exchanges), len(rb.Events))
	}
	if ra.Exchanges[0].SessionID != a.ID() || rb.Events[0].SessionID != b.ID() {
		t.Error("records carry the wrong session id")
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), filepath.FromSlash(ba.Ref))); err == nil {
		t.Error("a's body is in b's store")
	}
}

func TestRoundTripAndIntegrity(t *testing.T) {
	s := mustCreate(t, t.TempDir())
	if err := s.UpdateSession(func(sess *Session) {
		sess.Proxy = &ProxyInfo{Addr: "127.0.0.1:8080"}
	}); err != nil {
		t.Fatal(err)
	}
	clock := s.Clock()
	for i := 0; i < 3; i++ {
		req, _ := s.WriteBody([]byte(`{"q":"laptop"}`))
		w, err := s.NewBody()
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, "<html>result</html>")
		resp, err := w.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.ContentType = "text/html"
		start := clock.Now()
		ex := &HTTPExchange{
			ConnectionID: "conn_1",
			StartedAt:    start,
			Request:      Request{Method: "POST", Scheme: "https", Host: "shop.test", Port: 443, URL: "https://shop.test/s", Protocol: "HTTP/1.1", Headers: []Header{{"Content-Type", "application/json"}}, Body: req},
			Response:     &Response{Status: 200, Protocol: "HTTP/1.1", Headers: []Header{{"Content-Type", "text/html"}}, Body: resp},
			Timing:       Timing{RequestStart: start.T, ResponseStart: clock.Now().T, ResponseEnd: clock.Now().T},
			CompletedAt:  clock.Now(),
		}
		if err := s.AppendExchange(ex); err != nil {
			t.Fatal(err)
		}
	}
	closeStore(t, s)
	if err := s.AppendExchange(&HTTPExchange{}); err != ErrClosed {
		t.Errorf("append after close = %v, want ErrClosed", err)
	}

	r := mustLoad(t, s.Dir())
	if r.Session.Status != StatusComplete || r.Session.EndedAt == nil || r.Session.Exchanges != 3 {
		t.Errorf("session = %+v, want complete with 3 exchanges", r.Session)
	}
	if r.Session.Proxy == nil || r.Session.Proxy.Addr != "127.0.0.1:8080" {
		t.Errorf("proxy info lost: %+v", r.Session.Proxy)
	}
	ids := map[string]bool{}
	for _, ex := range r.Exchanges {
		if ex.ID == "" || ids[ex.ID] {
			t.Errorf("exchange id %q missing or repeated", ex.ID)
		}
		ids[ex.ID] = true
		if ex.Seq == 0 || ex.Timing.RequestStart == 0 || ex.Response.Status != 200 || len(ex.Request.Headers) == 0 {
			t.Errorf("exchange %s incomplete: %+v", ex.ID, ex)
		}
		for _, b := range []*Body{ex.Request.Body, ex.Response.Body} {
			rc, err := r.OpenBody(b)
			if err != nil {
				t.Fatalf("body %s: %v", b.Ref, err)
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			if int64(len(data)) != b.Size {
				t.Errorf("body %s is %d bytes, record says %d", b.Ref, len(data), b.Size)
			}
		}
	}
	bodies, _ := os.ReadDir(filepath.Join(s.Dir(), BodiesDir))
	if len(bodies) != 2 {
		t.Errorf("bodies dir has %d files, want 2 (identical bodies shared)", len(bodies))
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	s := mustCreate(t, t.TempDir())
	s.WriteBody([]byte("secret"))
	closeStore(t, s)
	filepath.Walk(s.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %v, want no group or other access", path, perm)
		}
		return nil
	})
}

func TestLoadDropsPartialLastLine(t *testing.T) {
	s := mustCreate(t, t.TempDir())
	s.AppendBrowserEvent(&BrowserEvent{Type: "Page.frameNavigated", PageURL: "https://a.test/"})
	closeStore(t, s)
	f, _ := os.OpenFile(filepath.Join(s.Dir(), BrowserFile), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"ev_000002","ty`)
	f.Close()

	r := mustLoad(t, s.Dir())
	if !r.Truncated || len(r.Events) != 1 {
		t.Errorf("truncated=%v events=%d, want true and 1", r.Truncated, len(r.Events))
	}
}

func TestTimelineIsDeterministic(t *testing.T) {
	r := &Recording{
		Exchanges: []HTTPExchange{
			{ID: "ex_000001", Seq: 3, Request: Request{Method: "GET", URL: "https://shop.test/s?k=laptop"}, Response: &Response{Status: 200}, Timing: Timing{RequestStart: 14_291_000_000, ResponseStart: 14_612_000_000}},
			{ID: "ex_000002", Seq: 4, Request: Request{Method: "GET", URL: "https://shop.test/x"}, Error: "upstream tls: unknown authority", StartedAt: Stamp{T: 14_027_000_000}, CompletedAt: Stamp{T: 14_027_000_000}},
		},
		Events: []BrowserEvent{
			{ID: "ev_000001", Seq: 1, Type: "Page.frameNavigated", PageURL: "https://shop.test/", Timestamp: Stamp{T: 12_410_000_000}},
			{ID: "ev_000002", Seq: 2, Type: "interaction.click", Timestamp: Stamp{T: 14_027_000_000}},
			{ID: "ev_000003", Seq: 5, Type: "Page.frameNavigated", PageURL: "https://shop.test/s?k=laptop", Timestamp: Stamp{T: 14_890_000_000}},
		},
	}
	want := []string{
		"browser.navigation",
		"browser.click",
		"http.request",
		"http.error",
		"http.request",
		"http.response",
		"browser.navigation",
	}
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 20; round++ {
		rng.Shuffle(len(r.Exchanges), func(i, j int) { r.Exchanges[i], r.Exchanges[j] = r.Exchanges[j], r.Exchanges[i] })
		rng.Shuffle(len(r.Events), func(i, j int) { r.Events[i], r.Events[j] = r.Events[j], r.Events[i] })
		var got []string
		for _, e := range Timeline(r, true) {
			got = append(got, e.Kind)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round %d: order = %v, want %v", round, got, want)
		}
	}
	var buf bytes.Buffer
	WriteTimeline(&buf, Timeline(r, true))
	if !strings.Contains(buf.String(), "   14.612 http.response            200 exchange=ex_000001") {
		t.Errorf("timeline text:\n%s", buf.String())
	}
}

func TestListAndDelete(t *testing.T) {
	root := t.TempDir()
	a := mustCreate(t, root)
	b := mustCreate(t, root)
	closeStore(t, a)
	closeStore(t, b)
	os.Mkdir(filepath.Join(root, "not-a-recording"), 0o700)

	list, err := List(root)
	if err != nil || len(list) != 2 || list[0].ID != a.ID() {
		t.Fatalf("List = %v, %v", list, err)
	}
	if err := Delete(root, "../escape"); err == nil {
		t.Error("Delete accepted a path")
	}
	if err := Delete(root, a.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Dir()); !os.IsNotExist(err) {
		t.Error("deleted session still exists")
	}
	if list, _ := List(root); len(list) != 1 || list[0].ID != b.ID() {
		t.Errorf("after delete List = %v", list)
	}
}

func TestSessionJSONWhileRecording(t *testing.T) {
	s := mustCreate(t, t.TempDir())
	defer s.Close()
	data, err := os.ReadFile(filepath.Join(s.Dir(), SessionFile))
	if err != nil {
		t.Fatal(err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil || sess.Status != StatusRecording || sess.ID != s.ID() {
		t.Errorf("session.json at start = %s (%v)", data, err)
	}
}

func mustCreate(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, dir string) *Recording {
	t.Helper()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
