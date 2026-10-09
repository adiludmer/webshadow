package ir

import (
	"bytes"
	"strings"
	"testing"
)

type refSet map[string]bool

func (s refSet) Has(ref string) bool { return s[ref] }

func sample() *InterfaceIR {
	x := New("cl_b", "cl_a")
	x.Hypotheses = []HypothesisRef{
		{ID: "hyp:2", Kind: KindOperation, Status: StatusProposed},
		{ID: "hyp:1", Kind: KindEntity, Status: StatusMechanicallySupported},
	}
	x.Entities = []Entity{{
		ID:   "ent:1",
		Name: "entity_1",
		Fields: []EntityField{
			{ID: "fld:2", Name: "b", Type: "string", Sources: []FieldRef{{Ref: "var:f1/v1#body.b", Type: "string"}}},
			{ID: "fld:1", Name: "a", Type: "string", Sources: []FieldRef{{Ref: "var:f1/v1#body.a", Type: "string"}}},
		},
		EvidenceRefs: []string{"fam:f1", "vl:l1", "fam:f1"},
		Hypothesis:   "hyp:1",
	}}
	x.Operations = []Operation{{
		ID:             "op:1",
		Name:           "operation_1",
		Inputs:         []InputField{{Name: "q", Type: "string", Slots: []FieldRef{{Ref: "fam:f1#query.q", Type: "string"}}}},
		Outputs:        []OutputField{{Name: "items", Entity: "ent:1", Many: true, Sources: []FieldRef{{Ref: "var:f1/v1#body", Type: "array"}}}},
		SourceFamilies: []string{"fam:f2", "fam:f1"},
		EvidenceRefs:   []string{"ep:e1"},
		Status:         StatusProposed,
		Hypothesis:     "hyp:2",
	}}
	return x
}

var evidence = refSet{"fam:f1": true, "fam:f2": true, "var:f1/v1": true, "vl:l1": true, "ep:e1": true}

func TestEncodeIsCanonical(t *testing.T) {
	a, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	b := sample()
	// Build the same content in another order.
	b.Hypotheses[0], b.Hypotheses[1] = b.Hypotheses[1], b.Hypotheses[0]
	b.Entities[0].Fields[0], b.Entities[0].Fields[1] = b.Entities[0].Fields[1], b.Entities[0].Fields[0]
	b.Evidence = []string{"cl_a", "cl_b", "cl_a"}
	got, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, got) {
		t.Fatalf("same content encoded differently:\n%s\n---\n%s", a, got)
	}
	if !bytes.Contains(a, []byte(`"evidence_refs": [
        "fam:f1",
        "vl:l1"
      ]`)) {
		t.Errorf("evidence refs not sorted and deduplicated:\n%s", a)
	}
}

func TestEmptyIREncodesEmptyLists(t *testing.T) {
	data, err := New().Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema_version": "webshadow.ir/v1",
  "revision": "",
  "evidence": [],
  "entities": [],
  "operations": [],
  "hypotheses": []
}
`
	if string(data) != want {
		t.Errorf("got\n%s\nwant\n%s", data, want)
	}
}

func TestValidateAcceptsSample(t *testing.T) {
	if err := sample().Validate(evidence); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	x := sample()
	x.SchemaVersion = "old"
	x.Entities[0].Fields[1].ID = "fld:2"                                        // duplicate
	x.Entities[0].EvidenceRefs = append(x.Entities[0].EvidenceRefs, "fam:nope") // unresolved evidence
	x.Operations[0].Outputs[0].Entity = "ent:missing"                           // unresolved node
	x.Operations[0].SourceFamilies = append(x.Operations[0].SourceFamilies, "ep:e1")
	x.Operations[0].Hypothesis = "hyp:unlisted"
	x.Hypotheses[0].Status = "maybe"
	x.Entities[0].ID = "entity:1"
	err := x.Validate(evidence)
	if err == nil {
		t.Fatal("invalid IR passed")
	}
	for _, want := range []string{
		`schema_version "old"`,
		`duplicate node id "fld:2"`,
		`"fam:nope" names no clustering evidence`,
		`"ent:missing" names no node`,
		`source family "ep:e1" is not a family reference`,
		`hypothesis "hyp:unlisted" is not listed`,
		`unknown status "maybe"`,
		`node id "entity:1" does not start with "ent:"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestNodeIDIsStableAndKindSpecific(t *testing.T) {
	a := NodeID(RefEntity, "fam:f1#body.asin")
	if a != NodeID(RefEntity, "fam:f1#body.asin") {
		t.Fatal("node id not stable")
	}
	if !strings.HasPrefix(a, "ent:") || len(a) != len("ent:")+12 {
		t.Fatalf("node id %q", a)
	}
	if NodeID(RefOperation, "fam:f1#body.asin")[3:] == a[3:] {
		t.Error("different kinds share a hash")
	}
}

func TestSplitRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"fam:abc":            true,
		"ex:rec_1/ex_000001": true,
		"hyp:1":              true,
		"fam:":               false,
		"abc":                false,
		"foo:abc":            false,
	} {
		if _, _, ok := SplitRef(ref); ok != want {
			t.Errorf("SplitRef(%q) ok = %v, want %v", ref, ok, want)
		}
	}
}
