package candidates

import (
	"fmt"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// Prerequisite kinds a model may label a state flow with. None of them is
// a proven dependency: correlation is all a recording shows.
const (
	PrereqSession    = "session_state"
	PrereqSetup      = "setup"
	PrereqOptional   = "optional_initialization"
	PrereqBackground = "background"
)

// PrereqMeanings is what each kind choice tells the model.
var PrereqMeanings = map[string]string{
	PrereqSession:        "session state the browser keeps and sends back, such as a session or preference cookie",
	PrereqSetup:          "a value one request must obtain first and the next request passes on, such as a token or a result id",
	PrereqOptional:       "initialization the later request was sometimes made without",
	PrereqBackground:     "tracking or telemetry state, not something the later request needs",
	decide.ChoiceUnknown: "the evidence is not enough to tell",
}

// Carrier kinds, kept distinct as the spec asks.
const (
	CarrierCookie   = "cookie"   // Set-Cookie, or a page script, to Cookie
	CarrierRedirect = "redirect" // Location to the next URL
	CarrierScalar   = "scalar"   // a response value in a later request
)

// State scopes.
const (
	ScopeRequest    = "request"
	ScopeNavigation = "navigation"
	ScopeSession    = "browser_session"
	ScopeUnknown    = "unknown"
)

// maxPrereqsPerCarrier bounds the candidates one family gets per carrier
// kind, so a dozen cookies never crowd out the one token a response
// handed on; the rest are counted as pruned.
const maxPrereqsPerCarrier = 3

// PrereqCandidate is state that reached one family's requests from another
// family's responses: a candidate prerequisite, with the evidence against
// it as well as for it.
type PrereqCandidate struct {
	ID       string // pre:...
	Consumer string // family id
	Producer string // family id
	Carrier  string
	// Keys are the request locations the state arrived at, such as
	// "request.cookie.session-id"; From the response locations it left.
	Keys, From []string
	// Always lists the keys that carried the state on every consumer
	// request.
	Always []string
	Edge   string // sequence edge id
	// Matches counts consumer requests that carried the state; Of counts
	// the consumer's requests in all.
	Matches, Of int
	// WithoutPredecessor counts consumer requests with no earlier producer
	// in their trace; Sessions counts the traces with both.
	WithoutPredecessor, Sessions int
	// Alternates are other families the same state reached the consumer
	// from.
	Alternates []string
	Scope      string
	Caveats    []string
	Examples   []string // ex: refs of consumer requests that carried it
	Kind       decide.Task
}

// Contradicting counts the observations against the candidate: consumer
// requests without the state, and ones with no producer before them.
func (p PrereqCandidate) Contradicting() int { return p.Of - p.Matches + p.WithoutPredecessor }

// Prerequisites returns the state-flow candidates into the families in
// consumers, at most maxPrereqsPerCarrier per family and carrier kind, the
// state that arrived most often first. Flows of low-information values are left
// out; ubiquitous ones are kept, since session cookies are exactly that.
func Prerequisites(in *input.Input, consumers map[string]bool) []PrereqCandidate {
	navigation := map[string]string{}
	for _, t := range in.Traces {
		for _, s := range t.Steps {
			navigation[s.Ref.Key()] = s.NavigationID
		}
	}
	type group struct {
		c     PrereqCandidate
		keys  map[string]bool
		from  map[string]bool
		ex    map[string]bool
		scope map[string]bool
		keyM  map[string]int
	}
	groups := map[string]*group{}
	for _, e := range in.Sequences {
		if !consumers[e.ToFamily] || e.FromFamily == e.ToFamily {
			continue
		}
		for _, fl := range e.ValueFlows {
			if hasFlag(fl.Flags, model.FlagLowInformation) {
				continue
			}
			carrier := carrierOf(fl)
			key := e.ToFamily + "\x00" + e.FromFamily + "\x00" + carrier
			g := groups[key]
			if g == nil {
				g = &group{
					c: PrereqCandidate{
						Consumer: e.ToFamily, Producer: e.FromFamily, Carrier: carrier, Edge: e.ID,
						Of: e.ToObservations, WithoutPredecessor: e.WithoutPredecessor, Sessions: e.SessionsWithBoth,
					},
					keys: map[string]bool{}, from: map[string]bool{}, ex: map[string]bool{}, scope: map[string]bool{}, keyM: map[string]int{},
				}
				groups[key] = g
			}
			g.keys[fl.To.String()] = true
			g.keyM[fl.To.String()] = max(g.keyM[fl.To.String()], fl.Matches)
			g.from[fl.From.String()] = true
			g.c.Matches = max(g.c.Matches, fl.Matches)
			for _, x := range fl.Examples {
				g.ex[ir.Ref(ir.RefObservation, x.To.SessionID, x.To.ExchangeID)] = true
				switch {
				case carrier == CarrierCookie:
					g.scope[ScopeSession] = true
				case carrier == CarrierRedirect:
					g.scope[ScopeNavigation] = true
				case navigation[x.From.Key()] != "" && navigation[x.From.Key()] == navigation[x.To.Key()]:
					g.scope[ScopeNavigation] = true
				default:
					g.scope[ScopeUnknown] = true
				}
			}
		}
	}

	byConsumer := map[string][]*PrereqCandidate{}
	for _, g := range groups {
		c := &g.c
		c.Keys, c.From, c.Examples = sortedKeys(g.keys), sortedKeys(g.from), sortedKeys(g.ex)
		for _, k := range c.Keys {
			if g.keyM[k] >= c.Of {
				c.Always = append(c.Always, k)
			}
		}
		c.Scope = ScopeUnknown
		if len(g.scope) == 1 {
			for s := range g.scope {
				c.Scope = s
			}
		}
		byConsumer[c.Consumer] = append(byConsumer[c.Consumer], c)
	}

	var out []PrereqCandidate
	for _, consumer := range sortedKeys2(byConsumer) {
		cs := byConsumer[consumer]
		sort.Slice(cs, func(i, j int) bool {
			a, b := cs[i], cs[j]
			if a.Carrier != b.Carrier {
				return a.Carrier < b.Carrier
			}
			if a.Matches != b.Matches {
				return a.Matches > b.Matches
			}
			return a.Producer+a.Carrier < b.Producer+b.Carrier
		})
		// The same state reaching the consumer from other families is an
		// alternate predecessor of each.
		for _, c := range cs {
			for _, o := range cs {
				if o != c && overlaps(o.Keys, c.Keys) {
					c.Alternates = append(c.Alternates, o.Producer)
				}
			}
			sort.Strings(c.Alternates)
		}
		per := map[string]int{}
		for _, c := range cs {
			per[c.Carrier]++
		}
		taken := map[string]int{}
		for _, c := range cs {
			if taken[c.Carrier] == maxPrereqsPerCarrier {
				continue
			}
			taken[c.Carrier]++
			pruned := max(0, per[c.Carrier]-maxPrereqsPerCarrier)
			c.ID = ir.NodeID(ir.RefPrerequisite, c.Consumer+"\x00"+c.Producer+"\x00"+c.Carrier)
			c.Caveats = caveats(*c, pruned)
			c.Kind = prereqTask(in, *c)
			out = append(out, *c)
		}
	}
	return out
}

// carrierOf sorts a flow into the three carrier kinds. A value a page
// script put in a cookie is a cookie, whatever response it came from.
func carrierOf(fl model.ValueFlow) string {
	switch {
	case fl.Carrier == model.CarrierCookie || fl.To.Part == model.PartCookie:
		return CarrierCookie
	case fl.Carrier == model.CarrierRedirect:
		return CarrierRedirect
	}
	return CarrierScalar
}

// caveats records the negative evidence and the limits of one recording.
func caveats(c PrereqCandidate, pruned int) []string {
	var out []string
	if n := c.Of - c.Matches; n > 0 {
		out = append(out, fmt.Sprintf("%d of %d consumer requests carried none of this state", n, c.Of))
	}
	if c.WithoutPredecessor > 0 {
		out = append(out, fmt.Sprintf("%d consumer requests had no earlier producer request in their trace", c.WithoutPredecessor))
	}
	if len(c.Alternates) > 0 {
		out = append(out, fmt.Sprintf("the same state also arrived from %d other families", len(c.Alternates)))
	}
	if c.Carrier == CarrierCookie {
		out = append(out, "state may predate the recording")
	}
	if c.Sessions <= 1 {
		out = append(out, "seen in one recording session; scope is not general")
	}
	if pruned > 0 {
		out = append(out, fmt.Sprintf("%d weaker %s candidates for this consumer were not offered", pruned, c.Carrier))
	}
	return out
}

// prereqTask asks what kind of state a candidate is. The best structural
// fit comes first: cookies are session state, state carried every time
// with a producer always before it is setup, state sometimes missing is
// optional.
func prereqTask(in *input.Input, c PrereqCandidate) decide.Task {
	var choices []decide.Choice
	offer := func(id, why string) {
		choices = append(choices, decide.Choice{ID: id, Meaning: PrereqMeanings[id], Why: why})
	}
	always := c.Matches >= c.Of && c.WithoutPredecessor == 0
	switch c.Carrier {
	case CarrierCookie:
		offer(PrereqSession, "the state travelled as a cookie")
		if always {
			offer(PrereqSetup, "every consumer request carried it, after a producer request")
		} else {
			offer(PrereqOptional, "some consumer requests went without it")
		}
	default:
		if always {
			offer(PrereqSetup, "every consumer request carried it, after a producer request")
			offer(PrereqOptional, "a later request may work without it")
		} else {
			offer(PrereqOptional, "some consumer requests went without it")
			offer(PrereqSetup, "a response value reappeared in the request")
		}
	}
	offer(PrereqBackground, "any state may be tracking")
	offer(decide.ChoiceUnknown, "")

	return decide.Task{
		ID:              "prereq:" + strings.TrimPrefix(c.ID, ir.RefPrerequisite+":") + ":" + decide.PromptVersion,
		Type:            decide.TypeClassifyPrereq,
		Subject:         ir.Ref(ir.RefFamily, c.Consumer),
		EvidencePackRef: "EP-" + c.Consumer,
		Question:        "State from the first family's responses reappeared in the second family's requests. What kind of prerequisite is it, if any?",
		Choices:         choices,
		Evidence:        prereqEvidence(in, c),
	}
}

// prereqEvidence describes a candidate without any of the values that
// flowed: names and counts only.
func prereqEvidence(in *input.Input, c PrereqCandidate) []decide.Evidence {
	var items []decide.Evidence
	add := func(ref, text string) {
		items = append(items, decide.Evidence{ID: fmt.Sprintf("E%d", len(items)+1), Ref: ref, Text: text})
	}
	add(ir.Ref(ir.RefFamily, c.Consumer), "later request: "+route(in, c.Consumer))
	add(ir.Ref(ir.RefFamily, c.Producer), "earlier request: "+route(in, c.Producer))
	add(ir.Ref(ir.RefSequence, c.Edge), fmt.Sprintf("%s flow from %s to %s; carried by %d of %d later requests; %d later requests had no earlier producer; %d sessions with both; scope %s",
		c.Carrier, strings.Join(c.From, ", "), strings.Join(c.Keys, ", "), c.Matches, c.Of, c.WithoutPredecessor, c.Sessions, c.Scope))
	if len(c.Examples) > 0 {
		add(c.Examples[0], "a later request that carried the state")
	}
	for i, a := range c.Alternates {
		if i == 2 {
			break
		}
		add(ir.Ref(ir.RefFamily, a), "the same state also came from: "+route(in, a))
	}
	return items
}

// Precondition renders a candidate as the IR records it on an operation.
func (c PrereqCandidate) Precondition(kind string, status ir.HypothesisStatus, hypothesis string) ir.PrerequisiteHypothesis {
	evidence := append([]string{ir.Ref(ir.RefSequence, c.Edge)}, c.Examples...)
	for _, k := range c.Keys {
		evidence = append(evidence, ir.Ref(ir.RefFamily, c.Consumer)+"#"+k)
	}
	for _, f := range c.From {
		evidence = append(evidence, ir.Ref(ir.RefFamily, c.Producer)+"#"+f)
	}
	return ir.PrerequisiteHypothesis{
		ID: c.ID, Kind: kind, ProducerFamily: ir.Ref(ir.RefFamily, c.Producer),
		ConsumerFamily: ir.Ref(ir.RefFamily, c.Consumer), Carrier: c.Carrier, Scope: c.Scope,
		EvidenceRefs: evidence, Caveats: append([]string{}, c.Caveats...), Status: status, Hypothesis: hypothesis,
	}
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}
