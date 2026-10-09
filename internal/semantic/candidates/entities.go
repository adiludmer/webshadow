package candidates

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Entity candidate kinds.
const (
	// EntityKeyed is an object named by an identifier that requests in
	// several slots carry: a key class.
	EntityKeyed = "keyed_object"
	// EntityItem is an element of an array of objects in a JSON response.
	EntityItem = "list_item"
)

// ChoiceNotAnObject is the naming choice that rejects a candidate: the
// values are tracking or request ids, or the objects are configuration or
// layout, not something a person would look for on the site.
const ChoiceNotAnObject = "not_an_object"

// Bounds on entity candidates.
const (
	maxNames          = 6
	maxEntityEvidence = 10
	minItemFields     = 3
)

// EntityCandidate is a kind of object Go found in the structure, with the
// naming question a model answers about it.
type EntityCandidate struct {
	ID   string
	Kind string
	// Key is the key class a keyed entity was built from.
	Key *KeyClass
	// Host and Container locate an item entity: the response path of its
	// array, such as "body.suggestions[]".
	Host      string
	Container string
	// Subjects are the evidence references the entity's hypotheses are
	// about: a keyed entity's request slots, an item entity's arrays.
	Subjects []string
	Families []string
	Identity []ir.FieldRef
	Fields   []ir.EntityField
	// Evidence lists what supports the entity's existence.
	Evidence []string
	// Observations counts the responses an item was seen in.
	Observations int
	// Parent is the item entity whose elements hold this one's array.
	Parent string
	// Names are the Go-derived display names, best first.
	Names []string
	Name  decide.Task
}

// Entities returns the entity candidates in a clustering result, one per
// key class and one per array of objects merged by host and response path,
// with the relations among them. Arrays in families listed in skip, such
// as ones settled as background traffic, are left out.
func Entities(in *input.Input, keys []KeyClass, skip map[string]bool) ([]EntityCandidate, []RelationCandidate) {
	families := map[string]*model.RequestFamily{}
	for i := range in.Families {
		families[in.Families[i].ID] = &in.Families[i]
	}
	var out []EntityCandidate
	for i := range keys {
		out = append(out, keyedEntity(in, families, &keys[i]))
	}
	out = append(out, itemEntities(in, skip)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	rels := Relations(in, out)
	objects := objectLike(out, rels)
	for i := range out {
		out[i].Name = nameTask(in, &out[i], objects[out[i].ID])
	}
	return out, rels
}

func keyedEntity(in *input.Input, families map[string]*model.RequestFamily, k *KeyClass) EntityCandidate {
	e := EntityCandidate{ID: k.ID, Kind: EntityKeyed, Key: k, Subjects: append([]string{}, k.Consumers...)}
	fams := map[string]bool{}
	for _, c := range k.Consumers {
		e.Identity = append(e.Identity, ir.FieldRef{Ref: c, Type: model.TypeString})
		fams[familyOf(c)] = true
	}
	fields := map[string]*ir.EntityField{}
	for _, p := range k.Producers {
		fam, loc := familyOf(p), locationOf(p)
		path, ok := strings.CutPrefix(loc, "response.")
		if !ok || !strings.HasPrefix(path, "body.") {
			continue
		}
		e.Identity = append(e.Identity, ir.FieldRef{Ref: p, Type: model.TypeString})
		f := families[fam]
		if f == nil {
			continue
		}
		fams[fam] = true
		// The object holding the identifier is the entity as that family
		// returns it; its scalar fields are the entity's fields.
		object := path[:strings.LastIndex(path, ".")]
		for _, v := range f.ResponseVariants {
			if s, ok := shapeAt(v.Shape, strings.TrimPrefix(object, "body")); ok {
				addFields(e.ID, fields, f.ID, v, object, s)
			}
		}
	}
	e.Fields = sortedFields(fields)
	e.Families = sortedKeys(fams)
	e.Evidence = append([]string{}, k.Links...)
	e.Names = keyNames(families, k)
	return e
}

// itemEntities finds the arrays of objects in JSON responses. Arrays at
// the same response path on one host are one entity, whatever route
// returned them.
func itemEntities(in *input.Input, skip map[string]bool) []EntityCandidate {
	byKey := map[string]*EntityCandidate{}
	fields := map[string]map[string]*ir.EntityField{}
	fams := map[string]map[string]bool{}
	var order []string
	for i := range in.Families {
		f := &in.Families[i]
		if f.Static || skip[f.ID] {
			continue
		}
		for _, v := range f.ResponseVariants {
			walkArrays(v.Shape, "body", func(path string, elem model.Shape) {
				if scalarCount(elem) < minItemFields {
					return
				}
				key := f.Host + "\x00" + path
				e, ok := byKey[key]
				if !ok {
					e = &EntityCandidate{
						ID: ir.NodeID(ir.RefEntity, "item\x00"+key), Kind: EntityItem,
						Host: f.Host, Container: path,
					}
					byKey[key] = e
					fields[key] = map[string]*ir.EntityField{}
					fams[key] = map[string]bool{}
					order = append(order, key)
				}
				container := ir.Ref(ir.RefFamily, f.ID) + "#response." + path
				if !contains(e.Subjects, container) {
					e.Subjects = append(e.Subjects, container)
				}
				e.Evidence = append(e.Evidence, ir.Ref(ir.RefVariant, f.ID, v.ID))
				e.Observations += v.Count
				fams[key][f.ID] = true
				addFields(e.ID, fields[key], f.ID, v, path, elem)
			})
		}
	}
	out := make([]EntityCandidate, 0, len(order))
	for _, key := range order {
		e := byKey[key]
		e.Fields = sortedFields(fields[key])
		e.Families = sortedKeys(fams[key])
		sort.Strings(e.Subjects)
		sort.Strings(e.Evidence)
		out = append(out, *e)
	}
	// An array inside another array's elements belongs to that entity.
	for i := range out {
		best := ""
		for j := range out {
			p := out[j].Container + "."
			if i != j && out[i].Host == out[j].Host && strings.HasPrefix(out[i].Container, p) && len(p) > len(best) {
				best, out[i].Parent = p, out[j].ID
			}
		}
		out[i].Names = itemNames(out[i].Container)
	}
	return out
}

// walkArrays calls fn for every array of objects in a shape, with its
// path, such as "body.suggestions[]".
func walkArrays(s model.Shape, path string, fn func(string, model.Shape)) {
	switch s.Type {
	case model.TypeObject:
		for _, f := range s.Fields {
			walkArrays(f.Shape, path+"."+f.Name, fn)
		}
	case model.TypeArray:
		if s.Elem == nil {
			return
		}
		if s.Elem.Type == model.TypeObject {
			fn(path+"[]", *s.Elem)
		}
		walkArrays(*s.Elem, path+"[]", fn)
	}
}

// shapeAt follows a path such as ".entity" or ".suggestions[]" into a
// shape.
func shapeAt(s model.Shape, path string) (model.Shape, bool) {
	for path != "" {
		if rest, ok := strings.CutPrefix(path, "[]"); ok {
			if s.Type != model.TypeArray || s.Elem == nil {
				return model.Shape{}, false
			}
			s, path = *s.Elem, rest
			continue
		}
		if path[0] != '.' || s.Type != model.TypeObject {
			return model.Shape{}, false
		}
		path = path[1:]
		end := strings.IndexAny(path, ".[")
		if end < 0 {
			end = len(path)
		}
		name := path[:end]
		path = path[end:]
		found := false
		for _, f := range s.Fields {
			if f.Name == name {
				s, found = f.Shape, true
				break
			}
		}
		if !found {
			return model.Shape{}, false
		}
	}
	return s, true
}

func scalarCount(s model.Shape) int {
	n := 0
	for _, f := range s.Fields {
		if scalar(f.Shape.Type) {
			n++
		}
	}
	return n
}

func scalar(t string) bool {
	switch t {
	case model.TypeString, model.TypeInteger, model.TypeNumber, model.TypeBool, model.TypeNull:
		return true
	}
	return false
}

// addFields records an object's scalar fields as entity fields, read from
// one response variant.
func addFields(entity string, fields map[string]*ir.EntityField, family string, v model.ResponseVariant, object string, s model.Shape) {
	for _, f := range s.Fields {
		if !scalar(f.Shape.Type) {
			continue
		}
		ef := fields[f.Name]
		if ef == nil {
			ef = &ir.EntityField{ID: ir.NodeID(ir.RefField, entity+"\x00"+f.Name), Name: f.Name, Sources: []ir.FieldRef{}}
			fields[f.Name] = ef
		}
		t := f.Shape.Type
		if t == model.TypeNull {
			ef.Nulls += v.Count
		} else if ef.Type == "" || ef.Type == t {
			ef.Type = t
		} else {
			ef.Type = model.TypeAny
		}
		ef.Observations += v.Count
		ef.Sources = append(ef.Sources, ir.FieldRef{Ref: ir.Ref(ir.RefVariant, family, v.ID) + "#" + object + "." + f.Name, Type: t})
	}
}

func sortedFields(m map[string]*ir.EntityField) []ir.EntityField {
	out := make([]ir.EntityField, 0, len(m))
	for _, f := range m {
		if f.Type == "" {
			f.Type = model.TypeNull
		}
		sort.Slice(f.Sources, func(i, j int) bool { return f.Sources[i].Ref < f.Sources[j].Ref })
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// keyNames derives display names from where a key class's values sit: the
// query or body key that carries them, or the path segment before a path
// slot, singularized. More slots giving one name rank it higher.
func keyNames(families map[string]*model.RequestFamily, k *KeyClass) []string {
	votes := map[string]int{}
	vote := func(n string) {
		if n = cleanName(n); n != "" {
			votes[n]++
		}
	}
	for _, c := range k.Consumers {
		loc := strings.TrimPrefix(locationOf(c), "request.")
		part, pattern, _ := strings.Cut(loc, ".")
		if part != model.PartPath {
			vote(lastName(pattern))
			continue
		}
		f := families[familyOf(c)]
		i, err := strconv.Atoi(pattern)
		if f == nil || err != nil || i < 1 || i > len(f.Route) {
			continue
		}
		vote(singular(f.Route[i-1].Literal))
	}
	for _, p := range k.Producers {
		loc := locationOf(p)
		if strings.HasPrefix(loc, "response.body.") {
			vote(lastName(loc))
		}
	}
	return ranked(votes)
}

func itemNames(container string) []string {
	segs := strings.Split(strings.TrimSuffix(container, "[]"), ".")
	votes := map[string]int{}
	if n := cleanName(singular(segs[len(segs)-1])); n != "" {
		votes[n] = 2
	}
	if len(segs) > 2 {
		if n := cleanName(singular(strings.TrimSuffix(segs[len(segs)-2], "[]"))); n != "" {
			votes[n]++
		}
	}
	return ranked(votes)
}

func ranked(votes map[string]int) []string {
	names := make([]string, 0, len(votes))
	for n := range votes {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if votes[names[i]] != votes[names[j]] {
			return votes[names[i]] > votes[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > maxNames {
		names = names[:maxNames]
	}
	return names
}

// lastName is the last key of a pattern such as "events[].data.requestId".
func lastName(pattern string) string {
	pattern = strings.TrimSuffix(pattern, "[]")
	if i := strings.LastIndex(pattern, "."); i >= 0 {
		pattern = pattern[i+1:]
	}
	return strings.TrimSuffix(pattern, "[]")
}

// cleanName keeps a name a choice id can carry: letters, digits and
// underscores, starting with a letter, and not one of the fixed choices.
func cleanName(n string) string {
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9' && b.Len() > 0:
			b.WriteRune(r)
		case (r == '-' || r == '_') && b.Len() > 0:
			b.WriteByte('_')
		}
	}
	s := strings.TrimRight(b.String(), "_")
	if len(s) < 2 || len(s) > 40 || s == decide.ChoiceUnknown || s == ChoiceNotAnObject || strings.Contains(s, "redacted") {
		return ""
	}
	return s
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 4:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses") || strings.HasSuffix(s, "xes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 3:
		return s[:len(s)-1]
	}
	return s
}

// objectLike lists the entities whose structure marks them as objects
// rather than tracking or request ids: every list item, every key class a
// request path carries or a JSON response field returns, and every key
// class one of those references. The rest are offered as not an object
// first.
func objectLike(entities []EntityCandidate, rels []RelationCandidate) map[string]bool {
	out := map[string]bool{}
	for _, e := range entities {
		if e.Kind != EntityKeyed {
			out[e.ID] = true
			continue
		}
		for _, c := range e.Key.Consumers {
			if strings.HasPrefix(locationOf(c), "request.path.") {
				out[e.ID] = true
			}
		}
		for _, p := range e.Key.Producers {
			if strings.HasPrefix(locationOf(p), "response.body.") {
				out[e.ID] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, r := range rels {
			if r.Kind == RelationReferences && out[r.From] && !out[r.To] {
				out[r.To], changed = true, true
			}
		}
	}
	return out
}

// nameTask asks what to call an entity candidate, or whether it is an
// object at all.
func nameTask(in *input.Input, e *EntityCandidate, object bool) decide.Task {
	var choices []decide.Choice
	reject := decide.Choice{ID: ChoiceNotAnObject, Meaning: "a tracking, request or session id, or configuration or layout data, not an object a person would look for on the site"}
	if !object {
		reject.Why = "no request path carries these values, no JSON response field returns them, and no object's requests name them"
		choices = append(choices, reject)
	}
	for _, n := range e.Names {
		why := "the container key holding these objects"
		if e.Kind == EntityKeyed {
			why = "a key, path segment or response field carrying these values"
		}
		choices = append(choices, decide.Choice{ID: n, Meaning: "call it a " + n, Why: why})
	}
	if object {
		reject.Why = "any candidate may be tracking or configuration data"
		choices = append(choices, reject)
	}
	choices = append(choices, decide.Choice{ID: decide.ChoiceUnknown, Meaning: RoleMeanings[decide.ChoiceUnknown]})

	question := "These request slots carry values from one pool of identifiers. What kind of object do the identifiers name?"
	if e.Kind == EntityItem {
		question = "These responses hold a list of objects. What kind of object is each element?"
	}
	return decide.Task{
		ID:              "entity:" + strings.TrimPrefix(e.ID, ir.RefEntity+":") + ":" + decide.PromptVersion,
		Type:            decide.TypeNameEntity,
		Subject:         e.ID,
		EvidencePackRef: "EP-" + e.ID,
		Question:        question,
		Choices:         choices,
		Evidence:        entityEvidence(in, e),
	}
}

// entityEvidence lists what the model may cite about an entity candidate:
// the identity or arrays, its fields, and the families involved.
func entityEvidence(in *input.Input, e *EntityCandidate) []decide.Evidence {
	var items []decide.Evidence
	add := func(ref, text string) {
		if len(items) == maxEntityEvidence {
			return
		}
		if len(text) > maxEvidenceLen {
			text = text[:maxEvidenceLen] + "…"
		}
		items = append(items, decide.Evidence{ID: fmt.Sprintf("E%d", len(items)+1), Ref: ref, Text: text})
	}
	fieldList := func() string {
		names := make([]string, 0, len(e.Fields))
		for _, f := range e.Fields {
			names = append(names, f.Name+" "+f.Type)
		}
		return strings.Join(names, ", ")
	}
	if e.Kind == EntityKeyed {
		k := e.Key
		shape := k.Shape
		if shape == "" {
			shape = "mixed"
		}
		if len(k.Links) > 0 {
			add(k.Links[0], fmt.Sprintf("identifier pool: %d distinct values shaped %s, e.g. %s", k.Values, shape, examples(k.Examples)))
		}
		for _, c := range k.Consumers {
			add(c, fmt.Sprintf("carried by request %s at %s", route(in, familyOf(c)), locationOf(c)))
		}
		for _, p := range k.Producers {
			add(p, fmt.Sprintf("returned by %s at %s", route(in, familyOf(p)), locationOf(p)))
		}
		if len(e.Fields) > 0 {
			add(e.Fields[0].Sources[0].Ref, "the response object holding the identifier has fields: "+fieldList())
		}
		return items
	}
	for _, s := range e.Subjects {
		add(s, fmt.Sprintf("array %s in responses of %s", e.Container, route(in, familyOf(s))))
	}
	if len(e.Evidence) > 0 {
		add(e.Evidence[0], fmt.Sprintf("each element has fields: %s; seen in %d responses", fieldList(), e.Observations))
	}
	return items
}

// familyOf returns the family id of a reference such as
// "fam:<id>#request.path.2".
func familyOf(ref string) string {
	base, _, _ := strings.Cut(ref, "#")
	_, id, _ := ir.SplitRef(base)
	if fam, _, ok := strings.Cut(id, "/"); ok {
		return fam
	}
	return id
}

// locationOf returns the location after "#" in a reference.
func locationOf(ref string) string {
	_, loc, _ := strings.Cut(ref, "#")
	return loc
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
