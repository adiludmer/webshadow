package trace

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/episode"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/recording"
)

const sec = int64(1e9)

// fixture is one browsing session: a homepage load the user did not click
// for, a click into a product page, then a search submitted with Enter.
func fixture() Session {
	ex := func(seq uint64, t float64, url string) model.Observation {
		return model.Observation{
			SessionID: "rec_1", ExchangeID: "ex_" + string(rune('a'+seq)), Seq: seq,
			RequestStart: int64(t * float64(sec)), Method: "GET", URL: url,
		}
	}
	ev := func(seq uint64, t float64, typ, target, url, payload string) recording.BrowserEvent {
		return recording.BrowserEvent{
			ID: "ev_" + string(rune('a'+seq)), Seq: seq, SessionID: "rec_1",
			Timestamp: recording.Stamp{T: int64(t * float64(sec))},
			Type:      typ, TargetID: target, PageURL: url, Payload: json.RawMessage(payload),
		}
	}
	return Session{
		ID: "rec_1",
		Observations: []model.Observation{
			ex(1, 0.30, "https://www.google.com/async/folae"),
			ex(2, 0.44, "https://shop.test/"),
			ex(4, 1.50, "https://shop.test/api/recs"),
			ex(6, 5.10, "https://shop.test/dp/B0FV975MJK"),
			ex(8, 6.00, "https://shop.test/api/price"),
			ex(11, 9.20, "https://shop.test/s?k=ring"),
		},
		Events: []recording.BrowserEvent{
			ev(1, 0.10, "Target.targetCreated", "T1", "about:blank", `{"targetInfo":{"targetId":"T1","type":"page"}}`),
			ev(2, 0.20, "Target.targetCreated", "T2", "", `{"targetInfo":{"targetId":"T2","type":"iframe"}}`),
			ev(3, 1.10, "Page.frameNavigated", "T1", "https://shop.test/", `{"frame":{"id":"F1","url":"https://shop.test/"}}`),
			ev(5, 1.20, "Page.frameNavigated", "T2", "https://ads.test/slot", `{"frame":{"id":"F9","url":"https://ads.test/slot"}}`),
			ev(6, 1.49, "Network.requestWillBeSent", "T1", "https://shop.test/", `{"frameId":"F1","request":{"url":"https://shop.test/api/recs","method":"GET"},"initiator":{"type":"script"}}`),
			ev(7, 5.00, "interaction.click", "T1", "https://shop.test/", `{"type":"click","target":{"tag":"a"}}`),
			ev(9, 5.60, "Page.frameNavigated", "T1", "https://shop.test/dp/B0FV975MJK", `{"frame":{"id":"F1","url":"https://shop.test/dp/B0FV975MJK"}}`),
			ev(10, 9.00, "interaction.enter", "T1", "https://shop.test/dp/B0FV975MJK", `{"type":"enter","value":"ring"}`),
			ev(12, 9.40, "Page.navigatedWithinDocument", "T1", "https://shop.test/s?k=ring", `{"frameId":"F1","url":"https://shop.test/s?k=ring"}`),
		},
	}
}

func families() map[string]string {
	return map[string]string{
		"rec_1/ex_b": "fam_google", "rec_1/ex_c": "fam_home", "rec_1/ex_e": "fam_recs",
		"rec_1/ex_g": "fam_dp", "rec_1/ex_i": "fam_price", "rec_1/ex_l": "fam_search",
	}
}

func TestTraceKeepsRecordedOrderExactly(t *testing.T) {
	s := fixture()
	tr, _ := Build(s, families(), episode.DefaultOptions())
	want := []string{"fam_google", "fam_home", "fam_recs", "fam_dp", "fam_price", "fam_search"}
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
	if tr.Steps[5].Ref.ExchangeID != "ex_x" || tr.Steps[6].Ref.ExchangeID != "ex_y" {
		t.Errorf("tie order = %s, %s; want ex_x then ex_y", tr.Steps[5].Ref.ExchangeID, tr.Steps[6].Ref.ExchangeID)
	}
}

func TestEpisodesFollowTriggers(t *testing.T) {
	tr, eps := Build(fixture(), families(), episode.DefaultOptions())
	if len(eps) != 4 {
		t.Fatalf("got %d episodes, want 4 (untriggered, homepage load, click, enter): %+v", len(eps), eps)
	}
	type want struct {
		trigger  string
		start    int64
		families []string
	}
	wants := []want{
		{"", 0, []string{"fam_google"}},
		{"Page.frameNavigated", 440 * sec / 1000, []string{"fam_home", "fam_recs"}},
		{"interaction.click", 5 * sec, []string{"fam_dp", "fam_price"}},
		{"interaction.enter", 9 * sec, []string{"fam_search"}},
	}
	for i, w := range wants {
		e := eps[i]
		trig := ""
		if e.Trigger != nil {
			trig = e.Trigger.Type
		}
		if trig != w.trigger || e.Start != w.start || !equal(e.FamilyIDs, w.families) {
			t.Errorf("episode %d = trigger %q start %d families %v; want %q %d %v", i, trig, e.Start, e.FamilyIDs, w.trigger, w.start, w.families)
		}
		if i+1 < len(eps) && e.End != eps[i+1].Start {
			t.Errorf("episode %d ends at %d, next starts at %d", i, e.End, eps[i+1].Start)
		}
	}
	// The product page navigation came from the click, and the search's
	// same-document navigation from the Enter: neither starts an episode.
	// The ad frame's navigation is not the user's at all.
	if len(tr.Navigations) != 2 {
		t.Fatalf("got %d navigations, want 2: %+v", len(tr.Navigations), tr.Navigations)
	}
	if tr.Navigations[1].Start != 5100*sec/1000 {
		t.Errorf("product navigation starts at %d, want its document request at 5.1s", tr.Navigations[1].Start)
	}
	if got := tr.Steps[4]; got.SinceTrigger != sec || got.NavigationID != tr.Navigations[1].ID {
		t.Errorf("price step = %+v, want 1s after the click, in the product navigation", got)
	}
	if eps[2].FirstStep != 3 || eps[2].LastStep != 4 {
		t.Errorf("click episode spans steps %d..%d, want 3..4", eps[2].FirstStep, eps[2].LastStep)
	}
}

func TestStepsBorrowTheBrowsersInitiator(t *testing.T) {
	tr, _ := Build(fixture(), families(), episode.DefaultOptions())
	recs := tr.Steps[2]
	if recs.Initiator != "script" || recs.FrameID != "F1" || recs.TargetID != "T1" {
		t.Errorf("recs step = %+v, want the script initiator from CDP", recs)
	}
	if tr.Steps[0].Initiator != "" {
		t.Errorf("an exchange with no browser report got initiator %q", tr.Steps[0].Initiator)
	}
}

func TestSameInputsGiveTheSameTraceAndEpisodes(t *testing.T) {
	want := encode(t, fixture())
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 20; i++ {
		s := fixture()
		rng.Shuffle(len(s.Observations), func(a, b int) { s.Observations[a], s.Observations[b] = s.Observations[b], s.Observations[a] })
		rng.Shuffle(len(s.Events), func(a, b int) { s.Events[a], s.Events[b] = s.Events[b], s.Events[a] })
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
