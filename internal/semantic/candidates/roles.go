// Package candidates builds the decision tasks semantic discovery asks a
// model, deterministically from the clustering evidence. Every choice a
// task offers comes with the structural reason it was offered, every
// choice it leaves out with the reason it was pruned, and unknown is always
// among them. The same evidence always gives the same tasks.
package candidates

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/evidence"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Family roles.
const (
	RoleCollectionRead = "collection_read"
	RoleItemRead       = "item_read"
	RoleMutation       = "mutation"
	RoleSetupState     = "setup_state"
	RolePresentation   = "presentation"
	RoleBackground     = "background"
)

// RoleMeanings is what each role choice tells the model.
var RoleMeanings = map[string]string{
	RoleCollectionRead:   "fetches a list of items, such as search results or a category listing",
	RoleItemRead:         "fetches one item, such as a product or article, by its identifier",
	RoleMutation:         "changes state on the site on the user's behalf, such as adding to a cart",
	RoleSetupState:       "establishes session or protocol state that later requests use, such as cookies or tokens",
	RolePresentation:     "page chrome or UI fragments with no item data: navigation menus, widgets, layout pieces",
	RoleBackground:       "traffic the user never asked for: telemetry, ads, tracking, metrics, beacons",
	decide.ChoiceUnknown: "the evidence is not enough to tell",
}

// Settlement rules: roles Go assigns without asking a model, because the
// structure alone decides them.
const (
	RuleStaticAsset   = "static_asset"
	RuleCORSPreflight = "cors_preflight"
	RuleNoContent     = "no_content_no_state"
)

// Bounds on a role task's evidence.
const (
	maxVariants    = 4
	maxLinks       = 5
	maxNeighbors   = 3
	maxEvidenceLen = 400
)

// FamilyRole is the role question for one family: a task for a model, or a
// role settled by rule.
type FamilyRole struct {
	Family string
	// Task is nil when the role was settled.
	Task *decide.Task
	// Settled and Rule are set when the structure decided the role.
	Settled string
	Rule    string
	Detail  string
}

// features are the structural facts role choices are offered on.
type features struct {
	method       string
	read         bool // GET or HEAD
	pathSlots    []model.Slot
	querySlots   []model.Slot
	bodySlots    []model.Slot
	jsonArray    bool
	jsonObject   bool
	html         bool
	otherContent bool
	setsCookies  bool
	feedsValues  int // unflagged value flows out of its responses
	// pageHost is set when the family's host served a page the browser
	// navigated to.
	pageHost bool
}

func (f features) content() bool { return f.jsonArray || f.jsonObject || f.html || f.otherContent }

// FamilyRoles returns one role question per family, in family order.
func FamilyRoles(in *input.Input) []FamilyRole {
	entries := map[string]*evidence.Entry{}
	for i := range in.Evidence.Families {
		entries[in.Evidence.Families[i].Family] = &in.Evidence.Families[i]
	}
	setCookies := map[string]bool{}
	for _, l := range in.Links {
		for _, o := range l.Locations {
			if o.Location.Part == model.PartSetCookie {
				setCookies[o.FamilyID] = true
			}
		}
	}
	feeds := map[string]int{}
	for _, e := range in.Sequences {
		for _, fl := range e.ValueFlows {
			if len(fl.Flags) == 0 && fl.From.Side == model.SideResponse {
				feeds[e.FromFamily]++
			}
		}
	}
	pageHosts := map[string]bool{}
	for _, t := range in.Traces {
		for _, n := range t.Navigations {
			if u, err := url.Parse(n.URL); err == nil {
				pageHosts[u.Host] = true
			}
		}
	}
	episodes := map[string][]string{}
	for _, ep := range in.Episodes {
		for _, f := range ep.FamilyIDs {
			episodes[f] = append(episodes[f], ep.ID)
		}
	}

	var out []FamilyRole
	for i := range in.Families {
		f := &in.Families[i]
		if f.Static {
			media := ""
			if len(f.ResponseVariants) > 0 {
				media = f.ResponseVariants[0].Media
			}
			out = append(out, FamilyRole{Family: f.ID, Settled: RolePresentation, Rule: RuleStaticAsset,
				Detail: "every response is a static asset (" + media + ")"})
			continue
		}
		ft := extract(f)
		ft.setsCookies = setCookies[f.ID]
		ft.feedsValues = feeds[f.ID]
		ft.pageHost = pageHosts[f.Host]
		if f.Method == "OPTIONS" {
			out = append(out, FamilyRole{Family: f.ID, Settled: RoleBackground, Rule: RuleCORSPreflight,
				Detail: "OPTIONS requests are CORS preflights the browser sends itself"})
			continue
		}
		if ft.read && !ft.content() && !ft.setsCookies && ft.feedsValues == 0 {
			out = append(out, FamilyRole{Family: f.ID, Settled: RoleBackground, Rule: RuleNoContent,
				Detail: "a read whose responses carry no content, set no cookie and feed no value to a later request"})
			continue
		}
		task := roleTask(in, f, entries[f.ID], episodes[f.ID], ft)
		out = append(out, FamilyRole{Family: f.ID, Task: &task})
	}
	return out
}

func extract(f *model.RequestFamily) features {
	ft := features{method: f.Method, read: f.Method == "GET" || f.Method == "HEAD"}
	for _, s := range f.Slots {
		switch {
		case strings.HasPrefix(s.Location, "path."):
			ft.pathSlots = append(ft.pathSlots, s)
		case strings.HasPrefix(s.Location, "query."):
			ft.querySlots = append(ft.querySlots, s)
		default:
			ft.bodySlots = append(ft.bodySlots, s)
		}
	}
	for _, v := range f.ResponseVariants {
		switch v.Shape.Type {
		case model.TypeArray:
			ft.jsonArray = true
		case model.TypeObject:
			if hasArray(v.Shape, 2) {
				ft.jsonArray = true
			} else {
				ft.jsonObject = true
			}
		case model.TypeOpaque:
			// An unknown size class counts as content.
			big := v.Shape.SizeClass != model.SizeEmpty && v.Shape.SizeClass != model.SizeTiny
			switch {
			case big && strings.Contains(v.Media, "html"):
				ft.html = true
			case big:
				ft.otherContent = true
			}
		}
	}
	return ft
}

// hasArray reports whether an object shape holds an array of objects
// within depth levels.
func hasArray(s model.Shape, depth int) bool {
	if depth < 0 {
		return false
	}
	for _, f := range s.Fields {
		if f.Shape.Type == model.TypeArray && f.Shape.Elem != nil && f.Shape.Elem.Type == model.TypeObject {
			return true
		}
		if f.Shape.Type == model.TypeObject && hasArray(f.Shape, depth-1) {
			return true
		}
	}
	return false
}

// roleTask offers the roles the structure allows, best structural fit
// first, and prunes the rest with a reason.
func roleTask(in *input.Input, f *model.RequestFamily, e *evidence.Entry, episodes []string, ft features) decide.Task {
	var choices []decide.Choice
	var pruned []decide.Pruned
	offer := func(id, why string) {
		choices = append(choices, decide.Choice{ID: id, Meaning: RoleMeanings[id], Why: why})
	}
	prune := func(id, reason string) { pruned = append(pruned, decide.Pruned{ID: id, Reason: reason}) }

	freeQuery := freeText(ft.querySlots)
	reads := []func(){}
	collection := func() {
		switch {
		case ft.jsonArray:
			offer(RoleCollectionRead, "a response holds an array of objects")
		case ft.html && len(ft.querySlots) > 0:
			offer(RoleCollectionRead, "an HTML page whose query varies ("+slotNames(ft.querySlots)+")")
		case ft.html:
			offer(RoleCollectionRead, "an HTML page, which may list items")
		default:
			prune(RoleCollectionRead, "no response holds an array or a page")
		}
	}
	item := func() {
		switch {
		case len(ft.pathSlots) > 0:
			offer(RoleItemRead, "the path varies ("+slotNames(ft.pathSlots)+")")
		case ft.jsonObject || ft.html:
			offer(RoleItemRead, "a response holds one object or page")
		default:
			prune(RoleItemRead, "no response holds an object or a page")
		}
	}
	if ft.content() {
		// A path slot points at one item; a free-text query or an array
		// points at a list.
		if len(ft.pathSlots) > 0 && !ft.jsonArray && freeQuery == "" {
			reads = append(reads, item, collection)
		} else {
			reads = append(reads, collection, item)
		}
	} else {
		prune(RoleCollectionRead, "responses carry no content")
		prune(RoleItemRead, "responses carry no content")
	}
	mutation := func() {
		if ft.read {
			prune(RoleMutation, ft.method+" requests do not change state")
			return
		}
		offer(RoleMutation, ft.method+" request")
	}
	switch {
	case !ft.pageHost && (!ft.read || !ft.content()):
		// Writes and empty reads to a host that never served a page are
		// the shape of beacons and pixels.
		offer(RoleBackground, f.Host+" served no page the browser navigated to")
		for _, r := range reads {
			r()
		}
		mutation()
	case !ft.read && !ft.content():
		// A write with nothing to read back is a beacon or an action.
		offer(RoleBackground, ft.method+" with no response content")
		mutation()
	default:
		for _, r := range reads {
			r()
		}
		mutation()
	}
	if ft.setsCookies || ft.feedsValues > 0 {
		why := []string{}
		if ft.setsCookies {
			why = append(why, "responses set cookies")
		}
		if ft.feedsValues > 0 {
			why = append(why, fmt.Sprintf("%d value flows from its responses into later requests", ft.feedsValues))
		}
		offer(RoleSetupState, strings.Join(why, "; "))
	} else {
		prune(RoleSetupState, "sets no cookie and feeds no value to a later request")
	}
	if ft.content() {
		offer(RolePresentation, "responses carry content")
	} else {
		prune(RolePresentation, "responses carry no content")
	}
	if _, ok := find(choices, RoleBackground); !ok {
		offer(RoleBackground, "any request can be background traffic")
	}
	offer(decide.ChoiceUnknown, "")

	return decide.Task{
		ID:              "role:" + f.ID + ":" + decide.PromptVersion,
		Type:            decide.TypeClassifyFamily,
		Subject:         ir.Ref(ir.RefFamily, f.ID),
		EvidencePackRef: "EP-" + f.ID,
		Question:        "What role does this request family play for a person using the site?",
		Choices:         choices,
		Pruned:          pruned,
		Evidence:        roleEvidence(in, f, e, episodes),
	}
}

func find(cs []decide.Choice, id string) (decide.Choice, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return decide.Choice{}, false
}

// freeText returns the first query slot that looks like typed input: a
// string with many distinct values.
func freeText(slots []model.Slot) string {
	for _, s := range slots {
		if s.Type == model.TypeString && s.Cardinality >= 3 && s.Cardinality*2 >= s.Observations {
			return s.Name
		}
	}
	return ""
}

func slotNames(slots []model.Slot) string {
	names := make([]string, len(slots))
	for i, s := range slots {
		names[i] = s.Location
	}
	return strings.Join(names, ", ")
}

// roleEvidence lists what the model may cite, in a fixed order: the
// request, its response variants, shared values, the families around it,
// and the browser context.
func roleEvidence(in *input.Input, f *model.RequestFamily, e *evidence.Entry, episodes []string) []decide.Evidence {
	var items []decide.Evidence
	add := func(ref, text string) {
		if len(text) > maxEvidenceLen {
			text = text[:maxEvidenceLen] + "…"
		}
		items = append(items, decide.Evidence{ID: fmt.Sprintf("E%d", len(items)+1), Ref: ref, Text: text})
	}

	sessions := map[string]bool{}
	for _, o := range f.Observations {
		sessions[o.SessionID] = true
	}
	req := fmt.Sprintf("request: %s %s%s; %d requests in %d sessions", f.Method, f.Host, f.PathTemplate, len(f.Observations), len(sessions))
	if f.BodyKind != model.BodyNone {
		req += "; " + f.BodyKind + " body " + f.RequestBody.Canonical()
	}
	for _, s := range f.Slots {
		req += fmt.Sprintf("; %s varies (%s, %d distinct of %d, e.g. %s)", s.Location, s.Type, s.Cardinality, s.Observations, examples(s.Examples))
	}
	if len(f.Slots) == 0 && len(f.QueryShape.Fields) > 0 {
		req += "; query keys " + fieldNames(f.QueryShape.Fields)
	}
	add(ir.Ref(ir.RefFamily, f.ID), req)

	for i, v := range f.ResponseVariants {
		if i == maxVariants {
			break
		}
		status := fmt.Sprintf("%d", v.Status)
		if v.Error != "" {
			status = "error " + v.Error
		}
		add(ir.Ref(ir.RefVariant, f.ID, v.ID), fmt.Sprintf("response: %s %s, %d times, shape %s", status, v.Media, v.Count, v.Shape.Canonical()))
	}
	if e == nil {
		return items
	}
	for i, l := range e.ValueLinks {
		if i == maxLinks {
			break
		}
		others := make([]string, 0, len(l.Families))
		for _, o := range l.Families {
			others = append(others, route(in, o))
		}
		flags := ""
		if len(l.Flags) > 0 {
			flags = " [" + strings.Join(l.Flags, ", ") + "]"
		}
		add(ir.Ref(ir.RefValueLink, l.Link), fmt.Sprintf("value %q%s at %s, also in: %s", short(l.Value), flags, strings.Join(l.Locations, ", "), strings.Join(others, "; ")))
	}
	neighbor := func(label string, n evidence.Neighbor) {
		text := fmt.Sprintf("%s: %s (ordered %d of %d, immediately %d times)", label, n.Route, n.Ordered, n.Of, n.Immediate)
		for _, fl := range n.Flows {
			text += fmt.Sprintf("; value flow %s %s -> %s in %d of %d", fl.Carrier, fl.From, fl.To, fl.Matches, fl.Of)
		}
		add(ir.Ref(ir.RefSequence, n.Edge), text)
	}
	for i, n := range e.Predecessors {
		if i == maxNeighbors {
			break
		}
		neighbor("often before it", n)
	}
	for i, n := range e.Successors {
		if i == maxNeighbors {
			break
		}
		neighbor("often after it", n)
	}
	if len(episodes) > 0 {
		sort.Strings(episodes)
		text := "browser context: triggered by " + counts(e.Episodes.Triggers)
		if len(e.Episodes.Initiators) > 0 {
			text += "; initiated by " + counts(e.Episodes.Initiators)
		}
		text += fmt.Sprintf("; in %d episodes", len(episodes))
		add(ir.Ref(ir.RefEpisode, episodes[0]), text)
	}
	return items
}

func route(in *input.Input, family string) string {
	for i := range in.Families {
		if in.Families[i].ID == family {
			f := &in.Families[i]
			return f.Method + " " + f.Host + f.PathTemplate
		}
	}
	return family
}

func counts(cs []evidence.Count) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s %d", c.Key, c.Count)
	}
	return strings.Join(parts, ", ")
}

func examples(vals []string) string {
	out := make([]string, 0, 3)
	for i, v := range vals {
		if i == 3 {
			break
		}
		out = append(out, fmt.Sprintf("%q", short(v)))
	}
	return strings.Join(out, ", ")
}

func fieldNames(fs []model.Field) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
