package values

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/shape"
)

func TestResponseValueReusedInALaterRequestLinks(t *testing.T) {
	obs := []model.Observation{
		jsonGet("rec_a", 1, "https://api.test/search?q=ring", `{"products":[{"asin":"B0FV975MJK"},{"asin":"B0FMJCCJBD"}]}`),
		jsonGet("rec_a", 2, "https://shop.test/dp/B0FV975MJK", `{"title":"Ring camera"}`),
	}
	ix := build(obs, nil)
	links := ix.Links()
	l := find(links, "B0FV975MJK")
	if l == nil {
		t.Fatalf("no link for the reused value; links = %+v", links)
	}
	if len(l.FamilyIDs) != 2 || l.Count != 2 {
		t.Errorf("link = %+v, want 2 families and 2 sightings", l)
	}
	var locs []string
	for _, o := range l.Locations {
		locs = append(locs, o.Location.String())
	}
	sort.Strings(locs)
	if want := "request.path.1,response.body.products[].asin"; strings.Join(locs, ",") != want {
		t.Errorf("locations = %v, want %s", locs, want)
	}
	// The value seen once, in one family, connects nothing.
	if find(links, "B0FMJCCJBD") != nil {
		t.Error("a value seen in one family produced a link")
	}
	if l.ID != "vl_"+model.Hash("link", "B0FV975MJK") {
		t.Errorf("link id %s does not derive from the value", l.ID)
	}
}

func TestWeakValuesAreLinkedAndFlagged(t *testing.T) {
	obs := []model.Observation{
		jsonGet("rec_a", 1, "https://api.test/a", `{"ok":true,"n":7}`),
		jsonGet("rec_a", 2, "https://api.test/b", `{"done":true,"n":7}`),
	}
	links := build(obs, nil).Links()
	for _, v := range []string{"true", "7"} {
		l := find(links, v)
		if l == nil {
			t.Errorf("weak value %q was dropped", v)
			continue
		}
		if !contains(l.Flags, model.FlagLowInformation) {
			t.Errorf("weak value %q is not flagged: %v", v, l.Flags)
		}
	}
}

func TestValueAcrossManyFamiliesIsFlaggedUbiquitous(t *testing.T) {
	var obs []model.Observation
	for i := 0; i < 12; i++ {
		o := jsonGet("rec_a", uint64(i+1), fmt.Sprintf("https://shop.test/page%c", 'a'+i), "")
		o.Values = append(o.Values, model.ValueRef{
			Value: "sess-7f3a9c21", Type: model.TypeString,
			Loc: model.Location{Side: model.SideRequest, Part: model.PartCookie, Path: "session-id", Pattern: "session-id"},
		})
		obs = append(obs, o)
	}
	l := find(build(obs, nil).Links(), "sess-7f3a9c21")
	if l == nil {
		t.Fatal("ubiquitous value was dropped")
	}
	if !contains(l.Flags, model.FlagUbiquitous) || len(l.FamilyIDs) != 12 {
		t.Errorf("link = flags %v over %d families, want ubiquitous over 12", l.Flags, len(l.FamilyIDs))
	}
}

func TestValuesInsideHTMLAreFoundWhenARequestCarriesThem(t *testing.T) {
	page := jsonGet("rec_a", 1, "https://shop.test/s?k=ring", "")
	page.ResponseBody = model.Body{Kind: model.BodyOpaque, Media: "text/html", Size: 200, Shape: shape.Opaque("text/html", 200)}
	html := []byte(`<a href="/Ring-Camera/dp/B0FV975MJK/ref=sr_1_1">Ring</a> <div data-id="NOTREQUESTED1"></div> <span>ring</span>`)
	product := jsonGet("rec_a", 2, "https://shop.test/dp/B0FV975MJK", "")
	text := func(o *model.Observation) ([]byte, bool) {
		if o.ExchangeID == page.ExchangeID {
			return html, true
		}
		return nil, false
	}
	ix := build([]model.Observation{page, product}, text)
	l := find(ix.Links(), "B0FV975MJK")
	if l == nil {
		t.Fatal("value in the HTML page did not link to the request carrying it")
	}
	var sawText bool
	for _, o := range l.Locations {
		if o.Location.Part == model.PartText && o.Location.Side == model.SideResponse {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("locations = %+v, want a response.text sighting", l.Locations)
	}
	// Only values some request carried are looked for, and short ones are
	// skipped: "ring" was requested but is too short to find by chance-free
	// token match.
	if ix.Occurrences("NOTREQUESTED1") != nil {
		t.Error("a token no request carried was indexed")
	}
	for _, o := range ix.Occurrences("ring") {
		if o.Loc.Part == model.PartText {
			t.Error("a short value was matched inside text")
		}
	}
}

func TestEmptyValuesAreNotIndexed(t *testing.T) {
	obs := []model.Observation{
		jsonGet("rec_a", 1, "https://api.test/a", `{"x":""}`),
		jsonGet("rec_a", 2, "https://api.test/b", `{"y":""}`),
	}
	if ix := build(obs, nil); ix.Occurrences("") != nil {
		t.Error("the empty string was indexed")
	}
}

func TestLinksIgnoreInputOrder(t *testing.T) {
	var obs []model.Observation
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("B0ID%06d", i)
		obs = append(obs,
			jsonGet("rec_a", uint64(2*i+1), "https://api.test/search?q=term"+strconv.Itoa(i), `{"products":[{"asin":"`+id+`"}],"page":1}`),
			jsonGet("rec_b", uint64(2*i+2), "https://shop.test/dp/"+id, `{"asin":"`+id+`","ok":true}`),
		)
	}
	want := encode(t, build(obs, nil).Links())
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 20; i++ {
		shuffled := append([]model.Observation(nil), obs...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := encode(t, build(shuffled, nil).Links()); got != want {
			t.Fatalf("permutation %d changed the links", i)
		}
	}
}

func TestTokens(t *testing.T) {
	got := tokens([]byte(`href="/a-b/dp/B0X_1?x=1" B0X_1`))
	want := []string{"href", "a-b", "dp", "B0X_1", "x", "1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens = %v, want %v", got, want)
	}
}

// --- fixtures -------------------------------------------------------------

func build(obs []model.Observation, text TextSource) *Index {
	return Build(obs, family.Build(obs), text, DefaultOptions())
}

// jsonGet builds an observation the way normalize would, without a
// recording: path, query and JSON response values included.
func jsonGet(session string, seq uint64, rawURL, body string) model.Observation {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	o := model.Observation{
		ExchangeID: fmt.Sprintf("ex_%06d", seq),
		SessionID:  session,
		Seq:        seq,
		Method:     "GET",
		Scheme:     u.Scheme,
		Host:       u.Host,
		URL:        rawURL,
		Status:     200,
	}
	if p := strings.TrimPrefix(u.EscapedPath(), "/"); p != "" {
		o.PathSegments = strings.Split(p, "/")
	}
	for i, seg := range o.PathSegments {
		o.Values = append(o.Values, ref(model.SideRequest, model.PartPath, strconv.Itoa(i), strconv.Itoa(i), model.TypeString, seg))
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
		o.Values = append(o.Values, ref(model.SideRequest, model.PartQuery, s.Path, s.Pattern, s.Type, s.Value))
	}
	o.RequestBody = model.Body{Kind: model.BodyNone, Shape: model.Shape{Type: model.TypeAbsent}}
	o.ResponseBody = model.Body{Kind: model.BodyNone, Shape: model.Shape{Type: model.TypeAbsent}}
	if body != "" {
		res, err := shape.OfJSON([]byte(body), shape.DefaultBudget)
		if err != nil {
			panic(err)
		}
		o.ResponseBody = model.Body{Kind: model.BodyJSON, Media: "application/json", Size: int64(len(body)), Shape: res.Shape}
		for _, s := range res.Scalars {
			o.Values = append(o.Values, ref(model.SideResponse, model.PartBody, s.Path, s.Pattern, s.Type, s.Value))
		}
	}
	return o
}

func ref(side, part, path, pattern, typ, value string) model.ValueRef {
	v := model.ValueRef{Value: value, Type: typ, Loc: model.Location{Side: side, Part: part, Path: path, Pattern: pattern}}
	// The same rule normalize applies, reduced to what these tests use.
	if typ == model.TypeBool || ((typ == model.TypeInteger || typ == model.TypeNumber) && len(value) <= 3) || len(value) < 4 {
		v.Flags = []string{model.FlagLowInformation}
	}
	return v
}

func find(links []model.ValueLink, value string) *model.ValueLink {
	for i := range links {
		if links[i].Value == value {
			return &links[i]
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

func encode(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
