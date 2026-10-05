package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/har"
)

// benchSanitize writes a sanitized copy of a HAR file.
func benchSanitize(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sanitize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the sanitized HAR here (required)")
	strip := fs.String("strip-bodies", "", "comma-separated classes whose response bodies are removed, e.g. media,script,stylesheet,font")
	trim := fs.Bool("trim-initiators", false, "remove Chrome's _initiator call stacks")
	drop := fs.String("drop", "", "comma-separated classes whose entries are removed entirely, e.g. media,font,telemetry")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench sanitize <session.har> --out <sanitized.har> [--strip-bodies classes] [--drop classes] [--trim-initiators]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fs.Usage()
		return 2
	}
	cfg := har.DefaultSanitizeConfig()
	classes, err := parseClasses(*strip)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	cfg.StripBodies = classes
	cfg.TrimInitiators = *trim
	if cfg.DropClasses, err = parseClasses(*drop); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	in, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer in.Close()
	tmp := *out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	report, err := har.Sanitize(in, f, cfg)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, *out)
	}
	if err != nil {
		os.Remove(tmp)
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s: %s\n", *out, report)
	return 0
}

// benchImportBurp converts a Burp Suite "Save items" XML export to a HAR.
// The result still holds cookies and tokens, so it is meant to go straight
// into bench sanitize.
func benchImportBurp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-burp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the HAR here (required); name it *.raw.har so git ignores it")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench import-burp <items.xml> --out <capture.raw.har>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fs.Usage()
		return 2
	}
	in, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer in.Close()
	tmp := *out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	report, err := har.ConvertBurp(in, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, *out)
	}
	if err != nil {
		os.Remove(tmp)
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s: %d entries", *out, report.Entries)
	if report.Undecoded > 0 {
		fmt.Fprintf(stdout, ", %d bodies left compressed (unsupported content encoding)", report.Undecoded)
	}
	fmt.Fprintln(stdout, "\nnot sanitized yet: run webshadow bench sanitize on it before storing it anywhere")
	return 0
}

// benchInspectHAR prints what the benchmark extracts from a HAR file.
func benchInspectHAR(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect-har", flag.ContinueOnError)
	fs.SetOutput(stderr)
	classFlag := fs.String("class", "", "comma-separated classes to list, or \"all\" (default document,api,unknown)")
	host := fs.String("host", "", "only list entries whose host contains this text")
	names := fs.Bool("names", false, "also print every header and query parameter name kept in the HAR")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench inspect-har <session.har> [--class classes] [--host text] [--names]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	var filter har.Filter
	if *classFlag == "all" {
		filter.Classes = har.Classes
	} else {
		classes, err := parseClasses(*classFlag)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
		filter.Classes = classes
	}
	filter.Host = *host

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer f.Close()
	trace, err := har.Parse(f)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	har.WriteSummary(stdout, trace)
	fmt.Fprintln(stdout)
	har.WriteIndex(stdout, trace.Select(filter))
	if *names {
		fmt.Fprintln(stdout)
		har.WriteKeptNames(stdout, trace)
	}
	if found := har.Unsanitized(trace); len(found) > 0 {
		fmt.Fprintf(stdout, "\nnot sanitized: %d credential header(s) or cookie list(s) remain; run webshadow bench sanitize\n", len(found))
	}
	return 0
}

func parseClasses(s string) ([]har.Class, error) {
	if s == "" {
		return nil, nil
	}
	var classes []har.Class
	for _, name := range strings.Split(s, ",") {
		c, ok := har.ParseClass(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown class %q (known: %s)", name, classList())
		}
		classes = append(classes, c)
	}
	return classes, nil
}

func classList() string {
	names := make([]string, len(har.Classes))
	for i, c := range har.Classes {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// reorder moves flags ahead of positional arguments, so both
// "sanitize in.har --out x" and "sanitize --out x in.har" work with the
// standard flag package, which stops at the first positional argument.
func reorder(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !isBoolFlag(a) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func isBoolFlag(a string) bool {
	switch strings.TrimLeft(a, "-") {
	case "names", "trim-initiators":
		return true
	}
	return false
}
