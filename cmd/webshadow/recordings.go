package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/adiludmer/webshadow/internal/recording"
)

const recordingsUsage = `usage: webshadow recordings <command> [arguments]

Commands:
  list               list recorded sessions
  show <id>          summarize a session
  timeline <id>      print HTTP and browser events in time order
  delete <id>        delete a session and everything it recorded
`

func runRecordings(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, recordingsUsage)
		return 2
	}
	home, err := webshadowHome()
	if err != nil {
		fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
		return 1
	}
	root := filepath.Join(home, "recordings")
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, recordingsUsage)
		return 0
	case "list":
		if len(rest) != 0 {
			fmt.Fprint(stderr, recordingsUsage)
			return 2
		}
		return recordingsList(root, stdout, stderr)
	case "show", "timeline", "delete":
		if len(rest) != 1 {
			fmt.Fprintf(stderr, "usage: webshadow recordings %s <id>\n", cmd)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "webshadow recordings: unknown command %q\n\n%s", cmd, recordingsUsage)
		return 2
	}

	id := rest[0]
	if cmd == "delete" {
		if err := recording.Delete(root, id); err != nil {
			fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Deleted %s\n", id)
		return 0
	}
	dir, err := recording.Dir(root, id)
	if err != nil {
		fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
		return 1
	}
	r, err := recording.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
		return 1
	}
	if cmd == "timeline" {
		if err := recording.WriteTimeline(stdout, recording.Timeline(r)); err != nil {
			fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
			return 1
		}
		return 0
	}
	recordingsShow(r, stdout)
	return 0
}

func recordingsList(root string, stdout, stderr io.Writer) int {
	sessions, err := recording.List(root)
	if err != nil {
		fmt.Fprintf(stderr, "webshadow recordings: %v\n", err)
		return 1
	}
	if len(sessions) == 0 {
		fmt.Fprintf(stdout, "No recordings in %s\n", root)
		return 0
	}
	for _, s := range sessions {
		fmt.Fprintf(stdout, "%s  %-9s  %s  %5d exchanges  %5d events\n",
			s.ID, s.Status, s.StartedAt.Local().Format("2006-01-02 15:04"), s.Exchanges, s.BrowserEvents)
	}
	return 0
}

// recordingsShow prints what a session holds without any header or body
// contents, which can carry credentials.
func recordingsShow(r *recording.Recording, stdout io.Writer) {
	s := r.Session
	fmt.Fprintf(stdout, "Session: %s (%s)\n", s.ID, s.Status)
	fmt.Fprintf(stdout, "Directory: %s\n", r.Dir)
	fmt.Fprintf(stdout, "Started: %s\n", s.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if s.EndedAt != nil {
		fmt.Fprintf(stdout, "Duration: %s\n", s.EndedAt.Sub(s.StartedAt).Round(1e9))
	}
	if s.Browser != nil {
		fmt.Fprintf(stdout, "Browser: %s\n", s.Browser.Version)
	}
	if s.Proxy != nil {
		fmt.Fprintf(stdout, "Proxy: %s\n", s.Proxy.Addr)
	}
	fmt.Fprintf(stdout, "HTTP exchanges: %d\nBrowser events: %d\n", len(r.Exchanges), len(r.Events))
	if r.Truncated {
		fmt.Fprintln(stdout, "Warning: the recording ends in a partial record, which was skipped")
	}

	hosts := map[string]int{}
	failed := 0
	for _, ex := range r.Exchanges {
		hosts[ex.Request.Host]++
		if ex.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "Failed exchanges: %d\n", failed)
	}
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Slice(names, func(i, j int) bool {
		if hosts[names[i]] != hosts[names[j]] {
			return hosts[names[i]] > hosts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 0 {
		fmt.Fprintln(stdout, "Top hosts:")
	}
	for i, h := range names {
		if i == 10 {
			fmt.Fprintf(stdout, "  ... %d more\n", len(names)-10)
			break
		}
		fmt.Fprintf(stdout, "  %5d  %s\n", hosts[h], h)
	}
}
