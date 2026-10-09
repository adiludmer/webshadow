// Package route infers path templates from repeated observations. It never
// guesses from one URL: a position becomes a slot only when enough sibling
// paths, sharing everything else, put distinct values of one character
// class there. With too little evidence the route stays literal, and a later
// run over more recordings may generalize it.
//
// Inference runs over a set of paths, so the result depends only on which
// paths were seen, not on the order they arrived in.
package route

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// MinSlotValues is how many distinct values of one class must sit at a
// position, under one parent route, before that position becomes a slot.
// Two products would be a coincidence; three is a pattern. Part of the
// deterministic contract: changing it changes family identity.
const MinSlotValues = 3

// Character classes. A class describes the characters of a segment and
// nothing else: "id" means letters and digits mixed, not an identifier of
// anything.
const (
	ClassEmpty = "empty"
	ClassInt   = "int"
	ClassUUID  = "uuid"
	ClassID    = "id"
	ClassWord  = "word"
	ClassSlug  = "slug"
	ClassFile  = "file"
	ClassOther = "other"
)

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	intRe  = regexp.MustCompile(`^[0-9]+$`)
	idRe   = regexp.MustCompile(`^[0-9A-Za-z]+$`)
	wordRe = regexp.MustCompile(`^[A-Za-z]+$`)
	slugRe = regexp.MustCompile(`^[0-9A-Za-z]+([-_][0-9A-Za-z]+)+$`)
	fileRe = regexp.MustCompile(`^[^/]+\.([0-9A-Za-z]{1,5})$`)
)

// Classify returns the character class of a path segment. A file name is
// classed by its extension, so logo.png and hero.jpg are not siblings.
//
// There is deliberately no hex class: a mixed letter-and-digit token that
// happens to use only a-f would otherwise split from its siblings, so
// B0ABC12345 and B0XYZ12345 would land in different slots.
func Classify(seg string) string {
	switch {
	case seg == "":
		return ClassEmpty
	case intRe.MatchString(seg):
		return ClassInt
	case uuidRe.MatchString(seg):
		return ClassUUID
	case wordRe.MatchString(seg):
		return ClassWord
	case idRe.MatchString(seg):
		return ClassID
	case slugRe.MatchString(seg):
		return ClassSlug
	}
	if m := fileRe.FindStringSubmatch(seg); m != nil {
		return ClassFile + ":" + strings.ToLower(m[1])
	}
	return ClassOther
}

// slottable reports whether a class may ever become a slot. Plain words are
// how routes are named, so /products/search, /products/deals and
// /products/new stay three literal routes however many such words appear;
// an empty segment is a trailing slash, which is structure.
func slottable(class string) bool {
	return class != ClassWord && class != ClassEmpty
}

// Path is one observed path under its method and host.
type Path struct {
	Method   string
	Host     string
	Segments []string
}

// Router holds the inferred templates for a set of paths.
type Router struct {
	roots map[string]*node
}

type node struct {
	literals map[string]*node
	slots    map[string]*node
	end      bool
}

func newNode() *node {
	return &node{literals: map[string]*node{}, slots: map[string]*node{}}
}

func rootKey(method, host string) string { return method + " " + host }

// Infer builds templates for the given paths. Duplicates are harmless.
func Infer(paths []Path) *Router {
	r := &Router{roots: map[string]*node{}}
	for _, p := range paths {
		key := rootKey(p.Method, p.Host)
		n := r.roots[key]
		if n == nil {
			n = newNode()
			r.roots[key] = n
		}
		for _, seg := range p.Segments {
			child := n.literals[seg]
			if child == nil {
				child = newNode()
				n.literals[seg] = child
			}
			n = child
		}
		n.end = true
	}
	for _, key := range sortedKeys(r.roots) {
		generalize(r.roots[key])
	}
	return r
}

// generalize works top down. At each node, literal children of one
// slottable class are folded into a slot when there are at least
// MinSlotValues of them, their subtrees merged; then each remaining child is
// generalized the same way. Folding before descending is what lets a slug
// and an id that vary together (/{slug}/dp/{id}) both become slots: the
// slugs merge first, which brings all the ids under one parent.
func generalize(n *node) {
	byClass := map[string][]string{}
	for seg := range n.literals {
		if c := Classify(seg); slottable(c) {
			byClass[c] = append(byClass[c], seg)
		}
	}
	for _, class := range sortedKeys(byClass) {
		segs := byClass[class]
		if len(segs) < MinSlotValues {
			continue
		}
		sort.Strings(segs)
		slot := n.slots[class]
		if slot == nil {
			slot = newNode()
			n.slots[class] = slot
		}
		for _, seg := range segs {
			merge(slot, n.literals[seg])
			delete(n.literals, seg)
		}
	}
	for _, seg := range sortedKeys(n.literals) {
		generalize(n.literals[seg])
	}
	for _, class := range sortedKeys(n.slots) {
		generalize(n.slots[class])
	}
}

// merge folds src into dst. It is a union, so the result does not depend on
// the order siblings are folded in.
func merge(dst, src *node) {
	dst.end = dst.end || src.end
	for _, seg := range sortedKeys(src.literals) {
		if d := dst.literals[seg]; d != nil {
			merge(d, src.literals[seg])
		} else {
			dst.literals[seg] = src.literals[seg]
		}
	}
	for _, class := range sortedKeys(src.slots) {
		if d := dst.slots[class]; d != nil {
			merge(d, src.slots[class])
		} else {
			dst.slots[class] = src.slots[class]
		}
	}
}

// Template is the route a path matched.
type Template struct {
	Segments []model.RouteSegment
	// Positions maps each slot to the path segment index it sits at.
	Positions []int
}

// Match returns the template for a path. A path the router was not built
// from still gets a template: whatever its known prefix generalized to,
// then literal segments for the rest.
func (r *Router) Match(method, host string, segments []string) Template {
	var t Template
	n := r.roots[rootKey(method, host)]
	for i, seg := range segments {
		var next *node
		if n != nil {
			if lit := n.literals[seg]; lit != nil {
				next = lit
				t.Segments = append(t.Segments, model.RouteSegment{Literal: seg})
			} else if slot := n.slots[Classify(seg)]; slot != nil {
				next = slot
				t.Positions = append(t.Positions, i)
				t.Segments = append(t.Segments, model.RouteSegment{
					Slot:  "slot_" + strconv.Itoa(len(t.Positions)),
					Class: Classify(seg),
				})
			}
		}
		if next == nil {
			t.Segments = append(t.Segments, model.RouteSegment{Literal: seg})
		}
		n = next
	}
	return t
}

// Display renders the template for people: "/products/{slot_1}".
func (t Template) Display() string {
	return render(t.Segments, func(s model.RouteSegment) string { return "{" + s.Slot + "}" })
}

// Canonical renders the template for identity: slots by class, so that
// /a/{int} and /a/{id} are different routes while slot numbering stays out
// of the hash.
func (t Template) Canonical() string {
	return render(t.Segments, func(s model.RouteSegment) string { return "{" + s.Class + "}" })
}

func render(segs []model.RouteSegment, slot func(model.RouteSegment) string) string {
	if len(segs) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteByte('/')
		if s.Slot != "" {
			b.WriteString(slot(s))
		} else {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
