package evidence

import (
	"strconv"
	"testing"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

func fam(id string, static bool, n int) model.RequestFamily {
	f := model.RequestFamily{ID: id, Method: "GET", Host: "shop.test", PathTemplate: "/" + id, BodyKind: model.BodyNone, Static: static}
	for i := 0; i < n; i++ {
		f.Observations = append(f.Observations, model.ObservationRef{SessionID: "rec_" + strconv.Itoa(i%2), ExchangeID: "ex_" + strconv.Itoa(i), Seq: uint64(i)})
	}
	return f
}

func loc(side, part, pattern string) model.Location {
	return model.Location{Side: side, Part: part, Pattern: pattern}
}

func TestFlaggedFlowsShowOnlyWhenConsistent(t *testing.T) {
	flow := func(matches int) model.ValueFlow {
		return model.ValueFlow{
			Carrier: model.CarrierQuery, Matches: matches, Values: 1, Flags: []string{model.FlagLowInformation},
			From: loc(model.SideResponse, model.PartBody, "page"), To: loc(model.SideRequest, model.PartQuery, "p"),
		}
	}
	in := Input{
		Families: []model.RequestFamily{fam("A", false, 10), fam("B", false, 10), fam("C", false, 10)},
		Sequences: []model.SequenceEdge{
			{ID: "se_1", FromFamily: "A", ToFamily: "C", OrderedCount: 10, ToObservations: 10, ValueFlows: []model.ValueFlow{flow(10)}},
			{ID: "se_2", FromFamily: "B", ToFamily: "C", OrderedCount: 10, ToObservations: 10, ValueFlows: []model.ValueFlow{flow(3)}},
		},
	}
	p := Build(in, DefaultOptions())
	c := p.Families[2]
	if c.Family != "C" || len(c.Predecessors) != 2 {
		t.Fatalf("entry = %+v", c)
	}
	// The predecessor with a shown flow ranks first.
	a, b := c.Predecessors[0], c.Predecessors[1]
	if a.Family != "A" || len(a.Flows) != 1 || a.Flows[0].Of != 10 {
		t.Errorf("consistent flagged flow = %+v", a)
	}
	if b.Family != "B" || len(b.Flows) != 0 || b.HiddenFlows != 1 {
		t.Errorf("inconsistent flagged flow = %+v", b)
	}
}

func TestNeighborsAreBoundedAndStaticOnesCollapsed(t *testing.T) {
	in := Input{Families: []model.RequestFamily{fam("Z", false, 4), fam("S", true, 9)}}
	in.Sequences = append(in.Sequences, model.SequenceEdge{ID: "se_s", FromFamily: "S", ToFamily: "Z", OrderedCount: 4, ToObservations: 4})
	for i := 0; i < 8; i++ {
		id := "P" + strconv.Itoa(i)
		in.Families = append(in.Families, fam(id, false, 2))
		in.Sequences = append(in.Sequences, model.SequenceEdge{ID: "se_" + id, FromFamily: id, ToFamily: "Z", OrderedCount: i % 4, ToObservations: 4})
	}
	p := Build(in, DefaultOptions())
	var z *Entry
	for i := range p.Families {
		if p.Families[i].Family == "Z" {
			z = &p.Families[i]
		}
	}
	if z == nil {
		t.Fatal("no entry for Z")
	}
	if len(z.Predecessors) != 5 || z.Omitted.Predecessors != 3 || z.Omitted.StaticNeighbors != 1 {
		t.Errorf("predecessors = %d, omitted %+v", len(z.Predecessors), z.Omitted)
	}
	if z.Predecessors[0].Ordered != 3 {
		t.Errorf("the most ordered predecessor is not first: %+v", z.Predecessors[0])
	}
	if len(p.Static) != 1 || p.Static[0].Observations != 9 {
		t.Errorf("static = %+v", p.Static)
	}
}
