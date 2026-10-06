package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/chromium"
	"github.com/adiludmer/webshadow/internal/recording"
)

// browserForTest returns a Chromium executable or skips.
func browserForTest(t *testing.T) string {
	t.Helper()
	if p := os.Getenv(chromium.EnvExecutable); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skipf("no Chromium found; set %s to run browser tests", chromium.EnvExecutable)
	return ""
}

// recordOnce runs one headless session that loads origin and stops once
// the page's image has been fetched.
func recordOnce(t *testing.T, home, exe string, origin *httptest.Server, loaded <-chan struct{}) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- recordSession(ctx, browserConfig{
			home: home, proxyAddr: "127.0.0.1:0", chromium: exe, headless: true,
			startURL: origin.URL + "/",
		}, &out, &errOut)
	}()
	select {
	case <-loaded:
	case err := <-done:
		t.Fatalf("session ended early: %v\n%s%s", err, out.String(), errOut.String())
	case <-time.After(time.Minute):
		t.Fatal("page never loaded")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("session: %v\n%s", err, errOut.String())
	}
	for _, want := range []string{"Webshadow recorder started", "Proxy: 127.0.0.1:", "Browser: Chromium ", "CDP: 127.0.0.1:", "Session: rec_", "Recording complete: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if id, ok := strings.CutPrefix(line, "Session: "); ok {
			return id
		}
	}
	t.Fatalf("no session id in output:\n%s", out.String())
	return ""
}

// TestBrowserSessionsStayIsolated records two sessions back to back and
// checks each has its own directory, ids and bodies, and that neither
// leaves its browser profile behind.
func TestBrowserSessionsStayIsolated(t *testing.T) {
	exe := browserForTest(t)
	home := t.TempDir()
	t.Setenv(envHome, home)

	loaded := make(chan struct{}, 1)
	var visit int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			visit++
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<!doctype html><title>shop</title><p>visit "+strings.Repeat("x", visit)+`</p><img src="/pixel.gif">`)
		case "/pixel.gif":
			select {
			case loaded <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	a := recordOnce(t, home, exe, origin, loaded)
	b := recordOnce(t, home, exe, origin, loaded)
	if a == b {
		t.Fatalf("both sessions are %s", a)
	}

	root := filepath.Join(home, "recordings")
	bodies := map[string]string{}
	for _, id := range []string{a, b} {
		r, err := recording.Load(filepath.Join(root, id))
		if err != nil {
			t.Fatal(err)
		}
		if r.Session.Status != recording.StatusComplete || r.Session.Browser == nil || r.Session.Proxy == nil {
			t.Errorf("%s session = %+v", id, r.Session)
		}
		var page *recording.HTTPExchange
		for i := range r.Exchanges {
			if ex := &r.Exchanges[i]; ex.SessionID != id {
				t.Errorf("%s holds exchange %s of session %s", id, ex.ID, ex.SessionID)
			} else if ex.Request.URL == origin.URL+"/" {
				page = ex
			}
		}
		if page == nil || page.Response == nil {
			t.Fatalf("%s did not record the page", id)
		}
		rc, err := r.OpenBody(page.Response.Body)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		bodies[id] = string(data)
		if _, err := os.Stat(filepath.Join(home, "profiles", id)); !os.IsNotExist(err) {
			t.Errorf("%s left its browser profile behind", id)
		}
	}
	if bodies[a] == bodies[b] || !strings.Contains(bodies[b], "visit xx") {
		t.Errorf("page bodies not separated: %q / %q", bodies[a], bodies[b])
	}

	code, out, _ := runCLI("recordings", "list")
	if code != 0 || !strings.Contains(out, a) || !strings.Contains(out, b) {
		t.Errorf("recordings list = %d\n%s", code, out)
	}
	code, out, _ = runCLI("recordings", "show", a)
	if code != 0 || !strings.Contains(out, "Session: "+a+" (complete)") || !strings.Contains(out, "127.0.0.1") {
		t.Errorf("recordings show = %d\n%s", code, out)
	}
	code, out, _ = runCLI("recordings", "timeline", a)
	if code != 0 || !strings.Contains(out, "http.request") || !strings.Contains(out, "http.response") {
		t.Errorf("recordings timeline = %d\n%s", code, out)
	}
	if code, _, _ = runCLI("recordings", "delete", a); code != 0 {
		t.Errorf("recordings delete exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, a)); !os.IsNotExist(err) {
		t.Error("deleted session still on disk")
	}
	if _, err := os.Stat(filepath.Join(root, b)); err != nil {
		t.Error("deleting one session touched the other")
	}
}

func TestRecordingsRejectsPaths(t *testing.T) {
	t.Setenv(envHome, t.TempDir())
	if code, _, _ := runCLI("recordings", "delete", "../../etc"); code == 0 {
		t.Error("delete accepted a path")
	}
	if code, out, _ := runCLI("recordings", "list"); code != 0 || !strings.Contains(out, "No recordings") {
		t.Errorf("empty list = %d %s", code, out)
	}
}
