package candidates

import (
	"strings"
	"testing"
)

// searchFamily is the Amazon fixture's /s search family.
const searchFamily = "98258e0892fb"

func TestPrerequisitesOnAmazon(t *testing.T) {
	in := load(t)
	ps := Prerequisites(in, map[string]bool{searchFamily: true, productPage: true})
	var cookie, token *PrereqCandidate
	perCarrier := map[string]int{}
	for i := range ps {
		p := &ps[i]
		perCarrier[p.Consumer+p.Carrier]++
		if p.Consumer != searchFamily {
			continue
		}
		if p.Carrier == CarrierCookie && contains(p.Keys, "request.cookie.session-id-time") {
			cookie = p
		}
		if p.Producer == suggestFamily && p.Carrier == CarrierScalar {
			token = p
		}
	}
	for k, n := range perCarrier {
		if n > maxPrereqsPerCarrier {
			t.Errorf("%s has %d candidates", k, n)
		}
	}
	if cookie == nil || cookie.Scope != ScopeSession || cookie.Kind.Choices[0].ID != PrereqSession || !contains(cookie.Caveats, "state may predate the recording") {
		t.Fatalf("session cookie candidate = %+v", cookie)
	}
	// The response id the suggestions hand to the search request.
	if token == nil || !contains(token.Keys, "request.query.crid") || token.Matches != 10 || token.Of != 10 || token.Kind.Choices[0].ID != PrereqSetup {
		t.Fatalf("crid candidate = %+v", token)
	}
	// Absence is recorded: a partial flow names how many went without.
	for _, p := range ps {
		if p.Matches < p.Of && !strings.Contains(strings.Join(p.Caveats, ";"), "carried none of this state") {
			t.Errorf("%s lacks its absence caveat: %v", p.ID, p.Caveats)
		}
		if p.Kind.Choices[len(p.Kind.Choices)-1].ID != "unknown" {
			t.Errorf("%s offers no unknown", p.ID)
		}
	}
}

// Prompts name state and count it; the values that flowed never appear in
// the flow and example evidence. (Routes may hold literals such as a
// product id in a path, which are the family's own name.)
func TestPrerequisiteEvidenceHasNoValues(t *testing.T) {
	in := load(t)
	ps := Prerequisites(in, map[string]bool{searchFamily: true, productPage: true, suggestFamily: true})
	values := map[string]bool{}
	for _, e := range in.Sequences {
		for _, f := range e.ValueFlows {
			for _, x := range f.Examples {
				if len(x.Value) >= 8 && idLike(x.Value) {
					values[x.Value] = true
				}
			}
		}
	}
	for _, p := range ps {
		for _, ev := range p.Kind.Evidence {
			if strings.HasPrefix(ev.Ref, "fam:") {
				continue
			}
			for v := range values {
				if strings.Contains(ev.Text, v) {
					t.Fatalf("%s evidence %s shows a flowed value", p.ID, ev.ID)
				}
			}
		}
	}
	again := Prerequisites(in, map[string]bool{searchFamily: true, productPage: true, suggestFamily: true})
	for i := range ps {
		if ps[i].Kind.Hash() != again[i].Kind.Hash() {
			t.Errorf("task %s differs between runs", ps[i].ID)
		}
	}
}
