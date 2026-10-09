package candidates

import (
	"crypto/sha256"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/privacy"
)

// KeyClass is an identity candidate: request slots in one or more families
// that carry values from one shared pool, and the response locations those
// values were also seen at. A product id that appears in a product path, a
// cart request body and a search page is one key class.
type KeyClass struct {
	ID string
	// Consumers are request slots, as "fam:<id>#request.<part>.<pattern>".
	Consumers []string
	// Producers are response locations the values were seen at.
	Producers []string
	// Values counts the distinct values; Examples holds a few, in hash
	// order.
	Values   int
	Examples []string
	// Links are the value links the class was built from.
	Links []string
	// HeldOut is the result of the held-out join test: the class's values
	// split in two by hash, each half joining slots on its own, and the
	// consumer pairs both halves join.
	HeldOut HeldOut
	// Shape is the character pattern every value shares, such as
	// "[A-Z0-9]{10}", or "" when they differ.
	Shape string
}

// HeldOut is the outcome of joining a key class's slots on each half of
// its values separately.
type HeldOut struct {
	HalfA, HalfB int // distinct values in each half
	// PairsA and PairsB count the consumer pairs each half joins on its
	// own; Reproduced counts the pairs both halves join.
	PairsA, PairsB, Reproduced int
}

// Passed reports whether some join proposed from one half reappeared in
// the other.
func (h HeldOut) Passed() bool { return h.Reproduced > 0 }

// minSharedValues is how many distinct values two compatible slots must
// share to be joined, unless the value they share is identifier-like.
const minSharedValues = 2

// KeyClasses finds the identity candidates in a clustering result. Values
// flagged low information or ubiquitous and redacted values never join
// anything; two slots join when their values share one identifier shape
// and they share at least two values, or one identifier-like value.
func KeyClasses(in *input.Input) []KeyClass {
	valueLocs := map[string][]keyOcc{} // value -> locations
	valueLinks := map[string]string{}
	for _, l := range in.Links {
		if len(l.Flags) > 0 || privacy.IsTag(l.Value) || strings.Contains(l.Value, "{{redacted:") {
			continue
		}
		seen := map[string]bool{}
		for _, o := range l.Locations {
			loc, consumer, ok := keyLocation(o.FamilyID, o.Location)
			if !ok || seen[loc] {
				continue
			}
			seen[loc] = true
			valueLocs[l.Value] = append(valueLocs[l.Value], keyOcc{loc, consumer})
		}
		valueLinks[l.Value] = l.ID
	}

	// shared[a][b] lists the values consumer slots a and b share; slotValues
	// lists every linked value each slot carried.
	shared := map[string]map[string][]string{}
	slotValues := map[string][]string{}
	for v, occs := range valueLocs {
		var cons []string
		for _, o := range occs {
			if o.consumer {
				cons = append(cons, o.loc)
				slotValues[o.loc] = append(slotValues[o.loc], v)
			}
		}
		for i := range cons {
			for j := range cons {
				if i == j {
					continue
				}
				if shared[cons[i]] == nil {
					shared[cons[i]] = map[string][]string{}
				}
				shared[cons[i]][cons[j]] = append(shared[cons[i]][cons[j]], v)
			}
		}
	}
	shapes := map[string]shape{}
	for slot, vals := range slotValues {
		shapes[slot] = shapeOf(vals)
	}
	// Two slots join only when each carries values of one identifier shape,
	// the shapes agree, and they share an identifier-like value or two
	// values. Free text and mixed slots never join: a search word or a
	// request id chains unrelated slots through the odd coincidence.
	joined := func(a, b string, vals []string) bool {
		sa, sb := shapes[a], shapes[b]
		if !sa.compatible(sb) {
			return false
		}
		return len(vals) >= minSharedValues || idLike(vals[0])
	}

	uf := newUnionFind()
	for a, row := range shared {
		for b, vals := range row {
			if joined(a, b, vals) {
				uf.union(a, b)
			}
		}
	}
	groups := map[string][]string{}
	for a := range shared {
		root := uf.find(a)
		groups[root] = append(groups[root], a)
	}

	var out []KeyClass
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		sort.Strings(members)
		inClass := map[string]bool{}
		for _, m := range members {
			inClass[m] = true
		}
		var values []string
		producers := map[string]bool{}
		links := map[string]bool{}
		for v, occs := range valueLocs {
			hit := false
			for _, o := range occs {
				if o.consumer && inClass[o.loc] {
					hit = true
				}
			}
			if !hit {
				continue
			}
			values = append(values, v)
			links[ir.Ref(ir.RefValueLink, valueLinks[v])] = true
			for _, o := range occs {
				if !o.consumer {
					producers[o.loc] = true
				}
			}
		}
		sort.Slice(values, func(i, j int) bool { return hashOrder(values[i]) < hashOrder(values[j]) })
		k := KeyClass{
			Consumers: members,
			Producers: sortedKeys(producers),
			Values:    len(values),
			Links:     sortedKeys(links),
			Shape:     shapeOf(values).String(),
		}
		k.Examples = append([]string{}, values[:min(3, len(values))]...)
		k.HeldOut = heldOut(values, classLocations(valueLocs, inClass))
		k.ID = ir.NodeID(ir.RefEntity, "key\x00"+strings.Join(members, ","))
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// keyOcc is one location a value was seen at.
type keyOcc struct {
	loc      string
	consumer bool
}

// classLocations narrows each value's locations to the class's consumers.
func classLocations(all map[string][]keyOcc, inClass map[string]bool) map[string][]string {
	out := map[string][]string{}
	for v, occs := range all {
		for _, o := range occs {
			if o.consumer && inClass[o.loc] {
				out[v] = append(out[v], o.loc)
			}
		}
	}
	return out
}

// heldOut splits the values by hash parity and joins the class's slots on
// each half alone. A pair of slots joined by both halves is a join that
// reproduced on observations it was not proposed from.
func heldOut(values []string, locs map[string][]string) HeldOut {
	var h HeldOut
	pairs := [2]map[string]bool{{}, {}}
	for _, v := range values {
		half := int(sha256.Sum256([]byte(v))[0] & 1)
		if half == 0 {
			h.HalfA++
		} else {
			h.HalfB++
		}
		ls := locs[v]
		for i := range ls {
			for j := i + 1; j < len(ls); j++ {
				a, b := ls[i], ls[j]
				if b < a {
					a, b = b, a
				}
				pairs[half][a+"|"+b] = true
			}
		}
	}
	h.PairsA, h.PairsB = len(pairs[0]), len(pairs[1])
	for p := range pairs[0] {
		if pairs[1][p] {
			h.Reproduced++
		}
	}
	return h
}

// keyLocation renders where a value sat as a key-class location. Request
// path, query and body slots consume values; response bodies, text and
// redirect targets produce them. Headers and cookies are transport, not
// identity, and are left out.
func keyLocation(family string, loc model.Location) (string, bool, bool) {
	ref := ir.Ref(ir.RefFamily, family) + "#" + loc.String()
	switch {
	case loc.Side == model.SideRequest && (loc.Part == model.PartPath || loc.Part == model.PartQuery || loc.Part == model.PartBody):
		return ref, true, true
	case loc.Side == model.SideResponse && (loc.Part == model.PartBody || loc.Part == model.PartText || loc.Part == model.PartLocation):
		return ref, false, true
	}
	return "", false, false
}

// idLike reports whether a value looks like an identifier: six or more
// characters, letters and digits mixed, no spaces.
func idLike(v string) bool {
	if len(v) < 6 || strings.ContainsAny(v, " \t\n") {
		return false
	}
	var letter, digit bool
	for _, r := range v {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return letter && digit
}

// shape is a slot's value fingerprint: one length and the character
// classes its values use. The zero shape means the values differ in length
// or use characters beyond letters and digits.
type shape struct {
	n     int
	class uint8 // bit 0 upper, 1 lower, 2 digit
}

func shapeOf(values []string) shape {
	if len(values) == 0 {
		return shape{}
	}
	s := shape{n: len(values[0])}
	for _, v := range values {
		if len(v) != s.n {
			return shape{}
		}
		for _, r := range v {
			switch {
			case r >= 'A' && r <= 'Z':
				s.class |= 1
			case r >= 'a' && r <= 'z':
				s.class |= 2
			case r >= '0' && r <= '9':
				s.class |= 4
			default:
				return shape{}
			}
		}
	}
	return s
}

// String renders the shape as a pattern such as "[A-Z0-9]{10}", or "" for
// the zero shape.
func (s shape) String() string {
	if s.n == 0 {
		return ""
	}
	class := ""
	if s.class&1 != 0 {
		class += "A-Z"
	}
	if s.class&2 != 0 {
		class += "a-z"
	}
	if s.class&4 != 0 {
		class += "0-9"
	}
	return "[" + class + "]{" + strconv.Itoa(s.n) + "}"
}

// compatible reports whether two slots can hold values from one pool: same
// length, and one slot's character classes within the other's, so a slot
// that only saw digit-only product ids still joins one that saw mixed ones.
func (s shape) compatible(o shape) bool {
	if s.n < 6 || s.n != o.n {
		return false
	}
	return s.class&o.class == s.class || s.class&o.class == o.class
}

func hashOrder(v string) string {
	sum := sha256.Sum256([]byte(v))
	return string(sum[:8])
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type unionFind map[string]string

func newUnionFind() unionFind { return unionFind{} }

func (u unionFind) find(a string) string {
	for {
		p, ok := u[a]
		if !ok || p == a {
			return a
		}
		if gp, ok := u[p]; ok {
			u[a] = gp
		}
		a = p
	}
}

func (u unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u[rb] = ra
}
