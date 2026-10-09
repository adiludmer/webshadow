package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/cluster/live"
	"github.com/adiludmer/webshadow/internal/recording"
)

// followConfig is how `cluster -follow` watches a recording.
type followConfig struct {
	dir      string
	interval time.Duration
	out      string
	root     string
	withYAML bool
	// terminal redraws in place with colors; otherwise each changed frame is
	// printed after the last.
	terminal bool
	size     live.Size
}

// runFollow re-clusters a recording each interval while it grows and
// redraws the view, until the recording completes or ctx ends. The last
// result is then written out as a normal cluster run would.
func runFollow(ctx context.Context, cfg followConfig, stdout, stderr io.Writer) int {
	runner := cluster.NewRunner(cluster.DefaultOptions())
	view := &live.View{Color: cfg.terminal}
	var res *cluster.Result
	var rec *recording.Recording
	lastExchanges, lastEvents := -1, -1
	tick := time.NewTicker(cfg.interval)
	defer tick.Stop()
	for {
		r, err := recording.Load(cfg.dir)
		if err != nil {
			fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
			return 1
		}
		done := r.Session.Status != recording.StatusRecording
		if len(r.Exchanges) != lastExchanges || len(r.Events) != lastEvents || done {
			rec = r
			res, err = runner.Run([]*recording.Recording{r})
			if err != nil {
				fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
				return 1
			}
			lastExchanges, lastEvents = len(r.Exchanges), len(r.Events)
			draw(view, res, r, done, cfg, stdout)
		} else if cfg.terminal {
			// Keep the elapsed time moving.
			draw(view, res, r, done, cfg, stdout)
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			done = true
		case <-tick.C:
		}
		if done {
			break
		}
	}
	dir := cfg.out
	if dir == "" {
		dir = filepath.Join(cfg.root, "clusters", res.Manifest.ID)
	}
	if err := cluster.Write(dir, res, cfg.withYAML); err != nil {
		fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nClustered %s at %d exchanges\nOutput: %s\n", rec.Session.ID, len(rec.Exchanges), dir)
	return 0
}

// draw renders one frame. A view that is not redrawn in place shows its
// next frame only when the recording changed, so the frame repeats nothing.
func draw(view *live.View, res *cluster.Result, r *recording.Recording, done bool, cfg followConfig, stdout io.Writer) {
	st := live.Status{Session: r.Session.ID, Recording: !done, Elapsed: elapsed(r, done)}
	lines := view.Frame(res, st, cfg.size)
	var b strings.Builder
	if cfg.terminal {
		b.WriteString("\x1b[H\x1b[2J")
	} else {
		b.WriteString("\n")
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	io.WriteString(stdout, b.String())
}

func elapsed(r *recording.Recording, done bool) time.Duration {
	if done && r.Session.EndedAt != nil {
		return r.Session.EndedAt.Sub(r.Session.StartedAt)
	}
	if r.Session.StartedAt.IsZero() {
		return 0
	}
	return time.Since(r.Session.StartedAt)
}

// terminalSize reads the size from COLUMNS and LINES, which shells export,
// and falls back to 120 by 40.
func terminalSize() live.Size {
	size := live.Size{Width: 120, Height: 40}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		size.Width = n
	}
	if n, err := strconv.Atoi(os.Getenv("LINES")); err == nil && n > 0 {
		size.Height = n
	}
	return size
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func followContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
