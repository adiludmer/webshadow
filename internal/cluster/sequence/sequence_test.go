package sequence

import (
	"encoding/json"
	"math/rand"
	"strconv"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/stateflow"
	"github.com/adiludmer/webshadow/internal/cluster/values"
)

const ms = int64(1e6)

// trace builds a session whose steps are the given families, 10 ms apart
// unless times are given.
func trace(session string, fams []string, times ...int64) model.Trace {
	tr := model.Trace{SessionID: session}
	for i, f := range fams {
		t := int64(i) * 10 * ms
		if i < len(times) {
			t = times[i] * ms
		}
		tr.Steps = append(tr.Steps, model.TraceStep{
			T:        t,
			Ref:      model.ObservationRef{SessionID: session, ExchangeID: "ex_" + strconv.Itoa(i), Seq: uint64(i + 1)},
			FamilyID: f,
		})
	}
	return tr
}

func emptyIndex() *values.Index {
	return values.Build(nil, family.Result{}, nil, values.DefaultOptions())
}

func find(edges []model.SequenceEdge, from, to string) *model.SequenceEdge {
	for i := range edges {
		if edges[i].FromFamily == from && edges[i].ToFamily == to {
			return &edges[i]
		}
	}
	return nil
}

func TestRecordingsAggregateIntoOneGraph(t *testing.T) {
	edges := Aggregate([]model.Trace{
		trace("rec_1", []string{"F1", "F2", "F3", "F4"}),
		trace("rec_2", []string{"F1", "F2", "F3", "F5"}),
		trace("rec_3", []string{"F1", "F2", "F3", "F4"}),
		trace("rec_4", []string{"F1", "F2", "F3", "F6"}),
	}, nil, emptyIndex())
	for _, c := range []struct {
		from, to  string
		immediate int
	}{
		{"F1", "F2", 4}, {"F2", "F3", 4}, {"F3", "F4", 2}, {"F3", "F5", 1}, {"F3", "F6", 1},
	} {
		e := find(edges, c.from, c.to)
		if e == nil || e.Immediate != c.immediate {
			t.Errorf("%s -> %s = %+v, want immediate %d", c.from, c.to, e, c.immediate)
		}
	}
	// A predecessor counts however far back it was.
	e := find(edges, "F1", "F4")
	if e == nil || e.OrderedCount != 2 || e.Immediate != 0 || e.SessionsWithBoth != 2 || e.WithoutPredecessor != 0 {
		t.Errorf("F1 -> F4 = %+v", e)
	}
	if find(edges, "F4", "F1") != nil {
		t.Error("an order never observed produced an edge")
	}
}

func TestSuccessorsWithoutThePredecessorAreCounted(t *testing.T) {
	edges := Aggregate([]model.Trace{
		trace("rec_a", []string{"F12", "F31"}),
		trace("rec_b", []string{"F31", "F31"}),
		trace("rec_c", []string{"F31", "F12", "F31"}),
	}, nil, emptyIndex())
	e := find(edges, "F12", "F31")
	if e == nil {
		t.Fatal("no F12 -> F31 edge")
	}
	type counts struct{ to, ordered, without, together, sessions, immediate int }
	want := counts{to: 5, ordered: 2, without: 3, together: 3, sessions: 2, immediate: 2}
	got := counts{e.ToObservations, e.OrderedCount, e.WithoutPredecessor, e.ObservedTogether, e.SessionsWithBoth, e.Immediate}
	if got != want {
		t.Errorf("F12 -> F31 counts = %+v, want %+v", got, want)
	}
}

func TestTimingIsMeasuredFromTheMostRecentPredecessor(t *testing.T) {
	edges := Aggregate([]model.Trace{
		trace("rec_a", []string{"F12", "F31"}, 0, 83),
		trace("rec_b", []string{"F12", "F31", "F31"}, 0, 100, 300),
		trace("rec_c", []string{"F12", "F12", "F31"}, 0, 500, 520),
	}, nil, emptyIndex())
	e := find(edges, "F12", "F31")
	if e == nil {
		t.Fatal("no edge")
	}
	// Gaps are 83, 100, 300 and 20 ms (from the second F12); with an even
	// count the lower middle is the median.
	if want := (model.Timing{Min: 20 * ms, Median: 83 * ms, Max: 300 * ms}); e.Timing != want {
		t.Errorf("timing = %+v, want %+v", e.Timing, want)
	}
}

func TestValueFlowsAttachToTheirEdge(t *testing.T) {
	tr := trace("rec_a", []string{"F12", "F31", "F31"})
	ref := func(i int) model.ObservationRef { return tr.Steps[i].Ref }
	flow := func(to int, value string) stateflow.Flow {
		return stateflow.Flow{
			Carrier: model.CarrierCookie, Value: value,
			From: ref(0), FromFamily: "F12", FromLoc: model.Location{Side: model.SideResponse, Part: model.PartSetCookie, Pattern: "session"},
			To: ref(to), ToFamily: "F31", ToLoc: model.Location{Side: model.SideRequest, Part: model.PartCookie, Pattern: "session"},
		}
	}
	edges := Aggregate([]model.Trace{tr}, []stateflow.Flow{flow(2, "XYZ789abc"), flow(1, "XYZ789abc")}, emptyIndex())
	e := find(edges, "F12", "F31")
	if e == nil || len(e.ValueFlows) != 1 {
		t.Fatalf("edge = %+v, want one value flow", e)
	}
	vf := e.ValueFlows[0]
	if vf.Matches != 2 || vf.Values != 1 || vf.Carrier != model.CarrierCookie || len(vf.Examples) != 2 {
		t.Errorf("flow = %+v", vf)
	}
	if vf.Examples[0].To != ref(1) {
		t.Errorf("examples are not in trace order: %+v", vf.Examples)
	}
}

func TestEdgesIgnoreTraceOrder(t *testing.T) {
	traces := []model.Trace{
		trace("rec_1", []string{"F1", "F2", "F3", "F2", "F4"}),
		trace("rec_2", []string{"F2", "F1", "F3"}),
		trace("rec_3", []string{"F3", "F3", "F1", "F4"}),
	}
	want := encode(t, Aggregate(traces, nil, emptyIndex()))
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 20; i++ {
		shuffled := append([]model.Trace(nil), traces...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := encode(t, Aggregate(shuffled, nil, emptyIndex())); got != want {
			t.Fatalf("permutation %d changed the edges", i)
		}
	}
}

func encode(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
