package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const analyzeUsage = `usage: webshadow analyze [-store dir] <cluster id or dir>
       webshadow analyze redact [-gz] <cluster id or dir> <out dir>

analyze reads the output of webshadow cluster and commits a new revision of
the Agent Interface IR, with the run's decisions and checks under
recordings/analysis/ unless -store names another directory. Cookies, tokens
and other secret values are replaced by stable tags before anything reads
them. Analysing the same evidence twice leaves the IR unchanged.

redact writes a copy of a cluster output with those values already
replaced, for sharing it or keeping it as a test fixture.
`

const irUsage = `usage: webshadow ir inspect [-store dir] [-revision latest] [-json]

Prints a revision of the Agent Interface IR: its entities, operations and
hypotheses by status. -json prints the revision file itself.
`

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "redact" {
		return runAnalyzeRedact(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, analyzeUsage) }
	storeDir := fs.String("store", "", "analysis directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, analyzeUsage)
		return 2
	}
	root, err := recordingsRoot()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow analyze: %v\n", err)
		return 1
	}
	st := store.Open(analysisRoot(root, *storeDir))
	res, err := semantic.Analyze(clusterDir(root, fs.Arg(0)), semantic.Options{Store: st})
	if err != nil {
		fmt.Fprintf(stderr, "webshadow analyze: %v\n", err)
		return 1
	}
	m := res.Manifest
	fmt.Fprintf(stdout, "Analysed %s: %d families (%d static)\n", m.Evidence, m.Counts.Families, m.Counts.StaticFamilies)
	fmt.Fprintf(stdout, "Redacted %d secret values\n", secretTotal(m))
	if res.Changed {
		fmt.Fprintf(stdout, "Revision: %s (new", res.Revision)
		if res.Prior != "" {
			fmt.Fprintf(stdout, ", from %s", res.Prior)
		}
		fmt.Fprintln(stdout, ")")
	} else {
		fmt.Fprintf(stdout, "Revision: %s (unchanged)\n", res.Revision)
	}
	fmt.Fprintf(stdout, "Entities: %d, operations: %d, hypotheses: %d\n", m.Counts.Entities, m.Counts.Operations, m.Counts.Hypotheses)
	fmt.Fprintf(stdout, "Run: %s\n", filepath.Join(st.Root, "runs", res.Run))
	return 0
}

func runAnalyzeRedact(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze redact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, analyzeUsage) }
	gz := fs.Bool("gz", false, "gzip every file")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, analyzeUsage)
		return 2
	}
	root, err := recordingsRoot()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow analyze redact: %v\n", err)
		return 1
	}
	raw, err := input.ReadRaw(clusterDir(root, fs.Arg(0)))
	if err != nil {
		fmt.Fprintf(stderr, "webshadow analyze redact: %v\n", err)
		return 1
	}
	stats := raw.Redact()
	if err := raw.Write(fs.Arg(1), *gz); err != nil {
		fmt.Fprintf(stderr, "webshadow analyze redact: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Redacted %d secret values in %d strings\n", secretTotal(store.RunManifest{Redaction: stats}), stats.Replaced)
	fmt.Fprintf(stdout, "Output: %s\n", fs.Arg(1))
	return 0
}

func runIR(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "inspect" {
		fmt.Fprint(stderr, irUsage)
		return 2
	}
	fs := flag.NewFlagSet("ir inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, irUsage) }
	storeDir := fs.String("store", "", "analysis directory")
	revision := fs.String("revision", store.Latest, "revision to print")
	asJSON := fs.Bool("json", false, "print the revision file")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || !store.ValidRevision(*revision) {
		fmt.Fprint(stderr, irUsage)
		return 2
	}
	root, err := recordingsRoot()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow ir: %v\n", err)
		return 1
	}
	st := store.Open(analysisRoot(root, *storeDir))
	x, err := st.Load(*revision)
	if err != nil {
		fmt.Fprintf(stderr, "webshadow ir: %v\n", err)
		return 1
	}
	if x == nil {
		fmt.Fprintf(stderr, "webshadow ir: no revisions in %s; run webshadow analyze first\n", st.Root)
		return 1
	}
	if *asJSON {
		data, err := x.Encode()
		if err != nil {
			fmt.Fprintf(stderr, "webshadow ir: %v\n", err)
			return 1
		}
		stdout.Write(data)
		return 0
	}
	printIR(stdout, x)
	return 0
}

func printIR(w io.Writer, x *ir.InterfaceIR) {
	fmt.Fprintf(w, "Revision %s", x.Revision)
	if x.Parent != "" {
		fmt.Fprintf(w, " (from %s)", x.Parent)
	}
	fmt.Fprintf(w, ", schema %s\n", x.SchemaVersion)
	fmt.Fprintf(w, "Evidence: %s\n", strings.Join(x.Evidence, ", "))
	fmt.Fprintf(w, "\nEntities: %d\n", len(x.Entities))
	for _, e := range x.Entities {
		fmt.Fprintf(w, "  %s  %s  %d fields, %d relations\n", e.ID, e.Name, len(e.Fields), len(e.Relations))
	}
	fmt.Fprintf(w, "\nOperations: %d\n", len(x.Operations))
	for _, op := range x.Operations {
		fmt.Fprintf(w, "  %s  %s  [%s]  %d families\n", op.ID, op.Name, op.Status, len(op.SourceFamilies))
	}
	byStatus := map[ir.HypothesisStatus]int{}
	for _, h := range x.Hypotheses {
		byStatus[h.Status]++
	}
	fmt.Fprintf(w, "\nHypotheses: %d\n", len(x.Hypotheses))
	for _, s := range ir.Statuses {
		if byStatus[s] > 0 {
			fmt.Fprintf(w, "  %-24s %d\n", s, byStatus[s])
		}
	}
}

func secretTotal(m store.RunManifest) int {
	kinds := make([]string, 0, len(m.Redaction.Secrets))
	for k := range m.Redaction.Secrets {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	n := 0
	for _, k := range kinds {
		n += m.Redaction.Secrets[k]
	}
	return n
}

func recordingsRoot() (string, error) {
	home, err := webshadowHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "recordings"), nil
}

// clusterDir resolves a cluster argument: a directory as given, otherwise a
// result id under recordings/clusters/.
func clusterDir(root, arg string) string {
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		return arg
	}
	return filepath.Join(root, "clusters", arg)
}

func analysisRoot(root, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return filepath.Join(root, "analysis")
}
