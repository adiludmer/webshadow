package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adiludmer/webshadow/internal/chromium"
	"github.com/adiludmer/webshadow/internal/mitm"
	"github.com/adiludmer/webshadow/internal/recording"
)

const defaultProxyAddr = "127.0.0.1:8080"

// EnvHome overrides where webshadow keeps its CA, browser runtime,
// profiles and recordings.
const envHome = "WEBSHADOW_HOME"

// webshadowHome returns ~/.webshadow unless WEBSHADOW_HOME says otherwise.
func webshadowHome() (string, error) {
	if h := os.Getenv(envHome); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".webshadow"), nil
}

type browserConfig struct {
	home        string
	proxyAddr   string
	fallback    bool // a busy proxyAddr falls back to a free port
	chromium    string
	headless    bool
	keepProfile bool
	resetCA     bool
	startURL    string
}

func runBrowser(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("browser", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow browser [flags] [url]\n\nLaunches an isolated Chromium on url through the recording proxy and\nrecords every request and response until you press Ctrl-C or close the\nbrowser. Nothing is read from the browser itself, so the recording holds\nwhat a server-side recorder would see.\n\nFlags:")
		fs.PrintDefaults()
	}
	var cfg browserConfig
	fs.StringVar(&cfg.proxyAddr, "proxy", defaultProxyAddr, "proxy listen address; the default falls back to a free port when taken")
	fs.StringVar(&cfg.chromium, "chromium", "", "Chromium executable to use instead of the bundled one (also "+chromium.EnvExecutable+")")
	fs.BoolVar(&cfg.headless, "headless", false, "run Chromium without a window")
	fs.BoolVar(&cfg.keepProfile, "keep-profile", false, "keep the session's browser profile after it stops")
	fs.BoolVar(&cfg.resetCA, "reset-ca", false, "replace the local Webshadow CA before starting")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	cfg.startURL = fs.Arg(0)
	cfg.fallback = true
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "proxy" {
			cfg.fallback = false
		}
	})
	home, err := webshadowHome()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow browser: %v\n", err)
		return 1
	}
	cfg.home = home

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := recordSession(ctx, cfg, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "webshadow browser: %v\n", err)
		return 1
	}
	return 0
}

// recordSession runs one recording until ctx is done or the browser exits.
func recordSession(ctx context.Context, cfg browserConfig, stdout, stderr io.Writer) error {
	caDir := filepath.Join(cfg.home, "ca")
	if cfg.resetCA {
		if err := mitm.ResetCA(caDir); err != nil {
			return err
		}
	}
	ca, err := mitm.LoadOrCreateCA(caDir)
	if err != nil {
		return fmt.Errorf("local CA: %w", err)
	}
	rt, err := chromium.Locate(filepath.Join(cfg.home, "runtime", "chromium"), cfg.chromium)
	if err != nil {
		return err
	}
	ln, err := mitm.Listen(cfg.proxyAddr, cfg.fallback)
	if err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	store, err := recording.Create(filepath.Join(cfg.home, "recordings"))
	if err != nil {
		ln.Close()
		return err
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(stderr, "webshadow: "+format+"\n", args...)
	}
	proxy := &mitm.Proxy{CA: ca, Store: store, Logf: logf}
	go proxy.Serve(ln)

	addr := ln.Addr().String()
	// abort drops a session that never got a browser: nothing was recorded.
	abort := func(err error) error {
		proxy.Close()
		store.Close()
		recording.Delete(filepath.Dir(store.Dir()), store.ID())
		return err
	}

	profile, err := chromium.NewProfile(filepath.Join(cfg.home, "profiles"), store.ID())
	if err != nil {
		return abort(err)
	}
	if !cfg.keepProfile {
		defer chromium.RemoveProfile(profile)
	}
	log, err := os.OpenFile(filepath.Join(store.Dir(), "chromium.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return abort(err)
	}
	defer log.Close()
	b, err := chromium.Launch(ctx, chromium.LaunchOptions{
		Executable: rt.Executable,
		ProfileDir: profile,
		ProxyAddr:  addr,
		SPKIHash:   ca.LeafSPKIHash(),
		Headless:   cfg.headless,
		StartURL:   cfg.startURL,
		Log:        log,
	})
	if err != nil {
		log.Close()
		return abort(err)
	}
	return runRecording(ctx, cfg, rt, b, proxy, store, addr, profile, stdout)
}

// runRecording runs a launched session to its end and stops everything it owns.
func runRecording(ctx context.Context, cfg browserConfig, rt *chromium.Runtime, b *chromium.Browser, proxy *mitm.Proxy, store *recording.Store, addr, profile string, stdout io.Writer) error {
	uerr := store.UpdateSession(func(s *recording.Session) {
		s.Proxy = &recording.ProxyInfo{Addr: addr, CAFingerprint: proxy.CA.Fingerprint()}
		s.Browser = &recording.BrowserInfo{Version: b.Version, Executable: rt.Executable, Args: b.Args, Profile: profile}
	})
	fmt.Fprintf(stdout, "Webshadow recorder started\n")
	fmt.Fprintf(stdout, "Proxy: %s\n", addr)
	fmt.Fprintf(stdout, "Browser: %s (%s)\n", browserName(b.Version), rt.Source)
	fmt.Fprintf(stdout, "Session: %s\n", store.ID())
	fmt.Fprintf(stdout, "Recording: %s\n", store.Dir())
	fmt.Fprintf(stdout, "Press Ctrl-C or close the browser to stop.\n")

	// Flushed each second, so `cluster -follow` sees the session as it grows.
	flushDone := make(chan struct{})
	stopFlush := make(chan struct{})
	go func() {
		defer close(flushDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopFlush:
				return
			case <-t.C:
				store.Flush()
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-b.Done():
	}
	close(stopFlush)
	<-flushDone
	berr := b.Close()
	perr := proxy.Close()
	serr := store.Close()
	s := store.Session()
	fmt.Fprintf(stdout, "Recording complete: %d HTTP exchanges in %s\n", s.Exchanges, store.Dir())
	if cfg.keepProfile {
		fmt.Fprintf(stdout, "Browser profile kept: %s\n", profile)
	}
	return errors.Join(uerr, berr, ignoreClosed(perr), serr)
}

// browserName is the version line Chromium printed, or a plain name when
// it printed none.
func browserName(version string) string {
	if version == "" {
		return "Chromium"
	}
	return version
}

func ignoreClosed(err error) error {
	if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
		return nil
	}
	return err
}
