package family

import (
	"encoding/json"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/shape"
)

func TestSameOperationWithNewValuesLandsInOneFamily(t *testing.T) {
	recA := []model.Observation{
		get("rec_a", 1, "https://shop.test/dp/B0AAA11111", 200, `{"title":"Ring camera"}`),
		get("rec_a", 2, "https://shop.test/dp/B0BBB22222", 200, `{"title":"Doorbell"}`),
		get("rec_a", 3, "https://shop.test/dp/B0CCC33333", 200, `{"title":"Chime"}`),
	}
	recB := []model.Observation{
		get("rec_b", 1, "https://shop.test/dp/B0DDD44444", 200, `{"title":"Keyboard"}`),
		get("rec_b", 2, "https://shop.test/dp/B0EEE55555", 200, `{"title":"Keycaps"}`),
		get("rec_b", 3, "https://shop.test/dp/B0FFF66666", 200, `{"title":"Switches"}`),
	}
	a, b := Build(recA), Build(recB)
	both := Build(append(append([]model.Observation(nil), recA...), recB...))
	if len(a.Families) != 1 || len(b.Families) != 1 || len(both.Families) != 1 {
		t.Fatalf("families: A=%d B=%d both=%d, want 1 each", len(a.Families), len(b.Families), len(both.Families))
	}
	// The id depends on structure only, so separate recordings converge.
	if a.Families[0].ID != b.Families[0].ID || a.Families[0].ID != both.Families[0].ID {
		t.Errorf("ids differ: A=%s B=%s both=%s", a.Families[0].ID, b.Families[0].ID, both.Families[0].ID)
	}
	f := both.Families[0]
	if f.PathTemplate != "/dp/{slot_1}" || len(f.Observations) != 6 {
		t.Errorf("family = %s with %d observations", f.PathTemplate, len(f.Observations))
	}
	slot := findSlot(f, "slot_1")
	if slot == nil || slot.Location != "path.1" || slot.Type != "id" || slot.Cardinality != 6 || len(slot.Examples) != model.MaxExamples {
		t.Errorf("slot = %+v", slot)
	}
}

func TestQueryValuesDoNotSplitAFamily(t *testing.T) {
	res := Build([]model.Observation{
		get("rec_a", 1, "https://shop.test/s?k=ring+camera", 200, ""),
		get("rec_b", 1, "https://shop.test/s?k=mechanical+keyboard", 200, ""),
	})
	if len(res.Families) != 1 {
		t.Fatalf("got %d families, want 1", len(res.Families))
	}
	slot := findSlot(res.Families[0], "query.k")
	if slot == nil || slot.Cardinality != 2 || slot.Observations != 2 {
		t.Fatalf("query slot = %+v", slot)
	}
	if !contains(slot.Examples, "ring camera") || !contains(slot.Examples, "mechanical keyboard") {
		t.Errorf("examples = %v", slot.Examples)
	}
}

func TestQueryShapeSplitsButSharesARoute(t *testing.T) {
	// Spec v2 puts the query shape in family identity, so an extra key makes
	// a separate family. RouteID keeps the relationship visible.
	res := Build([]model.Observation{
		get("rec_a", 1, "https://shop.test/s?k=ring", 200, ""),
		get("rec_a", 2, "https://shop.test/s?k=ring&ref=nav_logo", 200, ""),
	})
	if len(res.Families) != 2 {
		t.Fatalf("got %d families, want 2", len(res.Families))
	}
	if res.Families[0].RouteID != res.Families[1].RouteID {
		t.Error("families on one route have different route ids")
	}
	if res.Families[0].ID == res.Families[1].ID {
		t.Error("families with different query shapes share an id")
	}
}

func TestResponsesBecomeVariantsNotFamilies(t *testing.T) {
	res := Build([]model.Observation{
		get("rec_a", 1, "https://api.test/search?q=a", 200, `{"products":[{"id":"x1"}],"nextIndex":10}`),
		get("rec_a", 2, "https://api.test/search?q=b", 200, `{"products":[{"id":"x2"}],"nextIndex":20}`),
		get("rec_a", 3, "https://api.test/search?q=c", 200, `{"products":[],"nextIndex":0}`),
		get("rec_a", 4, "https://api.test/search?q=", 400, `{"error":{"code":"EMPTY"}}`),
		transportError("rec_a", 5, "https://api.test/search?q=d"),
	})
	if len(res.Families) != 1 {
		t.Fatalf("got %d families, want 1", len(res.Families))
	}
	vs := res.Families[0].ResponseVariants
	if len(vs) != 4 {
		t.Fatalf("got %d variants, want 4: %+v", len(vs), vs)
	}
	if vs[0].Count != 2 || vs[0].Status != 200 || vs[0].Shape.Canonical() != "{nextIndex:integer,products:[{id:string}]}" {
		t.Errorf("leading variant = %+v", vs[0])
	}
	var statuses []int
	var sawError bool
	for _, v := range vs {
		statuses = append(statuses, v.Status)
		if v.Error != "" {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("no transport error variant: %v", statuses)
	}
}

func TestOpaqueBodiesOfDifferentSizesShareAFamily(t *testing.T) {
	small := post("rec_a", 1, "https://t.test/batch", "text/plain", 100)
	large := post("rec_a", 2, "https://t.test/batch", "text/plain", 50000)
	res := Build([]model.Observation{small, large})
	if len(res.Families) != 1 {
		t.Fatalf("got %d families, want 1", len(res.Families))
	}
	if got := res.Families[0].RequestBody.SizeClass; got != "" {
		t.Errorf("size class = %q, want cleared when members differ", got)
	}
	// A different media type is a different structure.
	other := post("rec_a", 3, "https://t.test/batch", "application/octet-stream", 100)
	if res := Build([]model.Observation{small, other}); len(res.Families) != 2 {
		t.Errorf("got %d families for two media types, want 2", len(res.Families))
	}
}

func TestStaticFamiliesAreMarked(t *testing.T) {
	img := get("rec_a", 1, "https://cdn.test/logo.png", 200, "")
	img.ResponseBody = model.Body{Kind: model.BodyOpaque, Media: "image/png", Size: 900, Shape: shape.Opaque("image/png", 900)}
	page := get("rec_a", 2, "https://shop.test/", 200, "")
	res := Build([]model.Observation{img, page})
	for _, f := range res.Families {
		if want := f.Host == "cdn.test"; f.Static != want {
			t.Errorf("%s static = %v, want %v", f.Host, f.Static, want)
		}
	}
}

func TestFamiliesIgnoreInputOrder(t *testing.T) {
	var obs []model.Observation
	for i, u := range []string{
		"https://shop.test/dp/B0AAA11111", "https://shop.test/dp/B0BBB22222", "https://shop.test/dp/B0CCC33333",
		"https://shop.test/s?k=ring", "https://shop.test/s?k=keyboard", "https://shop.test/s?k=pot&ref=x",
		"https://shop.test/", "https://api.test/search?q=a", "https://api.test/search?q=b",
	} {
		session := "rec_a"
		if i%2 == 1 {
			session = "rec_b"
		}
		obs = append(obs, get(session, uint64(i+1), u, 200, `{"id":"`+u+`","n":`+string(rune('0'+i))+`}`))
	}
	want := encode(t, Build(obs))
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20; i++ {
		shuffled := append([]model.Observation(nil), obs...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := encode(t, Build(shuffled)); got != want {
			t.Fatalf("permutation %d changed the output", i)
		}
	}
}

func TestEveryObservationIsMapped(t *testing.T) {
	obs := []model.Observation{
		get("rec_a", 1, "https://shop.test/a", 200, ""),
		get("rec_b", 1, "https://shop.test/a", 200, ""),
	}
	res := Build(obs)
	for i := range obs {
		if res.ByObservation[obs[i].Ref().Key()] != res.Families[0].ID {
			t.Errorf("%s is not mapped to its family", obs[i].Ref().Key())
		}
	}
}

// --- fixtures -------------------------------------------------------------

// get builds an observation the way normalize would, without a recording.
func get(session string, seq uint64, rawURL string, status int, jsonBody string) model.Observation {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	o := model.Observation{
		ExchangeID: "ex_" + string(rune('a'+seq)),
		SessionID:  session,
		Seq:        seq,
		Method:     "GET",
		Scheme:     u.Scheme,
		Host:       u.Host,
		URL:        rawURL,
		RawPath:    u.EscapedPath(),
		Status:     status,
	}
	if p := strings.TrimPrefix(u.EscapedPath(), "/"); p != "" {
		o.PathSegments = strings.Split(p, "/")
	}
	vals, _ := url.ParseQuery(u.RawQuery)
	for k, list := range vals {
		for _, v := range list {
			o.Query = append(o.Query, model.Pair{Name: k, Value: v})
		}
	}
	sort.Slice(o.Query, func(i, j int) bool { return o.Query[i].Name < o.Query[j].Name })
	o.QueryShape = shape.OfPairs(o.Query)
	for _, s := range shape.ScalarsOfPairs(o.Query) {
		o.Values = append(o.Values, model.ValueRef{Value: s.Value, Type: s.Type, Loc: model.Location{Side: model.SideRequest, Part: model.PartQuery, Path: s.Path, Pattern: s.Pattern}})
	}
	o.RequestBody = model.Body{Kind: model.BodyNone, Shape: model.Shape{Type: model.TypeAbsent}}
	o.ResponseBody = model.Body{Kind: model.BodyNone, Shape: model.Shape{Type: model.TypeAbsent}}
	if jsonBody != "" {
		res, err := shape.OfJSON([]byte(jsonBody), shape.DefaultBudget)
		if err != nil {
			panic(err)
		}
		o.ResponseBody = model.Body{Kind: model.BodyJSON, Media: "application/json", Size: int64(len(jsonBody)), Shape: res.Shape}
	}
	return o
}

func post(session string, seq uint64, rawURL, media string, size int64) model.Observation {
	o := get(session, seq, rawURL, 204, "")
	o.Method = "POST"
	o.RequestBody = model.Body{Kind: model.BodyOpaque, Media: media, Size: size, SizeClass: model.SizeClass(size), Shape: shape.Opaque(media, size)}
	return o
}

func transportError(session string, seq uint64, rawURL string) model.Observation {
	o := get(session, seq, rawURL, 0, "")
	o.Error = "dial tcp 10.0.0.1:443: connection reset"
	return o
}

func findSlot(f model.RequestFamily, name string) *model.Slot {
	for i := range f.Slots {
		if f.Slots[i].Name == name {
			return &f.Slots[i]
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func encode(t *testing.T, res Result) string {
	t.Helper()
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
