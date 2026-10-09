package candidates

import (
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/input"
)

var (
	once    sync.Once
	fixture *input.Input
	loadErr error
)

func load(t *testing.T) *input.Input {
	t.Helper()
	once.Do(func() { fixture, loadErr = input.Load("../testdata/amazon-search") })
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return fixture
}

func TestFamilyRolesOnAmazon(t *testing.T) {
	in := load(t)
	qs := FamilyRoles(in)
	if len(qs) != len(in.Families) {
		t.Fatalf("%d questions for %d families", len(qs), len(in.Families))
	}
	rules := map[string]int{}
	firstChoice := map[string]string{}
	for _, q := range qs {
		if q.Task == nil {
			rules[q.Rule]++
			continue
		}
		task := q.Task
		if len(task.Choices) < 2 || len(task.Choices) > 7 {
			t.Errorf("%s offers %d choices", task.ID, len(task.Choices))
		}
		if task.Choices[len(task.Choices)-1].ID != decide.ChoiceUnknown {
			t.Errorf("%s does not end with unknown", task.ID)
		}
		seen := map[string]bool{}
		for _, c := range task.Choices {
			if seen[c.ID] {
				t.Errorf("%s offers %s twice", task.ID, c.ID)
			}
			seen[c.ID] = true
			if c.ID != decide.ChoiceUnknown && c.Why == "" {
				t.Errorf("%s offers %s without a reason", task.ID, c.ID)
			}
		}
		for _, p := range task.Pruned {
			if seen[p.ID] || p.Reason == "" {
				t.Errorf("%s: pruned %+v", task.ID, p)
			}
		}
		for i, e := range task.Evidence {
			if e.ID != "E"+strconv.Itoa(i+1) || !in.Has(e.Ref) {
				t.Errorf("%s: evidence %s -> %s", task.ID, e.ID, e.Ref)
			}
		}
		firstChoice[route(in, q.Family)] = task.Choices[0].ID
	}
	if rules[RuleStaticAsset] != 151 {
		t.Errorf("%d static families settled, want 151", rules[RuleStaticAsset])
	}
	if rules[RuleCORSPreflight] == 0 || rules[RuleNoContent] == 0 {
		t.Errorf("rules applied: %v", rules)
	}
	for r, want := range map[string]string{
		"GET www.amazon.com/s":                               RoleCollectionRead,
		"GET www.amazon.com/{slot_1}/dp/{slot_2}/ref=sr_1_1": RoleItemRead,
		"POST unagi.amazon.com/1/events/{slot_1}":            RoleBackground,
		"GET ib.adnxs.com/getuid":                            RoleBackground,
	} {
		if got := firstChoice[r]; got != want {
			t.Errorf("%s: first choice %q, want %q", r, got, want)
		}
	}
}

func TestFamilyRolesAreDeterministic(t *testing.T) {
	in := load(t)
	if !reflect.DeepEqual(FamilyRoles(in), FamilyRoles(in)) {
		t.Error("two runs over the same evidence gave different tasks")
	}
}

func TestGetPrunesMutation(t *testing.T) {
	for _, q := range FamilyRoles(load(t)) {
		if q.Task == nil || !strings.HasPrefix(q.Task.Evidence[0].Text, "request: GET ") {
			continue
		}
		if _, ok := q.Task.Choice(RoleMutation); ok {
			t.Errorf("%s: a GET family is offered mutation", q.Task.ID)
		}
	}
}
