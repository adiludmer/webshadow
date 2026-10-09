// Package sequence aggregates traces into sequence edges: for each pair of
// families, how often the first was observed before the second, how often
// the second occurred without it, how far apart they were, and which values
// flowed between them.
//
// A predecessor counts if it occurred anywhere earlier in the same session,
// however long before, so a setup request made once and relied on for the
// rest of the session is not lost. Nothing is pruned: every pair observed in
// order at least once becomes an edge, static assets included, and a later
// reader decides what is noise.
package sequence

import (
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/stateflow"
	"github.com/adiludmer/webshadow/internal/cluster/values"
)

type pair struct{ from, to string }

type edge struct {
	ordered   int
	immediate int
	deltas    []int64
	flows     map[flowKey]*flowAgg
}

type flowKey struct {
	carrier  string
	from, to model.Location
}

type flowAgg struct {
	to       map[string]bool
	values   map[string]bool
	examples []model.FlowExample
}

// Aggregate builds the edges of all traces. flows are the stateflow
// instances of those traces; ix supplies value flags.
func Aggregate(traces []model.Trace, flows []stateflow.Flow, ix *values.Index) []model.SequenceEdge {
	traces = append([]model.Trace(nil), traces...)
	sort.Slice(traces, func(i, j int) bool { return traces[i].SessionID < traces[j].SessionID })

	edges := map[pair]*edge{}
	get := func(p pair) *edge {
		e := edges[p]
		if e == nil {
			e = &edge{flows: map[flowKey]*flowAgg{}}
			edges[p] = e
		}
		return e
	}
	counts := make([]map[string]int, len(traces))
	total := map[string]int{}

	for t, tr := range traces {
		counts[t] = map[string]int{}
		last := map[string]int64{}
		prev := ""
		for j, s := range tr.Steps {
			b := s.FamilyID
			if b == "" {
				continue
			}
			for a, ta := range last {
				e := get(pair{a, b})
				e.ordered++
				e.deltas = append(e.deltas, s.T-ta)
			}
			if j > 0 && prev != "" {
				get(pair{prev, b}).immediate++
			}
			last[b] = s.T
			prev = b
			counts[t][b]++
			total[b]++
		}
	}

	ordered := append([]stateflow.Flow(nil), flows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.To != b.To {
			return a.To.Less(b.To)
		}
		if a.From != b.From {
			return a.From.Less(b.From)
		}
		if a.Carrier != b.Carrier {
			return a.Carrier < b.Carrier
		}
		if a.FromLoc != b.FromLoc {
			return a.FromLoc.String() < b.FromLoc.String()
		}
		if a.ToLoc != b.ToLoc {
			return a.ToLoc.String() < b.ToLoc.String()
		}
		return a.Value < b.Value
	})
	for _, f := range ordered {
		if f.FromFamily == "" || f.ToFamily == "" {
			continue
		}
		// The source preceded the receiver in its trace, so the edge exists.
		e := get(pair{f.FromFamily, f.ToFamily})
		k := flowKey{f.Carrier, f.FromLoc, f.ToLoc}
		agg := e.flows[k]
		if agg == nil {
			agg = &flowAgg{to: map[string]bool{}, values: map[string]bool{}}
			e.flows[k] = agg
		}
		agg.to[f.To.Key()] = true
		agg.values[f.Value] = true
		if len(agg.examples) < model.MaxVariantExamples {
			agg.examples = append(agg.examples, model.FlowExample{From: f.From, To: f.To, Value: f.Value})
		}
	}

	out := make([]model.SequenceEdge, 0, len(edges))
	for p, e := range edges {
		if e.ordered == 0 {
			// Only a flow or adjacency could create an edge with no ordered
			// observation, and both imply one; guard against a mismatched
			// trace all the same.
			continue
		}
		se := model.SequenceEdge{
			ID:                 "se_" + model.Hash("edge", p.from+">"+p.to),
			FromFamily:         p.from,
			ToFamily:           p.to,
			ToObservations:     total[p.to],
			OrderedCount:       e.ordered,
			WithoutPredecessor: total[p.to] - e.ordered,
			Immediate:          e.immediate,
			Timing:             timing(e.deltas),
		}
		for t := range traces {
			ca, cb := counts[t][p.from], counts[t][p.to]
			if ca == 0 || cb == 0 || (p.from == p.to && cb < 2) {
				continue
			}
			se.SessionsWithBoth++
			se.ObservedTogether += cb
		}
		se.ValueFlows = valueFlows(e.flows, ix)
		out = append(out, se)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromFamily != out[j].FromFamily {
			return out[i].FromFamily < out[j].FromFamily
		}
		return out[i].ToFamily < out[j].ToFamily
	})
	return out
}

func timing(deltas []int64) model.Timing {
	if len(deltas) == 0 {
		return model.Timing{}
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	// The lower middle, so an even count still picks an observed gap.
	return model.Timing{Min: deltas[0], Median: deltas[(len(deltas)-1)/2], Max: deltas[len(deltas)-1]}
}

func valueFlows(flows map[flowKey]*flowAgg, ix *values.Index) []model.ValueFlow {
	out := make([]model.ValueFlow, 0, len(flows))
	for k, agg := range flows {
		vf := model.ValueFlow{
			Carrier:  k.carrier,
			From:     k.from,
			To:       k.to,
			Matches:  len(agg.to),
			Values:   len(agg.values),
			Flags:    sharedFlags(agg.values, ix),
			Examples: agg.examples,
		}
		out = append(out, vf)
	}
	// Most observations first: the flow that held most often leads.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Matches != b.Matches {
			return a.Matches > b.Matches
		}
		if a.Carrier != b.Carrier {
			return a.Carrier < b.Carrier
		}
		if a.From != b.From {
			return a.From.String() < b.From.String()
		}
		return a.To.String() < b.To.String()
	})
	return out
}

// sharedFlags returns the flags every value carries.
func sharedFlags(vals map[string]bool, ix *values.Index) []string {
	var shared []string
	first := true
	for _, v := range family.Examples(vals, len(vals)) {
		flags := ix.Flags(v)
		if first {
			shared, first = flags, false
			continue
		}
		var keep []string
		for _, f := range shared {
			for _, g := range flags {
				if f == g {
					keep = append(keep, f)
				}
			}
		}
		shared = keep
	}
	return shared
}
