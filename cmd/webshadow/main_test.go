package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestValidateCheckedInSuite(t *testing.T) {
	code, out, errOut := runCLI("bench", "validate", filepath.Join("..", "..", "benchmarks"))
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "2 scenario(s) checked, 0 error(s), 0 warning(s)") {
		t.Fatalf("unexpected summary: %s", out)
	}
}

func TestValidateReportsErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte("id: broken\nname: Broken\nversion: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("bench", "validate", dir)
	if code != 1 {
		t.Fatalf("want exit 1, got %d\n%s", code, out)
	}
	for _, want := range []string{"error: har: file session.har not found", "error: evaluation.type is required"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"bench"}, {"bench", "nope"}, {"bench", "validate"}} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
	if code, _, errOut := runCLI("bench", "validate", t.TempDir()); code != 1 || !strings.Contains(errOut, "no scenarios found") {
		t.Errorf("empty dir: exit %d, stderr %q", code, errOut)
	}
}

func TestSanitizeAndInspect(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "benchmark", "har", "testdata", "fixture.har")
	out := filepath.Join(t.TempDir(), "clean.har")

	code, stdout, stderr := runCLI("bench", "sanitize", fixture, "--out", out, "--drop", "telemetry", "--strip-bodies", "media", "--trim-initiators")
	if code != 0 {
		t.Fatalf("sanitize exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "9 entries kept, 1 dropped") {
		t.Errorf("unexpected sanitize output: %s", stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Error("sanitized output still holds a password")
	}

	code, stdout, _ = runCLI("bench", "inspect-har", "--names", out)
	if code != 0 || !strings.Contains(stdout, "9 entries") || !strings.Contains(stdout, "header names:") || strings.Contains(stdout, "not sanitized") {
		t.Errorf("inspect-har on clean HAR: exit %d\n%s", code, stdout)
	}
	code, stdout, _ = runCLI("bench", "inspect-har", fixture, "--class", "all")
	if code != 0 || !strings.Contains(stdout, "not sanitized") || !strings.Contains(stdout, "google-analytics.com") {
		t.Errorf("inspect-har on raw fixture: exit %d\n%s", code, stdout)
	}

	for _, args := range [][]string{
		{"bench", "sanitize", fixture},
		{"bench", "sanitize", fixture, "--out", out, "--drop", "pictures"},
		{"bench", "inspect-har"},
		{"bench", "inspect-har", fixture, "--class", "nope"},
	} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
}
