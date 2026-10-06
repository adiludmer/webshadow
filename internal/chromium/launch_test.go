package chromium

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/mitm"
	"github.com/adiludmer/webshadow/internal/recording"
)

// testExecutable returns a Chromium to launch, or skips the test.
func testExecutable(t *testing.T) string {
	t.Helper()
	if p := os.Getenv(EnvExecutable); p != "" {
		return p
	}
	if p := findSystem(); p != "" {
		return p
	}
	t.Skipf("no Chromium found; set %s to run browser tests", EnvExecutable)
	return ""
}

// TestLaunchThroughProxy launches Chromium on an HTTPS page served from
// loopback and checks the page and its image were fetched through the
// proxy, which only happens if Chromium accepted the Webshadow leaf.
func TestLaunchThroughProxy(t *testing.T) {
	exe := testExecutable(t)
	pixel := make(chan struct{}, 1)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!doctype html><title>shop</title><img src="/pixel.gif">`)
		case "/pixel.gif":
			w.Header().Set("Content-Type", "image/gif")
			select {
			case pixel <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	ca, err := mitm.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := recording.Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := mitm.NewTransport()
	tr.Proxy = nil
	pool := x509.NewCertPool()
	pool.AddCert(origin.Certificate())
	tr.TLSClientConfig.RootCAs = pool
	ln, err := mitm.Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mitm.Proxy{CA: ca, Store: store, Transport: tr, Logf: t.Logf}
	go proxy.Serve(ln)
	defer proxy.Close()

	profile, err := NewProfile(t.TempDir(), store.ID())
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b, err := Launch(ctx, LaunchOptions{
		Executable: exe,
		ProfileDir: profile,
		ProxyAddr:  ln.Addr().String(),
		SPKIHash:   ca.LeafSPKIHash(),
		Headless:   true,
		StartURL:   origin.URL + "/",
		Log:        &log, // one writer for stdout and stderr: exec serializes its writes
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !strings.Contains(b.Version, "/") || !strings.HasPrefix(b.DevToolsURL, "ws://127.0.0.1:") {
		t.Errorf("version %q, DevTools %q", b.Version, b.DevToolsURL)
	}

	select {
	case <-pixel:
	case <-ctx.Done():
		b.Close()
		t.Fatalf("page image never requested; chromium log:\n%s", log.String())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	default:
		t.Error("browser still running after Close")
	}
	proxy.Close()
	store.Close()

	r, err := recording.Load(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	var page, img bool
	for _, ex := range r.Exchanges {
		if !strings.HasPrefix(ex.Request.URL, origin.URL) {
			continue
		}
		if ex.Request.Scheme != "https" || ex.Response == nil || ex.Error != "" {
			t.Errorf("exchange %s: %+v", ex.ID, ex)
			continue
		}
		switch ex.Request.URL {
		case origin.URL + "/":
			page = ex.Response.Status == 200
		case origin.URL + "/pixel.gif":
			img = true
		}
	}
	if !page || !img {
		t.Errorf("page recorded=%v, image recorded=%v among %d exchanges", page, img, len(r.Exchanges))
	}
	if _, err := os.Stat(filepath.Join(profile, "DevToolsActivePort")); err != nil {
		t.Errorf("profile was not the one Chromium used: %v", err)
	}
}

func TestLaunchFailureShowsBrowserOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	exe := filepath.Join(t.TempDir(), "chrome")
	os.WriteFile(exe, []byte("#!/bin/sh\necho 'No usable sandbox!' >&2\nexit 1\n"), 0o755)
	_, err := Launch(context.Background(), LaunchOptions{
		Executable: exe, ProfileDir: t.TempDir(), ProxyAddr: "127.0.0.1:1", SPKIHash: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "No usable sandbox!") {
		t.Errorf("err = %v, want the browser's output", err)
	}
}
