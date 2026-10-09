package candidates

import (
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
)

// Families of the Amazon fixture.
const (
	productPage   = "dea048077b86" // /<title>/dp/<asin>, the search result click
	productAPI    = "5c82378a50f7" // data.amazon.com product, asin in path.4
	suggestFamily = "8e75ea4d5334" // /suggestions as the user types
)

func keyWith(t *testing.T, keys []KeyClass, consumer string) KeyClass {
	t.Helper()
	for _, k := range keys {
		for _, c := range k.Consumers {
			if c == consumer {
				return k
			}
		}
	}
	t.Fatalf("no key class carries %s", consumer)
	return KeyClass{}
}

func TestProductIDsFormOneKeyClass(t *testing.T) {
	keys := KeyClasses(load(t))
	asin := keyWith(t, keys, "fam:"+productPage+"#request.path.2")
	if asin.Shape != "[A-Z0-9]{10}" || !asin.HeldOut.Passed() {
		t.Errorf("product id class: shape %q, held out %+v", asin.Shape, asin.HeldOut)
	}
	for _, want := range []string{
		"fam:" + productAPI + "#request.path.4",
		"fam:236a4aa04dea#request.query.asin",
		"fam:f50dc767c393#request.path.1",
	} {
		if !contains(asin.Consumers, want) {
			t.Errorf("product id class lacks %s", want)
		}
	}
	if !contains(asin.Producers, "fam:"+productAPI+"#response.body.entity.asin") {
		t.Errorf("product id class producers = %v", asin.Producers)
	}
	// Search words, request ids and parent product ids stay out of it.
	for _, c := range asin.Consumers {
		for _, bad := range []string{"query.k", "query.prefix", "request-id", "pf_rd_r", "parentAsin", "qid"} {
			if strings.HasSuffix(c, bad) {
				t.Errorf("product id class holds %s", c)
			}
		}
	}
	// Every class keeps to one identifier shape.
	for _, k := range keys {
		if k.Shape == "" {
			t.Errorf("class %s mixes shapes: %v", k.ID, k.Examples)
		}
	}
}

func TestUnionFindCompressesPaths(t *testing.T) {
	u := newUnionFind()
	u.union("a", "b")
	u.union("c", "d")
	u.union("b", "d")
	u.union("e", "f")
	for _, x := range []string{"a", "b", "c", "d"} {
		if u.find(x) != u.find("a") {
			t.Errorf("%s not joined with a", x)
		}
	}
	if u.find("e") == u.find("a") || u.find("e") != u.find("f") {
		t.Error("e and f joined wrongly")
	}
}

func entityNamed(t *testing.T, es []EntityCandidate, name string) EntityCandidate {
	t.Helper()
	for _, e := range es {
		if len(e.Names) > 0 && e.Names[0] == name {
			return e
		}
	}
	t.Fatalf("no entity candidate named %s", name)
	return EntityCandidate{}
}

func TestEntityCandidatesOnAmazon(t *testing.T) {
	in := load(t)
	es, rels := Entities(in, KeyClasses(in), nil)

	product := entityNamed(t, es, "asin")
	if product.Kind != EntityKeyed || len(product.Fields) == 0 {
		t.Errorf("product entity = %+v", product)
	}
	for _, n := range []string{"product", "landingAsin", "pd_rd_i"} {
		if !contains(product.Names, n) {
			t.Errorf("product names %v lack %s", product.Names, n)
		}
	}
	task := product.Name
	if task.Type != decide.TypeNameEntity || task.Choices[0].ID != "asin" || task.Choices[len(task.Choices)-1].ID != decide.ChoiceUnknown {
		t.Errorf("product naming task choices = %+v", task.Choices)
	}
	if _, ok := task.Choice(ChoiceNotAnObject); !ok {
		t.Error("naming task does not offer not_an_object")
	}

	// A request id is offered as not an object first.
	reqID := keyWith(t, KeyClasses(in), "fam:"+suggestFamily+"#request.query.request-id")
	for _, e := range es {
		if e.ID == reqID.ID && e.Name.Choices[0].ID != ChoiceNotAnObject {
			t.Errorf("request id choices = %+v", e.Name.Choices)
		}
	}

	suggestion := entityNamed(t, es, "suggestion")
	scope := entityNamed(t, es, "scope")
	if suggestion.Kind != EntityItem || scope.Parent != suggestion.ID || len(suggestion.Families) != 2 {
		t.Errorf("suggestion %+v, scope parent %s", suggestion.Families, scope.Parent)
	}
	parent := entityNamed(t, es, "parentAsin")

	var refs, nests int
	for _, r := range rels {
		switch {
		case r.Kind == RelationReferences && r.From == product.ID && r.To == parent.ID:
			refs++
			if r.Cardinality != CardinalityOne || r.Contradicting != 0 || len(r.Families) < 2 {
				t.Errorf("product -> parent = %+v", r)
			}
		case r.Kind == RelationContains && r.From == suggestion.ID && r.To == scope.ID:
			nests++
		}
		if r.Kind == RelationReferences && (r.From == reqID.ID || r.To == reqID.ID) {
			t.Errorf("request ids relate: %+v", r)
		}
	}
	if refs != 1 || nests != 1 {
		t.Errorf("relations = %+v", rels)
	}

	// The same evidence gives the same tasks.
	again, _ := Entities(in, KeyClasses(in), nil)
	for i := range es {
		if es[i].Name.Hash() != again[i].Name.Hash() {
			t.Errorf("task for %s differs between runs", es[i].ID)
		}
	}
}

func TestSkippedFamiliesContributeNoItems(t *testing.T) {
	in := load(t)
	es, _ := Entities(in, KeyClasses(in), map[string]bool{suggestFamily: true, "236a4aa04dea": true})
	for _, e := range es {
		if e.Kind == EntityItem && strings.HasPrefix(e.Container, "body.suggestions") {
			t.Errorf("skipped families gave %s", e.Container)
		}
	}
}

func TestNameHelpers(t *testing.T) {
	for in, want := range map[string]string{"products": "product", "entries": "entry", "boxes": "box", "address": "address", "dp": "dp"} {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]string{"request-id": "request_id", "9lives": "lives", "unknown": "", "x": "", "{{redacted:a:b}}": ""} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q", in, got)
		}
	}
	if got := lastName("events[].data.requestId"); got != "requestId" {
		t.Errorf("lastName = %q", got)
	}
}
