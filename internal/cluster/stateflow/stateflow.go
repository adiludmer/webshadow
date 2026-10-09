// Package stateflow detects values that travel from an earlier response to
// a later request in the same session, through a small set of transport
// carriers: a Set-Cookie value sent back as a Cookie, a redirect Location
// that becomes the next URL, and a response value that reappears in a
// later request's header, query, path or body.
//
// A flow is a structural fact about transport. Nothing here calls a cookie a
// credential or decides that a flow is required for anything.
package stateflow

import (
	"net/url"
	"sort"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/values"
)

// Flow is one instance: a value seen in From's response and then in To's
// request.
type Flow struct {
	Carrier    string
	Value      string
	From       model.ObservationRef
	FromFamily string
	FromLoc    model.Location
	To         model.ObservationRef
	ToFamily   string
	ToLoc      model.Location
	// Delta is To's request start minus From's, in nanoseconds.
	Delta int64
}

type source struct {
	step int
	locs []model.Location
}

type sighting struct {
	value string
	loc   model.Location
}

// Detect returns the flows in one trace, in trace order of their receiving
// request. obs maps a reference key to its observation; ix supplies every
// sighting, including values found inside text bodies.
//
// A value is attributed to the most recent earlier response that carried it
// and had completed before the request started, since that is the copy the
// browser or page could have used.
func Detect(tr model.Trace, obs map[string]*model.Observation, ix *values.Index) []Flow {
	n := len(tr.Steps)
	if n == 0 {
		return nil
	}
	req, resp := sightings(tr, ix)

	steps := make([]*model.Observation, n)
	for i, s := range tr.Steps {
		steps[i] = obs[s.Ref.Key()]
	}
	// Responses become usable in the order they completed.
	order := make([]int, 0, n)
	for i := range steps {
		if steps[i] != nil {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ta, tb := available(steps[order[a]]), available(steps[order[b]])
		if ta != tb {
			return ta < tb
		}
		return order[a] < order[b]
	})

	avail := map[string]*source{}
	redirects := map[string]int{}
	next := 0
	var out []Flow
	for j, y := range steps {
		if y == nil {
			continue
		}
		for next < len(order) && available(steps[order[next]]) <= y.RequestStart {
			i := order[next]
			next++
			if i == j {
				continue
			}
			for _, s := range resp[i] {
				// Concrete array positions are dropped, so a value in row 4
				// of one response and row 7 of another aggregate as one
				// source location.
				loc := s.loc
				loc.Path = ""
				src := avail[s.value]
				if src == nil || src.step != i {
					src = &source{step: i}
					avail[s.value] = src
				}
				src.locs = appendLoc(src.locs, loc)
				if s.loc.Part == model.PartLocation {
					if target := resolve(steps[i].URL, s.value); target != "" {
						redirects[target] = i
					}
				}
			}
		}

		if i, ok := redirects[y.URL]; ok && i != j {
			out = append(out, flow(tr, steps, i, j, model.CarrierRedirect, y.URL,
				model.Location{Side: model.SideResponse, Part: model.PartLocation, Path: "Location", Pattern: "Location"},
				model.Location{Side: model.SideRequest, Part: model.PartURL}))
			delete(redirects, y.URL)
		}
		for _, s := range req[j] {
			src := avail[s.value]
			if src == nil || src.step == j {
				continue
			}
			to := s.loc
			to.Path = ""
			for _, from := range src.locs {
				out = append(out, flow(tr, steps, src.step, j, carrier(from, s.loc), s.value, from, to))
			}
		}
	}
	return out
}

func flow(tr model.Trace, steps []*model.Observation, i, j int, carrier, value string, from, to model.Location) Flow {
	return Flow{
		Carrier:    carrier,
		Value:      value,
		From:       tr.Steps[i].Ref,
		FromFamily: tr.Steps[i].FamilyID,
		FromLoc:    from,
		To:         tr.Steps[j].Ref,
		ToFamily:   tr.Steps[j].FamilyID,
		ToLoc:      to,
		Delta:      steps[j].RequestStart - steps[i].RequestStart,
	}
}

// carrier names how a value reached a request location.
func carrier(from, to model.Location) string {
	switch to.Part {
	case model.PartCookie:
		if from.Part == model.PartSetCookie && from.Pattern == to.Pattern {
			return model.CarrierCookie
		}
		// A cookie whose value the server sent some other way is still a
		// header the browser carried.
		return model.CarrierHeader
	case model.PartHeader:
		return model.CarrierHeader
	case model.PartQuery:
		return model.CarrierQuery
	case model.PartPath:
		return model.CarrierPath
	default:
		return model.CarrierBody
	}
}

// available is when an exchange's response could first have been used.
func available(o *model.Observation) int64 {
	switch {
	case o.End != 0:
		return o.End
	case o.ResponseStart != 0:
		return o.ResponseStart
	}
	return o.RequestStart
}

// sightings splits the index's sightings by trace step and side. Values
// found inside text bodies only exist in the index, which is why flows are
// detected from it rather than from the observations alone.
func sightings(tr model.Trace, ix *values.Index) (req, resp [][]sighting) {
	pos := make(map[string]int, len(tr.Steps))
	for i, s := range tr.Steps {
		pos[s.Ref.Key()] = i
	}
	req = make([][]sighting, len(tr.Steps))
	resp = make([][]sighting, len(tr.Steps))
	for _, v := range ix.Values() {
		for _, o := range ix.Occurrences(v) {
			i, ok := pos[o.Ref.Key()]
			if !ok {
				continue
			}
			s := sighting{value: v, loc: o.Loc}
			if o.Loc.Side == model.SideRequest {
				req[i] = append(req[i], s)
			} else {
				resp[i] = append(resp[i], s)
			}
		}
	}
	return req, resp
}

func appendLoc(list []model.Location, loc model.Location) []model.Location {
	for _, l := range list {
		if l == loc {
			return list
		}
	}
	list = append(list, loc)
	sort.Slice(list, func(a, b int) bool { return list[a].String() < list[b].String() })
	return list
}

// resolve turns a Location header into the absolute URL the browser would
// request next.
func resolve(base, location string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	l, err := url.Parse(location)
	if err != nil {
		return ""
	}
	u := b.ResolveReference(l)
	u.Fragment = ""
	return u.String()
}
