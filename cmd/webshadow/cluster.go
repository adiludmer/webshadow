package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/recording"
)

const clusterUsage = `usage: webshadow cluster [-out dir] [-yaml] <id>...

Groups the requests of one or more recordings into request families and
writes the families, value links, traces, episodes, sequence edges and an
evidence pack. The output goes to recordings/clusters/<result id>/ unless
-out names a directory. The same recordings always give the same output.
`

func runCluster(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, clusterUsage) }
	out := fs.String("out", "", "output directory")
	withYAML := fs.Bool("yaml", false, "also write evidence.yaml")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprint(stderr, clusterUsage)
		return 2
	}
	home, err := webshadowHome()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
		return 1
	}
	root := filepath.Join(home, "recordings")
	var recs []*recording.Recording
	for _, id := range fs.Args() {
		dir, err := recording.Dir(root, id)
		if err != nil {
			fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
			return 1
		}
		r, err := recording.Load(dir)
		if err != nil {
			fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
			return 1
		}
		if r.Truncated {
			fmt.Fprintf(stderr, "webshadow cluster: warning: %s ends in a partial record, which was skipped\n", id)
		}
		recs = append(recs, r)
	}
	res, err := cluster.Run(recs, cluster.DefaultOptions())
	if err != nil {
		fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
		return 1
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(root, "clusters", res.Manifest.ID)
	}
	if err := cluster.Write(dir, res, *withYAML); err != nil {
		fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
		return 1
	}
	c := res.Manifest.Counts
	fmt.Fprintf(stdout, "Clustered %d recordings, %d exchanges\n", len(recs), c.Observations)
	fmt.Fprintf(stdout, "Families: %d (%d static)\nValue links: %d\nEpisodes: %d\nSequence edges: %d (%d value flows)\n",
		c.Families, c.Static, c.Links, c.Episodes, c.Edges, c.Flows)
	fmt.Fprintf(stdout, "Output: %s\n", dir)
	return 0
}
