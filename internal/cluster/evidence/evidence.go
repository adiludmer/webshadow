// Package evidence projects the clustering output into bounded per-family
// entries, so a later reader can judge one family without loading every
// trace: what its requests look like, which values it shares with other
// families, which families tend to come before and after it, which values
// flowed along those edges, and what the browser was doing when it ran.
//
// The pack is a view. It drops nothing from the underlying output, which is
// written next to it; it only bounds what one entry shows and refers back by
// id. Static asset families are collapsed into a counts-only list. Values
// flagged low information or ubiquitous are hidden unless they flowed
// consistently along an edge, and every entry says how much it left out.
package evidence

import (
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Options bound each entry.
type Options struct {
	// Neighbors bounds the predecessors and successors an entry lists.
	Neighbors int
	// Flows bounds the value flows listed per neighbor.
	Flows int
	// Links bounds the value links an entry lists.
	Links int
	// Refs bounds the example observations an entry lists.
	Refs int
}

// DefaultOptions is what the clustering pass uses.
func DefaultOptions() Options {
	return Options{Neighbors: 5, Flows: 3, Links: 5, Refs: 3}
}

// Input is the clustering output a pack is built from.
type Input struct {
	Families  []model.RequestFamily
	Links     []model.ValueLink
	Traces    []model.Trace
	Episodes  []model.Episode
	Sequences []model.SequenceEdge
}

// Pack is the evidence for every family.
type Pack struct {
	Version  int     `json:"version"`
	ID       string  `json:"id"`
	Families []Entry `json:"families"`
	// Static lists the static asset families with their counts only.
	Static []StaticEntry `json:"static"`
}

// Entry is the bounded evidence for one non-static family.
type Entry struct {
	Family       string       `json:"family"`
	Request      Request      `json:"request"`
	Observations int          `json:"observations"`
	Sessions     int          `json:"sessions"`
	Slots        []model.Slot `json:"slots,omitempty"`
	Responses    []Response   `json:"responses"`
	ValueLinks   []Link       `json:"value_links,omitempty"`
	Predecessors []Neighbor   `json:"common_predecessors,omitempty"`
	Successors   []Neighbor   `json:"common_successors,omitempty"`
	Episodes     Episodes     `json:"episodes"`
	Refs         Refs         `json:"refs"`
	Omitted      Omitted      `json:"omitted"`
}

// Request is a family's request structure.
type Request struct {
	Method   string        `json:"method"`
	Host     string        `json:"host"`
	Path     string        `json:"path"`
	Query    []model.Field `json:"query,omitempty"`
	BodyKind string        `json:"body_kind"`
	Body     string        `json:"body,omitempty"`
}

// Response is one response variant without its examples.
type Response struct {
	Variant string `json:"variant"`
	Status  int    `json:"status,omitempty"`
	Media   string `json:"media,omitempty"`
	Error   string `json:"error,omitempty"`
	Shape   string `json:"shape"`
	Count   int    `json:"count"`
}

// Link is a value this family shares with other families.
type Link struct {
	Link  string   `json:"link"`
	Value string   `json:"value"`
	Type  string   `json:"type"`
	Flags []string `json:"flags,omitempty"`
	// Locations are where this family carried the value.
	Locations []string `json:"locations"`
	// Families are the other families that carried it.
	Families []string `json:"families"`
	Count    int      `json:"count"`
}

// Neighbor is one sequence edge seen from an entry's family.
type Neighbor struct {
	Family string `json:"family"`
	// Route is the neighbor's method, host and path template, so an entry
	// reads without looking the family up.
	Route string `json:"route"`
	Edge  string `json:"edge"`
	// Ordered of Of: for a predecessor, how many of this family's
	// observations had it earlier in their trace; for a successor, how many
	// of the successor's observations had this family earlier.
	Ordered            int    `json:"ordered"`
	Of                 int    `json:"of"`
	Immediate          int    `json:"immediate"`
	WithoutPredecessor int    `json:"without_predecessor"`
	SessionsWithBoth   int    `json:"sessions_with_both"`
	MedianDelta        int64  `json:"median_delta"`
	Flows              []Flow `json:"flows,omitempty"`
	HiddenFlows        int    `json:"hidden_flows,omitempty"`
}

// Flow is one value flow along an edge.
type Flow struct {
	Carrier string   `json:"carrier"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Matches int      `json:"matches"`
	Of      int      `json:"of"`
	Values  int      `json:"values"`
	Flags   []string `json:"flags,omitempty"`
	Example string   `json:"example,omitempty"`
}

// Episodes summarizes the browser context of a family's observations.
type Episodes struct {
	// Triggers counts observations by the type of their episode's trigger;
	// "untriggered" is traffic before a session's first trigger.
	Triggers []Count `json:"triggers"`
	// MedianSinceTrigger is the median gap from trigger to request over the
	// triggered observations, in nanoseconds.
	MedianSinceTrigger int64   `json:"median_since_trigger"`
	Initiators         []Count `json:"initiators,omitempty"`
}

// Count is one counted key.
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// Refs point back into the full output.
type Refs struct {
	Observations []model.ObservationRef `json:"observations"`
	Traces       []string               `json:"traces"`
	Episodes     int                    `json:"episodes"`
}

// Omitted says how much an entry left out.
type Omitted struct {
	Predecessors int `json:"predecessors,omitempty"`
	Successors   int `json:"successors,omitempty"`
	// StaticNeighbors counts static families left out of the neighbor
	// lists; their edges are still in the sequence output.
	StaticNeighbors int `json:"static_neighbors,omitempty"`
	Links           int `json:"links,omitempty"`
	// FlaggedLinks counts links hidden because their value was flagged.
	FlaggedLinks int `json:"flagged_links,omitempty"`
}

// StaticEntry is a static asset family reduced to counts.
type StaticEntry struct {
	Family       string `json:"family"`
	Method       string `json:"method"`
	Host         string `json:"host"`
	Path         string `json:"path"`
	Media        string `json:"media,omitempty"`
	Observations int    `json:"observations"`
}

const untriggered = "untriggered"

// Build makes the pack. It does not fill in Version or ID.
func Build(in Input, opts Options) Pack {
	if opts.Neighbors == 0 {
		opts = DefaultOptions()
	}
	static := map[string]bool{}
	routes := map[string]string{}
	for _, f := range in.Families {
		if f.Static {
			static[f.ID] = true
		}
		routes[f.ID] = f.Method + " " + f.Host + f.PathTemplate
	}

	preds := map[string][]model.SequenceEdge{}
	succs := map[string][]model.SequenceEdge{}
	for _, e := range in.Sequences {
		if e.FromFamily == e.ToFamily {
			continue
		}
		preds[e.ToFamily] = append(preds[e.ToFamily], e)
		succs[e.FromFamily] = append(succs[e.FromFamily], e)
	}
	links := map[string][]model.ValueLink{}
	for _, l := range in.Links {
		for _, f := range l.FamilyIDs {
			links[f] = append(links[f], l)
		}
	}
	ctx := browserContext(in)

	pack := Pack{Families: []Entry{}, Static: []StaticEntry{}}
	for _, f := range in.Families {
		if f.Static {
			s := StaticEntry{Family: f.ID, Method: f.Method, Host: f.Host, Path: f.PathTemplate, Observations: len(f.Observations)}
			if len(f.ResponseVariants) > 0 {
				s.Media = f.ResponseVariants[0].Media
			}
			pack.Static = append(pack.Static, s)
			continue
		}
		e := Entry{
			Family: f.ID,
			Request: Request{
				Method: f.Method, Host: f.Host, Path: f.PathTemplate,
				Query: f.QueryShape.Fields, BodyKind: f.BodyKind,
			},
			Observations: len(f.Observations),
			Slots:        f.Slots,
		}
		if f.BodyKind != model.BodyNone {
			e.Request.Body = f.RequestBody.Canonical()
		}
		sessions := map[string]bool{}
		for _, r := range f.Observations {
			sessions[r.SessionID] = true
		}
		e.Sessions = len(sessions)
		for _, v := range f.ResponseVariants {
			e.Responses = append(e.Responses, Response{
				Variant: v.ID, Status: v.Status, Media: v.Media, Error: v.Error,
				Shape: v.Shape.Canonical(), Count: v.Count,
			})
		}
		e.ValueLinks, e.Omitted.Links, e.Omitted.FlaggedLinks = familyLinks(f.ID, links[f.ID], opts.Links)
		e.Predecessors, e.Omitted.Predecessors = neighbors(preds[f.ID], true, static, routes, &e.Omitted.StaticNeighbors, opts)
		e.Successors, e.Omitted.Successors = neighbors(succs[f.ID], false, static, routes, &e.Omitted.StaticNeighbors, opts)
		c := ctx[f.ID]
		if c == nil {
			c = &familyContext{triggers: map[string]int{}, initiators: map[string]int{}, traces: map[string]bool{}, episodes: map[string]bool{}}
		}
		e.Episodes = Episodes{
			Triggers:           counts(c.triggers),
			MedianSinceTrigger: median(c.since),
			Initiators:         counts(c.initiators),
		}
		e.Refs = Refs{Observations: f.Observations, Traces: sortedKeys(c.traces), Episodes: len(c.episodes)}
		if len(e.Refs.Observations) > opts.Refs {
			e.Refs.Observations = e.Refs.Observations[:opts.Refs]
		}
		pack.Families = append(pack.Families, e)
	}
	return pack
}

type familyContext struct {
	triggers   map[string]int
	initiators map[string]int
	since      []int64
	traces     map[string]bool
	episodes   map[string]bool
}

func browserContext(in Input) map[string]*familyContext {
	trigger := map[string]string{}
	for _, ep := range in.Episodes {
		if ep.Trigger != nil {
			trigger[ep.ID] = ep.Trigger.Type
		} else {
			trigger[ep.ID] = untriggered
		}
	}
	out := map[string]*familyContext{}
	for _, tr := range in.Traces {
		for _, s := range tr.Steps {
			if s.FamilyID == "" {
				continue
			}
			c := out[s.FamilyID]
			if c == nil {
				c = &familyContext{triggers: map[string]int{}, initiators: map[string]int{}, traces: map[string]bool{}, episodes: map[string]bool{}}
				out[s.FamilyID] = c
			}
			t := trigger[s.EpisodeID]
			if t == "" {
				t = untriggered
			}
			c.triggers[t]++
			if t != untriggered {
				c.since = append(c.since, s.SinceTrigger)
			}
			if s.Initiator != "" {
				c.initiators[s.Initiator]++
			}
			c.traces[tr.ID] = true
			if s.EpisodeID != "" {
				c.episodes[s.EpisodeID] = true
			}
		}
	}
	return out
}

func flagged(flags []string) bool { return len(flags) > 0 }

// familyLinks lists the family's unflagged links, those shared with the
// most families first.
func familyLinks(id string, all []model.ValueLink, limit int) (shown []Link, omitted, hidden int) {
	var keep []model.ValueLink
	for _, l := range all {
		if flagged(l.Flags) {
			hidden++
			continue
		}
		keep = append(keep, l)
	}
	sort.Slice(keep, func(i, j int) bool {
		a, b := keep[i], keep[j]
		if len(a.FamilyIDs) != len(b.FamilyIDs) {
			return len(a.FamilyIDs) > len(b.FamilyIDs)
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.ID < b.ID
	})
	if len(keep) > limit {
		omitted = len(keep) - limit
		keep = keep[:limit]
	}
	for _, l := range keep {
		out := Link{Link: l.ID, Value: l.Value, Type: l.ValueType, Flags: l.Flags, Count: l.Count, Locations: []string{}, Families: []string{}}
		seen := map[string]bool{}
		for _, o := range l.Locations {
			if o.FamilyID == id {
				if s := o.Location.String(); !seen[s] {
					seen[s] = true
					out.Locations = append(out.Locations, s)
				}
			}
		}
		sort.Strings(out.Locations)
		for _, f := range l.FamilyIDs {
			if f != id {
				out.Families = append(out.Families, f)
			}
		}
		shown = append(shown, out)
	}
	return shown, omitted, hidden
}

// neighbors ranks an entry's non-static neighbors: those with a visible
// value flow first, then by how often the order held, then by adjacency.
func neighbors(edges []model.SequenceEdge, pred bool, static map[string]bool, routes map[string]string, staticOmitted *int, opts Options) ([]Neighbor, int) {
	var out []Neighbor
	for _, e := range edges {
		other := e.ToFamily
		if pred {
			other = e.FromFamily
		}
		if static[other] {
			*staticOmitted++
			continue
		}
		n := Neighbor{
			Family: other, Route: routes[other], Edge: e.ID,
			Ordered: e.OrderedCount, Of: e.ToObservations,
			Immediate: e.Immediate, WithoutPredecessor: e.WithoutPredecessor,
			SessionsWithBoth: e.SessionsWithBoth, MedianDelta: e.Timing.Median,
		}
		for _, vf := range e.ValueFlows {
			if flagged(vf.Flags) && !consistent(vf, e) {
				n.HiddenFlows++
				continue
			}
			if len(n.Flows) == opts.Flows {
				n.HiddenFlows++
				continue
			}
			f := Flow{
				Carrier: vf.Carrier, From: vf.From.String(), To: vf.To.String(),
				Matches: vf.Matches, Of: e.OrderedCount, Values: vf.Values, Flags: vf.Flags,
			}
			if len(vf.Examples) > 0 {
				f.Example = vf.Examples[0].Value
			}
			n.Flows = append(n.Flows, f)
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (len(a.Flows) > 0) != (len(b.Flows) > 0) {
			return len(a.Flows) > 0
		}
		if a.Ordered != b.Ordered {
			return a.Ordered > b.Ordered
		}
		if a.Immediate != b.Immediate {
			return a.Immediate > b.Immediate
		}
		return a.Family < b.Family
	})
	omitted := 0
	if len(out) > opts.Neighbors {
		omitted = len(out) - opts.Neighbors
		out = out[:opts.Neighbors]
	}
	return out, omitted
}

// consistent reports whether a flow held for nearly every ordered
// occurrence of its edge, which makes even a flagged value worth showing: a
// one-character value echoed every time is still a dependency signal.
func consistent(vf model.ValueFlow, e model.SequenceEdge) bool {
	return vf.Matches >= 2 && vf.Matches*10 >= e.OrderedCount*9
}

func counts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		out = append(out, Count{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	v = append([]int64(nil), v...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[(len(v)-1)/2]
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
