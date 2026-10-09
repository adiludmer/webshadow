package ir

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Canonicalize puts every list in the IR in a fixed order and drops
// duplicate references, so two IRs with the same content encode to the
// same bytes whatever order they were built in.
func (x *InterfaceIR) Canonicalize() {
	x.Evidence = sortedSet(x.Evidence)
	if x.Entities == nil {
		x.Entities = []Entity{}
	}
	if x.Operations == nil {
		x.Operations = []Operation{}
	}
	if x.Hypotheses == nil {
		x.Hypotheses = []HypothesisRef{}
	}
	for i := range x.Entities {
		e := &x.Entities[i]
		e.Aliases = sortedSet(e.Aliases)
		e.Identity = sortFieldRefs(e.Identity)
		for j := range e.Fields {
			e.Fields[j].Sources = sortFieldRefs(e.Fields[j].Sources)
		}
		sort.Slice(e.Fields, func(a, b int) bool { return e.Fields[a].ID < e.Fields[b].ID })
		for j := range e.Relations {
			e.Relations[j].FieldPaths = sortedSet(e.Relations[j].FieldPaths)
		}
		sort.Slice(e.Relations, func(a, b int) bool { return e.Relations[a].ID < e.Relations[b].ID })
		e.EvidenceRefs = sortedSet(e.EvidenceRefs)
	}
	sort.Slice(x.Entities, func(a, b int) bool { return x.Entities[a].ID < x.Entities[b].ID })
	for i := range x.Operations {
		op := &x.Operations[i]
		for j := range op.Inputs {
			op.Inputs[j].Slots = sortFieldRefs(op.Inputs[j].Slots)
		}
		sort.Slice(op.Inputs, func(a, b int) bool { return op.Inputs[a].Name < op.Inputs[b].Name })
		for j := range op.Outputs {
			op.Outputs[j].Sources = sortFieldRefs(op.Outputs[j].Sources)
		}
		sort.Slice(op.Outputs, func(a, b int) bool { return op.Outputs[a].Name < op.Outputs[b].Name })
		op.SourceFamilies = sortedSet(op.SourceFamilies)
		for j := range op.Preconditions {
			p := &op.Preconditions[j]
			p.EvidenceRefs = sortedSet(p.EvidenceRefs)
			p.Caveats = sortedSet(p.Caveats)
		}
		sort.Slice(op.Preconditions, func(a, b int) bool { return op.Preconditions[a].ID < op.Preconditions[b].ID })
		op.EvidenceRefs = sortedSet(op.EvidenceRefs)
	}
	sort.Slice(x.Operations, func(a, b int) bool { return x.Operations[a].ID < x.Operations[b].ID })
	sort.Slice(x.Hypotheses, func(a, b int) bool { return x.Hypotheses[a].ID < x.Hypotheses[b].ID })
}

// Canonicalize puts a hypothesis's lists in a fixed order.
func (h *Hypothesis) Canonicalize() {
	h.SubjectRefs = sortedSet(h.SubjectRefs)
	h.EvidenceRefs = sortedSet(h.EvidenceRefs)
	h.Supersedes = sortedSet(h.Supersedes)
	for i := range h.Checks {
		h.Checks[i].EvidenceRefs = sortedSet(h.Checks[i].EvidenceRefs)
	}
	sort.SliceStable(h.Checks, func(a, b int) bool { return h.Checks[a].Check < h.Checks[b].Check })
}

// Encode canonicalizes the IR and renders it as indented JSON ending in a
// newline. Equal content gives equal bytes.
func (x *InterfaceIR) Encode() ([]byte, error) {
	x.Canonicalize()
	return EncodeJSON(x)
}

// EncodeJSON renders v as indented JSON without HTML escaping, the form
// every analysis file is written in.
func EncodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func sortFieldRefs(refs []FieldRef) []FieldRef {
	if refs == nil {
		return []FieldRef{}
	}
	sort.Slice(refs, func(a, b int) bool {
		if refs[a].Ref != refs[b].Ref {
			return refs[a].Ref < refs[b].Ref
		}
		return refs[a].Type < refs[b].Type
	})
	return refs
}

// sortedSet returns s sorted with duplicates removed. It never returns
// nil, so a required list encodes as [] rather than null.
func sortedSet(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	out := append([]string(nil), s...)
	sort.Strings(out)
	n := 1
	for i := 1; i < len(out); i++ {
		if out[i] != out[n-1] {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}
