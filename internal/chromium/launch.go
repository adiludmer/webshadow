package chromium

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LaunchOptions configures one recording browser.
type LaunchOptions struct {
	Executable string
	// ProfileDir is the isolated user data directory; it must already exist.
	ProfileDir string
	// ProxyAddr is the recording proxy, host:port.
	ProxyAddr string
	// SPKIHash is the base64 SHA-256 of the key every Webshadow leaf
	// certificate uses; only certificates with that key are accepted
	// without the system trusting the Webshadow CA.
	SPKIHash string
	Headless bool
	// StartURL is opened in the first tab; empty opens a blank tab.
	StartURL string
	// Log receives the browser's own output. Nil discards it.
	Log io.Writer
}

// Args returns the command-line flags for a launch, without the
// executable.
func (o *LaunchOptions) Args() []string {
	args := []string{
		"--user-data-dir=" + o.ProfileDir,
		"--proxy-server=http://" + o.ProxyAddr,
		// Chromium skips the proxy for loopback by default; recording
		// local sites needs it too.
		"--proxy-bypass-list=<-loopback>",
		"--ignore-certificate-errors-spki-list=" + o.SPKIHash,
		// DevTools listens on a free loopback port, written to
		// DevToolsActivePort in the profile.
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		// QUIC would bypass the HTTP proxy.
		"--disable-quic",
		// Keep the recording to what the user does.
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--disable-features=OptimizationHints,Translate,MediaRouter,DialMediaRouteProvider,AutofillServerCommunication,CertificateTransparencyComponentUpdater,PasswordLeakDetection,NetworkTimeServiceQuerying,SpellcheckService",
		"--metrics-recording-only",
		"--password-store=basic",
		"--use-mock-keychain",
	}
	if o.Headless {
		args = append(args, "--headless=new")
	}
	// Chromium's sandbox refuses to run as root, as in containers and CI.
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	start := o.StartURL
	if start == "" {
		start = "about:blank"
	}
	return append(args, start)
}

// Browser is a launched Chromium owned by one recording session.
type Browser struct {
	Args []string
	// DevToolsURL is the browser-level CDP WebSocket endpoint.
	DevToolsURL string
	// Version is the product string Chromium reports, such as
	// "Chrome/157.0.8089.0".
	Version string

	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Launch starts Chromium and waits until its DevTools endpoint answers.
func Launch(ctx context.Context, o LaunchOptions) (*Browser, error) {
	if o.Executable == "" || o.ProfileDir == "" || o.ProxyAddr == "" || o.SPKIHash == "" {
		return nil, errors.New("chromium: executable, profile, proxy and SPKI hash are required")
	}
	// A stale port file from a previous run would point at a dead browser.
	os.Remove(filepath.Join(o.ProfileDir, "DevToolsActivePort"))

	b := &Browser{Args: o.Args(), done: make(chan struct{})}
	b.cmd = exec.Command(o.Executable, b.Args...)
	// The tail of the browser's output explains a failed start.
	tail := &tailWriter{max: 2048}
	var out io.Writer = tail
	if o.Log != nil {
		out = io.MultiWriter(o.Log, tail)
	}
	b.cmd.Stdout, b.cmd.Stderr = out, out
	setProcessGroup(b.cmd)
	if err := b.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting chromium: %w", err)
	}
	go func() {
		b.err = b.cmd.Wait()
		close(b.done)
	}()

	port, path, err := waitDevTools(ctx, o.ProfileDir, b.done)
	if err != nil {
		b.Close()
		if t := strings.TrimSpace(tail.String()); t != "" {
			err = fmt.Errorf("%w; chromium output ends:\n%s", err, t)
		}
		return nil, err
	}
	b.DevToolsURL = fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
	if b.Version, err = version(ctx, port); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// waitDevTools polls for the DevToolsActivePort file Chromium writes once
// its debugging endpoint listens.
func waitDevTools(ctx context.Context, profile string, exited <-chan struct{}) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	file := filepath.Join(profile, "DevToolsActivePort")
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if port, path, err := readDevToolsPort(file); err == nil {
			return port, path, nil
		}
		select {
		case <-exited:
			return 0, "", errors.New("chromium exited before DevTools started")
		case <-ctx.Done():
			return 0, "", errors.New("chromium did not open DevTools within 60s")
		case <-tick.C:
		}
	}
}

func readDevToolsPort(file string) (int, string, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if len(lines) < 2 {
		return 0, "", errors.New("DevToolsActivePort is incomplete")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port <= 0 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
		return 0, "", fmt.Errorf("DevToolsActivePort is malformed: %q", lines)
	}
	return port, lines[1], nil
}

// version asks the DevTools endpoint which browser it is.
func version(ctx context.Context, port int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return "", err
	}
	// Loopback only, never through a proxy from the environment.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reading chromium version: %w", err)
	}
	defer resp.Body.Close()
	var v struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("reading chromium version: %w", err)
	}
	return v.Browser, nil
}

// Done is closed when the browser process exits, for example because the
// user closed its window.
func (b *Browser) Done() <-chan struct{} { return b.done }

// Close stops this browser and every process it started, and nothing
// else: it signals only the process group created at launch. It asks
// politely first and kills after a grace period.
func (b *Browser) Close() error {
	select {
	case <-b.done:
		return nil
	default:
	}
	terminate(b.cmd)
	select {
	case <-b.done:
		return nil
	case <-time.After(5 * time.Second):
	}
	kill(b.cmd)
	<-b.done
	return nil
}
