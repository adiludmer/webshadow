package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/recording"
)

func TestClusterWritesEveryOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv(envHome, home)
	store, err := recording.Create(filepath.Join(home, "recordings"))
	if err != nil {
		t.Fatal(err)
	}
	for i, url := range []string{"https://shop.test/", "https://shop.test/dp/B0FV975MJK", "https://shop.test/dp/B0CXLM2QZ4"} {
		ex := &recording.HTTPExchange{
			Request:  recording.Request{Method: "GET", Scheme: "https", Host: "shop.test", URL: url},
			Response: &recording.Response{Status: 200},
			Timing:   recording.Timing{RequestStart: int64(i+1) * 1e8, ResponseEnd: int64(i+1)*1e8 + 5e7},
		}
		if err := store.AppendExchange(ex); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"cluster", "-yaml", store.ID()}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Clustered 1 recordings, 3 exchanges") {
		t.Errorf("stdout = %q", stdout.String())
	}
	dirs, err := filepath.Glob(filepath.Join(home, "recordings", "clusters", "cl_*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("output directories = %v, %v", dirs, err)
	}
	for _, name := range append(cluster.Files, "evidence.yaml") {
		if _, err := os.Stat(filepath.Join(dirs[0], name)); err != nil {
			t.Error(err)
		}
	}

	if code := run([]string{"cluster", "rec_missing"}, &stdout, &stderr); code != 1 {
		t.Errorf("a missing recording exited %d", code)
	}
	if code := run([]string{"cluster"}, &stdout, &stderr); code != 2 {
		t.Errorf("no arguments exited %d", code)
	}
}
