package ir

import (
	"errors"
	"fmt"
	"strings"
)

// Resolver answers whether an evidence reference names something the
// clustering output holds.
type Resolver interface {
	Has(ref string) bool
}

// Validate checks that the IR is referentially valid: the schema version
// matches, every node id is unique and carries its kind's prefix, every
// status and kind is known, every node names a listed hypothesis, and every
// reference resolves to evidence or to a node in this IR. It reports every
// problem, not just the first.
func (x *InterfaceIR) Validate(evidence Resolver) error {
	v := validator{evidence: evidence, nodes: map[string]bool{}}
	if x.SchemaVersion != SchemaVersion {
		v.addf("schema_version %q, want %q", x.SchemaVersion, SchemaVersion)
	}
	hyps := map[string]bool{}
	for _, h := range x.Hypotheses {
		v.node(h.ID, RefHypothesis)
		hyps[h.ID] = true
		if !h.Kind.Valid() {
			v.addf("%s: unknown kind %q", h.ID, h.Kind)
		}
		if !h.Status.Valid() {
			v.addf("%s: unknown status %q", h.ID, h.Status)
		}
	}
	for _, e := range x.Entities {
		v.node(e.ID, RefEntity)
		for _, f := range e.Fields {
			v.node(f.ID, RefField)
		}
		for _, r := range e.Relations {
			v.node(r.ID, RefRelation)
		}
	}
	for _, op := range x.Operations {
		v.node(op.ID, RefOperation)
		for _, p := range op.Preconditions {
			v.node(p.ID, RefPrerequisite)
		}
	}

	hyp := func(owner, id string) {
		if !hyps[id] {
			v.addf("%s: hypothesis %q is not listed", owner, id)
		}
	}
	for _, e := range x.Entities {
		hyp(e.ID, e.Hypothesis)
		v.fieldRefs(e.ID, e.Identity)
		for _, f := range e.Fields {
			v.fieldRefs(f.ID, f.Sources)
		}
		for _, r := range e.Relations {
			hyp(r.ID, r.Hypothesis)
			v.ref(r.ID, r.Target)
		}
		v.refs(e.ID, e.EvidenceRefs)
	}
	for _, op := range x.Operations {
		hyp(op.ID, op.Hypothesis)
		if !op.Status.Valid() {
			v.addf("%s: unknown status %q", op.ID, op.Status)
		}
		for _, in := range op.Inputs {
			v.fieldRefs(op.ID, in.Slots)
		}
		for _, out := range op.Outputs {
			v.fieldRefs(op.ID, out.Sources)
			if out.Entity != "" {
				v.ref(op.ID, out.Entity)
			}
		}
		for _, f := range op.SourceFamilies {
			if p, _, _ := SplitRef(f); p != RefFamily {
				v.addf("%s: source family %q is not a family reference", op.ID, f)
				continue
			}
			v.ref(op.ID, f)
		}
		for _, p := range op.Preconditions {
			hyp(p.ID, p.Hypothesis)
			if !p.Status.Valid() {
				v.addf("%s: unknown status %q", p.ID, p.Status)
			}
			if p.ProducerFamily != "" {
				v.ref(p.ID, p.ProducerFamily)
			}
			v.ref(p.ID, p.ConsumerFamily)
			v.refs(p.ID, p.EvidenceRefs)
		}
		v.refs(op.ID, op.EvidenceRefs)
	}
	return errors.Join(v.errs...)
}

type validator struct {
	evidence Resolver
	nodes    map[string]bool
	errs     []error
}

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) node(id, prefix string) {
	if p, _, ok := SplitRef(id); !ok || p != prefix {
		v.addf("node id %q does not start with %q", id, prefix+":")
	}
	if v.nodes[id] {
		v.addf("duplicate node id %q", id)
	}
	v.nodes[id] = true
}

func (v *validator) refs(owner string, refs []string) {
	for _, r := range refs {
		v.ref(owner, r)
	}
}

// ref resolves one reference. A field reference may carry a path after
// "#", such as "var:<family>/<variant>#body.entity.asin"; only the part
// before it is resolved here, and later checks verify the path.
func (v *validator) ref(owner, ref string) {
	base, _, _ := strings.Cut(ref, "#")
	if _, _, ok := SplitRef(base); !ok {
		v.addf("%s: malformed reference %q", owner, ref)
		return
	}
	if IsNodeRef(base) {
		if !v.nodes[base] {
			v.addf("%s: reference %q names no node in this IR", owner, ref)
		}
		return
	}
	if v.evidence == nil || !v.evidence.Has(base) {
		v.addf("%s: reference %q names no clustering evidence", owner, ref)
	}
}

func (v *validator) fieldRefs(owner string, refs []FieldRef) {
	for _, r := range refs {
		v.ref(owner, r.Ref)
	}
}
