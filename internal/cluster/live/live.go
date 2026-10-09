// Package live renders clustering results of a growing recording as a
// sequence of text frames, so the families can be watched forming while the
// browsing happens. Each frame is a full redraw; the view remembers the
// previous result only to mark what changed.
package live

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Size bounds a frame.
type Size struct {
	Width, Height int
}

// Status is what the view shows about the recording itself.
type Status struct {
	Session   string
	Recording bool
	Elapsed   time.Duration
}

// View turns successive results into frames.
type View struct {
	// Color adds ANSI colors and bold.
	Color bool

	prev    map[string]bool   // family ids in the previous result
	prevObs map[string]string // observation key -> family id previously
	fresh   map[string]int    // family id -> frames left to mark it new
	events  []string

	seenFlows map[flowKey]bool
	flows     []shownFlow // the latest discovered, oldest first
}

type flowKey struct{ carrier, from, to, fromLoc, toLoc string }

type shownFlow struct {
	carrier, from, fromLoc, to, toLoc, value string
}

const (
	maxEvents  = 6
	freshFrame = 3
)

// Frame renders res and returns the frame's lines.
func (v *View) Frame(res *cluster.Result, st Status, size Size) []string {
	if size.Width < 60 {
		size.Width = 60
	}
	if size.Height < 20 {
		size.Height = 20
	}
	v.track(res)

	var lines []string
	add := func(s string) { lines = append(lines, clip(s, size.Width)) }
	state := "recording"
	if !st.Recording {
		state = "complete"
	}
	add(v.bold(fmt.Sprintf("webshadow cluster --follow %s", st.Session)) + fmt.Sprintf("   %s   %s", state, st.Elapsed.Round(time.Second)))
	c := res.Manifest.Counts
	add(fmt.Sprintf("%d exchanges · %d families (%d static) · %d value links · %d episodes · %d edges · %d value flows",
		c.Observations, c.Families, c.Static, c.Links, c.Episodes, c.Edges, c.Flows))
	add("")

	byID := map[string]*model.RequestFamily{}
	var fams []*model.RequestFamily
	staticObs := 0
	for i := range res.Families {
		f := &res.Families[i]
		byID[f.ID] = f
		if f.Static {
			staticObs += len(f.Observations)
			continue
		}
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool {
		a, b := fams[i], fams[j]
		if len(a.Observations) != len(b.Observations) {
			return len(a.Observations) > len(b.Observations)
		}
		return route(a) < route(b)
	})

	// Fixed rows: 3 header, 2 family header, 1 static, 1 blank, 2 flow
	// header and rows, 2 event header and rows.
	flowRows, eventRows := 5, maxEvents
	famRows := size.Height - 3 - 3 - 1 - (2 + flowRows) - (2 + eventRows)
	if famRows < 5 {
		famRows = 5
	}
	add(v.bold(fmt.Sprintf("  %-*s %5s %5s %s", size.Width-30, "FAMILIES", "OBS", "SLOTS", "RESPONSES")))
	for i, f := range fams {
		if i == famRows {
			add(fmt.Sprintf("  ... %d more", len(fams)-famRows))
			break
		}
		mark := "  "
		if v.fresh[f.ID] > 0 {
			mark = v.green("+ ")
		}
		add(fmt.Sprintf("%s%-*s %5d %5d %s", mark, size.Width-30, clip(route(f), size.Width-30),
			len(f.Observations), pathSlots(f), responses(f)))
	}
	add(v.dim(fmt.Sprintf("  static assets: %d families, %d requests", c.Static, staticObs)))
	add("")

	add(v.bold("  NEW VALUE FLOWS"))
	v.discover(res, byID, flowRows)
	if len(v.flows) == 0 {
		add(v.dim("  none yet"))
	}
	for i := len(v.flows) - 1; i >= 0; i-- {
		f := v.flows[i]
		add(fmt.Sprintf("  %-8s %s %s -> %s %s  %s", f.carrier, f.from, f.fromLoc, f.to, f.toLoc, v.dim(quote(f.value, 24))))
	}
	add("")
	add(v.bold("  CHANGES"))
	if len(v.events) == 0 {
		add(v.dim("  none yet"))
	}
	for _, e := range v.events {
		add("  " + e)
	}
	return lines
}

// discover keeps the n most recently discovered kinds of value flow: a
// carrier between two family locations seen for the first time. A flow that
// repeats on every request, such as a cookie, shows once, when it appears.
func (v *View) discover(res *cluster.Result, byID map[string]*model.RequestFamily, n int) {
	if v.seenFlows == nil {
		v.seenFlows = map[flowKey]bool{}
	}
	for _, f := range res.Flows {
		from, to := byID[f.FromFamily], byID[f.ToFamily]
		if from == nil || to == nil || literal(to, f.ToLoc) {
			continue
		}
		k := flowKey{f.Carrier, shortRoute(from), shortRoute(to), f.FromLoc.String(), f.ToLoc.String()}
		if v.seenFlows[k] {
			continue
		}
		v.seenFlows[k] = true
		v.flows = append(v.flows, shownFlow{f.Carrier, k.from, k.fromLoc, k.to, k.toLoc, f.Value})
		if len(v.flows) > n {
			v.flows = v.flows[len(v.flows)-n:]
		}
	}
}

// track compares res with the previous result and records new families and
// families that merged into a generalized one.
func (v *View) track(res *cluster.Result) {
	if v.fresh == nil {
		v.fresh = map[string]int{}
	}
	for id := range v.fresh {
		v.fresh[id]--
		if v.fresh[id] <= 0 {
			delete(v.fresh, id)
		}
	}
	cur := map[string]bool{}
	curObs := map[string]string{}
	for _, f := range res.Families {
		cur[f.ID] = true
		for _, r := range f.Observations {
			curObs[r.Key()] = f.ID
		}
	}
	if v.prev != nil {
		byID := map[string]*model.RequestFamily{}
		for i := range res.Families {
			byID[res.Families[i].ID] = &res.Families[i]
		}
		// A family that vanished had its observations regrouped; name the
		// family that holds them now.
		gone := map[string]map[string]bool{}
		absorbed := map[string]int{}
		for obs, old := range v.prevObs {
			now, ok := curObs[obs]
			if cur[old] || !ok {
				continue
			}
			if gone[now] == nil {
				gone[now] = map[string]bool{}
			}
			gone[now][old] = true
			absorbed[now]++
		}
		for _, id := range sortedKeys(gone) {
			f := byID[id]
			if f.Static {
				continue
			}
			v.event(fmt.Sprintf("%s regrouped %s (%s)", v.yellow(route(f)), plural(len(gone[id]), "family", "families"), plural(absorbed[id], "request", "requests")))
		}
		var added []*model.RequestFamily
		for i := range res.Families {
			f := &res.Families[i]
			if !v.prev[f.ID] && gone[f.ID] == nil && !f.Static {
				added = append(added, f)
			}
		}
		for _, f := range added {
			v.event(fmt.Sprintf("new family %s", v.green(route(f))))
		}
	}
	for id := range cur {
		if v.prev != nil && !v.prev[id] {
			v.fresh[id] = freshFrame
		}
	}
	v.prev, v.prevObs = cur, curObs
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (v *View) event(s string) {
	v.events = append(v.events, s)
	if len(v.events) > maxEvents {
		v.events = v.events[len(v.events)-maxEvents:]
	}
}

// literal reports a flow into a path position the family's template holds
// fixed, such as a page that mentions "images" before an image request. The
// flow is kept in the output; the live view leaves it out as uninteresting.
func literal(f *model.RequestFamily, loc model.Location) bool {
	if loc.Part != model.PartPath {
		return false
	}
	i, err := strconv.Atoi(loc.Pattern)
	return err == nil && i < len(f.Route) && f.Route[i].Slot == ""
}

func route(f *model.RequestFamily) string {
	r := f.Method + " " + f.Host + f.PathTemplate
	if len(f.QueryShape.Fields) > 0 {
		keys := make([]string, len(f.QueryShape.Fields))
		for i, k := range f.QueryShape.Fields {
			keys[i] = k.Name
		}
		r += "?" + strings.Join(keys, "&")
	}
	return r
}

func shortRoute(f *model.RequestFamily) string {
	return clip(f.Method+" "+f.PathTemplate, 28)
}

func pathSlots(f *model.RequestFamily) int {
	n := 0
	for _, s := range f.Route {
		if s.Slot != "" {
			n++
		}
	}
	return n
}

func responses(f *model.RequestFamily) string {
	var parts []string
	for i, v := range f.ResponseVariants {
		if i == 3 {
			parts = append(parts, "...")
			break
		}
		if v.Error != "" {
			parts = append(parts, "error")
			continue
		}
		parts = append(parts, fmt.Sprintf("%d", v.Status))
	}
	return strings.Join(parts, " ")
}

func quote(s string, n int) string {
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return fmt.Sprintf("%q", s)
}

// clip cuts s to n visible characters, not counting ANSI escapes.
func clip(s string, n int) string {
	var b strings.Builder
	visible, esc := 0, false
	for _, r := range s {
		switch {
		case esc:
			b.WriteRune(r)
			if r == 'm' {
				esc = false
			}
			continue
		case r == '\x1b':
			esc = true
			b.WriteRune(r)
			continue
		}
		if visible == n {
			if strings.Contains(s, "\x1b[") {
				b.WriteString("\x1b[0m")
			}
			return b.String()
		}
		b.WriteRune(r)
		visible++
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (v *View) style(code, s string) string {
	if !v.Color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (v *View) bold(s string) string   { return v.style("1", s) }
func (v *View) dim(s string) string    { return v.style("2", s) }
func (v *View) green(s string) string  { return v.style("32", s) }
func (v *View) yellow(s string) string { return v.style("33", s) }
