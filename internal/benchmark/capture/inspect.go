package capture

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// Filter selects entries for an index listing.
type Filter struct {
	// Classes keeps only these classes; empty means DefaultIndexClasses.
	Classes []Class
	// Host keeps only entries whose host contains this substring.
	Host string
}

// DefaultIndexClasses are the classes worth an agent's attention: pages and
// data. Static assets and telemetry are listed only on request.
var DefaultIndexClasses = []Class{ClassDocument, ClassAPI, ClassUnknown}

// Select returns the entries that pass the filter, in capture order.
func (t *Trace) Select(f Filter) []Entry {
	classes := f.Classes
	if len(classes) == 0 {
		classes = DefaultIndexClasses
	}
	var out []Entry
	for _, e := range t.Entries {
		if !slices.Contains(classes, e.Class) {
			continue
		}
		if f.Host != "" && !strings.Contains(e.Host, strings.ToLower(f.Host)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// WriteSummary prints entry counts by class and the busiest hosts.
func WriteSummary(w io.Writer, t *Trace) {
	byClass := map[Class]int{}
	bodies := map[Class]int64{}
	byHost := map[string]int{}
	for _, e := range t.Entries {
		byClass[e.Class]++
		bodies[e.Class] += int64(len(e.ResponseBody))
		byHost[e.Host]++
	}
	fmt.Fprintf(w, "%d entries\n\n", len(t.Entries))
	fmt.Fprintf(w, "%-12s %7s %12s\n", "CLASS", "ENTRIES", "BODY BYTES")
	for _, c := range Classes {
		if byClass[c] > 0 {
			fmt.Fprintf(w, "%-12s %7d %12d\n", c, byClass[c], bodies[c])
		}
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if byHost[hosts[i]] != byHost[hosts[j]] {
			return byHost[hosts[i]] > byHost[hosts[j]]
		}
		return hosts[i] < hosts[j]
	})
	if len(hosts) > 10 {
		hosts = hosts[:10]
	}
	fmt.Fprintf(w, "\n%-40s %7s\n", "HOST", "ENTRIES")
	for _, h := range hosts {
		fmt.Fprintf(w, "%-40s %7d\n", h, byHost[h])
	}
}

// WriteIndex prints one line per entry: sequence, method, status, class,
// body size and normalized URL.
func WriteIndex(w io.Writer, entries []Entry) {
	fmt.Fprintf(w, "%5s %-7s %6s %-10s %9s  %s\n", "SEQ", "METHOD", "STATUS", "CLASS", "BODY", "URL")
	for _, e := range entries {
		fmt.Fprintf(w, "%5d %-7s %6d %-10s %9d  %s\n", e.Sequence, e.Method, e.Status, e.Class, len(e.ResponseBody), NormalizeURL(e.URL, DefaultVolatileKeys))
	}
}

// WriteKeptNames prints every distinct header and query parameter name
// left in the trace, so a reviewer can spot sensitive names the sanitizer
// does not know about.
func WriteKeptNames(w io.Writer, t *Trace) {
	headers := map[string]bool{}
	params := map[string]bool{}
	for _, e := range t.Entries {
		for _, h := range e.RequestHeaders {
			headers[strings.ToLower(h.Name)] = true
		}
		for _, h := range e.ResponseHeaders {
			headers[strings.ToLower(h.Name)] = true
		}
		for k := range e.Query {
			params[k] = true
		}
	}
	fmt.Fprintf(w, "header names: %s\n", strings.Join(sortedKeys(headers), ", "))
	fmt.Fprintf(w, "query parameter names: %s\n", strings.Join(sortedKeys(params), ", "))
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
