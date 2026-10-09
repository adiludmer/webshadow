package family

import (
	"sort"
	"strconv"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// slots describes where the family's requests carry values: the path
// template's slots, then every query key and every request body field. A
// query key or field whose value never changed is still listed, with a
// cardinality of one; whether a constant matters is for a later reader to
// judge, not for clustering to drop.
func slots(g *group) []model.Slot {
	var out []model.Slot
	for i, pos := range g.tmpl.Positions {
		seg := g.tmpl.Segments[slotSegment(g.tmpl.Segments, i)]
		acc := newAccumulator()
		for _, m := range g.members {
			if pos < len(m.obs.PathSegments) {
				acc.add(m.obs.PathSegments[pos], m.obs)
			}
		}
		out = append(out, acc.slot(seg.Slot, "path."+strconv.Itoa(pos), seg.Class))
	}

	type key struct{ part, pattern string }
	byLoc := map[key]*accumulator{}
	types := map[key]string{}
	for _, m := range g.members {
		for _, v := range m.obs.Values {
			if v.Loc.Side != model.SideRequest || (v.Loc.Part != model.PartQuery && v.Loc.Part != model.PartBody) {
				continue
			}
			k := key{v.Loc.Part, v.Loc.Pattern}
			acc := byLoc[k]
			if acc == nil {
				acc = newAccumulator()
				byLoc[k] = acc
			}
			acc.add(v.Value, m.obs)
			types[k] = mergeType(types[k], v.Type)
		}
	}
	keys := make([]key, 0, len(byLoc))
	for k := range byLoc {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].part != keys[j].part {
			// Query before body, matching the order a request is read in.
			return keys[i].part > keys[j].part
		}
		return keys[i].pattern < keys[j].pattern
	})
	for _, k := range keys {
		loc := k.part + "." + k.pattern
		out = append(out, byLoc[k].slot(loc, loc, types[k]))
	}
	return out
}

// slotSegment returns the index in segs of the i-th slot.
func slotSegment(segs []model.RouteSegment, i int) int {
	n := 0
	for j, s := range segs {
		if s.Slot == "" {
			continue
		}
		if n == i {
			return j
		}
		n++
	}
	return -1
}

func mergeType(a, b string) string {
	switch {
	case a == "" || a == b:
		return b
	case (a == model.TypeInteger || a == model.TypeNumber) && (b == model.TypeInteger || b == model.TypeNumber):
		return model.TypeNumber
	}
	return model.TypeAny
}

// accumulator collects the distinct values seen at one location and how
// many members carried it.
type accumulator struct {
	values  map[string]bool
	members map[string]bool
}

func newAccumulator() *accumulator {
	return &accumulator{values: map[string]bool{}, members: map[string]bool{}}
}

func (a *accumulator) add(value string, o *model.Observation) {
	a.values[value] = true
	a.members[o.Ref().Key()] = true
}

func (a *accumulator) slot(name, loc, typ string) model.Slot {
	return model.Slot{
		Name:         name,
		Location:     loc,
		Type:         typ,
		Observations: len(a.members),
		Cardinality:  len(a.values),
		Examples:     Examples(a.values, model.MaxExamples),
	}
}

// Examples picks up to n values in the order of their hashes. Hash order is
// arbitrary but fixed: it does not favour the first recording, the shortest
// value or the alphabet, so the sample stays diverse and is the same on
// every run.
func Examples(values map[string]bool, n int) []string {
	list := make([]string, 0, len(values))
	for v := range values {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		hi, hj := model.Hash("example", list[i]), model.Hash("example", list[j])
		if hi != hj {
			return hi < hj
		}
		return list[i] < list[j]
	})
	if len(list) > n {
		list = list[:n]
	}
	return list
}
