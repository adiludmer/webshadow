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
	if !strings.Contains(out, "1 scenario(s) checked, 0 error(s), 0 warning(s)") {
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
