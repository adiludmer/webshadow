package candidates

import (
	"fmt"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// MergeSamePage is the one merge rule: families with the same role on
// one host whose paths start with the same literal and whose responses have
// the same shapes, such as three URL forms of one search page.
//
// Families are never merged because they carry the same identifier. A key
// in two requests does not make them one capability (a product page, its
// reviews and add-to-cart all take the product id), whether two families
// return the same object cannot be checked for HTML pages, and a model
// that keeps a tracking id as an entity would let it join unrelated pages.
// Operations that take the same entity stay separate and share its input
// type; a later stage that sees what each returns may merge them.
const MergeSamePage = "same_page"

// OpEntity is what operation synthesis needs to know about an entity the
// IR kept.
type OpEntity struct {
	ID, Name string
	// Verified is set when the entity's identity passed its checks.
	Verified bool
	// Consumers are the request slots carrying a keyed entity's id;
	// Producers the response locations returning it; Containers the arrays
	// holding a list item entity.
	Consumers, Producers, Containers []string
}

// MergeRecord is one merge decision: two families joined, or a pair that
// was considered and kept apart, and why.
type MergeRecord struct {
	A, B   string // family ids
	Rule   string // a merge rule, or "" when kept apart
	Detail string
}

// OperationCandidate is an agent-facing capability Go assembled from
// families that share a role.
type OperationCandidate struct {
	ID       string
	Role     string
	Families []string
	Merges   []MergeRecord
	Rejected []MergeRecord
	Inputs   []ir.InputField
	Outputs  []ir.OutputField
	// State lists request slots left out of the inputs because an earlier
	// response always supplied them.
	State    []string
	Evidence []string
	Names    []string
	Name     decide.Task
}

// Operations assembles operation candidates from families whose role is a
// read or a mutation. roles maps family ids to their standing role; fed
// lists request slots ("fam:<id>#request.<location>") that an earlier
// response filled on every request.
func Operations(in *input.Input, roles map[string]string, entities []OpEntity, fed map[string]bool) []OperationCandidate {
	families := map[string]*model.RequestFamily{}
	for i := range in.Families {
		families[in.Families[i].ID] = &in.Families[i]
	}
	var ids []string
	for id, role := range roles {
		if families[id] != nil && (role == RoleCollectionRead || role == RoleItemRead || role == RoleMutation) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	slotEntity := map[string]*OpEntity{}
	for i := range entities {
		for _, c := range entities[i].Consumers {
			slotEntity[c] = &entities[i]
		}
	}
	uf := newUnionFind()
	var merges, rejected []MergeRecord
	for i, a := range ids {
		uf.find(a)
		for _, b := range ids[i+1:] {
			if roles[a] != roles[b] {
				continue
			}
			fa, fb := families[a], families[b]
			if fa.Host != fb.Host {
				continue
			}
			why, ok := samePage(fa, fb)
			switch {
			case ok:
				uf.union(a, b)
				merges = append(merges, MergeRecord{A: a, B: b, Rule: MergeSamePage, Detail: why})
			case firstLiteral(fa) != "" && firstLiteral(fa) == firstLiteral(fb):
				// Only near misses are worth recording as kept apart.
				rejected = append(rejected, MergeRecord{A: a, B: b, Detail: why})
			}
		}
	}
	groups := map[string][]string{}
	for _, id := range ids {
		r := uf.find(id)
		groups[r] = append(groups[r], id)
	}

	var out []OperationCandidate
	for _, members := range groups {
		sort.Strings(members)
		in := map[string]bool{}
		for _, m := range members {
			in[m] = true
		}
		op := OperationCandidate{
			ID:       ir.NodeID(ir.RefOperation, strings.Join(members, ",")),
			Role:     roles[members[0]],
			Families: members,
		}
		for _, m := range merges {
			if in[m.A] {
				op.Merges = append(op.Merges, m)
			}
		}
		for _, m := range rejected {
			if (in[m.A] || in[m.B]) && uf.find(m.A) != uf.find(m.B) {
				op.Rejected = append(op.Rejected, m)
			}
		}
		op.Inputs, op.State = opInputs(families, members, slotEntity, fed)
		op.Outputs = opOutputs(families, members, op.Role, entities)
		for _, f := range members {
			op.Evidence = append(op.Evidence, ir.Ref(ir.RefFamily, f))
			for _, v := range families[f].ResponseVariants {
				op.Evidence = append(op.Evidence, ir.Ref(ir.RefVariant, f, v.ID))
			}
		}
		op.Names = opNames(families, &op, entities)
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := range out {
		out[i].Name = opNameTask(in, &out[i])
	}
	return out
}

// samePage reports whether two families on one host are the same page in
// different URL forms: the same first path literal and the same response
// shapes. The reason is returned either way.
func samePage(a, b *model.RequestFamily) (string, bool) {
	la, lb := firstLiteral(a), firstLiteral(b)
	if la == "" || la != lb {
		return fmt.Sprintf("paths start differently (%q, %q)", la, lb), false
	}
	va, vb := variantSet(a), variantSet(b)
	if va != vb {
		return "responses differ in shape", false
	}
	return fmt.Sprintf("paths start with %q and responses have the same shapes", la), true
}

// firstLiteral is a family's first fixed path segment. Leading slots are
// skipped: in /{slug}/dp/{asin} the page is "dp", not the product's slug.
func firstLiteral(f *model.RequestFamily) string {
	for _, seg := range f.Route {
		if seg.Literal != "" {
			return seg.Literal
		}
	}
	return ""
}

func variantSet(f *model.RequestFamily) string {
	ids := make([]string, 0, len(f.ResponseVariants))
	for _, v := range f.ResponseVariants {
		if v.Status >= 200 && v.Status < 300 {
			ids = append(ids, v.ID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func slotRef(family string, s model.Slot) string {
	return ir.Ref(ir.RefFamily, family) + "#request." + s.Location
}

// opInputs turns the member families' varying request slots into inputs.
// A slot carrying a verified entity's id becomes that entity's input; the
// rest are grouped by name. A slot seen once joins an input other families
// show varies; otherwise constant slots, redacted ones and ones an earlier
// response filled every time are not inputs, and the last are listed as
// state. Typed text that the families carry under different names, never
// together, is one input: the same search box behind two URL forms.
func opInputs(families map[string]*model.RequestFamily, members []string, slotEntity map[string]*OpEntity, fed map[string]bool) ([]ir.InputField, []string) {
	type acc struct {
		field    ir.InputField
		types    map[string]bool
		perFam   map[string]bool
		minimal  bool // present in every observation of its families
		words    bool // every slot is typed text
		observed int
	}
	inputs := map[string]*acc{}
	var state []string
	nameOf := func(id string, s model.Slot) string {
		name := s.Name
		if strings.HasPrefix(name, "slot_") || strings.HasPrefix(name, "query.") || strings.HasPrefix(name, "body.") {
			name = lastName(s.Location)
		}
		if e := slotEntity[slotRef(id, s)]; e != nil && e.Verified {
			name = e.Name
		}
		return inputName(name)
	}
	add := func(id string, s model.Slot, name string) {
		f := families[id]
		a := inputs[name]
		if a == nil {
			a = &acc{field: ir.InputField{Name: name}, types: map[string]bool{}, perFam: map[string]bool{}, minimal: true, words: true}
			inputs[name] = a
		}
		a.types[s.Type] = true
		a.perFam[id] = true
		a.observed += s.Observations
		a.minimal = a.minimal && s.Observations >= len(f.Observations)
		a.words = a.words && s.Type == model.TypeString && wordy(s.Examples)
		a.field.Slots = append(a.field.Slots, ir.FieldRef{Ref: slotRef(id, s), Type: s.Type})
	}
	var once [][2]any
	for _, id := range members {
		for _, s := range families[id].Slots {
			ref := slotRef(id, s)
			path := strings.HasPrefix(s.Location, "path.")
			name := nameOf(id, s)
			switch {
			case name == "" || redacted(s.Examples):
			case fed[ref]:
				state = append(state, ref)
			case s.Cardinality >= 2 || (path && slotEntity[ref] != nil):
				add(id, s, name)
			case s.Observations == 1:
				once = append(once, [2]any{id, s})
			}
		}
	}
	for _, o := range once {
		id, s := o[0].(string), o[1].(model.Slot)
		if name := nameOf(id, s); inputs[name] != nil {
			add(id, s, name)
		}
	}

	// Join typed-text inputs that never share a family, most observed
	// first; the joined input keeps the most observed name.
	names := sortedKeys2(inputs)
	sort.SliceStable(names, func(i, j int) bool { return inputs[names[i]].observed > inputs[names[j]].observed })
	for i, n := range names {
		a := inputs[n]
		if a == nil || !a.words {
			continue
		}
		for _, m := range names[i+1:] {
			b := inputs[m]
			if b == nil || !b.words || overlapsSet(a.perFam, b.perFam) {
				continue
			}
			a.field.Slots = append(a.field.Slots, b.field.Slots...)
			for t := range b.types {
				a.types[t] = true
			}
			for f := range b.perFam {
				a.perFam[f] = true
			}
			a.minimal = a.minimal && b.minimal
			a.observed += b.observed
			delete(inputs, m)
		}
	}

	out := make([]ir.InputField, 0, len(inputs))
	for _, name := range sortedKeys2(inputs) {
		a := inputs[name]
		a.field.Type = strings.Join(sortedKeys(a.types), "|")
		a.field.Required = a.minimal && len(a.perFam) == len(members)
		sort.Slice(a.field.Slots, func(i, j int) bool { return a.field.Slots[i].Ref < a.field.Slots[j].Ref })
		out = append(out, a.field)
	}
	sort.Strings(state)
	return out, state
}

// wordy reports whether every example looks like typed text: lowercase
// letters, no uppercase ones, no punctuation beyond spaces and hyphens.
func wordy(examples []string) bool {
	if len(examples) == 0 {
		return false
	}
	for _, e := range examples {
		lower := false
		for _, r := range e {
			switch {
			case r >= 'a' && r <= 'z':
				lower = true
			case r >= '0' && r <= '9', r == ' ', r == '-':
			default:
				return false
			}
		}
		if !lower {
			return false
		}
	}
	return true
}

func overlapsSet(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// inputName keeps an input name a person can read: letters, digits and
// underscores. Unlike entity names, one letter is enough, as in "k".
func inputName(n string) string {
	if len(n) == 1 && (n[0] >= 'a' && n[0] <= 'z' || n[0] >= 'A' && n[0] <= 'Z') {
		return n
	}
	return cleanName(n)
}

func redacted(examples []string) bool {
	for _, e := range examples {
		if strings.Contains(e, "{{redacted:") {
			return true
		}
	}
	return false
}

// opOutputs lists what the member families return: entities whose ids
// their responses carry, lists of item entities, and otherwise the raw
// response.
func opOutputs(families map[string]*model.RequestFamily, members []string, role string, entities []OpEntity) []ir.OutputField {
	in := map[string]bool{}
	for _, m := range members {
		in[m] = true
	}
	outputs := map[string]*ir.OutputField{}
	add := func(e *OpEntity, many bool, src ir.FieldRef) {
		o := outputs[e.ID]
		if o == nil {
			o = &ir.OutputField{Name: e.Name, Type: "entity", Entity: e.ID}
			outputs[e.ID] = o
		}
		o.Many = o.Many || many
		o.Sources = append(o.Sources, src)
	}
	for i := range entities {
		e := &entities[i]
		for _, p := range e.Producers {
			if in[familyOf(p)] {
				loc := locationOf(p)
				many := role == RoleCollectionRead || strings.Contains(loc, "[]")
				add(e, many, ir.FieldRef{Ref: p, Type: model.TypeString})
			}
		}
		for _, c := range e.Containers {
			if in[familyOf(c)] {
				add(e, true, ir.FieldRef{Ref: c, Type: model.TypeArray})
			}
		}
	}
	out := make([]ir.OutputField, 0, len(outputs)+1)
	for _, id := range sortedKeys2(outputs) {
		out = append(out, *outputs[id])
	}
	if len(out) == 0 {
		raw := ir.OutputField{Name: "response"}
		media := map[string]bool{}
		for _, m := range members {
			for _, v := range families[m].ResponseVariants {
				raw.Sources = append(raw.Sources, ir.FieldRef{Ref: ir.Ref(ir.RefVariant, m, v.ID), Type: v.Shape.Type})
				if v.Media != "" {
					media[v.Media] = true
				}
			}
		}
		raw.Type = strings.Join(sortedKeys(media), "|")
		if raw.Type == "" {
			raw.Type = model.TypeOpaque
		}
		out = append(out, raw)
	}
	return out
}

// opNames derives names from the role, the entities the operation returns
// or takes, and the path literals of its families.
func opNames(families map[string]*model.RequestFamily, op *OperationCandidate, entities []OpEntity) []string {
	verbs := map[string][]string{
		RoleCollectionRead: {"search", "list"},
		RoleItemRead:       {"get"},
	}[op.Role]
	var objects []string
	for _, o := range op.Outputs {
		if o.Entity != "" {
			objects = append(objects, o.Name)
		}
	}
	for _, in := range op.Inputs {
		for _, e := range entities {
			if e.Name == in.Name {
				objects = append(objects, e.Name)
			}
		}
	}
	literals := map[string]int{}
	for _, f := range op.Families {
		for _, r := range families[f].Route {
			if n := cleanName(singular(r.Literal)); n != "" && !strings.HasPrefix(r.Literal, "ref=") && len(n) > 2 {
				literals[n]++
			}
		}
	}
	for _, l := range ranked(literals) {
		objects = append(objects, l)
	}
	votes := map[string]int{}
	order := 0
	for _, o := range objects {
		if op.Role == RoleMutation {
			// A mutation's path usually names the action itself, as in
			// add-to-cart.
			votes[o] += 100 - order
		}
		for _, v := range verbs {
			votes[v+"_"+o] += 100 - order
		}
		order++
	}
	return ranked(votes)
}

// opNameTask asks what to call an operation.
func opNameTask(in *input.Input, op *OperationCandidate) decide.Task {
	var choices []decide.Choice
	for _, n := range op.Names {
		choices = append(choices, decide.Choice{ID: n, Meaning: "call it " + n, Why: "built from the role, the entities it returns or takes, and its path"})
	}
	choices = append(choices, decide.Choice{ID: decide.ChoiceUnknown, Meaning: RoleMeanings[decide.ChoiceUnknown]})

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
	for _, f := range op.Families {
		add(ir.Ref(ir.RefFamily, f), "request: "+route(in, f))
	}
	for _, i := range op.Inputs {
		add(i.Slots[0].Ref, fmt.Sprintf("input %s (%s), required %v", i.Name, i.Type, i.Required))
	}
	for _, o := range op.Outputs {
		what := o.Name
		if o.Many {
			what = "a list of " + what
		}
		add(o.Sources[0].Ref, "returns "+what)
	}
	return decide.Task{
		ID:              "operation:" + strings.TrimPrefix(op.ID, ir.RefOperation+":") + ":" + decide.PromptVersion,
		Type:            decide.TypeNameOperation,
		Subject:         op.ID,
		EvidencePackRef: "EP-" + op.ID,
		Question:        "These requests serve one capability (" + op.Role + "). What should an agent call it?",
		Choices:         choices,
		Evidence:        items,
	}
}
