package stateflow

import (
	"strconv"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/values"
)

const ms = int64(1e6)

// step builds an observation that started at start ms and completed 20 ms
// later, with the given values.
func step(seq uint64, start int64, url string, vals ...model.ValueRef) model.Observation {
	return model.Observation{
		SessionID: "rec_1", ExchangeID: "ex_" + strconv.Itoa(int(seq)), Seq: seq,
		RequestStart: start * ms, End: (start + 20) * ms,
		Method: "GET", URL: url, Values: vals,
	}
}

func v(side, part, pattern, value string) model.ValueRef {
	return model.ValueRef{Value: value, Type: model.TypeString, Loc: model.Location{Side: side, Part: part, Path: pattern, Pattern: pattern}}
}

func detect(obs []model.Observation) []Flow {
	byObs := map[string]string{}
	tr := model.Trace{SessionID: "rec_1"}
	m := map[string]*model.Observation{}
	for i := range obs {
		o := &obs[i]
		fam := "fam_" + o.URL
		byObs[o.Ref().Key()] = fam
		m[o.Ref().Key()] = o
		tr.Steps = append(tr.Steps, model.TraceStep{T: o.RequestStart, Ref: o.Ref(), Method: o.Method, URL: o.URL, FamilyID: fam})
	}
	ix := values.Build(obs, familiesOf(obs, byObs), nil, values.DefaultOptions())
	return Detect(tr, m, ix)
}

func TestSetCookieSentBackIsACookieFlow(t *testing.T) {
	flows := detect([]model.Observation{
		step(1, 0, "https://shop.test/", v(model.SideResponse, model.PartSetCookie, "session-id", "131-7766012-5550344")),
		step(2, 100, "https://shop.test/s", v(model.SideRequest, model.PartCookie, "session-id", "131-7766012-5550344")),
	})
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1: %+v", len(flows), flows)
	}
	f := flows[0]
	if f.Carrier != model.CarrierCookie || f.FromFamily != "fam_https://shop.test/" || f.ToFamily != "fam_https://shop.test/s" || f.Delta != 100*ms {
		t.Errorf("flow = %+v", f)
	}
}

func TestResponseValueInALaterRequestIsAFlow(t *testing.T) {
	flows := detect([]model.Observation{
		step(1, 0, "https://api.test/token", v(model.SideResponse, model.PartBody, "token", "abc123def456")),
		step(2, 45, "https://api.test/search", v(model.SideRequest, model.PartHeader, "Authorization", "abc123def456")),
		step(3, 90, "https://api.test/more", v(model.SideRequest, model.PartQuery, "t", "abc123def456")),
	})
	if len(flows) != 2 {
		t.Fatalf("got %d flows, want 2: %+v", len(flows), flows)
	}
	if flows[0].Carrier != model.CarrierHeader || flows[0].FromLoc.String() != "response.body.token" || flows[0].ToLoc.String() != "request.header.Authorization" {
		t.Errorf("header flow = %+v", flows[0])
	}
	if flows[1].Carrier != model.CarrierQuery {
		t.Errorf("query flow = %+v", flows[1])
	}
}

func TestOnlyCompletedResponsesAreSources(t *testing.T) {
	// The second request starts before the first response completed, so it
	// cannot have taken the value from it.
	flows := detect([]model.Observation{
		step(1, 0, "https://api.test/a", v(model.SideResponse, model.PartBody, "id", "B0FV975MJK")),
		step(2, 10, "https://api.test/b", v(model.SideRequest, model.PartPath, "1", "B0FV975MJK")),
	})
	if len(flows) != 0 {
		t.Errorf("got flows from an unfinished response: %+v", flows)
	}
}

func TestTheMostRecentSourceIsCredited(t *testing.T) {
	flows := detect([]model.Observation{
		step(1, 0, "https://api.test/first", v(model.SideResponse, model.PartBody, "id", "B0FV975MJK")),
		step(2, 50, "https://api.test/second", v(model.SideResponse, model.PartBody, "items[].id", "B0FV975MJK")),
		step(3, 100, "https://shop.test/dp", v(model.SideRequest, model.PartPath, "1", "B0FV975MJK")),
	})
	if len(flows) != 1 || flows[0].FromFamily != "fam_https://api.test/second" {
		t.Errorf("flows = %+v, want one from the second response", flows)
	}
}

func TestRedirectLocationBecomesTheNextURL(t *testing.T) {
	flows := detect([]model.Observation{
		step(1, 0, "https://shop.test/gp/goldbox", v(model.SideResponse, model.PartLocation, "Location", "/primebigdealdays?ref=x")),
		step(2, 30, "https://shop.test/primebigdealdays?ref=x"),
	})
	var redirect *Flow
	for i := range flows {
		if flows[i].Carrier == model.CarrierRedirect {
			redirect = &flows[i]
		}
	}
	if redirect == nil || redirect.ToLoc.Part != model.PartURL || redirect.Value != "https://shop.test/primebigdealdays?ref=x" {
		t.Errorf("flows = %+v, want a redirect flow to the resolved URL", flows)
	}
}

// familiesOf names each observation's family after its URL, which is all
// these tests need from family construction.
func familiesOf(_ []model.Observation, byObs map[string]string) family.Result {
	return family.Result{ByObservation: byObs}
}
