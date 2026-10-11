package chromium

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
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

// Browser is a launched Chromium owned by one recording session. Nothing
// talks to it once it runs: the recording is what crosses the proxy.
type Browser struct {
	Args []string
	// Version is what the executable reports for --version, such as
	// "Chromium 157.0.8089.0"; empty where it cannot say without starting
	// a browser (Windows).
	Version string

	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// startupGrace is how long a launched browser must keep running to count
// as started.
var startupGrace = 2 * time.Second

// Launch starts Chromium on the start URL and returns once it has kept
// running for a moment, or with its output if it exited at once.
func Launch(ctx context.Context, o LaunchOptions) (*Browser, error) {
	if o.Executable == "" || o.ProfileDir == "" || o.ProxyAddr == "" || o.SPKIHash == "" {
		return nil, errors.New("chromium: executable, profile, proxy and SPKI hash are required")
	}
	ver, err := version(ctx, o.Executable)
	if err != nil {
		return nil, err
	}
	b := &Browser{Args: o.Args(), Version: ver, done: make(chan struct{})}
	b.cmd = exec.Command(o.Executable, b.Args...)
	b.cmd.Env = env(os.Environ())
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

	select {
	case <-b.done:
		err = errors.New("chromium exited right after starting")
		if t := strings.TrimSpace(tail.String()); t != "" {
			err = fmt.Errorf("%w; chromium output ends:\n%s", err, t)
		}
		return nil, err
	case <-ctx.Done():
		// Stopped while starting; the caller sees ctx and closes it.
	case <-time.After(startupGrace):
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

// env is the browser's environment. An open-source Chromium without Google
// API keys shows an infobar saying so on every launch; "no" turns it off.
func env(base []string) []string {
	out := append([]string(nil), base...)
	for _, k := range []string{"GOOGLE_API_KEY", "GOOGLE_DEFAULT_CLIENT_ID", "GOOGLE_DEFAULT_CLIENT_SECRET"} {
		set := false
		for _, kv := range base {
			if strings.HasPrefix(kv, k+"=") {
				set = true
				break
			}
		}
		if !set {
			out = append(out, k+"=no")
		}
	}
	return out
}

// version asks the executable which browser it is. On Windows Chromium
// answers --version by starting a browser, so it is not asked there.
func version(ctx context.Context, exe string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 2048 {
			msg = msg[len(msg)-2048:]
		}
		return "", fmt.Errorf("chromium --version: %w; output: %s", err, msg)
	}
	return strings.TrimSpace(string(out)), nil
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
