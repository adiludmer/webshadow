package trace

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/episode"
	"github.com/adiludmer/webshadow/internal/cluster/model"
)

const sec = int64(1e9)

// req builds one exchange. Headers are given as "Name: value" lines; a
// "<" prefix marks a response header.
func req(seq uint64, t float64, url string, status int, headers ...string) model.Observation {
	o := model.Observation{
		SessionID: "rec_1", ExchangeID: "ex_" + string(rune('a'+seq)), Seq: seq,
		RequestStart: int64(t * float64(sec)), Method: "GET", URL: url, Status: status,
	}
	for _, h := range headers {
		resp := strings.HasPrefix(h, "<")
		name, value, _ := strings.Cut(strings.TrimPrefix(h, "<"), ": ")
		if resp {
			o.ResponseHeaders = append(o.ResponseHeaders, model.Pair{Name: name, Value: value})
		} else {
			o.RequestHeaders = append(o.RequestHeaders, model.Pair{Name: name, Value: value})
		}
	}
	return o
}

// Header sets for the common request kinds.
var (
	typed = []string{"Sec-Fetch-Dest: document", "Sec-Fetch-Mode: navigate", "Sec-Fetch-Site: none", "Sec-Fetch-User: ?1"}
	click = []string{"Sec-Fetch-Dest: document", "Sec-Fetch-Mode: navigate", "Sec-Fetch-Site: same-origin", "Sec-Fetch-User: ?1"}
	auto  = []string{"Sec-Fetch-Dest: document", "Sec-Fetch-Mode: navigate", "Sec-Fetch-Site: same-origin"}
)

func with(base []string, more ...string) []string {
	return append(append([]string(nil), base...), more...)
}

func fetch(referer string) []string {
	return []string{"Sec-Fetch-Dest: empty", "Sec-Fetch-Mode: cors", "Sec-Fetch-Site: same-origin", "Referer: " + referer}
}

// fixture is one browsing session: a typed homepage URL that redirects, a
// click into a product page whose late fetch the homepage still makes, an
// ad iframe, and a search submitted with Enter.
func fixture() Session {
	return Session{
		ID: "rec_1",
		Observations: []model.Observation{
			req(1, 0.30, "http://shop.test/", 301, with(typed, "<Location: https://shop.test/home")...),
			req(2, 0.44, "https://shop.test/home", 200, with(typed, "<Content-Type: text/html")...),
			req(3, 0.60, "https://shop.test/app.js", 200, "Sec-Fetch-Dest: script", "Sec-Fetch-Mode: no-cors", "Referer: https://shop.test/home"),
			req(4, 1.50, "https://shop.test/api/recs", 200, fetch("https://shop.test/home")...),
			req(5, 2.00, "https://ads.test/slot", 200, "Sec-Fetch-Dest: iframe", "Sec-Fetch-Mode: navigate", "Sec-Fetch-Site: cross-site", "Referer: https://shop.test/"),
			req(6, 5.10, "https://shop.test/dp/B0FV975MJK", 200, with(click, "Referer: https://shop.test/home")...),
			// The homepage's request still in flight names its own page.
			req(7, 5.20, "https://shop.test/api/log", 200, fetch("https://shop.test/home")...),
			req(8, 6.00, "https://shop.test/api/price", 200, fetch("https://shop.test/dp/B0FV975MJK")...),
			req(9, 9.20, "https://shop.test/s?k=ring", 200, with(click, "Referer: https://shop.test/dp/B0FV975MJK")...),
			req(10, 9.90, "https://shop.test/api/more", 200, fetch("https://shop.test/s?k=ring")...),
		},
	}
}

func families() map[string]string {
	return map[string]string{
		"rec_1/ex_b": "fam_root", "rec_1/ex_c": "fam_home", "rec_1/ex_d": "fam_js", "rec_1/ex_e": "fam_recs",
		"rec_1/ex_f": "fam_ad", "rec_1/ex_g": "fam_dp", "rec_1/ex_h": "fam_log", "rec_1/ex_i": "fam_price",
		"rec_1/ex_j": "fam_search", "rec_1/ex_k": "fam_more",
	}
}

func TestTraceKeepsRecordedOrderExactly(t *testing.T) {
	s := fixture()
	tr, _ := Build(s, families(), episode.DefaultOptions())
	want := []string{"fam_root", "fam_home", "fam_js", "fam_recs", "fam_ad", "fam_dp", "fam_log", "fam_price", "fam_search", "fam_more"}
	if len(tr.Steps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(tr.Steps), len(want))
	}
	for i, st := range tr.Steps {
		if st.FamilyID != want[i] {
			t.Errorf("step %d = %s, want %s", i, st.FamilyID, want[i])
		}
	}
	// Two exchanges that started at the same instant keep their recorded
	// sequence order.
	s.Observations = append(s.Observations,
		model.Observation{SessionID: "rec_1", ExchangeID: "ex_y", Seq: 21, RequestStart: 7 * sec, Method: "GET", URL: "https://shop.test/b"},
		model.Observation{SessionID: "rec_1", ExchangeID: "ex_x", Seq: 20, RequestStart: 7 * sec, Method: "GET", URL: "https://shop.test/a"},
	)
	tr, _ = Build(s, families(), episode.DefaultOptions())
	if tr.Steps[8].Ref.ExchangeID != "ex_x" || tr.Steps[9].Ref.ExchangeID != "ex_y" {
		t.Errorf("tie order = %s, %s; want ex_x then ex_y", tr.Steps[8].Ref.ExchangeID, tr.Steps[9].Ref.ExchangeID)
	}
}

type wantView struct {
	trigger  string
	url      string
	families []string
}

func checkViews(t *testing.T, eps []model.Episode, wants []wantView) {
	t.Helper()
	if len(eps) != len(wants) {
		t.Fatalf("got %d page views, want %d: %+v", len(eps), len(wants), eps)
	}
	for i, w := range wants {
		e := eps[i]
		trig, url := "", ""
		if e.Trigger != nil {
			trig, url = e.Trigger.Kind, e.Trigger.URL
		}
		if trig != w.trigger || url != w.url || !equal(e.FamilyIDs, w.families) {
			t.Errorf("page view %d = %s %s %v, want %s %s %v", i, trig, url, e.FamilyIDs, w.trigger, w.url, w.families)
		}
	}
}

func TestPageViewsFromRequestHeaders(t *testing.T) {
	tr, eps := Build(fixture(), families(), episode.DefaultOptions())
	checkViews(t, eps, []wantView{
		// The redirect joins the typed navigation; the iframe and the
		// homepage's late log call stay in the homepage's view.
		{model.TriggerTyped, "http://shop.test/", []string{"fam_ad", "fam_home", "fam_js", "fam_log", "fam_recs", "fam_root"}},
		{model.TriggerUser, "https://shop.test/dp/B0FV975MJK", []string{"fam_dp", "fam_price"}},
		{model.TriggerUser, "https://shop.test/s?k=ring", []string{"fam_more", "fam_search"}},
	})
	if len(tr.Navigations) != 3 {
		t.Fatalf("got %d navigations, want 3", len(tr.Navigations))
	}
	home := tr.Navigations[0]
	if home.URL != "https://shop.test/home" || home.Document != "ex_c" || !equal(home.Redirects, []string{"ex_b"}) {
		t.Errorf("homepage navigation = %s document %s redirects %v; want https://shop.test/home, ex_c, [ex_b]", home.URL, home.Document, home.Redirects)
	}
	kinds := map[string]string{}
	for _, s := range tr.Steps {
		kinds[s.FamilyID] = s.Kind
	}
	for fam, want := range map[string]string{"fam_home": "document", "fam_js": "script", "fam_recs": "fetch", "fam_ad": "iframe"} {
		if kinds[fam] != want {
			t.Errorf("%s kind = %q, want %q", fam, kinds[fam], want)
		}
	}
	if got := tr.Steps[7]; got.SinceTrigger != 900*sec/1000 || got.NavigationID != tr.Navigations[1].ID {
		t.Errorf("price step: since trigger %d, navigation %s; want 0.9s in the product page", got.SinceTrigger, got.NavigationID)
	}
}

func TestNavigationWithoutGestureAndRedirect(t *testing.T) {
	s := Session{ID: "rec_1", Observations: []model.Observation{
		req(1, 1, "https://shop.test/cart", 200, click...),
		req(2, 2, "https://shop.test/checkout", 302, with(auto, "<Location: /signin?to=checkout")...),
		req(3, 2.1, "https://shop.test/signin?to=checkout", 200, auto...),
		req(4, 5, "https://shop.test/refresh", 200, auto...),
	}}
	_, eps := Build(s, nil, episode.DefaultOptions())
	checkViews(t, eps, []wantView{
		{model.TriggerUser, "https://shop.test/cart", []string{}},
		{model.TriggerRedirect, "https://shop.test/checkout", []string{}},
		{model.TriggerLink, "https://shop.test/refresh", []string{}},
	})
	if len(eps[1].ExchangeIDs) != 2 {
		t.Errorf("redirect page view holds %v, want both chain exchanges", eps[1].ExchangeIDs)
	}
}

func TestTrafficBeforeTheFirstPageView(t *testing.T) {
	s := Session{ID: "rec_1", Observations: []model.Observation{
		req(1, 0.1, "https://shop.test/api/ping", 200, fetch("https://shop.test/x")...),
		req(2, 1, "https://shop.test/", 200, typed...),
	}}
	_, eps := Build(s, map[string]string{"rec_1/ex_b": "fam_ping"}, episode.DefaultOptions())
	checkViews(t, eps, []wantView{
		{"", "", []string{"fam_ping"}},
		{model.TriggerTyped, "https://shop.test/", []string{}},
	})
}

func TestWithoutSecFetchHTMLStartsPageViews(t *testing.T) {
	html := []string{"Accept: text/html,application/xhtml+xml,*/*;q=0.8", "<Content-Type: text/html; charset=utf-8"}
	s := Session{ID: "rec_1", Observations: []model.Observation{
		req(1, 1, "https://shop.test/", 200, html...),
		req(2, 1.2, "https://shop.test/api/a", 200, "Accept: application/json", "<Content-Type: text/html"),
		req(3, 4, "https://shop.test/old", 301, "Accept: text/html", "<Location: https://shop.test/new"),
		req(4, 4.1, "https://shop.test/new", 200, html...),
		// Wants JSON first, so not a page even though HTML came back.
		req(5, 6, "https://shop.test/api/b", 200, "Accept: application/json, text/html;q=0.5", "<Content-Type: text/html"),
	}}
	tr, eps := Build(s, nil, episode.DefaultOptions())
	checkViews(t, eps, []wantView{
		{model.TriggerHTML, "https://shop.test/", []string{}},
		{model.TriggerHTML, "https://shop.test/old", []string{}},
	})
	if len(eps[1].ExchangeIDs) != 3 || len(tr.Navigations[1].Redirects) != 1 {
		t.Errorf("second page view holds %v with redirects %v; want old, new and api/b with one redirect", eps[1].ExchangeIDs, tr.Navigations[1].Redirects)
	}
}

func TestWithNoNavigationSignalGapsSplitPageViews(t *testing.T) {
	s := Session{ID: "rec_1", Observations: []model.Observation{
		req(1, 1, "https://api.test/a", 200),
		req(2, 20, "https://api.test/b", 200),
		req(3, 51, "https://api.test/c", 200),
		req(4, 52, "https://api.test/d", 200),
	}}
	tr, eps := Build(s, nil, episode.DefaultOptions())
	checkViews(t, eps, []wantView{
		{model.TriggerGap, "https://api.test/a", []string{}},
		{model.TriggerGap, "https://api.test/c", []string{}},
	})
	if len(tr.Navigations) != 0 || eps[0].NavigationID != "" {
		t.Errorf("gap page views got navigations: %+v", tr.Navigations)
	}
}

func TestSameInputsGiveTheSameTraceAndEpisodes(t *testing.T) {
	want := encode(t, fixture())
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 20; i++ {
		s := fixture()
		rng.Shuffle(len(s.Observations), func(a, b int) { s.Observations[a], s.Observations[b] = s.Observations[b], s.Observations[a] })
		if got := encode(t, s); got != want {
			t.Fatalf("permutation %d changed the trace or episodes", i)
		}
	}
}

func encode(t *testing.T, s Session) string {
	t.Helper()
	tr, eps := Build(s, families(), episode.DefaultOptions())
	data, err := json.Marshal(struct {
		T model.Trace
		E []model.Episode
	}{tr, eps})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
