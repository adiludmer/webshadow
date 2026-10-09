package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

const semanticFixture = "../../internal/semantic/testdata/amazon-search"

func TestAnalyzeAndInspect(t *testing.T) {
	t.Setenv(envHome, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"analyze", semanticFixture}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"Analysed cl_a6df5fb6df42: 294 families (151 static)", "Revision: r0001 (new)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("missing %q in:\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if code := run([]string{"analyze", semanticFixture}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Revision: r0001 (unchanged)") {
		t.Errorf("second run:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"ir", "inspect"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Revision r0001, schema "+ir.SchemaVersion) {
		t.Errorf("inspect:\n%s", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"ir", "inspect", "-json", "-revision", "r0001"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var x ir.InterfaceIR
	if err := json.Unmarshal(stdout.Bytes(), &x); err != nil || x.Revision != "r0001" {
		t.Errorf("inspect -json: %v, %+v", err, x)
	}

	if code := run([]string{"ir", "inspect", "-revision", "../x"}, &stdout, &stderr); code != 2 {
		t.Errorf("a bad revision name exited %d", code)
	}
	if code := run([]string{"analyze", "cl_missing"}, &stdout, &stderr); code != 1 {
		t.Errorf("a missing cluster exited %d", code)
	}
}

func TestAnalyzeRedact(t *testing.T) {
	t.Setenv(envHome, t.TempDir())
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"analyze", "redact", "-gz", semanticFixture, out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	// The fixture is already redacted, so there is nothing left to replace.
	if !strings.Contains(stdout.String(), "in 0 strings") {
		t.Errorf("stdout:\n%s", stdout.String())
	}
	if code := run([]string{"analyze", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("analysing the redacted copy exited %d: %s", code, stderr.String())
	}
}
