package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/har"
)

// Severity tells whether an issue blocks a run.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Issue is one validation finding.
type Issue struct {
	Dir      string
	Severity Severity
	Message  string
}

func (i Issue) String() string {
	return fmt.Sprintf("%s: %s: %s", i.Dir, i.Severity, i.Message)
}

// Options configures validation.
type Options struct {
	// Scorers lists the evaluation types that are registered. Empty means
	// DefaultScorers.
	Scorers []string
}

// DefaultScorers are the evaluation types the benchmark ships with.
var DefaultScorers = []string{"fields"}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Result is the outcome of validating a set of scenario directories.
type Result struct {
	Scenarios []*Scenario // successfully loaded scenarios, in input order
	Issues    []Issue
}

// HasErrors reports whether any issue is an error.
func (r Result) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Severity == Error {
			return true
		}
	}
	return false
}

// ValidateDirs loads and validates every directory, and checks that
// scenario IDs are unique across them.
func ValidateDirs(dirs []string, opts Options) Result {
	var res Result
	seen := map[string]string{}
	for _, dir := range dirs {
		s, err := Load(dir)
		if err != nil {
			res.Issues = append(res.Issues, Issue{dir, Error, err.Error()})
			continue
		}
		res.Issues = append(res.Issues, Validate(s, opts)...)
		if s.ID != "" {
			if prev, ok := seen[s.ID]; ok {
				res.Issues = append(res.Issues, Issue{dir, Error, fmt.Sprintf("duplicate id %q (also used by %s)", s.ID, prev)})
			} else {
				seen[s.ID] = dir
			}
		}
		res.Scenarios = append(res.Scenarios, s)
	}
	return res
}

// Validate checks one loaded scenario: manifest fields, referenced files,
// expected.json against the evaluation contract, and limits.
func Validate(s *Scenario, opts Options) []Issue {
	v := validator{dir: s.Dir}

	switch {
	case s.ID == "":
		v.errorf("id is required")
	case !idPattern.MatchString(s.ID):
		v.errorf("id %q must be lowercase letters, digits, '.', '_' or '-', starting with a letter or digit", s.ID)
	}
	if strings.TrimSpace(s.Name) == "" {
		v.errorf("name is required")
	}
	if s.Version < 1 {
		v.errorf("version must be 1 or greater")
	}

	if path, ok := v.file("har", s.HAR); ok {
		v.checkHAR(path)
	}
	if path, ok := v.file("goal", s.Goal); ok {
		if data, err := os.ReadFile(path); err != nil {
			v.errorf("goal: %v", err)
		} else if strings.TrimSpace(string(data)) == "" {
			v.errorf("goal: %s is empty", s.Goal)
		}
	}
	var expected map[string]any
	if path, ok := v.file("expected", s.Expected); ok {
		expected = v.readExpected(path)
	}

	v.checkEvaluation(s.Evaluation, expected, opts)
	v.checkStage("limits.generate", s.Limits.Generate)
	v.checkStage("limits.answer", s.Limits.Answer)
	return v.issues
}

type validator struct {
	dir    string
	issues []Issue
}

func (v *validator) errorf(format string, args ...any) {
	v.issues = append(v.issues, Issue{v.dir, Error, fmt.Sprintf(format, args...)})
}

func (v *validator) warnf(format string, args ...any) {
	v.issues = append(v.issues, Issue{v.dir, Warning, fmt.Sprintf(format, args...)})
}

// file checks that a manifest file reference stays inside the scenario
// directory and exists, and returns its full path.
func (v *validator) file(key, name string) (string, bool) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		v.errorf("%s: %q must be a path inside the scenario directory", key, name)
		return "", false
	}
	path := filepath.Join(v.dir, clean)
	if !fileExists(path) {
		v.errorf("%s: file %s not found", key, name)
		return "", false
	}
	return path, true
}

// checkHAR parses the HAR and refuses it if credential material remains,
// so the corpus only ever holds sanitized captures.
func (v *validator) checkHAR(path string) {
	f, err := os.Open(path)
	if err != nil {
		v.errorf("har: %v", err)
		return
	}
	defer f.Close()
	trace, err := har.Parse(f)
	if err != nil {
		v.errorf("har: %s: %v", filepath.Base(path), err)
		return
	}
	if len(trace.Entries) == 0 {
		v.warnf("har: %s has no entries", filepath.Base(path))
	}
	if found := har.Unsanitized(trace); len(found) > 0 {
		v.errorf("har: %s is not sanitized (%s%s); run webshadow bench sanitize", filepath.Base(path), found[0], more(len(found)-1))
	}
}

func more(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" and %d more", n)
}

func (v *validator) readExpected(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		v.errorf("expected: %v", err)
		return nil
	}
	var expected map[string]any
	if err := json.Unmarshal(data, &expected); err != nil {
		v.errorf("expected: %s must be a JSON object: %v", filepath.Base(path), err)
		return nil
	}
	if expected == nil {
		v.errorf("expected: %s must be a JSON object, not null", filepath.Base(path))
	}
	return expected
}

func (v *validator) checkEvaluation(e Evaluation, expected map[string]any, opts Options) {
	scorers := opts.Scorers
	if len(scorers) == 0 {
		scorers = DefaultScorers
	}
	switch {
	case e.Type == "":
		v.errorf("evaluation.type is required (one of %s)", strings.Join(scorers, ", "))
	case !slices.Contains(scorers, e.Type):
		v.errorf("evaluation.type %q is not a known scorer (one of %s)", e.Type, strings.Join(scorers, ", "))
	}

	if len(e.RequiredFields) == 0 {
		v.errorf("evaluation.required_fields must list at least one field")
	}
	seen := map[string]bool{}
	for _, f := range e.RequiredFields {
		switch {
		case strings.TrimSpace(f) == "":
			v.errorf("evaluation.required_fields contains an empty name")
		case seen[f]:
			v.errorf("evaluation.required_fields lists %q twice", f)
		case expected != nil:
			if _, ok := expected[f]; !ok {
				v.errorf("evaluation.required_fields: %q is missing from expected.json", f)
			}
		}
		seen[f] = true
	}

	names := make([]string, 0, len(e.Normalizers))
	for name := range e.Normalizers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			v.errorf("evaluation.normalizers: %q is not a required field", name)
		}
		v.checkNormalizer(name, e.Normalizers[name])
	}
}

func (v *validator) checkNormalizer(field string, n Normalizer) {
	prefix := "evaluation.normalizers." + field
	if n.Case != "" && n.Case != "lower" && n.Case != "upper" {
		v.errorf("%s.case must be lower or upper, not %q", prefix, n.Case)
	}
	if n.Whitespace != "" && n.Whitespace != "trim" && n.Whitespace != "collapse" {
		v.errorf("%s.whitespace must be trim or collapse, not %q", prefix, n.Whitespace)
	}
	if n.NumericTolerance != nil && *n.NumericTolerance < 0 {
		v.errorf("%s.numeric_tolerance must not be negative", prefix)
	}
}

func (v *validator) checkStage(key string, l StageLimits) {
	if l.MaxSteps < 1 {
		v.errorf("%s.max_steps must be 1 or greater", key)
	}
	if l.TimeoutSeconds < 1 {
		v.errorf("%s.timeout_seconds must be 1 or greater", key)
	}
	if l.MaxTokens < 0 {
		v.errorf("%s.max_tokens must not be negative", key)
	}
}
