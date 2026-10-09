package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/semantic"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const analyzeUsage = `usage: webshadow analyze [-store dir] [-model mock|<id>] [-models models.yaml] <cluster id or dir>
       webshadow analyze redact [-gz] <cluster id or dir> <out dir>

analyze reads the output of webshadow cluster and commits a new revision of
the Agent Interface IR, with the run's decisions and checks under
recordings/analysis/ unless -store names another directory. Cookies, tokens
and other secret values are replaced by stable tags before anything reads
them. Analysing the same evidence twice leaves the IR unchanged.

-model names the model that answers the role questions Go cannot settle
by rule: "mock" (the default) picks each task's first structural choice
offline; any other id is an entry in the -models file, such as a local
GGUF model run through llama.cpp.

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
	modelID := fs.String("model", decide.MockID, "model that answers decision tasks")
	modelsPath := fs.String("models", "models.yaml", "models file")
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
	decider, err := openDecider(*modelID, *modelsPath)
	if err != nil {
		fmt.Fprintf(stderr, "webshadow analyze: %v\n", err)
		return 1
	}
	defer decider.Model.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st := store.Open(analysisRoot(root, *storeDir))
	res, err := semantic.Analyze(ctx, clusterDir(root, fs.Arg(0)), semantic.Options{Store: st, Decider: decider})
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
	fmt.Fprintf(stdout, "Decisions: %d settled by rule, %d asked of %s: %d decided, %d unknown, %d unresolved\n",
		m.Counts.Settled, m.Counts.Tasks, m.Model, m.Counts.Decided, m.Counts.Unknown, m.Counts.Unresolved)
	fmt.Fprintln(stdout, "Family roles:")
	printRoles(stdout, res.IR)
	fmt.Fprintf(stdout, "Entities: %d\n", m.Counts.Entities)
	printEntities(stdout, res.IR)
	fmt.Fprintf(stdout, "Prerequisites: %d proposed\n", m.Counts.Prerequisites)
	printPrerequisites(stdout, res.IR)
	fmt.Fprintf(stdout, "Operations: %d, hypotheses: %d\n", m.Counts.Operations, m.Counts.Hypotheses)
	fmt.Fprintf(stdout, "Run: %s\n", filepath.Join(st.Root, "runs", res.Run))
	return 0
}

// openDecider opens the model a run asks: the built-in mock, or an entry
// of the models file.
func openDecider(id, modelsPath string) (*decide.Decider, error) {
	if id == decide.MockID {
		return decide.New(&decide.Mock{}, decide.ModelInfo{ID: decide.MockID, Adapter: "mock"}, decide.DefaultParams()), nil
	}
	reg, err := model.LoadRegistry(modelsPath)
	if err != nil {
		return nil, err
	}
	cfg, ok := reg.Get(id)
	if !ok {
		return nil, fmt.Errorf("no model %q in %s (have %s)", id, modelsPath, strings.Join(reg.IDs(), ", "))
	}
	m, err := reg.Open(id, os.Getenv)
	if err != nil {
		return nil, err
	}
	info := decide.ModelInfo{ID: id, Adapter: cfg.Adapter}
	if cfg.ModelPath != "" {
		if path, err := expandPath(cfg.ModelPath); err == nil {
			info.Path = path
			info.Quantization = quantization(path)
		}
	}
	return decide.New(m, info, decide.DefaultParams()), nil
}

func expandPath(p string) (string, error) {
	out := os.Expand(p, os.Getenv)
	if out == "" {
		return "", fmt.Errorf("empty path")
	}
	return out, nil
}

// quantization reads the quantization from a GGUF file name, such as
// Q4_K_M in Qwen3-4B-Q4_K_M.gguf.
func quantization(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '.' })
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.ToUpper(parts[i])
		if strings.HasPrefix(p, "Q") && len(p) > 1 && p[1] >= '0' && p[1] <= '9' || p == "F16" || p == "BF16" || p == "F32" {
			return parts[i]
		}
	}
	return ""
}

// printRoles counts the standing family-role hypotheses by role.
func printRoles(w io.Writer, x *ir.InterfaceIR) {
	counts := map[string]int{}
	for _, h := range x.Hypotheses {
		if h.Kind == ir.KindFamilyRole && h.Status != ir.StatusSuperseded && h.Status != ir.StatusRejected {
			counts[h.CandidateID]++
		}
	}
	roles := make([]string, 0, len(counts))
	for r := range counts {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		fmt.Fprintf(w, "  %-16s %d\n", r, counts[r])
	}
}

// printEntities lists the entities with the status of the hypothesis
// behind each, and their relations.
func printEntities(w io.Writer, x *ir.InterfaceIR) {
	status := map[string]ir.HypothesisStatus{}
	for _, h := range x.Hypotheses {
		status[h.ID] = h.Status
	}
	names := map[string]string{}
	for _, e := range x.Entities {
		names[e.ID] = e.Name
	}
	for _, e := range x.Entities {
		fmt.Fprintf(w, "  %s  %-20s [%s]  %d identity locations, %d fields\n",
			e.ID, e.Name, status[e.Hypothesis], len(e.Identity), len(e.Fields))
		for _, r := range e.Relations {
			fmt.Fprintf(w, "      %s %s (%s, %s) [%s]\n", r.Kind, names[r.Target], r.Target, r.Cardinality, status[r.Hypothesis])
		}
	}
}

// printPrerequisites counts the standing prerequisite hypotheses by kind.
func printPrerequisites(w io.Writer, x *ir.InterfaceIR) {
	counts := map[string]int{}
	for _, h := range x.Hypotheses {
		if h.Kind == ir.KindPrerequisite && h.Status != ir.StatusSuperseded && h.Status != ir.StatusRejected {
			counts[h.CandidateID]++
		}
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(w, "  %-24s %d\n", k, counts[k])
	}
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
	printEntities(w, x)
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
