package bench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/semantic"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

const fixture = "../testdata/amazon-search"

func mockRun(t *testing.T) (*store.Store, *semantic.Result) {
	t.Helper()
	s := store.Open(t.TempDir())
	d := decide.New(&decide.Mock{}, decide.ModelInfo{ID: decide.MockID, Adapter: "mock"}, decide.DefaultParams())
	res, err := semantic.Analyze(context.Background(), fixture, semantic.Options{Store: s, Decider: d})
	if err != nil {
		t.Fatal(err)
	}
	return s, res
}

func TestExpectedMatchesFixture(t *testing.T) {
	e, err := Load(filepath.Join(fixture, ExpectedFile))
	if err != nil {
		t.Fatal(err)
	}
	_, res := mockRun(t)
	if err := e.Validate(res.Input); err != nil {
		t.Fatal(err)
	}
	// A label naming something the clustering never saw is refused.
	e.Operations[0].Required[0].Slots = append(e.Operations[0].Required[0].Slots, "fam:98258e0892fb#request.query.invented")
	e.Roles["background"] = append(e.Roles["background"], "000000000000")
	err = e.Validate(res.Input)
	if err == nil || !strings.Contains(err.Error(), "query.invented") || !strings.Contains(err.Error(), "family 000000000000") {
		t.Errorf("validation = %v", err)
	}
}

// The mock picks every task's first structural choice, so its score is
// fixed: these numbers move only when analysis or the expected IR does.
func TestScoreMockRun(t *testing.T) {
	e, err := Load(filepath.Join(fixture, ExpectedFile))
	if err != nil {
		t.Fatal(err)
	}
	s, res := mockRun(t)
	r, err := Score(s, res.Run, e)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != decide.MockID || r.Run != res.Run || r.Coverage != res.Manifest.Coverage.Score {
		t.Errorf("result = %s %s %v", r.Model, r.Run, r.Coverage)
	}
	if r.Roles.Expected != 83 || r.Roles.Predicted != 83 || r.Roles.Correct == 0 {
		t.Errorf("roles = %+v", r.Roles)
	}
	// Search is one operation over its three URL forms, with the query as
	// its only required input.
	if r.Operations.Expected != 3 || r.Operations.Correct < 1 {
		t.Errorf("operations = %+v", r.Operations)
	}
	if !has(r.Mistakes, "operation get_asin splits expected get_product") {
		t.Errorf("mistakes lack the product page split: %v", r.Mistakes)
	}
	if r.IdentityJoins.Precision < 0.9 || r.Entities.Correct != 4 {
		t.Errorf("identity %+v, entities %+v", r.IdentityJoins, r.Entities)
	}
	if r.Prerequisites.Predicted == 0 || r.Prerequisites.FalsePositives == 0 || r.Prerequisites.Rate >= 1 {
		t.Errorf("prerequisites = %+v", r.Prerequisites)
	}
	if r.Abstention.Tasks != res.Manifest.Counts.Tasks || r.Abstention.Rate != 0 {
		t.Errorf("abstention = %+v", r.Abstention)
	}
	path, err := Write(s, r)
	if err != nil || !strings.HasSuffix(path, filepath.Join("benchmarks", "amazon-search", res.Run+".json")) {
		t.Errorf("score written to %s: %v", path, err)
	}
	var md strings.Builder
	Markdown(&md, []*Result{r, r})
	if !strings.Contains(md.String(), "| Family roles (P / R) |") || strings.Count(md.String(), "## Mistakes") != 2 {
		t.Errorf("comparison:\n%s", md.String())
	}
}

func TestMetric(t *testing.T) {
	m := metric(3, 4, 6)
	if m.Precision != 0.75 || m.Recall != 0.5 {
		t.Errorf("metric = %+v", m)
	}
	m.add(metric(1, 1, 2))
	if m.Correct != 4 || m.Predicted != 5 || m.Expected != 8 || m.Precision != 0.8 || m.Recall != 0.5 {
		t.Errorf("sum = %+v", m)
	}
	if z := metric(0, 0, 0); z.Precision != 0 || z.Recall != 0 {
		t.Errorf("empty = %+v", z)
	}
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
