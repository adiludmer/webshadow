// Package trace keeps the observed protocol order that clustering would
// otherwise throw away. Each session becomes one trace: every exchange in
// the order its request started, named by its family, placed in its
// navigation and episode, and matched where possible to the browser's own
// report of the request.
//
// A trace says only that these families were observed in this order. It
// never says one step required another.
package trace

import (
	"encoding/json"
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster/episode"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/recording"
)

// Session is what one trace is built from.
type Session struct {
	ID string
	// Observations are the session's exchanges, in any order.
	Observations []model.Observation
	Events       []recording.BrowserEvent
}

// Build returns the session's trace and episodes. byObs maps an
// observation's reference key to its family id.
func Build(s Session, byObs map[string]string, opts episode.Options) (model.Trace, []model.Episode) {
	if opts.MatchWindow == 0 {
		opts = episode.DefaultOptions()
	}
	obs := make([]*model.Observation, 0, len(s.Observations))
	for i := range s.Observations {
		if s.Observations[i].SessionID == s.ID {
			obs = append(obs, &s.Observations[i])
		}
	}
	// Request start orders the trace; the recorded sequence breaks ties, so
	// the order is exactly the recorded one whatever order the input had.
	sort.SliceStable(obs, func(i, j int) bool {
		if obs[i].RequestStart != obs[j].RequestStart {
			return obs[i].RequestStart < obs[j].RequestStart
		}
		return obs[i].Seq < obs[j].Seq
	})

	reports := networkReports(s.Events)
	steps := make([]model.TraceStep, 0, len(obs))
	for _, o := range obs {
		step := model.TraceStep{
			T:        o.RequestStart,
			Ref:      o.Ref(),
			Method:   o.Method,
			URL:      o.URL,
			FamilyID: byObs[o.Ref().Key()],
		}
		if r := reports.match(o, opts.MatchWindow); r != nil {
			step.Initiator, step.FrameID, step.TargetID = r.initiator, r.frameID, r.targetID
		}
		steps = append(steps, step)
	}

	navs, episodes := episode.Partition(s.ID, s.Events, steps, opts)
	return model.Trace{
		ID:          "tr_" + model.Hash("trace", s.ID),
		SessionID:   s.ID,
		Navigations: navs,
		Steps:       steps,
	}, episodes
}

// report is the browser's account of one request, from
// Network.requestWillBeSent.
type report struct {
	t         int64
	seq       uint64
	method    string
	url       string
	initiator string
	frameID   string
	targetID  string
	used      bool
}

type reports struct {
	byKey map[string][]*report
}

func networkReports(events []recording.BrowserEvent) *reports {
	rs := &reports{byKey: map[string][]*report{}}
	for i := range events {
		ev := &events[i]
		if ev.Type != "Network.requestWillBeSent" {
			continue
		}
		var p struct {
			FrameID string `json:"frameId"`
			Request *struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			} `json:"request"`
			Initiator *struct {
				Type string `json:"type"`
			} `json:"initiator"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Request == nil {
			continue
		}
		r := &report{
			t: ev.Timestamp.T, seq: ev.Seq,
			method: p.Request.Method, url: p.Request.URL,
			frameID:  firstNonEmpty(p.FrameID, ev.FrameID),
			targetID: ev.TargetID,
		}
		if p.Initiator != nil {
			r.initiator = p.Initiator.Type
		}
		k := r.method + " " + r.url
		rs.byKey[k] = append(rs.byKey[k], r)
	}
	for _, list := range rs.byKey {
		sort.Slice(list, func(i, j int) bool {
			if list[i].t != list[j].t {
				return list[i].t < list[j].t
			}
			return list[i].seq < list[j].seq
		})
	}
	return rs
}

// match pairs an exchange with the unused report of the same method and URL
// closest in time, within window. Exchanges are matched in trace order, so a
// URL fetched twice pairs first with first.
func (rs *reports) match(o *model.Observation, window int64) *report {
	var best *report
	var bestGap int64
	for _, r := range rs.byKey[o.Method+" "+o.URL] {
		if r.used {
			continue
		}
		gap := r.t - o.RequestStart
		if gap < 0 {
			gap = -gap
		}
		if gap > window {
			continue
		}
		if best == nil || gap < bestGap {
			best, bestGap = r, gap
		}
	}
	if best != nil {
		best.used = true
	}
	return best
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
