package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifest = `id: tiny
name: Tiny scenario
version: 1
evaluation:
  type: fields
  required_fields: [product_id, price]
  normalizers:
    price:
      numeric_tolerance: 0.01
limits:
  generate:
    max_steps: 50
    timeout_seconds: 120
  answer:
    max_steps: 10
    timeout_seconds: 30
`

const validHAR = `{"log": {"version": "1.2", "entries": [{"request": {"method": "GET", "url": "https://example.test/"}}]}}`

// writeScenario creates a valid scenario in a temp dir, then applies
// overrides: a file name mapped to new contents, or to "" to delete it.
func writeScenario(t *testing.T, overrides map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"scenario.yaml": validManifest,
		"goal.md":       "# Goal\n\nFind the product.\n",
		"expected.json": `{"product_id": "123", "price": 1299}`,
		"session.har":   validHAR,
	}
	for name, content := range overrides {
		files[name] = content
	}
	for name, content := range files {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func validate(t *testing.T, dir string) Result {
	t.Helper()
	return ValidateDirs([]string{dir}, Options{})
}

func TestValidScenarioHasNoIssues(t *testing.T) {
	res := validate(t, writeScenario(t, nil))
	if len(res.Issues) != 0 {
		t.Fatalf("want no issues, got %v", res.Issues)
	}
	if len(res.Scenarios) != 1 {
		t.Fatalf("want 1 scenario, got %d", len(res.Scenarios))
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := writeScenario(t, map[string]string{
		"scenario.yaml": "id: tiny\nname: Tiny\nversion: 1\nevaluation:\n  type: fields\n  required_fields: [price]\n",
	})
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.HAR != DefaultHAR || s.Goal != DefaultGoal || s.Expected != DefaultExpected {
		t.Errorf("file defaults not applied: %+v", s)
	}
	if s.Limits.Generate.MaxSteps != DefaultGenerateMaxSteps || s.Limits.Answer.TimeoutSeconds != DefaultAnswerTimeoutSeconds {
		t.Errorf("limit defaults not applied: %+v", s.Limits)
	}
	if !s.Evaluation.AllowsExtraFields() {
		t.Errorf("allow_extra_fields should default to true")
	}
}

func TestValidationErrors(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(validManifest, old, new, 1) }
	tests := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"missing manifest", map[string]string{"scenario.yaml": ""}, "no such file"},
		{"malformed yaml", map[string]string{"scenario.yaml": "id: [unclosed"}, "scenario.yaml"},
		{"unknown manifest key", map[string]string{"scenario.yaml": validManifest + "model: gpt\n"}, "field model not found"},
		{"empty manifest", map[string]string{"scenario.yaml": "# nothing\n"}, "empty manifest"},
		{"missing id", map[string]string{"scenario.yaml": replace("id: tiny\n", "")}, "id is required"},
		{"bad id", map[string]string{"scenario.yaml": replace("id: tiny", "id: Tiny Scenario")}, "must be lowercase"},
		{"missing name", map[string]string{"scenario.yaml": replace("name: Tiny scenario\n", "")}, "name is required"},
		{"bad version", map[string]string{"scenario.yaml": replace("version: 1", "version: 0")}, "version must be 1"},
		{"missing har", map[string]string{"session.har": ""}, "har: file session.har not found"},
		{"har escapes dir", map[string]string{"scenario.yaml": validManifest + "har: ../outside.har\n"}, "inside the scenario directory"},
		{"har not json", map[string]string{"session.har": "not json"}, "invalid JSON"},
		{"har without entries", map[string]string{"session.har": `{"log": {}}`}, "missing log.entries"},
		{"unsanitized har", map[string]string{"session.har": `{"log": {"entries": [{"request": {"url": "https://example.test/", "headers": [{"name": "Cookie", "value": "sid=1"}]}}]}}`}, "is not sanitized"},
		{"missing goal", map[string]string{"goal.md": ""}, "goal: file goal.md not found"},
		{"empty goal", map[string]string{"goal.md": "  \n\n"}, "goal.md is empty"},
		{"missing expected", map[string]string{"expected.json": ""}, "expected: file expected.json not found"},
		{"malformed expected", map[string]string{"expected.json": `{"price": `}, "must be a JSON object"},
		{"expected is array", map[string]string{"expected.json": `[1, 2]`}, "must be a JSON object"},
		{"expected is null", map[string]string{"expected.json": `null`}, "not null"},
		{"missing scorer", map[string]string{"scenario.yaml": replace("  type: fields\n", "")}, "evaluation.type is required"},
		{"unknown scorer", map[string]string{"scenario.yaml": replace("type: fields", "type: llm_judge")}, `"llm_judge" is not a known scorer`},
		{"no required fields", map[string]string{"scenario.yaml": replace("[product_id, price]", "[]")}, "at least one field"},
		{"duplicate required field", map[string]string{"scenario.yaml": replace("[product_id, price]", "[price, price]")}, `lists "price" twice`},
		{"required field not expected", map[string]string{"scenario.yaml": replace("[product_id, price]", "[product_id, price, sku]")}, `"sku" is missing from expected.json`},
		{"normalizer for unknown field", map[string]string{"scenario.yaml": replace("    price:\n", "    sku:\n")}, `"sku" is not a required field`},
		{"bad case normalizer", map[string]string{"scenario.yaml": replace("numeric_tolerance: 0.01", "case: title")}, "case must be lower or upper"},
		{"bad whitespace normalizer", map[string]string{"scenario.yaml": replace("numeric_tolerance: 0.01", "whitespace: squash")}, "whitespace must be trim or collapse"},
		{"negative tolerance", map[string]string{"scenario.yaml": replace("numeric_tolerance: 0.01", "numeric_tolerance: -1")}, "must not be negative"},
		{"negative max steps", map[string]string{"scenario.yaml": replace("max_steps: 50", "max_steps: -5")}, "limits.generate.max_steps"},
		{"negative timeout", map[string]string{"scenario.yaml": replace("timeout_seconds: 30", "timeout_seconds: -1")}, "limits.answer.timeout_seconds"},
		{"negative token budget", map[string]string{"scenario.yaml": replace("    max_steps: 10\n", "    max_steps: 10\n    max_tokens: -1\n")}, "limits.answer.max_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validate(t, writeScenario(t, tt.overrides))
			if !res.HasErrors() {
				t.Fatalf("want an error containing %q, got none (issues: %v)", tt.want, res.Issues)
			}
			for _, issue := range res.Issues {
				if issue.Severity == Error && strings.Contains(issue.Message, tt.want) {
					return
				}
			}
			t.Fatalf("want an error containing %q, got %v", tt.want, res.Issues)
		})
	}
}

func TestEmptyHARIsAWarning(t *testing.T) {
	res := validate(t, writeScenario(t, map[string]string{"session.har": `{"log": {"entries": []}}`}))
	if res.HasErrors() {
		t.Fatalf("want no errors, got %v", res.Issues)
	}
	if len(res.Issues) != 1 || res.Issues[0].Severity != Warning {
		t.Fatalf("want one warning, got %v", res.Issues)
	}
}

func TestCustomScorerRegistry(t *testing.T) {
	dir := writeScenario(t, nil)
	res := ValidateDirs([]string{dir}, Options{Scorers: []string{"sets"}})
	if !res.HasErrors() {
		t.Fatal("fields should be rejected when only sets is registered")
	}
}

func TestDuplicateIDsAcrossSuite(t *testing.T) {
	a, b := writeScenario(t, nil), writeScenario(t, nil)
	res := ValidateDirs([]string{a, b}, Options{})
	if !res.HasErrors() {
		t.Fatal("want a duplicate id error")
	}
	last := res.Issues[len(res.Issues)-1]
	if last.Dir != b || !strings.Contains(last.Message, `duplicate id "tiny"`) {
		t.Fatalf("want duplicate id error on second dir, got %v", res.Issues)
	}
}

func TestDiscover(t *testing.T) {
	suite := t.TempDir()
	for _, name := range []string{"b-case", "a-case"} {
		dir := filepath.Join(suite, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(validManifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(suite, "not-a-scenario"), 0o755); err != nil {
		t.Fatal(err)
	}

	dirs, err := Discover(suite)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(suite, "a-case"), filepath.Join(suite, "b-case")}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("Discover(suite) = %v, want %v", dirs, want)
	}

	single, err := Discover(want[0])
	if err != nil || len(single) != 1 || single[0] != want[0] {
		t.Fatalf("Discover(scenario) = %v, %v", single, err)
	}

	if _, err := Discover(t.TempDir()); err == nil {
		t.Fatal("want an error for a directory with no scenarios")
	}
}

func TestCheckedInSuiteIsValid(t *testing.T) {
	dirs, err := Discover(filepath.Join("..", "..", "..", "benchmarks"))
	if err != nil {
		t.Fatal(err)
	}
	res := ValidateDirs(dirs, Options{})
	if len(res.Issues) != 0 {
		t.Fatalf("checked-in suite has issues: %v", res.Issues)
	}
}

func TestGenerateOnlyScenario(t *testing.T) {
	manifest := "id: browse\nname: Browse only\nversion: 1\nevaluation:\n  type: none\n"
	res := validate(t, writeScenario(t, map[string]string{"scenario.yaml": manifest, "goal.md": "", "expected.json": ""}))
	if len(res.Issues) != 0 {
		t.Fatalf("want no issues, got %v", res.Issues)
	}
	if res.Scenarios[0].Answerable() {
		t.Error("generate-only scenario reports itself answerable")
	}

	res = validate(t, writeScenario(t, map[string]string{"scenario.yaml": manifest + "  required_fields: [price]\n"}))
	if len(res.Issues) != 1 || !strings.Contains(res.Issues[0].Message, "type none takes no other settings") {
		t.Fatalf("issues: %v", res.Issues)
	}
}
