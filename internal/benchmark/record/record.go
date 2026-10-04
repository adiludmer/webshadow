// Package record defines the JSON record written for every benchmark case
// and where it lives under the runs directory.
package record

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/score"
)

// SchemaVersion changes whenever a field changes meaning.
const SchemaVersion = 1

// ExternalGenerator is the generator id for trees made outside the
// benchmark, such as one passed to `bench answer --tree`.
const ExternalGenerator = "external"

// Record is one case: one scenario, generator, reader and repetition.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`

	ScenarioID      string `json:"scenario_id"`
	ScenarioVersion int    `json:"scenario_version"`
	Repetition      int    `json:"repetition"`

	GeneratorModelID string `json:"generator_model_id"`
	GeneratorPrompt  string `json:"generator_prompt,omitempty"`
	ReaderModelID    string `json:"reader_model_id"`
	ReaderPrompt     string `json:"reader_prompt"`

	TreeDigest string `json:"tree_digest"`
	TreeFiles  int    `json:"tree_files"`
	TreeBytes  int64  `json:"tree_bytes"`

	Status string         `json:"status"`
	Answer map[string]any `json:"answer,omitempty"`
	Score  *score.Result  `json:"score,omitempty"`
	Error  string         `json:"error,omitempty"`

	Answering Stage `json:"answering"`
}

// Stage is one agent's run within a case.
type Stage struct {
	Outcome   agent.Outcome `json:"outcome"`
	Steps     int           `json:"steps"`
	Usage     model.Usage   `json:"usage"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	System    string        `json:"system_prompt"`
	// Transcript holds every reply and tool result.
	Transcript []agent.Step `json:"transcript"`
}

// StageOf summarizes an agent result.
func StageOf(r agent.Result) Stage {
	return Stage{
		Outcome:    r.Outcome,
		Steps:      len(r.Steps),
		Usage:      r.Usage,
		StartedAt:  r.StartedAt.UTC(),
		Duration:   r.Duration,
		System:     r.System,
		Transcript: r.Steps,
	}
}

// NewRunID names a run by its UTC start time.
func NewRunID(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// Path is where a case's record lives:
// <runs>/<run id>/cases/<scenario>/<generator>/<reader>/repetition-NN.json.
func Path(runsDir string, r *Record) string {
	return filepath.Join(runsDir, r.RunID, "cases", r.ScenarioID, r.GeneratorModelID, r.ReaderModelID,
		fmt.Sprintf("repetition-%02d.json", r.Repetition))
}

// Write stores r at Path, creating directories, and returns the path. It
// writes to a temporary file first so a crash never leaves half a record.
func Write(runsDir string, r *Record) (string, error) {
	path := Path(runsDir, r)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
