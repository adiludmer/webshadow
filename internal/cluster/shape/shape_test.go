package shape

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

func TestKeyOrderAndValuesDoNotChangeTheFingerprint(t *testing.T) {
	a := mustJSON(t, `{"query":"ring camera","page":1}`)
	b := mustJSON(t, `{"page":2,"query":"keyboard"}`)
	if a.Shape.Fingerprint() != b.Shape.Fingerprint() {
		t.Errorf("fingerprints differ:\n%s\n%s", a.Shape.Canonical(), b.Shape.Canonical())
	}
	if got, want := a.Shape.Canonical(), "{page:integer,query:string}"; got != want {
		t.Errorf("canonical = %s, want %s", got, want)
	}
	// A float where an integer stood is a different shape; only merging
	// makes them one.
	c := mustJSON(t, `{"page":1.5,"query":"x"}`)
	if c.Shape.Fingerprint() == a.Shape.Fingerprint() {
		t.Error("integer and number share a fingerprint")
	}
	if got := Merge(a.Shape, c.Shape).Canonical(); got != "{page:number,query:string}" {
		t.Errorf("merged = %s", got)
	}
}

func TestScalarsCarryConcreteAndPatternPaths(t *testing.T) {
	res := mustJSON(t, `{"products":[{"asin":"B0ABC"},{"asin":"B0XYZ"}],"nextIndex":24}`)
	want := []Scalar{
		{Path: "nextIndex", Pattern: "nextIndex", Type: model.TypeInteger, Value: "24"},
		{Path: "products[0].asin", Pattern: "products[].asin", Type: model.TypeString, Value: "B0ABC"},
		{Path: "products[1].asin", Pattern: "products[].asin", Type: model.TypeString, Value: "B0XYZ"},
	}
	if len(res.Scalars) != len(want) {
		t.Fatalf("got %d scalars, want %d: %+v", len(res.Scalars), len(want), res.Scalars)
	}
	for i, s := range res.Scalars {
		if s != want[i] {
			t.Errorf("scalar %d = %+v, want %+v", i, s, want[i])
		}
	}
	if got := res.Shape.Canonical(); got != "{nextIndex:integer,products:[{asin:string}]}" {
		t.Errorf("canonical = %s", got)
	}
}

func TestArrayElementShapesMerge(t *testing.T) {
	res := mustJSON(t, `{"items":[{"id":"a"},{"id":"b","extra":true}]}`)
	if got, want := res.Shape.Canonical(), "{items:[{extra:bool,id:string}]}"; got != want {
		t.Errorf("canonical = %s, want %s", got, want)
	}
}

func TestBudgetTruncatesWithoutFailing(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(`{"rows":[`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{"id":"value-of-row"}`)
	}
	buf.WriteString(`]}`)
	res, err := OfJSON(buf.Bytes(), Budget{MaxNodes: 1000, MaxDepth: 10, MaxElems: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("walk over 50 elements with MaxElems 5 is not marked truncated")
	}
	if len(res.Scalars) != 5 {
		t.Errorf("got %d scalars, want 5", len(res.Scalars))
	}
	if got := res.Shape.Canonical(); got != "{rows:[{id:string}]}" {
		t.Errorf("canonical = %s", got)
	}
}

func TestMalformedJSONIsAnError(t *testing.T) {
	for _, body := range []string{`{"a":`, `{"a":1} {"b":2}`} {
		if _, err := OfJSON([]byte(body), DefaultBudget); err == nil {
			t.Errorf("OfJSON(%q) = nil error", body)
		}
	}
}

func TestQueryAndFormShapesIgnoreOrderAndValues(t *testing.T) {
	a, err := OfForm([]byte("q=ring+camera&page=1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := OfForm([]byte("page=7&q=keyboard"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Shape.Fingerprint() != b.Shape.Fingerprint() {
		t.Errorf("form fingerprints differ: %s vs %s", a.Shape.Canonical(), b.Shape.Canonical())
	}
	// A numeric-looking parameter stays a string, so page=1 and page=next do
	// not split a family.
	if got, want := a.Shape.Canonical(), "{page:string,q:string}"; got != want {
		t.Errorf("canonical = %s, want %s", got, want)
	}
	if got := a.Scalars[1].Value; got != "ring camera" {
		t.Errorf("form value = %q, want decoded %q", got, "ring camera")
	}
}

func TestRepeatedKeyBecomesAnArray(t *testing.T) {
	pairs := []model.Pair{{Name: "id", Value: "1"}, {Name: "id", Value: "2"}, {Name: "q", Value: "x"}}
	if got, want := OfPairs(pairs).Canonical(), "{id:[string],q:string}"; got != want {
		t.Errorf("canonical = %s, want %s", got, want)
	}
	got := ScalarsOfPairs(pairs)
	if len(got) != 3 || got[0].Path != "id" || got[1].Path != "id[1]" || got[1].Pattern != "id" {
		t.Errorf("scalars = %+v", got)
	}
}

func TestMultipartKeepsFieldsAndDescribesFiles(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("token", "abc123")
	fw, _ := w.CreateFormFile("photo", "cat.png")
	fw.Write(bytes.Repeat([]byte{0x89}, 2048))
	w.Close()

	res, err := OfMultipart(buf.Bytes(), w.Boundary())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Shape.Canonical(), "multipart{photo:opaque(application/octet-stream,small),token:string}"; got != want {
		t.Errorf("canonical = %s, want %s", got, want)
	}
	if len(res.Scalars) != 1 || res.Scalars[0].Value != "abc123" {
		t.Errorf("scalars = %+v, want only the token field", res.Scalars)
	}
}

func TestKindAndMediaType(t *testing.T) {
	cases := []struct{ contentType, media, kind string }{
		{"application/json; charset=UTF-8", "application/json", model.BodyJSON},
		{"application/vnd.api+json", "application/vnd.api+json", model.BodyJSON},
		{"APPLICATION/X-WWW-FORM-URLENCODED", "application/x-www-form-urlencoded", model.BodyForm},
		{"multipart/form-data; boundary=x", "multipart/form-data", model.BodyMultipart},
		{"text/html", "text/html", model.BodyOpaque},
		{"", "", model.BodyOpaque},
	}
	for _, c := range cases {
		media, _ := MediaType(c.contentType)
		if media != c.media || Kind(media) != c.kind {
			t.Errorf("%q = (%q, %q), want (%q, %q)", c.contentType, media, Kind(media), c.media, c.kind)
		}
	}
}

func TestFingerprintsAreVersioned(t *testing.T) {
	// Two different kinds of thing with the same canonical form must not
	// share an id.
	if model.Hash("shape", "x") == model.Hash("family", "x") {
		t.Error("kinds share a hash")
	}
}

func mustJSON(t *testing.T, body string) Result {
	t.Helper()
	res, err := OfJSON([]byte(body), DefaultBudget)
	if err != nil {
		t.Fatalf("OfJSON(%s): %v", body, err)
	}
	return res
}
