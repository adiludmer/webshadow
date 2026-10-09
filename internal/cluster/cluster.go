// Package cluster runs the whole deterministic clustering pass over a set of
// recordings: normalize every exchange, group the observations into request
// families, index their values, build each session's trace and episodes,
// detect state and value flows, aggregate sequence edges, and project it all
// into an evidence pack.
//
// The same recordings processed with the same model.Version give the same
// output byte for byte. Nothing in the pass calls a model, reads the clock or
// depends on map order.
package cluster

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/episode"
	"github.com/adiludmer/webshadow/internal/cluster/evidence"
	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/normalize"
	"github.com/adiludmer/webshadow/internal/cluster/sequence"
	"github.com/adiludmer/webshadow/internal/cluster/stateflow"
	"github.com/adiludmer/webshadow/internal/cluster/trace"
	"github.com/adiludmer/webshadow/internal/cluster/values"
	"github.com/adiludmer/webshadow/internal/recording"
)

// Input describes one recording a result was built from.
type Input struct {
	SessionID     string `json:"session_id"`
	Exchanges     int    `json:"exchanges"`
	BrowserEvents int    `json:"browser_events"`
	Truncated     bool   `json:"truncated,omitempty"`
}

// Manifest describes a result without repeating it.
type Manifest struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	Inputs  []Input `json:"inputs"`
	Counts  Counts  `json:"counts"`
}

// Counts summarizes a result's size.
type Counts struct {
	Observations int `json:"observations"`
	Families     int `json:"families"`
	Static       int `json:"static_families"`
	Links        int `json:"value_links"`
	Episodes     int `json:"episodes"`
	Edges        int `json:"sequence_edges"`
	Flows        int `json:"value_flows"`
}

// Result is everything one clustering pass produces.
type Result struct {
	Manifest  Manifest              `json:"manifest"`
	Families  []model.RequestFamily `json:"families"`
	Links     []model.ValueLink     `json:"links"`
	Traces    []model.Trace         `json:"traces"`
	Episodes  []model.Episode       `json:"episodes"`
	Sequences []model.SequenceEdge  `json:"sequences"`
	Evidence  evidence.Pack         `json:"evidence"`
	// Flows are the individual value flows the sequence edges aggregate, in
	// trace order per session. They are not written out; the live view
	// shows the latest ones.
	Flows []stateflow.Flow `json:"-"`
}

// Options bound and tune the pass.
type Options struct {
	Normalize normalize.Options
	Values    values.Options
	Episode   episode.Options
	Evidence  evidence.Options
}

// DefaultOptions is what `webshadow cluster` uses.
func DefaultOptions() Options {
	return Options{
		Normalize: normalize.DefaultOptions(),
		Values:    values.DefaultOptions(),
		Episode:   episode.DefaultOptions(),
		Evidence:  evidence.DefaultOptions(),
	}
}

// Run clusters the given recordings. The order they are given in does not
// matter.
func Run(recs []*recording.Recording, opts Options) (*Result, error) {
	return NewRunner(opts).Run(recs)
}

// Runner runs the pass repeatedly over recordings that grow, as a live view
// does. Each exchange is normalized, and its text body tokenized, once and
// cached; everything after that is recomputed, because a new observation
// can change any route template, family or edge.
type Runner struct {
	opts   Options
	cache  map[string]model.Observation
	tokens map[string][]string
}

// NewRunner returns a runner with an empty cache.
func NewRunner(opts Options) *Runner {
	return &Runner{opts: opts, cache: map[string]model.Observation{}, tokens: map[string][]string{}}
}

// Run clusters the recordings as they are now.
func (r *Runner) Run(recs []*recording.Recording) (*Result, error) {
	recs = append([]*recording.Recording(nil), recs...)
	sort.Slice(recs, func(i, j int) bool { return recs[i].Session.ID < recs[j].Session.ID })
	for i := 1; i < len(recs); i++ {
		if recs[i].Session.ID == recs[i-1].Session.ID {
			return nil, fmt.Errorf("cluster: recording %s given twice", recs[i].Session.ID)
		}
	}

	type source struct {
		rec *recording.Recording
		ex  *recording.HTTPExchange
	}
	sources := map[string]source{}
	var obs []model.Observation
	bySession := map[string][]model.Observation{}
	for _, rec := range recs {
		for i := range rec.Exchanges {
			ex := &rec.Exchanges[i]
			if ex.SessionID == "" {
				ex.SessionID = rec.Session.ID
			}
			key := ex.SessionID + "/" + ex.ID
			o, ok := r.cache[key]
			if !ok {
				var err error
				o, err = normalize.Exchange(rec, ex, r.opts.Normalize)
				if err != nil {
					return nil, err
				}
				r.cache[key] = o
			}
			sources[key] = source{rec, ex}
			obs = append(obs, o)
			bySession[rec.Session.ID] = append(bySession[rec.Session.ID], o)
		}
	}

	fams := family.Build(obs)
	text := func(o *model.Observation) ([]string, bool) {
		key := o.Ref().Key()
		if toks, ok := r.tokens[key]; ok {
			return toks, toks != nil
		}
		s, ok := sources[key]
		if !ok || s.ex.Response == nil || s.ex.Response.Body == nil {
			return nil, false
		}
		data, err := normalize.ReadBody(s.rec, s.ex.Response.Body)
		if err != nil {
			// Kept as nil so an unreadable body is not read again.
			r.tokens[key] = nil
			return nil, false
		}
		toks := []string{}
		for _, t := range values.Tokens(data) {
			if len(t) >= r.opts.Values.MinTextValueLen {
				toks = append(toks, t)
			}
		}
		r.tokens[key] = toks
		return toks, true
	}
	ix := values.BuildFromTokens(obs, fams, text, r.opts.Values)

	byKey := make(map[string]*model.Observation, len(obs))
	for i := range obs {
		byKey[obs[i].Ref().Key()] = &obs[i]
	}
	res := &Result{Families: fams.Families, Links: ix.Links()}
	var flows []stateflow.Flow
	for _, rec := range recs {
		tr, eps := trace.Build(trace.Session{
			ID:           rec.Session.ID,
			Observations: bySession[rec.Session.ID],
			Events:       rec.Events,
		}, fams.ByObservation, r.opts.Episode)
		res.Traces = append(res.Traces, tr)
		res.Episodes = append(res.Episodes, eps...)
		flows = append(flows, stateflow.Detect(tr, byKey, ix)...)
	}
	res.Sequences = sequence.Aggregate(res.Traces, flows, ix)
	res.Flows = flows

	inputs := make([]Input, 0, len(recs))
	for _, rec := range recs {
		inputs = append(inputs, Input{
			SessionID:     rec.Session.ID,
			Exchanges:     len(rec.Exchanges),
			BrowserEvents: len(rec.Events),
			Truncated:     rec.Truncated,
		})
	}
	res.Manifest = Manifest{Version: model.Version, ID: inputID(inputs), Inputs: inputs}
	res.Evidence = evidence.Build(evidence.Input{
		Families:  res.Families,
		Links:     res.Links,
		Traces:    res.Traces,
		Episodes:  res.Episodes,
		Sequences: res.Sequences,
	}, r.opts.Evidence)
	res.Evidence.Version = model.Version
	res.Evidence.ID = res.Manifest.ID

	c := &res.Manifest.Counts
	c.Observations = len(obs)
	c.Families = len(res.Families)
	for _, f := range res.Families {
		if f.Static {
			c.Static++
		}
	}
	c.Links = len(res.Links)
	c.Episodes = len(res.Episodes)
	c.Edges = len(res.Sequences)
	for _, e := range res.Sequences {
		c.Flows += len(e.ValueFlows)
	}
	return res, nil
}

// inputID names a result after what went into it, so the same recordings
// map to the same output directory and a recording that grew does not.
func inputID(inputs []Input) string {
	var b strings.Builder
	for _, in := range inputs {
		b.WriteString(in.SessionID)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(in.Exchanges))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(in.BrowserEvents))
		b.WriteByte(';')
	}
	return "cl_" + model.Hash("input", b.String())
}
