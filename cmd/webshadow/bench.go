package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

const benchUsage = `usage: webshadow bench <command> [arguments]

Commands:
  validate <path>...      check scenario directories or suites before running them
  sanitize <har> --out f  write a copy of a HAR with credentials and personal data removed
  inspect-har <har>       show what the benchmark extracts from a HAR
  generate <scenario> --generator <model>
                          write a shadow tree from a scenario's HAR
  answer <scenario> --tree <dir> --reader <model>
                          answer a scenario's goal from a shadow tree and score it
`

func runBench(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, benchUsage)
		return 2
	}
	switch args[0] {
	case "validate":
		return benchValidate(args[1:], stdout, stderr)
	case "sanitize":
		return benchSanitize(args[1:], stdout, stderr)
	case "inspect-har":
		return benchInspectHAR(args[1:], stdout, stderr)
	case "generate":
		return benchGenerate(args[1:], stdout, stderr)
	case "answer":
		return benchAnswer(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, benchUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "webshadow bench: unknown command %q\n\n%s", args[0], benchUsage)
		return 2
	}
}

// benchValidate validates every scenario found under the given paths. Each
// path is either one scenario directory or a suite of them. It exits 1 when
// any error is found; warnings are printed but do not fail.
func benchValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: webshadow bench validate <path>...") }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}

	var dirs []string
	for _, root := range fs.Args() {
		found, err := scenario.Discover(root)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		dirs = append(dirs, found...)
	}

	res := scenario.ValidateDirs(dirs, scenario.Options{})
	for _, issue := range res.Issues {
		fmt.Fprintln(stdout, issue)
	}
	errs, warns := 0, 0
	for _, issue := range res.Issues {
		if issue.Severity == scenario.Error {
			errs++
		} else {
			warns++
		}
	}
	fmt.Fprintf(stdout, "%d scenario(s) checked, %d error(s), %d warning(s)\n", len(dirs), errs, warns)
	if res.HasErrors() {
		return 1
	}
	return 0
}
