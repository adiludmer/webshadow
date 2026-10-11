// Package trace keeps the observed protocol order that clustering would
// otherwise throw away. Each session becomes one trace: every exchange in
// the order its request started, named by its family, given the kind its
// request headers declare and placed in its page view.
//
// A trace says only that these families were observed in this order. It
// never says one step required another.
package trace

import (
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster/episode"
	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Session is what one trace is built from.
type Session struct {
	ID string
	// Observations are the session's exchanges, in any order.
	Observations []model.Observation
}

// Build returns the session's trace and episodes. byObs maps an
// observation's reference key to its family id.
func Build(s Session, byObs map[string]string, opts episode.Options) (model.Trace, []model.Episode) {
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

	steps := make([]model.TraceStep, 0, len(obs))
	for _, o := range obs {
		steps = append(steps, model.TraceStep{
			T:        o.RequestStart,
			Ref:      o.Ref(),
			Method:   o.Method,
			URL:      o.URL,
			FamilyID: byObs[o.Ref().Key()],
			Kind:     episode.RequestKind(o),
		})
	}

	navs, episodes := episode.Partition(s.ID, obs, steps, opts)
	if navs == nil {
		navs = []model.Navigation{}
	}
	return model.Trace{
		ID:          "tr_" + model.Hash("trace", s.ID),
		SessionID:   s.ID,
		Navigations: navs,
		Steps:       steps,
	}, episodes
}
