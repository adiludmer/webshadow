package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/adiludmer/webshadow/internal/benchmark/generator"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/record"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

// benchGenerate runs the generate stage for one scenario and writes the
// frozen tree and its manifest under the runs directory. It exits 0 when
// the tree was written, even if the generator produced nothing useful.
func benchGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	generatorID := fs.String("generator", "", "model id from the models file (required)")
	modelsPath := fs.String("models", "models.yaml", "models file")
	runsDir := fs.String("runs", "runs", "directory for run output")
	runID := fs.String("run-id", "", "run id (default: the UTC start time)")
	rep := fs.Int("repetition", 1, "repetition number, used in the tree's directory name")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench generate <scenario-dir> --generator <model-id> [--models file] [--runs dir] [--run-id id] [--repetition n]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *generatorID == "" || *rep < 1 {
		fs.Usage()
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	res := scenario.ValidateDirs([]string{fs.Arg(0)}, scenario.Options{})
	if res.HasErrors() {
		for _, issue := range res.Issues {
			fmt.Fprintln(stderr, issue)
		}
		return 1
	}
	s := res.Scenarios[0]

	reg, err := model.LoadRegistry(*modelsPath)
	if err != nil {
		return fail(err)
	}
	m, err := reg.Open(*generatorID, os.Getenv)
	if err != nil {
		return fail(err)
	}
	defer m.Close()

	id := *runID
	if id == "" {
		id = record.NewRunID(now())
	}
	dir := record.TreeDir(*runsDir, id, s.ID, *generatorID, *rep)
	fmt.Fprintf(stdout, "run %s: generating %s with %s into %s\n", id, s.ID, *generatorID, dir)
	g, err := generator.Run(context.Background(), m, s, dir, generator.Options{Now: now})
	if err != nil {
		return fail(err)
	}
	tree := &record.Tree{
		SchemaVersion:    record.SchemaVersion,
		RunID:            id,
		ScenarioID:       s.ID,
		ScenarioVersion:  s.Version,
		Repetition:       *rep,
		GeneratorModelID: *generatorID,
		GeneratorPrompt:  generator.PromptVersion,
		Status:           string(g.Status),
		Digest:           g.Digest,
		Files:            g.Stats.Files,
		Bytes:            g.Stats.Bytes,
		Generation:       record.StageOf(g.Agent),
	}
	if g.Agent.Err != nil {
		tree.Error = g.Agent.Err.Error()
	}
	if err := record.WriteTree(dir, tree); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "%s: %s after %d steps, %d files (%d bytes), %s\n  %s\n",
		g.Status, g.Agent.Outcome, len(g.Agent.Steps), g.Stats.Files, g.Stats.Bytes, g.Digest, record.TreeManifestPath(dir))
	return 0
}
