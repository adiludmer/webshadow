package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/evidence"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/recording"
)

const ms = int64(1e6)

// session records one synthetic visit to a shop: the homepage sets a cookie,
// a search submitted with Enter returns item ids, and two clicks open
// product pages whose ids came from the search, each followed by an API call
// keyed by the same id.
type session struct {
	cookie   string
	term     string
	products []string
}

type writer struct {
	t     *testing.T
	store *recording.Store
	now   int64
}

func (w *writer) exchange(method, url string, reqHeaders []recording.Header, status int, respHeaders []recording.Header, media string, body []byte) {
	w.t.Helper()
	start := w.now
	w.now += 40 * ms
	resp := &recording.Response{Status: status, Protocol: "HTTP/1.1", Headers: respHeaders}
	if body != nil {
		b, err := w.store.WriteBody(body)
		if err != nil {
			w.t.Fatal(err)
		}
		b.ContentType = media
		resp.Body = b
	}
	ex := &recording.HTTPExchange{
		ConnectionID: "c1",
		StartedAt:    recording.Stamp{T: start},
		CompletedAt:  recording.Stamp{T: w.now},
		Request:      recording.Request{Method: method, Scheme: "https", Host: "shop.test", Port: 443, URL: url, Protocol: "HTTP/1.1", Headers: reqHeaders},
		Response:     resp,
		Timing:       recording.Timing{RequestStart: start, ResponseStart: start + 20*ms, ResponseEnd: w.now},
	}
	if err := w.store.AppendExchange(ex); err != nil {
		w.t.Fatal(err)
	}
	w.now += 10 * ms
}

func (w *writer) event(typ, url, payload string) {
	w.t.Helper()
	ev := &recording.BrowserEvent{Timestamp: recording.Stamp{T: w.now}, TargetID: "T1", Type: typ, PageURL: url, Payload: json.RawMessage(payload)}
	if err := w.store.AppendBrowserEvent(ev); err != nil {
		w.t.Fatal(err)
	}
	w.now += 5 * ms
}

func (w *writer) navigate(url string) {
	w.event("Page.frameNavigated", url, fmt.Sprintf(`{"frame":{"id":"F1","url":%q}}`, url))
}

func record(t *testing.T, root string, s session) *recording.Recording {
	t.Helper()
	store, err := recording.Create(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &writer{t: t, store: store, now: 100 * ms}
	cookie := []recording.Header{{Name: "Cookie", Value: "session-id=" + s.cookie}}
	html := []recording.Header{{Name: "Content-Type", Value: "text/html; charset=utf-8"}}
	js := []recording.Header{{Name: "Content-Type", Value: "application/json"}}

	w.event("Target.targetCreated", "about:blank", `{"targetInfo":{"targetId":"T1","type":"page"}}`)
	w.exchange("GET", "https://shop.test/", nil, 200,
		append([]recording.Header{{Name: "Set-Cookie", Value: "session-id=" + s.cookie + "; Path=/"}}, html...),
		"text/html", []byte("<html><body>welcome</body></html>"))
	w.navigate("https://shop.test/")
	w.exchange("GET", "https://shop.test/static/app.js", nil, 200,
		[]recording.Header{{Name: "Content-Type", Value: "application/javascript"}},
		"application/javascript", []byte("console.log(1)"))

	w.now += 2000 * ms
	w.event("interaction.enter", "https://shop.test/", fmt.Sprintf(`{"type":"enter","value":%q}`, s.term))
	items := make([]string, len(s.products))
	for i, p := range s.products {
		items[i] = fmt.Sprintf(`{"id":%q,"rank":%d}`, p, i+1)
	}
	w.exchange("GET", "https://shop.test/s?k="+s.term, cookie, 200, js, "application/json",
		[]byte(`{"items":[`+strings.Join(items, ",")+`],"more":true}`))

	for _, p := range s.products {
		w.now += 1500 * ms
		w.event("interaction.click", "https://shop.test/", `{"type":"click","target":{"tag":"a"}}`)
		w.exchange("GET", "https://shop.test/dp/"+p, cookie, 200, html, "text/html",
			[]byte("<html><body>item "+p+"</body></html>"))
		w.navigate("https://shop.test/dp/" + p)
		w.exchange("GET", "https://shop.test/static/app.js", nil, 200,
			[]recording.Header{{Name: "Content-Type", Value: "application/javascript"}},
			"application/javascript", []byte("console.log(1)"))
		w.exchange("GET", "https://shop.test/api/offer?item="+p, cookie, 200, js, "application/json",
			[]byte(`{"item":`+fmt.Sprintf("%q", p)+`,"cents":1999,"more":true}`))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := recording.Load(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fixture(t *testing.T) []*recording.Recording {
	t.Helper()
	root := t.TempDir()
	return []*recording.Recording{
		record(t, root, session{cookie: "131-7766012-5550344", term: "ring", products: []string{"B0FV975MJK", "B0CXLM2QZ4"}}),
		record(t, root, session{cookie: "142-1188321-9034412", term: "kettle", products: []string{"B0D7HN3RT1", "B09WQK5L8P"}}),
	}
}

func familyAt(t *testing.T, res *Result, method, path string) *model.RequestFamily {
	t.Helper()
	for i := range res.Families {
		if f := &res.Families[i]; f.Method == method && f.PathTemplate == path {
			return f
		}
	}
	var have []string
	for _, f := range res.Families {
		have = append(have, f.Method+" "+f.PathTemplate)
	}
	t.Fatalf("no family %s %s; have %v", method, path, have)
	return nil
}

func TestAcceptanceOnSyntheticRecordings(t *testing.T) {
	res, err := Run(fixture(t), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	// 1, 2: equivalent requests across recordings converge, ids become slots.
	dp := familyAt(t, res, "GET", "/dp/{slot_1}")
	if len(dp.Observations) != 4 || len(dp.Slots) == 0 || dp.Slots[0].Cardinality != 4 {
		t.Errorf("product family = %d observations, slots %+v", len(dp.Observations), dp.Slots)
	}
	search := familyAt(t, res, "GET", "/s")
	home := familyAt(t, res, "GET", "/")
	offer := familyAt(t, res, "GET", "/api/offer")
	asset := familyAt(t, res, "GET", "/static/app.js")
	if !asset.Static || dp.Static {
		t.Error("static flags are wrong")
	}

	// 3: a response id reused by a later request is a value link and a flow.
	var linked bool
	for _, l := range res.Links {
		if l.Value == "B0FV975MJK" && contains(l.FamilyIDs, search.ID) && contains(l.FamilyIDs, dp.ID) {
			linked = true
		}
	}
	if !linked {
		t.Error("no value link from the item list to the product path")
	}
	edge := edgeOf(res, search.ID, dp.ID)
	if edge == nil || !hasFlow(edge, model.CarrierPath) {
		t.Errorf("search -> product edge = %+v, want a path flow", edge)
	}

	// 4: every recording keeps an ordered trace.
	if len(res.Traces) != 2 {
		t.Fatalf("got %d traces", len(res.Traces))
	}
	for _, tr := range res.Traces {
		if len(tr.Steps) != 2+1+3*2 {
			t.Errorf("trace %s has %d steps", tr.SessionID, len(tr.Steps))
		}
		for i := 1; i < len(tr.Steps); i++ {
			if tr.Steps[i].T < tr.Steps[i-1].T {
				t.Errorf("trace %s is out of order at %d", tr.SessionID, i)
			}
		}
	}

	// 5, 6: repeated order aggregates; Set-Cookie -> Cookie is a state flow.
	cookieEdge := edgeOf(res, home.ID, offer.ID)
	if cookieEdge == nil || cookieEdge.OrderedCount != 4 || cookieEdge.SessionsWithBoth != 2 || !hasFlow(cookieEdge, model.CarrierCookie) {
		t.Errorf("home -> offer edge = %+v", cookieEdge)
	}

	// 7: an occurrence without the predecessor is counted. The first product
	// page of each session has no earlier product page.
	self := edgeOf(res, dp.ID, offer.ID)
	if self == nil || self.WithoutPredecessor != 0 {
		t.Errorf("product -> offer edge = %+v", self)
	}
	dpSelf := edgeOf(res, dp.ID, dp.ID)
	if dpSelf == nil || dpSelf.OrderedCount != 2 || dpSelf.WithoutPredecessor != 2 {
		t.Errorf("product -> product edge = %+v, want 2 ordered and 2 without", dpSelf)
	}

	// 8: browser events create episodes over trace ranges.
	triggers := map[string]int{}
	for _, ep := range res.Episodes {
		if ep.Trigger != nil {
			triggers[ep.Trigger.Type]++
		}
	}
	if triggers["interaction.enter"] != 2 || triggers["interaction.click"] != 4 {
		t.Errorf("episode triggers = %v", triggers)
	}

	// 9: the evidence pack shows all of it.
	e := entry(t, res.Evidence, dp.ID)
	if e.Observations != 4 || e.Sessions != 2 {
		t.Errorf("product entry counts = %d observations in %d sessions", e.Observations, e.Sessions)
	}
	if n := neighbor(e.Predecessors, search.ID); n == nil || len(n.Flows) == 0 {
		t.Errorf("product entry lacks the search predecessor with its flow: %+v", e.Predecessors)
	}
	if neighbor(e.Predecessors, asset.ID) != nil || e.Omitted.StaticNeighbors == 0 {
		t.Error("a static family is listed as a neighbor")
	}
	if len(e.Episodes.Triggers) == 0 || e.Episodes.Triggers[0].Key != "interaction.click" || e.Episodes.Triggers[0].Count != 4 {
		t.Errorf("product entry episodes = %+v", e.Episodes)
	}
	if len(res.Evidence.Static) != 1 || res.Evidence.Static[0].Family != asset.ID || res.Evidence.Static[0].Observations != 6 {
		t.Errorf("static list = %+v", res.Evidence.Static)
	}
	for _, f := range res.Evidence.Families {
		if f.Family == asset.ID {
			t.Error("a static family has a full entry")
		}
	}
}

func TestFlaggedValuesAreHiddenFromTheEvidence(t *testing.T) {
	res, err := Run(fixture(t), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Evidence.Families {
		for _, l := range e.ValueLinks {
			if len(l.Flags) > 0 {
				t.Errorf("entry %s shows flagged link %+v", e.Family, l)
			}
		}
	}
	// The flagged links still exist in the full output.
	var flagged int
	for _, l := range res.Links {
		if len(l.Flags) > 0 {
			flagged++
		}
	}
	if flagged == 0 {
		t.Error("the fixture produced no flagged link to hide")
	}
}

func TestOutputIsIdempotentAndIgnoresInputOrder(t *testing.T) {
	recs := fixture(t)
	a, err := Run(recs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run([]*recording.Recording{recs[1], recs[0]}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	da, db := t.TempDir(), t.TempDir()
	if err := Write(da, a, true); err != nil {
		t.Fatal(err)
	}
	if err := Write(db, b, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(Files, "evidence.yaml") {
		x, err := os.ReadFile(filepath.Join(da, name))
		if err != nil {
			t.Fatal(err)
		}
		y, err := os.ReadFile(filepath.Join(db, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(x, y) {
			t.Errorf("%s differs between runs", name)
		}
	}
}

func TestRunnerCacheGivesTheSameResultAsAFreshRun(t *testing.T) {
	recs := fixture(t)
	partial := *recs[0]
	partial.Exchanges = partial.Exchanges[:4]
	partial.Events = partial.Events[:3]
	r := NewRunner(DefaultOptions())
	if _, err := r.Run([]*recording.Recording{&partial, recs[1]}); err != nil {
		t.Fatal(err)
	}
	cached, err := r.Run(recs)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Run(recs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if marshal(t, cached) != marshal(t, fresh) {
		t.Error("a runner that saw a shorter recording first gave a different result")
	}
}

// semanticWords are names the pass must never assign. They may appear in
// output only inside recorded strings, so the test reads keys and the
// values the pass itself generates.
var semanticWords = regexp.MustCompile(`(?i)login|token|search|product|cart|auth|session_state|checkout`)

func TestOutputAssignsNoSemanticNames(t *testing.T) {
	res, err := Run(fixture(t), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if semanticWords.MatchString(k) {
					t.Errorf("key %s.%s names a meaning", path, k)
				}
				walk(path+"."+k, x)
			}
		case []any:
			for _, x := range v {
				walk(path+"[]", x)
			}
		case string:
			// Ids, carriers, classes and kinds are generated; recorded
			// strings (urls, values, paths) are allowed to say anything.
			if strings.HasSuffix(path, ".carrier") || strings.HasSuffix(path, ".class") || strings.HasSuffix(path, ".body_kind") {
				if semanticWords.MatchString(v) {
					t.Errorf("%s = %q names a meaning", path, v)
				}
			}
		}
	}
	var doc any
	if err := json.Unmarshal([]byte(marshal(t, res)), &doc); err != nil {
		t.Fatal(err)
	}
	walk("", doc)
}

func TestTheSameRecordingTwiceIsRejected(t *testing.T) {
	recs := fixture(t)
	if _, err := Run([]*recording.Recording{recs[0], recs[0]}, DefaultOptions()); err == nil {
		t.Error("a duplicate recording was accepted")
	}
}

// TestAcceptanceOnRealRecordings runs on recordings named in
// WEBSHADOW_ACCEPTANCE_RECORDINGS, a list of recording directories
// separated by the OS path list separator. Real recordings carry session
// cookies, so they are never checked in.
func TestAcceptanceOnRealRecordings(t *testing.T) {
	list := os.Getenv("WEBSHADOW_ACCEPTANCE_RECORDINGS")
	if list == "" {
		t.Skip("WEBSHADOW_ACCEPTANCE_RECORDINGS is not set")
	}
	var recs []*recording.Recording
	for _, dir := range filepath.SplitList(list) {
		r, err := recording.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	res, err := Run(recs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	crossing, cookieFlows, without := 0, 0, 0
	for _, f := range res.Families {
		sessions := map[string]bool{}
		for _, r := range f.Observations {
			sessions[r.SessionID] = true
		}
		if len(sessions) == len(recs) {
			crossing++
		}
	}
	for _, e := range res.Sequences {
		if hasFlow(&e, model.CarrierCookie) {
			cookieFlows++
		}
		if e.WithoutPredecessor > 0 {
			without++
		}
	}
	c := res.Manifest.Counts
	t.Logf("%d exchanges, %d families (%d static, %d in every recording), %d links, %d episodes, %d edges, %d flows",
		c.Observations, c.Families, c.Static, crossing, c.Links, c.Episodes, c.Edges, c.Flows)
	if len(recs) > 1 && crossing == 0 {
		t.Error("no family spans every recording")
	}
	if cookieFlows == 0 {
		t.Error("no cookie state flow")
	}
	if without == 0 {
		t.Error("no absence evidence")
	}
	if len(res.Traces) != len(recs) {
		t.Errorf("got %d traces for %d recordings", len(res.Traces), len(recs))
	}
	again, err := Run(recs, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if marshal(t, again) != marshal(t, res) {
		t.Error("two runs over the same recordings differ")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func edgeOf(res *Result, from, to string) *model.SequenceEdge {
	for i := range res.Sequences {
		if e := &res.Sequences[i]; e.FromFamily == from && e.ToFamily == to {
			return e
		}
	}
	return nil
}

func hasFlow(e *model.SequenceEdge, carrier string) bool {
	for _, f := range e.ValueFlows {
		if f.Carrier == carrier {
			return true
		}
	}
	return false
}

func entry(t *testing.T, p evidence.Pack, family string) *evidence.Entry {
	t.Helper()
	for i := range p.Families {
		if p.Families[i].Family == family {
			return &p.Families[i]
		}
	}
	t.Fatalf("no evidence entry for %s", family)
	return nil
}

func neighbor(list []evidence.Neighbor, family string) *evidence.Neighbor {
	for i := range list {
		if list[i].Family == family {
			return &list[i]
		}
	}
	return nil
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
