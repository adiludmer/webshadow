package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestAnswer(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(models, []byte(`models:
  - id: scripted
    adapter: fake
    replies:
      - '{"action": "search", "args": {"query": "enso"}}'
      - '{"action": "finish", "answer": {"amount": 15000000, "currency": "USD"}}'
      - '{"action": "finish", "answer": {"amount": 1, "currency": "USD"}}'
`), 0o644); err != nil {
		t.Fatal(err)
	}
	now = func() time.Time { return time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	scenarioDir := filepath.Join("..", "..", "benchmarks", "geektime-enso-funding")
	tree := filepath.Join("..", "..", "internal", "benchmark", "reader", "testdata", "enso-tree")
	runs := filepath.Join(dir, "runs")
	code, stdout, stderr := runCLI("bench", "answer", scenarioDir, "--tree", tree, "--reader", "scripted", "--models", models, "--runs", runs, "--repetitions", "2")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"run 20261004T160000Z: geektime-enso-funding with reader scripted", "(3 files, sha256:", "repetition 1: success in 2 steps", "repetition 2: wrong_answer in 1 steps"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	rec := filepath.Join(runs, "20261004T160000Z", "cases", "geektime-enso-funding", "external", "scripted", "repetition-01.json")
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status": "success"`, `"reader_prompt": "reader-v1"`, `"tree_digest": "sha256:`, `"action": "search"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record lacks %s", want)
		}
	}

	for _, args := range [][]string{
		{"bench", "answer", scenarioDir},
		{"bench", "answer", scenarioDir, "--tree", tree},
		{"bench", "answer", "--tree", tree, "--reader", "scripted"},
		{"bench", "answer", scenarioDir, "--tree", tree, "--reader", "scripted", "--repetitions", "0"},
	} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
	for args, want := range map[string]string{
		"--reader nobody":  `model "nobody" is not in the registry`,
		"--tree /nonexist": "opening tree",
	} {
		full := append([]string{"bench", "answer", scenarioDir, "--tree", tree, "--reader", "scripted", "--models", models, "--runs", runs}, strings.Fields(args)...)
		if code, _, stderr := runCLI(full...); code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("%s: exit %d, stderr %q", args, code, stderr)
		}
	}
}
