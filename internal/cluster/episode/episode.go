// Package episode partitions a session's traffic by browser activity. A
// trigger is a user interaction (click, submit, enter, change) or a
// main-frame navigation the user did not just cause, and an episode runs
// from its trigger to the next one. Navigations group episodes by the
// document they happened in.
//
// An episode is context, not a claim. A request that started after a click
// is in that click's episode whether or not the click caused it; each step
// keeps its initiator and its offset from the trigger so a reader can judge.
package episode

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/recording"
)

// Options tune the partition. The defaults are part of the deterministic
// contract.
type Options struct {
	// NavigationGrace is how long after an interaction a navigation counts as
	// that interaction's consequence rather than a trigger of its own.
	NavigationGrace int64
	// MatchWindow is how far apart an exchange and the browser's report of
	// the same request may be to be matched.
	MatchWindow int64
	// DocumentLookback is how far before a navigation commits its document
	// request may have started.
	DocumentLookback int64
}

// DefaultOptions is what the clustering pass uses.
func DefaultOptions() Options {
	return Options{NavigationGrace: 1e9, MatchWindow: 2e9, DocumentLookback: 30e9}
}

// interactionTriggers are the instrumented interactions that start an
// episode. Typing is not one: it is reported when it pauses, and the enter,
// change or submit that follows is the boundary.
var interactionTriggers = map[string]bool{
	"interaction.click":  true,
	"interaction.submit": true,
	"interaction.enter":  true,
	"interaction.change": true,
}

// Partition assigns each step of a session to its navigation and episode,
// filling in the steps' EpisodeID, NavigationID and SinceTrigger. Steps must
// be in trace order.
func Partition(sessionID string, events []recording.BrowserEvent, steps []model.TraceStep, opts Options) ([]model.Navigation, []model.Episode) {
	if opts.MatchWindow == 0 {
		opts = DefaultOptions()
	}
	evs := sortedEvents(events)
	pages := pageTargets(evs)

	type boundary struct {
		start int64
		ref   model.BrowserEventRef
		nav   bool
	}
	var navs []boundary
	var triggers []boundary
	lastInteraction := int64(-1 << 62)
	for _, ev := range evs {
		switch {
		case interactionTriggers[ev.Type]:
			lastInteraction = ev.Timestamp.T
			triggers = append(triggers, boundary{start: ev.Timestamp.T, ref: ref(ev)})
		case ev.Type == "Page.frameNavigated" && mainFrame(ev, pages):
			// The navigation commits after its document arrived; the
			// document request is where the navigation began.
			start := documentStart(ev, steps, opts.DocumentLookback)
			b := boundary{start: start, ref: ref(ev), nav: true}
			navs = append(navs, b)
			if ev.Timestamp.T-lastInteraction > opts.NavigationGrace && start-lastInteraction > opts.NavigationGrace {
				triggers = append(triggers, b)
			}
		case ev.Type == "Page.navigatedWithinDocument" && mainTarget(ev, pages):
			if ev.Timestamp.T-lastInteraction > opts.NavigationGrace {
				triggers = append(triggers, boundary{start: ev.Timestamp.T, ref: ref(ev)})
			}
		}
	}
	sortBoundaries := func(bs []boundary) {
		sort.SliceStable(bs, func(i, j int) bool {
			if bs[i].start != bs[j].start {
				return bs[i].start < bs[j].start
			}
			return bs[i].ref.Seq < bs[j].ref.Seq
		})
	}
	sortBoundaries(navs)
	sortBoundaries(triggers)

	var end int64
	if n := len(steps); n > 0 {
		end = steps[n-1].T
	}
	for _, ev := range evs {
		if ev.Timestamp.T > end {
			end = ev.Timestamp.T
		}
	}

	navigations := make([]model.Navigation, len(navs))
	for i, b := range navs {
		r := b.ref
		navigations[i] = model.Navigation{
			ID:    "nav_" + model.Hash("navigation", sessionID+"/"+r.EventID),
			URL:   r.PageURL,
			Start: b.start,
			End:   end,
			Event: &r,
		}
		if i > 0 {
			navigations[i-1].End = b.start
		}
	}
	navAt := func(t int64) int {
		i := sort.Search(len(navigations), func(i int) bool { return navigations[i].Start > t })
		return i - 1
	}

	var episodes []model.Episode
	if len(triggers) == 0 || (len(steps) > 0 && steps[0].T < triggers[0].start) {
		episodes = append(episodes, model.Episode{
			ID:        "ep_" + model.Hash("episode", sessionID+"/untriggered"),
			SessionID: sessionID,
			Start:     0,
			End:       end,
		})
	}
	for _, b := range triggers {
		r := b.ref
		episodes = append(episodes, model.Episode{
			ID:        "ep_" + model.Hash("episode", sessionID+"/"+r.EventID+"/"+strconv.FormatInt(b.start, 10)),
			SessionID: sessionID,
			Start:     b.start,
			End:       end,
			Trigger:   &r,
		})
	}
	for i := range episodes {
		if i+1 < len(episodes) {
			episodes[i].End = episodes[i+1].Start
		}
		if n := navAt(episodes[i].Start); n >= 0 {
			episodes[i].NavigationID = navigations[n].ID
			navigations[n].EpisodeIDs = append(navigations[n].EpisodeIDs, episodes[i].ID)
		}
		episodes[i].FirstStep, episodes[i].LastStep = -1, -1
		episodes[i].ExchangeIDs = []string{}
		episodes[i].FamilyIDs = []string{}
	}
	for i := range navigations {
		if navigations[i].EpisodeIDs == nil {
			navigations[i].EpisodeIDs = []string{}
		}
	}

	families := make([]map[string]bool, len(episodes))
	for i := range steps {
		s := &steps[i]
		if n := navAt(s.T); n >= 0 {
			s.NavigationID = navigations[n].ID
		}
		// The last episode starting at or before the step; with no
		// untriggered episode, traffic before the first trigger cannot
		// exist, since one is created whenever it does.
		e := sort.Search(len(episodes), func(i int) bool { return episodes[i].Start > s.T }) - 1
		if e < 0 {
			e = 0
		}
		ep := &episodes[e]
		s.EpisodeID = ep.ID
		if ep.Trigger != nil {
			s.SinceTrigger = s.T - ep.Trigger.T
		}
		if ep.FirstStep < 0 {
			ep.FirstStep = i
		}
		ep.LastStep = i
		ep.ExchangeIDs = append(ep.ExchangeIDs, s.Ref.ExchangeID)
		if families[e] == nil {
			families[e] = map[string]bool{}
		}
		if s.FamilyID != "" {
			families[e][s.FamilyID] = true
		}
	}
	for i := range episodes {
		for f := range families[i] {
			episodes[i].FamilyIDs = append(episodes[i].FamilyIDs, f)
		}
		sort.Strings(episodes[i].FamilyIDs)
	}
	return navigations, episodes
}

// documentStart finds the request that fetched a navigation's document: the
// latest step for the same URL that started before the navigation
// committed. Without one, the navigation starts when it committed.
func documentStart(ev *recording.BrowserEvent, steps []model.TraceStep, lookback int64) int64 {
	var p struct {
		Frame struct {
			URL string `json:"url"`
		} `json:"frame"`
	}
	json.Unmarshal(ev.Payload, &p)
	want, _, _ := strings.Cut(firstNonEmpty(p.Frame.URL, ev.PageURL), "#")
	commit := ev.Timestamp.T
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if s.T > commit {
			continue
		}
		if commit-s.T > lookback {
			break
		}
		if s.Method == "GET" && s.URL == want {
			return s.T
		}
	}
	return commit
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ref(ev *recording.BrowserEvent) model.BrowserEventRef {
	return model.BrowserEventRef{
		EventID:  ev.ID,
		Seq:      ev.Seq,
		Type:     ev.Type,
		T:        ev.Timestamp.T,
		TargetID: ev.TargetID,
		FrameID:  ev.FrameID,
		PageURL:  ev.PageURL,
		Payload:  ev.Payload,
	}
}

func sortedEvents(events []recording.BrowserEvent) []*recording.BrowserEvent {
	out := make([]*recording.BrowserEvent, len(events))
	for i := range events {
		out[i] = &events[i]
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Timestamp.T != out[j].Timestamp.T {
			return out[i].Timestamp.T < out[j].Timestamp.T
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// pageTargets maps target ids to whether they are pages, from the target
// events the recorder kept. Out-of-process iframes, mostly ads, are their
// own targets and their navigations are not the user's.
func pageTargets(evs []*recording.BrowserEvent) map[string]bool {
	out := map[string]bool{}
	for _, ev := range evs {
		if !strings.HasPrefix(ev.Type, "Target.") {
			continue
		}
		var p struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.TargetInfo.TargetID != "" && p.TargetInfo.Type != "" {
			out[p.TargetInfo.TargetID] = p.TargetInfo.Type == "page"
		}
	}
	return out
}

// mainTarget reports whether an event came from a page target. A target the
// recording never described is assumed to be one.
func mainTarget(ev *recording.BrowserEvent, pages map[string]bool) bool {
	isPage, known := pages[ev.TargetID]
	return !known || isPage
}

func mainFrame(ev *recording.BrowserEvent, pages map[string]bool) bool {
	if !mainTarget(ev, pages) || strings.HasPrefix(ev.PageURL, "about:") {
		return false
	}
	var p struct {
		Frame struct {
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return false
	}
	return p.Frame.ParentID == "" && !strings.HasPrefix(p.Frame.URL, "about:")
}
