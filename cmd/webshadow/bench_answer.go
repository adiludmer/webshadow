package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/reader"
	"github.com/adiludmer/webshadow/internal/benchmark/record"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
	"github.com/adiludmer/webshadow/internal/benchmark/shadow"
)

// now is the CLI's clock; tests replace it.
var now = time.Now

// benchAnswer runs the reader stage on an existing shadow tree and writes
// one record per repetition. It exits 0 when every record was written,
// whatever the answers scored.
func benchAnswer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	treeDir := fs.String("tree", "", "shadow tree directory to answer from (required)")
	readerID := fs.String("reader", "", "model id from the models file (required)")
	modelsPath := fs.String("models", "models.yaml", "models file")
	runsDir := fs.String("runs", "runs", "directory for run records")
	runID := fs.String("run-id", "", "run id (default: the UTC start time)")
	reps := fs.Int("repetitions", 1, "how many times to answer")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench answer <scenario-dir> --tree <dir> --reader <model-id> [--models file] [--runs dir] [--run-id id] [--repetitions n]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *treeDir == "" || *readerID == "" || *reps < 1 {
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

	root, err := os.OpenRoot(*treeDir)
	if err != nil {
		return fail(fmt.Errorf("opening tree: %w", err))
	}
	defer root.Close()
	tree := root.FS()
	digest, stats, err := shadow.Digest(tree)
	if err != nil {
		return fail(fmt.Errorf("reading tree: %w", err))
	}

	reg, err := model.LoadRegistry(*modelsPath)
	if err != nil {
		return fail(err)
	}
	m, err := reg.Open(*readerID, os.Getenv)
	if err != nil {
		return fail(err)
	}
	defer m.Close()

	id := *runID
	if id == "" {
		id = record.NewRunID(now())
	}
	fmt.Fprintf(stdout, "run %s: %s with reader %s on %s (%d files, %s)\n", id, s.ID, *readerID, *treeDir, stats.Files, digest)
	for rep := 1; rep <= *reps; rep++ {
		r, err := reader.Run(context.Background(), m, s, tree, reader.Options{Now: now})
		if err != nil {
			return fail(err)
		}
		rec := &record.Record{
			SchemaVersion:    record.SchemaVersion,
			RunID:            id,
			ScenarioID:       s.ID,
			ScenarioVersion:  s.Version,
			Repetition:       rep,
			GeneratorModelID: record.ExternalGenerator,
			ReaderModelID:    *readerID,
			ReaderPrompt:     reader.PromptVersion,
			TreeDigest:       digest,
			TreeFiles:        stats.Files,
			TreeBytes:        stats.Bytes,
			Status:           string(r.Status),
			Answer:           r.Answer,
			Score:            r.Score,
			Answering:        record.StageOf(r.Agent),
		}
		if r.Agent.Err != nil {
			rec.Error = r.Agent.Err.Error()
		}
		path, err := record.Write(*runsDir, rec)
		if err != nil {
			return fail(err)
		}
		answer := "no answer"
		if r.Answer != nil {
			data, _ := json.Marshal(r.Answer)
			answer = string(data)
		}
		fmt.Fprintf(stdout, "repetition %d: %s in %d steps, answer %s\n  %s\n", rep, r.Status, len(r.Agent.Steps), answer, path)
	}
	return 0
}
