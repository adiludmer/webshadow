package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// Write saves a score as benchmarks/<case>/<run>.json in the store.
func Write(s *store.Store, r *Result) (string, error) {
	dir := filepath.Join(s.Root, "benchmarks", r.Case)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, r.Run+".json")
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// Markdown prints scores side by side, one column per run.
func Markdown(w io.Writer, rs []*Result) {
	if len(rs) == 0 {
		return
	}
	fmt.Fprintf(w, "# Benchmark %s\n\nExpected IR: %s.\n\n", rs[0].Case, rs[0].Review)
	head := []string{"Measure"}
	for _, r := range rs {
		head = append(head, r.Model)
	}
	row := func(cells ...string) { fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | ")) }
	row(head...)
	row(strings.Split(strings.Repeat("---,", len(head)), ",")[:len(head)]...)
	pr := func(name string, get func(*Result) Metric) {
		cells := []string{name}
		for _, r := range rs {
			m := get(r)
			cells = append(cells, fmt.Sprintf("%.2f / %.2f (%d of %d)", m.Precision, m.Recall, m.Correct, m.Expected))
		}
		row(cells...)
	}
	cell := func(name string, get func(*Result) string) {
		cells := []string{name}
		for _, r := range rs {
			cells = append(cells, get(r))
		}
		row(cells...)
	}
	pr("Family roles (P / R)", func(r *Result) Metric { return r.Roles })
	pr("Identity joins", func(r *Result) Metric { return r.IdentityJoins })
	pr("Entities", func(r *Result) Metric { return r.Entities })
	pr("Entity names", func(r *Result) Metric { return r.EntityNames })
	pr("Entity fields", func(r *Result) Metric { return r.EntityFields })
	pr("Relations", func(r *Result) Metric { return r.Relations })
	pr("Operations", func(r *Result) Metric { return r.Operations })
	pr("Operation merges", func(r *Result) Metric { return r.OperationMerges })
	pr("Required inputs", func(r *Result) Metric { return r.RequiredInputs })
	pr("Outputs", func(r *Result) Metric { return r.Outputs })
	cell("Prerequisite false positives", func(r *Result) string {
		p := r.Prerequisites
		return fmt.Sprintf("%.2f (%d of %d; %d called background)", p.Rate, p.FalsePositives, p.Predicted, p.Background)
	})
	cell("Abstention", func(r *Result) string {
		a := r.Abstention
		return fmt.Sprintf("%.2f (%d unknown, %d unresolved of %d; %d on labelled roles)", a.Rate, a.Unknown, a.Unresolved, a.Tasks, a.OnLabelled)
	})
	cell("Coverage", func(r *Result) string { return fmt.Sprintf("%.3f", r.Coverage) })
	cell("Model calls", func(r *Result) string {
		return fmt.Sprintf("%d (%d repairs, %d reused)", r.Cost.Calls, r.Cost.Repairs, r.Cost.Reused)
	})
	cell("Tokens in / out", func(r *Result) string {
		return fmt.Sprintf("%d / %d", r.Cost.InputTokens, r.Cost.OutputTokens)
	})
	cell("Model time", func(r *Result) string {
		s := float64(r.Cost.LatencyMS) / 1000
		per := 0.0
		if r.Cost.Calls > 0 {
			per = s / float64(r.Cost.Calls)
		}
		return fmt.Sprintf("%.0f s (%.1f s per call)", s, per)
	})
	fmt.Fprintln(w, "\nPrecision is correct over predicted, recall correct over expected; only what the expected IR labels is scored.")
	for _, r := range rs {
		fmt.Fprintf(w, "\n## Mistakes: %s (%s)\n\n", r.Model, r.Run)
		if len(r.Mistakes) == 0 {
			fmt.Fprintln(w, "none")
		}
		for _, m := range r.Mistakes {
			fmt.Fprintf(w, "- %s\n", m)
		}
	}
}
