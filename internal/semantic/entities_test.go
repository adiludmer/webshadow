package semantic

import (
	"context"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// productTask is the naming task of the Amazon product id entity.
const productTask = "entity:5d8dfab6c68e:v1"

func entityByName(x *ir.InterfaceIR, name string) *ir.Entity {
	for i := range x.Entities {
		if x.Entities[i].Name == name {
			return &x.Entities[i]
		}
	}
	return nil
}

func hypothesis(x *ir.InterfaceIR, id string) ir.HypothesisRef {
	for _, h := range x.Hypotheses {
		if h.ID == id {
			return h
		}
	}
	return ir.HypothesisRef{}
}

func TestEntitiesReachTheIR(t *testing.T) {
	res, err := Analyze(context.Background(), fixture, mockOptions(testStore(t)))
	if err != nil {
		t.Fatal(err)
	}
	x := res.IR
	product := entityByName(x, "asin")
	if product == nil {
		t.Fatalf("no product entity in %d entities", len(x.Entities))
	}
	if h := hypothesis(x, product.Hypothesis); h.Kind != ir.KindEntity || h.Status != ir.StatusMechanicallySupported {
		t.Errorf("product hypothesis = %+v", h)
	}
	if len(product.Identity) < 10 || !contains(product.Aliases, "product") {
		t.Errorf("product identity %d locations, aliases %v", len(product.Identity), product.Aliases)
	}
	// Its identity was checked on held-out values.
	identity := false
	for _, h := range x.Hypotheses {
		if h.Kind == ir.KindIdentity && strings.Join(h.SubjectRefs, ",") == strings.Join(hypothesis(x, product.Hypothesis).SubjectRefs, ",") {
			identity = h.Status == ir.StatusMechanicallySupported
		}
	}
	if !identity {
		t.Error("product identity is not mechanically supported")
	}
	parent := entityByName(x, "parentAsin")
	var rel *ir.RelationRef
	for i := range product.Relations {
		if parent != nil && product.Relations[i].Target == parent.ID {
			rel = &product.Relations[i]
		}
	}
	if rel == nil || rel.Kind != "references" || rel.Cardinality != "one" || len(rel.FieldPaths) == 0 {
		t.Fatalf("product -> parent relation = %+v", rel)
	}
	if h := hypothesis(x, rel.Hypothesis); h.Kind != ir.KindRelation || h.Status != ir.StatusMechanicallySupported {
		t.Errorf("relation hypothesis = %+v", h)
	}
	// Request ids are not objects.
	for _, e := range x.Entities {
		for _, id := range e.Identity {
			if strings.HasSuffix(id.Ref, "request-id") {
				t.Errorf("request id entity %s reached the IR", e.Name)
			}
		}
	}
	if res.Manifest.Counts.Entities != len(x.Entities) {
		t.Errorf("manifest counts %d entities, IR has %d", res.Manifest.Counts.Entities, len(x.Entities))
	}
}

func TestNotAnObjectRemovesEntity(t *testing.T) {
	s := testStore(t)
	if _, err := Analyze(context.Background(), fixture, mockOptions(s)); err != nil {
		t.Fatal(err)
	}
	m := &decide.Mock{Script: map[string][]string{productTask: {`{"choice_id":"not_an_object","evidence_refs":["E1"]}`}}}
	res, err := Analyze(context.Background(), fixture, Options{Store: s, Decider: decide.New(m, decide.ModelInfo{}, decide.DefaultParams())})
	if err != nil {
		t.Fatal(err)
	}
	if e := entityByName(res.IR, "asin"); e != nil {
		t.Fatalf("rejected entity still listed: %+v", e.ID)
	}
	rejected, superseded := 0, 0
	for _, h := range res.IR.Hypotheses {
		if h.Kind == ir.KindEntity && h.CandidateID == "keyed_object" && contains(h.SubjectRefs, "fam:dea048077b86#request.path.2") {
			switch h.Status {
			case ir.StatusRejected:
				rejected++
			case ir.StatusSuperseded:
				superseded++
			}
		}
	}
	if rejected != 1 {
		t.Errorf("rejected %d, superseded %d", rejected, superseded)
	}
	// Nothing points at the removed entity.
	for _, e := range res.IR.Entities {
		for _, r := range e.Relations {
			if r.Target == "ent:5d8dfab6c68e" {
				t.Errorf("%s still relates to the removed entity", e.Name)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
