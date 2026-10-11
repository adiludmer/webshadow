// Package episode partitions a session's traffic into page views using only
// what a server receives: request headers and responses. A page view starts
// at each main-frame navigation request (Sec-Fetch-Mode: navigate with
// Sec-Fetch-Dest: document), with the redirects the server answered it with
// joined in. A subrequest belongs to the page view its Referer names, else to
// the latest one.
//
// Sec-Fetch-* and Referer are hints only browsers send. Without them, a GET
// that prefers HTML and gets HTML starts a page view, and a session with no
// navigation signal at all is split at quiet gaps.
//
// A page view is context, not a claim. A request in it was made while the
// page was current, whether or not the page caused it; each step keeps its
// kind and its offset from the trigger so a reader can judge.
package episode

import (
	"mime"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Options tune the partition. The defaults are part of the deterministic
// contract.
type Options struct {
	// Gap is how long a session with no navigation signal must be quiet
	// before the next request starts a new page view.
	Gap int64
	// RedirectWindow is how long after a redirect the request it points to
	// may start and still join it.
	RedirectWindow int64
}

// DefaultOptions is what the clustering pass uses.
func DefaultOptions() Options {
	return Options{Gap: 30e9, RedirectWindow: 30e9}
}

// RequestKind says what a request was for, from Sec-Fetch-Dest and
// Sec-Fetch-Mode. A request without them is a document when it looks like a
// navigation, and otherwise has no kind.
func RequestKind(o *model.Observation) string {
	dest := strings.ToLower(header(o.RequestHeaders, "Sec-Fetch-Dest"))
	mode := strings.ToLower(header(o.RequestHeaders, "Sec-Fetch-Mode"))
	switch {
	case dest == "document" && mode == "navigate":
		return model.KindDocument
	case dest == "empty" && mode == "websocket":
		return model.KindWebSocket
	case dest == "empty":
		return model.KindFetch
	case dest != "":
		return dest
	case mode == "" && htmlNavigation(o):
		return model.KindDocument
	}
	return ""
}

// htmlNavigation reports whether a request with no Sec-Fetch headers looks
// like a navigation: a GET whose Accept prefers HTML, answered with HTML or
// a redirect.
func htmlNavigation(o *model.Observation) bool {
	if o.Method != "GET" || !prefersHTML(header(o.RequestHeaders, "Accept")) {
		return false
	}
	if redirect(o) != "" {
		return true
	}
	ct, _, _ := mime.ParseMediaType(header(o.ResponseHeaders, "Content-Type"))
	return ct == "text/html" || ct == "application/xhtml+xml"
}

// prefersHTML reports whether HTML has the highest quality in an Accept
// header, ties going to the earlier entry.
func prefersHTML(accept string) bool {
	best, bestQ := "", -1.0
	for _, part := range strings.Split(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q > bestQ {
			best, bestQ = media, q
		}
	}
	return best == "text/html" || best == "application/xhtml+xml"
}

// redirect returns the absolute URL a 3xx response pointed to, or "".
func redirect(o *model.Observation) string {
	if o.Status < 300 || o.Status > 399 {
		return ""
	}
	loc := header(o.ResponseHeaders, "Location")
	if loc == "" {
		return ""
	}
	base, err := url.Parse(o.URL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	return stripFragment(base.ResolveReference(ref).String())
}

// trigger names how the navigation request that starts a chain came about.
func trigger(o *model.Observation, redirected bool) string {
	site := strings.ToLower(header(o.RequestHeaders, "Sec-Fetch-Site"))
	switch {
	case header(o.RequestHeaders, "Sec-Fetch-Mode") == "":
		return model.TriggerHTML
	case site == "none":
		return model.TriggerTyped
	case header(o.RequestHeaders, "Sec-Fetch-User") == "?1":
		return model.TriggerUser
	case redirected:
		return model.TriggerRedirect
	}
	return model.TriggerLink
}

// view is one page view while it is being built.
type view struct {
	steps []int // chain steps, document last
	url   string
	start int
	gap   bool
}

// Partition assigns each step of a session to its navigation and episode,
// filling in the steps' EpisodeID, NavigationID and SinceTrigger. Steps must
// be in trace order with their Kind set, and obs[i] is steps[i]'s
// observation.
func Partition(sessionID string, obs []*model.Observation, steps []model.TraceStep, opts Options) ([]model.Navigation, []model.Episode) {
	if opts.Gap == 0 {
		opts = DefaultOptions()
	}
	var views []*view
	member := make([]int, len(steps)) // the view a chain step belongs to, or -1
	pending := map[string]int{}       // redirect target URL -> view
	for i := range steps {
		member[i] = -1
		if steps[i].Kind != model.KindDocument {
			continue
		}
		u := stripFragment(steps[i].URL)
		v, ok := pending[u]
		if ok {
			last := views[v].steps[len(views[v].steps)-1]
			ok = steps[i].T-steps[last].T <= opts.RedirectWindow
			delete(pending, u)
		}
		if !ok {
			v = len(views)
			views = append(views, &view{start: i})
		}
		views[v].steps = append(views[v].steps, i)
		views[v].url = u
		member[i] = v
		if to := redirect(obs[i]); to != "" {
			pending[to] = v
		}
	}
	if len(views) == 0 {
		for i := range steps {
			if i == 0 || steps[i].T-steps[i-1].T > opts.Gap {
				views = append(views, &view{start: i, url: stripFragment(steps[i].URL), gap: true})
			}
		}
	}

	var navigations []model.Navigation
	var episodes []model.Episode
	if len(steps) > 0 && (len(views) == 0 || views[0].start > 0) {
		episodes = append(episodes, model.Episode{
			ID:        "ep_" + model.Hash("episode", sessionID+"/untriggered"),
			SessionID: sessionID,
		})
	}
	offset := len(episodes)
	for _, v := range views {
		first := steps[v.start]
		kind := model.TriggerGap
		if !v.gap {
			kind = trigger(obs[v.start], len(v.steps) > 1)
		}
		ep := model.Episode{
			ID:        "ep_" + model.Hash("episode", sessionID+"/"+first.Ref.ExchangeID),
			SessionID: sessionID,
			Start:     first.T,
			Trigger: &model.Trigger{
				Kind:       kind,
				ExchangeID: first.Ref.ExchangeID,
				Seq:        first.Ref.Seq,
				T:          first.T,
				URL:        first.URL,
			},
		}
		if !v.gap {
			nav := model.Navigation{
				ID:         "nav_" + model.Hash("navigation", sessionID+"/"+first.Ref.ExchangeID),
				URL:        v.url,
				Start:      first.T,
				Document:   steps[v.steps[len(v.steps)-1]].Ref.ExchangeID,
				EpisodeIDs: []string{ep.ID},
			}
			for _, s := range v.steps[:len(v.steps)-1] {
				nav.Redirects = append(nav.Redirects, steps[s].Ref.ExchangeID)
			}
			ep.NavigationID = nav.ID
			navigations = append(navigations, nav)
		}
		episodes = append(episodes, ep)
	}
	navOf := map[string]int{}
	for i := range navigations {
		navOf[navigations[i].ID] = i
	}
	for i := range episodes {
		episodes[i].FirstStep, episodes[i].LastStep = -1, -1
		episodes[i].ExchangeIDs = []string{}
		episodes[i].FamilyIDs = []string{}
	}

	families := make([]map[string]bool, len(episodes))
	for i := range steps {
		s := &steps[i]
		e := 0
		if v := member[i]; v >= 0 {
			e = offset + v
		} else if v := current(views, obs, i); v >= 0 {
			e = offset + v
		}
		ep := &episodes[e]
		s.EpisodeID = ep.ID
		if ep.Trigger != nil {
			s.SinceTrigger = s.T - ep.Trigger.T
		}
		if ep.NavigationID != "" {
			s.NavigationID = ep.NavigationID
		}
		if ep.FirstStep < 0 {
			ep.FirstStep = i
			ep.Start = s.T
		}
		ep.LastStep = i
		ep.End = s.T
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
		if n, ok := navOf[episodes[i].NavigationID]; ok {
			navigations[n].Start, navigations[n].End = episodes[i].Start, episodes[i].End
		}
	}
	return navigations, episodes
}

// current returns the page view a subrequest belongs to: the one its
// Referer names, else the latest one started at or before it, else -1. An
// origin-only Referer, which browsers send cross-origin, names no page.
func current(views []*view, obs []*model.Observation, i int) int {
	latest := sort.Search(len(views), func(v int) bool { return views[v].start > i }) - 1
	if latest < 0 {
		return -1
	}
	ref := referer(obs[i])
	if ref == "" || views[latest].url == ref {
		return latest
	}
	for v := latest - 1; v >= 0; v-- {
		if views[v].url == ref {
			return v
		}
	}
	return latest
}

// referer returns a request's Referer when it names a page: a URL with a
// path or a query, not just an origin.
func referer(o *model.Observation) string {
	raw := header(o.RequestHeaders, "Referer")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || ((u.Path == "" || u.Path == "/") && u.RawQuery == "") {
		return ""
	}
	return stripFragment(raw)
}

func stripFragment(u string) string {
	u, _, _ = strings.Cut(u, "#")
	return u
}

func header(pairs []model.Pair, name string) string {
	for _, p := range pairs {
		if p.Name == name {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}
