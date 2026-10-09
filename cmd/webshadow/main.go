// Command webshadow is the Webshadow command-line tool.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `usage: webshadow <command> [arguments]

Commands:
  browser      record a browsing session through an isolated Chromium
  recordings   list, inspect and delete recorded sessions
  cluster      group recorded requests into families and sequence evidence
  bench        run and inspect the HAR benchmark
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches a command line and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "browser":
		return runBrowser(args[1:], stdout, stderr)
	case "recordings":
		return runRecordings(args[1:], stdout, stderr)
	case "cluster":
		return runCluster(args[1:], stdout, stderr)
	case "bench":
		return runBench(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "webshadow: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
