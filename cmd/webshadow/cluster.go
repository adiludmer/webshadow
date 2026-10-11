package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/recording"
)

const clusterUsage = `usage: webshadow cluster [-hosts list] [-out dir] [-yaml] <id>...
       webshadow cluster -follow [-interval 1s] [-hosts list] [-out dir] [-yaml] <id>

Groups the requests of one or more recordings into request families and
writes the families, value links, traces, episodes, sequence edges and an
evidence pack. The output goes to recordings/clusters/<result id>/ unless
-out names a directory. The same recordings always give the same output.

Page views come from request headers (Sec-Fetch-*, Referer) and redirects,
so any request/response capture can be clustered. -hosts keeps only
exchanges with the given comma-separated hosts or their subdomains, such as
-hosts amazon.com, so a browser capture looks like what the site's own
backend would record.

-follow watches one recording while it is being recorded, redrawing the
families, counts and latest value flows as they change, and writes the
output once the recording completes or on Ctrl-C.
`

func runCluster(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, clusterUsage) }
	out := fs.String("out", "", "output directory")
	withYAML := fs.Bool("yaml", false, "also write evidence.yaml")
	follow := fs.Bool("follow", false, "watch one recording as it grows")
	interval := fs.Duration("interval", time.Second, "how often -follow re-clusters")
	hosts := fs.String("hosts", "", "comma-separated hosts to keep, with their subdomains")
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
	opts := cluster.DefaultOptions()
	if *hosts != "" {
		opts.Hosts = strings.Split(*hosts, ",")
	}
	if *follow {
		if fs.NArg() != 1 || *interval <= 0 {
			fmt.Fprint(stderr, clusterUsage)
			return 2
		}
		dir, err := recording.Dir(root, fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "webshadow cluster: %v\n", err)
			return 1
		}
		ctx, stop := followContext()
		defer stop()
		return runFollow(ctx, followConfig{
			dir: dir, interval: *interval, out: *out, root: root, withYAML: *withYAML, opts: opts,
			terminal: isTerminal(stdout), size: terminalSize(),
		}, stdout, stderr)
	}
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
	res, err := cluster.Run(recs, opts)
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
	if skipped := skippedExchanges(res); skipped > 0 {
		fmt.Fprintf(stdout, "Skipped %d exchanges to other hosts\n", skipped)
	}
	fmt.Fprintf(stdout, "Families: %d (%d static)\nValue links: %d\nPage views: %d\nSequence edges: %d (%d value flows)\n",
		c.Families, c.Static, c.Links, c.Episodes, c.Edges, c.Flows)
	fmt.Fprintf(stdout, "Output: %s\n", dir)
	return 0
}

func skippedExchanges(res *cluster.Result) int {
	n := 0
	for _, in := range res.Manifest.Inputs {
		n += in.Skipped
	}
	return n
}
