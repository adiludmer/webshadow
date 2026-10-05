package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/adiludmer/webshadow/internal/benchmark/capture"
)

// benchSanitize writes a sanitized copy of a Burp Suite XML export.
func benchSanitize(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sanitize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "write the sanitized capture here (required)")
	strip := fs.String("strip-bodies", "", "comma-separated classes whose response bodies are removed, e.g. media,script,stylesheet,font")
	drop := fs.String("drop", "", "comma-separated classes whose items are removed entirely, e.g. media,font,telemetry")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench sanitize <burp-items.xml> --out <session.xml> [--strip-bodies classes] [--drop classes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fs.Usage()
		return 2
	}
	cfg := capture.DefaultSanitizeConfig()
	classes, err := parseClasses(*strip)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	cfg.StripBodies = classes
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
	report, err := capture.Sanitize(in, f, cfg)
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

// benchInspect prints what the benchmark extracts from a capture.
func benchInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	classFlag := fs.String("class", "", "comma-separated classes to list, or \"all\" (default document,api,unknown)")
	host := fs.String("host", "", "only list items whose host contains this text")
	names := fs.Bool("names", false, "also print every header and query parameter name kept in the capture")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: webshadow bench inspect <session.xml> [--class classes] [--host text] [--names]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorder(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	var filter capture.Filter
	if *classFlag == "all" {
		filter.Classes = capture.Classes
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
	trace, err := capture.Parse(f)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	capture.WriteSummary(stdout, trace)
	fmt.Fprintln(stdout)
	capture.WriteIndex(stdout, trace.Select(filter))
	if *names {
		fmt.Fprintln(stdout)
		capture.WriteKeptNames(stdout, trace)
	}
	if found := capture.Unsanitized(trace); len(found) > 0 {
		fmt.Fprintf(stdout, "\nnot sanitized: %d credential header(s) remain; run webshadow bench sanitize\n", len(found))
	}
	return 0
}

func parseClasses(s string) ([]capture.Class, error) {
	if s == "" {
		return nil, nil
	}
	var classes []capture.Class
	for _, name := range strings.Split(s, ",") {
		c, ok := capture.ParseClass(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown class %q (known: %s)", name, classList())
		}
		classes = append(classes, c)
	}
	return classes, nil
}

func classList() string {
	names := make([]string, len(capture.Classes))
	for i, c := range capture.Classes {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// reorder moves flags ahead of positional arguments, so both
// "sanitize in.xml --out x" and "sanitize --out x in.xml" work with the
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
