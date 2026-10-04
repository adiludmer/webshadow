// Package scenario loads and validates benchmark scenarios.
//
// A scenario is a directory holding scenario.yaml (benchmark mechanics),
// goal.md (the natural-language task), expected.json (the structured answer)
// and a sanitized HAR. The manifest never carries model-specific settings.
package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ManifestName is the file that marks a directory as a scenario.
const ManifestName = "scenario.yaml"

// Default file names, used when the manifest leaves them out.
const (
	DefaultHAR      = "session.har"
	DefaultGoal     = "goal.md"
	DefaultExpected = "expected.json"
)

// Default limits, applied when the manifest leaves a value out.
const (
	DefaultGenerateMaxSteps       = 200
	DefaultGenerateTimeoutSeconds = 600
	DefaultAnswerMaxSteps         = 20
	DefaultAnswerTimeoutSeconds   = 90
)

// Scenario is a parsed scenario.yaml plus the directory it was loaded from.
type Scenario struct {
	ID         string     `yaml:"id"`
	Name       string     `yaml:"name"`
	Version    int        `yaml:"version"`
	Tags       []string   `yaml:"tags"`
	HAR        string     `yaml:"har"`
	Goal       string     `yaml:"goal"`
	Expected   string     `yaml:"expected"`
	Evaluation Evaluation `yaml:"evaluation"`
	Limits     Limits     `yaml:"limits"`

	// Dir is the scenario directory. It is set by Load, not by the manifest.
	Dir string `yaml:"-"`
}

// Evaluation selects the scorer and configures it.
type Evaluation struct {
	Type             string                `yaml:"type"`
	RequiredFields   []string              `yaml:"required_fields"`
	AllowExtraFields *bool                 `yaml:"allow_extra_fields"`
	Normalizers      map[string]Normalizer `yaml:"normalizers"`
}

// Normalizer describes how one expected field is compared with an answer.
// Every option is off by default, which means exact comparison.
type Normalizer struct {
	// Case folds strings before comparing: "lower" or "upper".
	Case string `yaml:"case"`
	// Whitespace trims ("trim") or also collapses inner runs ("collapse").
	Whitespace string `yaml:"whitespace"`
	// NumericTolerance accepts numbers within this absolute difference.
	NumericTolerance *float64 `yaml:"numeric_tolerance"`
	// Date parses both sides as dates in this Go layout and compares the day.
	Date string `yaml:"date"`
	// URL compares URLs after normalizing scheme, host case and trailing slash.
	URL bool `yaml:"url"`
	// Currency strips currency symbols and thousands separators from numbers.
	Currency bool `yaml:"currency"`
	// Unordered compares arrays as multisets instead of sequences.
	Unordered bool `yaml:"unordered"`
}

// Limits bounds each agent stage of a run.
type Limits struct {
	Generate StageLimits `yaml:"generate"`
	Answer   StageLimits `yaml:"answer"`
}

// StageLimits bounds one agent. MaxTokens is optional; zero means no budget.
type StageLimits struct {
	MaxSteps       int `yaml:"max_steps"`
	TimeoutSeconds int `yaml:"timeout_seconds"`
	MaxTokens      int `yaml:"max_tokens"`
}

// AllowsExtraFields reports whether answers may carry fields beyond the
// required ones. It defaults to true, as in the spec's example manifest.
func (e Evaluation) AllowsExtraFields() bool {
	return e.AllowExtraFields == nil || *e.AllowExtraFields
}

// HARPath, GoalPath and ExpectedPath join the manifest's file names onto
// the scenario directory.
func (s *Scenario) HARPath() string      { return filepath.Join(s.Dir, s.HAR) }
func (s *Scenario) GoalPath() string     { return filepath.Join(s.Dir, s.Goal) }
func (s *Scenario) ExpectedPath() string { return filepath.Join(s.Dir, s.Expected) }

// Load reads dir/scenario.yaml and fills in defaults. It rejects unknown
// manifest keys but does not otherwise validate; call Validate for that.
func Load(dir string) (*Scenario, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty manifest", ManifestName)
		}
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	s.Dir = dir
	s.applyDefaults()
	return &s, nil
}

func (s *Scenario) applyDefaults() {
	if s.HAR == "" {
		s.HAR = DefaultHAR
	}
	if s.Goal == "" {
		s.Goal = DefaultGoal
	}
	if s.Expected == "" {
		s.Expected = DefaultExpected
	}
	g, a := &s.Limits.Generate, &s.Limits.Answer
	if g.MaxSteps == 0 {
		g.MaxSteps = DefaultGenerateMaxSteps
	}
	if g.TimeoutSeconds == 0 {
		g.TimeoutSeconds = DefaultGenerateTimeoutSeconds
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = DefaultAnswerMaxSteps
	}
	if a.TimeoutSeconds == 0 {
		a.TimeoutSeconds = DefaultAnswerTimeoutSeconds
	}
}

// Discover returns the scenario directories under root. If root itself holds
// a manifest it is the only scenario; otherwise every immediate subdirectory
// with a manifest is one, in lexical order.
func Discover(root string) ([]string, error) {
	if fileExists(filepath.Join(root, ManifestName)) {
		return []string{root}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if fileExists(filepath.Join(dir, ManifestName)) {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s: no scenarios found (no %s here or in any subdirectory)", root, ManifestName)
	}
	return dirs, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
