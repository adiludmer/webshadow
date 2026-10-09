package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/cluster/live"
	"github.com/adiludmer/webshadow/internal/recording"
)

// syncBuffer is written by the follow loop and read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFollowWatchesARecordingUntilItCompletes(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "recordings")
	store, err := recording.Create(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- runFollow(context.Background(), followConfig{
			dir: store.Dir(), interval: 10 * time.Millisecond, root: root,
			size: live.Size{Width: 120, Height: 40},
		}, &stdout, &stderr)
	}()

	waitFor := func(what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(stdout.String(), what) {
			if time.Now().After(deadline) {
				t.Fatalf("never saw %q in:\n%s", what, stdout.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	add := func(i int, path string) {
		ex := &recording.HTTPExchange{
			Request:  recording.Request{Method: "GET", Scheme: "https", Host: "shop.test", URL: "https://shop.test" + path},
			Response: &recording.Response{Status: 200},
			Timing:   recording.Timing{RequestStart: int64(i) * 1e8, ResponseEnd: int64(i)*1e8 + 5e7},
		}
		if err := store.AppendExchange(ex); err != nil {
			t.Fatal(err)
		}
		if err := store.Flush(); err != nil {
			t.Fatal(err)
		}
	}

	add(1, "/dp/B0FV975MJK")
	waitFor("GET shop.test/dp/B0FV975MJK")
	add(2, "/dp/B0CXLM2QZ4")
	waitFor("GET shop.test/dp/B0CXLM2QZ4")
	add(3, "/dp/B0D7HN3RT1")
	// The third id generalizes the path, and the view says so.
	waitFor("GET shop.test/dp/{slot_1} regrouped 2 families")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit %d: %s", c, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop when the recording completed")
	}
	out := stdout.String()
	if !strings.Contains(out, "complete") || !strings.Contains(out, "Clustered "+store.ID()+" at 3 exchanges") {
		t.Errorf("final output:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("a non-terminal got escape codes")
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "clusters", "cl_*", "manifest.json"))
	if len(dirs) != 1 {
		t.Errorf("manifests written: %v", dirs)
	}

	// The followed result is the one a plain run gives.
	data, err := os.ReadFile(filepath.Join(filepath.Dir(dirs[0]), "families.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := recording.Load(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := cluster.Run([]*recording.Recording{r}, cluster.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	if err := cluster.Write(fresh, res, false); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(filepath.Join(fresh, "families.json"))
	if !bytes.Equal(data, want) {
		t.Error("the followed families differ from a plain run")
	}
}

func TestFollowNeedsOneRecording(t *testing.T) {
	t.Setenv(envHome, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"cluster", "-follow", "a", "b"}, &stdout, &stderr); code != 2 {
		t.Errorf("two recordings exited %d", code)
	}
}
