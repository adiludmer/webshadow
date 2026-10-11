package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic"
	"github.com/adiludmer/webshadow/internal/semantic/bench"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const analyzeBenchUsage = `usage: webshadow analyze bench [-store dir] [-models models.yaml] [-expected file] [-model id]... [-reuse] [-out file] <cluster id or dir>

bench analyses one clustering result with each -model in turn (the mock
when none is given) and scores every run against the reviewed expected IR,
expected-ir.json in the cluster directory unless -expected names another.
Each model keeps its own analysis store under bench/<case>/<model>/ so
runs never build on another model's revision. Scores are written to
benchmarks/<case>/<run>.json in that store, and a side-by-side table is
printed, or written to -out.

Every task is asked afresh so cost and latency are comparable; -reuse
re-scores a model's earlier answers without asking it again.
`

type modelList []string

func (m *modelList) String() string     { return strings.Join(*m, ",") }
func (m *modelList) Set(v string) error { *m = append(*m, strings.Fields(v)...); return nil }

func runAnalyzeBench(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, analyzeBenchUsage) }
	storeDir := fs.String("store", "", "analysis directory")
	modelsPath := fs.String("models", "models.yaml", "models file")
	expectedPath := fs.String("expected", "", "expected IR (default: expected-ir.json in the cluster directory)")
	reuse := fs.Bool("reuse", false, "repeat a model's previous answers to unchanged tasks")
	out := fs.String("out", "", "write the comparison here instead of stdout")
	var models modelList
	fs.Var(&models, "model", "model to benchmark; repeat to compare")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, analyzeBenchUsage)
		return 2
	}
	if len(models) == 0 {
		models = modelList{decide.MockID}
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "webshadow analyze bench: %v\n", err)
		return 1
	}
	root, err := recordingsRoot()
	if err != nil {
		return fail(err)
	}
	dir := clusterDir(root, fs.Arg(0))
	if *expectedPath == "" {
		*expectedPath = filepath.Join(dir, bench.ExpectedFile)
	}
	expected, err := bench.Load(*expectedPath)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var results []*bench.Result
	for _, id := range models {
		decider, err := openDecider(id, *modelsPath)
		if err != nil {
			return fail(err)
		}
		st := store.Open(filepath.Join(analysisRoot(root, *storeDir), "bench", expected.Case, id))
		fmt.Fprintf(stderr, "Analysing %s with %s into %s\n", dir, id, st.Root)
		res, err := semantic.Analyze(ctx, dir, semantic.Options{Store: st, Decider: decider, Reuse: *reuse})
		decider.Model.Close()
		if err != nil {
			return fail(err)
		}
		if err := expected.Validate(res.Input); err != nil {
			return fail(err)
		}
		r, err := bench.Score(st, res.Run, expected)
		if err != nil {
			return fail(err)
		}
		path, err := bench.Write(st, r)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "Score: %s\n", path)
		results = append(results, r)
	}
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		w = f
	}
	bench.Markdown(w, results)
	return 0
}
