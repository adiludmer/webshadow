package candidates

import (
	"sort"

	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Relation kinds.
const (
	// RelationContains is an entity whose responses nest another's list.
	RelationContains = "contains"
	// RelationReferences is an entity whose identifier requests carry
	// together with another's, the same pair every time.
	RelationReferences = "references"
)

// Cardinalities: how many targets one source relates to.
const (
	CardinalityOne  = "one"
	CardinalityMany = "many"
)

// minPairSupport is how many exchanges a value pair must be seen in
// before it counts toward a reference: one sighting of two values together
// is coincidence as often as it is a link.
const minPairSupport = 2

// RelationCandidate is a relation Go derived from structure: nesting
// inside one response, or identifiers that travel together.
type RelationCandidate struct {
	ID          string
	Kind        string
	From, To    string // entity ids
	Cardinality string
	// FieldPaths are the request slots or response paths the relation
	// rests on.
	FieldPaths []string
	Evidence   []string
	// Supporting counts the observations for the relation; Contradicting
	// counts source values seen with more than one target value.
	Supporting, Contradicting int
	// Families lists the families the relation was observed across.
	Families []string
}

// Relations derives the relations among entity candidates.
func Relations(in *input.Input, entities []EntityCandidate) []RelationCandidate {
	var out []RelationCandidate
	byID := map[string]*EntityCandidate{}
	for i := range entities {
		byID[entities[i].ID] = &entities[i]
	}
	for _, e := range entities {
		if e.Parent == "" {
			continue
		}
		p := byID[e.Parent]
		r := RelationCandidate{
			Kind: RelationContains, From: e.Parent, To: e.ID, Cardinality: CardinalityMany,
			FieldPaths: append([]string{}, e.Subjects...), Evidence: append([]string{}, e.Evidence...),
			Supporting: e.Observations, Families: append([]string{}, e.Families...),
		}
		if p != nil {
			r.Families = mergeSorted(r.Families, p.Families)
		}
		out = append(out, r)
	}
	out = append(out, references(in, entities)...)
	for i := range out {
		r := &out[i]
		r.ID = ir.NodeID(ir.RefRelation, r.Kind+"\x00"+r.From+"\x00"+r.To)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// references finds keyed entities whose identifiers requests carry
// together. A references B when every A value repeatedly seen with a B
// value was seen with only that one: a product and the parent product its
// requests always name.
func references(in *input.Input, entities []EntityCandidate) []RelationCandidate {
	slotEntity := map[string]string{}
	for _, e := range entities {
		if e.Kind != EntityKeyed {
			continue
		}
		for _, c := range e.Key.Consumers {
			slotEntity[c] = e.ID
		}
	}
	// seen[exchange][entity] holds the values and slots each exchange
	// carried for each entity.
	type sighting struct {
		values, slots map[string]bool
		family        string
	}
	seen := map[string]map[string]*sighting{}
	for _, l := range in.Links {
		if len(l.Flags) > 0 {
			continue
		}
		for _, o := range l.Locations {
			loc, consumer, ok := keyLocation(o.FamilyID, o.Location)
			ent := slotEntity[loc]
			if !ok || !consumer || ent == "" {
				continue
			}
			for _, ex := range o.Examples {
				k := ex.Key()
				if seen[k] == nil {
					seen[k] = map[string]*sighting{}
				}
				s := seen[k][ent]
				if s == nil {
					s = &sighting{values: map[string]bool{}, slots: map[string]bool{}, family: o.FamilyID}
					seen[k][ent] = s
				}
				s.values[l.Value] = true
				s.slots[loc] = true
			}
		}
	}

	type pairKey struct{ from, to string }
	type pairStats struct {
		support  map[[2]string]int // value pair -> exchanges
		slots    map[string]bool
		exchange []string
		families map[string]bool
	}
	pairs := map[pairKey]*pairStats{}
	exchanges := sortedKeys2(seen)
	for _, ex := range exchanges {
		ents := seen[ex]
		for a, sa := range ents {
			for b, sb := range ents {
				if a == b {
					continue
				}
				pk := pairKey{a, b}
				ps := pairs[pk]
				if ps == nil {
					ps = &pairStats{support: map[[2]string]int{}, slots: map[string]bool{}, families: map[string]bool{}}
					pairs[pk] = ps
				}
				for va := range sa.values {
					for vb := range sb.values {
						ps.support[[2]string{va, vb}]++
					}
				}
				for s := range sa.slots {
					ps.slots[s] = true
				}
				for s := range sb.slots {
					ps.slots[s] = true
				}
				ps.families[sa.family] = true
				ps.families[sb.family] = true
				ps.exchange = append(ps.exchange, ex)
			}
		}
	}

	var out []RelationCandidate
	for pk, ps := range pairs {
		targets := map[string]map[string]bool{} // A value -> B values seen with it
		supported := 0
		for pair, n := range ps.support {
			if targets[pair[0]] == nil {
				targets[pair[0]] = map[string]bool{}
			}
			targets[pair[0]][pair[1]] = true
			if n >= minPairSupport {
				supported++
			}
		}
		contradicting := 0
		for _, bs := range targets {
			if len(bs) > 1 {
				contradicting++
			}
		}
		if supported == 0 || contradicting > 0 {
			continue
		}
		// One-to-one pairs relate both ways; keep the direction from the
		// lower id so the relation is listed once.
		if rev := pairs[pairKey{pk.to, pk.from}]; rev != nil && functional(rev.support) && pk.to < pk.from {
			continue
		}
		ev := make([]string, 0, min(len(ps.exchange), maxEntityEvidence))
		for i, ex := range ps.exchange {
			if i == maxEntityEvidence {
				break
			}
			ev = append(ev, ir.Ref(ir.RefObservation, ex))
		}
		fams := map[string]bool{}
		for f := range ps.families {
			fams[ir.Ref(ir.RefFamily, f)] = true
		}
		out = append(out, RelationCandidate{
			Kind: RelationReferences, From: pk.from, To: pk.to, Cardinality: CardinalityOne,
			FieldPaths: sortedKeys(ps.slots), Evidence: ev,
			Supporting: supported, Families: sortedKeys(fams),
		})
	}
	return out
}

// functional reports whether every source value was seen with one target.
func functional(support map[[2]string]int) bool {
	targets := map[string]string{}
	for pair := range support {
		if t, ok := targets[pair[0]]; ok && t != pair[1] {
			return false
		}
		targets[pair[0]] = pair[1]
	}
	return true
}

func sortedKeys2[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mergeSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	return sortedKeys(set)
}
