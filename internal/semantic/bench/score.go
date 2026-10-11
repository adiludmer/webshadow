package bench

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/candidates"
	"github.com/adiludmer/webshadow/internal/semantic/decide"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
	"github.com/adiludmer/webshadow/internal/semantic/report"
	"github.com/adiludmer/webshadow/internal/semantic/store"
)

// Metric counts what a run predicted against what the expected IR holds.
// Precision and recall are 0 when their denominator is.
type Metric struct {
	Correct   int     `json:"correct"`
	Predicted int     `json:"predicted"`
	Expected  int     `json:"expected"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

func metric(correct, predicted, expected int) Metric {
	return Metric{
		Correct: correct, Predicted: predicted, Expected: expected,
		Precision: ratio(correct, predicted), Recall: ratio(correct, expected),
	}
}

func (m *Metric) add(o Metric) {
	*m = metric(m.Correct+o.Correct, m.Predicted+o.Predicted, m.Expected+o.Expected)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return math.Round(float64(a)/float64(b)*1000) / 1000
}

// Prereqs scores prerequisite hypotheses on the labelled consumers.
type Prereqs struct {
	// Predicted counts the run's prerequisites that claim a need: not
	// rejected, not labelled background, not unknown.
	Predicted      int     `json:"predicted"`
	FalsePositives int     `json:"false_positives"`
	Rate           float64 `json:"false_positive_rate"`
	// Background counts the prerequisites the run itself called
	// background.
	Background int `json:"background"`
}

// Abstention counts the model tasks a run answered with unknown or could
// not resolve.
type Abstention struct {
	Tasks      int     `json:"tasks"`
	Unknown    int     `json:"unknown"`
	Unresolved int     `json:"unresolved"`
	Rate       float64 `json:"rate"`
	// OnLabelled counts role questions abstained on although the expected
	// IR gives the family a role: abstentions that cost recall.
	OnLabelled int `json:"on_labelled"`
}

// Result is one run's score.
type Result struct {
	Case   string `json:"case"`
	Run    string `json:"run"`
	Model  string `json:"model"`
	Review string `json:"review"`

	Roles         Metric `json:"roles"`
	IdentityJoins Metric `json:"identity_joins"`
	Entities      Metric `json:"entities"`
	EntityNames   Metric `json:"entity_names"`
	EntityFields  Metric `json:"entity_fields"`
	Relations     Metric `json:"relations"`
	Operations    Metric `json:"operations"`
	// OperationMerges scores which labelled families share an operation.
	OperationMerges Metric `json:"operation_merges"`
	RequiredInputs  Metric `json:"required_inputs"`
	Outputs         Metric `json:"outputs"`

	Prerequisites Prereqs     `json:"prerequisites"`
	Abstention    Abstention  `json:"abstention"`
	Coverage      float64     `json:"coverage"`
	Cost          report.Cost `json:"cost"`
	// Unscored counts entities and operations built only from what the
	// expected IR leaves out.
	Unscored int `json:"unscored"`
	// Mistakes lists every scored disagreement, for reading a run.
	Mistakes []string `json:"mistakes"`
}

// Score reads a run from the store and scores it against e.
func Score(s *store.Store, run string, e *Expected) (*Result, error) {
	m, err := s.LoadRun(run)
	if err != nil {
		return nil, err
	}
	x, err := s.Load(m.Revision)
	if err != nil {
		return nil, err
	}
	if x == nil {
		return nil, fmt.Errorf("run %s has no revision", run)
	}
	prereqs, err := store.ReadLedger[ir.PrerequisiteHypothesis](s, run, "prerequisites.jsonl")
	if err != nil {
		return nil, err
	}
	decs, err := store.ReadLedger[decide.Decision](s, run, "decisions.jsonl")
	if err != nil {
		return nil, err
	}
	rep, err := report.Build(s, run)
	if err != nil {
		return nil, err
	}
	r := score(x, prereqs, decs, e)
	r.Run, r.Model, r.Coverage, r.Cost = run, m.Model, m.Coverage.Score, rep.Cost
	return r, nil
}

type scorer struct {
	e    *Expected
	x    *ir.InterfaceIR
	r    *Result
	role map[string]string // labelled family id -> role
	hyp  map[string]ir.HypothesisRef
	// entity maps an IR entity id to the expected entity it matches, or
	// to "" when it is scored but matches none.
	entity map[string]string
}

func score(x *ir.InterfaceIR, prereqs []ir.PrerequisiteHypothesis, decs []decide.Decision, e *Expected) *Result {
	sc := &scorer{
		e: e, x: x, r: &Result{Case: e.Case, Review: e.Review},
		role: map[string]string{}, hyp: map[string]ir.HypothesisRef{}, entity: map[string]string{},
	}
	for role, ids := range e.Roles {
		for _, id := range ids {
			sc.role[id] = role
		}
	}
	for _, h := range x.Hypotheses {
		sc.hyp[h.ID] = h
	}
	sc.roles()
	sc.identity()
	sc.entities()
	sc.operations()
	sc.prerequisites(prereqs)
	sc.abstention(decs)
	sort.Strings(sc.r.Mistakes)
	return sc.r
}

func (sc *scorer) miss(format string, args ...any) {
	sc.r.Mistakes = append(sc.r.Mistakes, fmt.Sprintf(format, args...))
}

func familyID(ref string) string {
	base, _, _ := strings.Cut(ref, "#")
	_, id, _ := ir.SplitRef(base)
	return id
}

// roles compares each labelled family's standing role.
func (sc *scorer) roles() {
	got := map[string]string{}
	for _, h := range sc.x.Hypotheses {
		if h.Kind != ir.KindFamilyRole || !standing(h.Status) {
			continue
		}
		for _, s := range h.SubjectRefs {
			got[familyID(s)] = h.CandidateID
		}
	}
	var correct, predicted int
	for _, id := range sortedKeys(sc.role) {
		want, have := sc.role[id], got[id]
		if have != "" {
			predicted++
		}
		if have == want {
			correct++
			continue
		}
		sc.miss("role fam:%s: expected %s, got %s", id, want, orNone(have))
	}
	sc.r.Roles = metric(correct, predicted, len(sc.role))
}

func standing(s ir.HypothesisStatus) bool {
	return s == ir.StatusMechanicallySupported || s == ir.StatusReviewed
}

// identity scores identity joins as pairs of labelled slots that share an
// entity.
func (sc *scorer) identity() {
	owner := map[string]string{}
	labelled := map[string]bool{}
	expected := 0
	for _, en := range sc.e.Entities {
		for _, r := range en.Identity {
			owner[r] = en.Name
			labelled[r] = true
		}
		n := len(en.Identity)
		expected += n * (n - 1) / 2
	}
	for _, r := range sc.e.NotIdentity {
		labelled[r] = true
	}
	var correct, predicted int
	found := map[[2]string]bool{}
	for _, en := range sc.x.Entities {
		var refs []string
		for _, f := range en.Identity {
			if labelled[f.Ref] {
				refs = append(refs, f.Ref)
			}
		}
		sort.Strings(refs)
		for i := range refs {
			for j := i + 1; j < len(refs); j++ {
				pair := [2]string{refs[i], refs[j]}
				if found[pair] {
					continue
				}
				found[pair] = true
				predicted++
				if owner[refs[i]] != "" && owner[refs[i]] == owner[refs[j]] {
					correct++
				} else {
					sc.miss("identity join %s = %s is wrong", refs[i], refs[j])
				}
			}
		}
	}
	sc.r.IdentityJoins = metric(correct, predicted, expected)
}

// entities matches each IR entity to an expected one: by shared identity
// slots, or, for entities without identity, by their field names.
func (sc *scorer) entities() {
	type match struct {
		id, name string
		weight   float64
	}
	var matches []match
	for _, en := range sc.x.Entities {
		best, weight := "", 0.0
		for _, want := range sc.e.Entities {
			w := float64(overlap(identityRefs(en), want.Identity))
			if w == 0 && len(want.Identity) == 0 && len(en.Identity) == 0 {
				if j := jaccard(fieldNames(en), want.Fields); j >= 0.5 {
					w = j
				}
			}
			if w > weight {
				best, weight = want.Name, w
			}
		}
		switch {
		case best != "":
			matches = append(matches, match{en.ID, best, weight})
		case overlap(identityRefs(en), sc.e.NotIdentity) > 0 || sc.aside(en):
			sc.entity[en.ID] = ""
			sc.miss("entity %s (%s) matches no expected entity", en.Name, en.ID)
		default:
			sc.r.Unscored++
		}
	}
	// The strongest match for each expected entity is the entity; others
	// split it.
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].weight > matches[j].weight })
	taken := map[string]bool{}
	var correct, named int
	byID := map[string]ir.Entity{}
	for _, en := range sc.x.Entities {
		byID[en.ID] = en
	}
	for _, m := range matches {
		sc.entity[m.id] = m.name
		if taken[m.name] {
			sc.miss("entity %s (%s) splits expected %s", byID[m.id].Name, m.id, m.name)
			continue
		}
		taken[m.name] = true
		correct++
		want := sc.expectedEntity(m.name)
		if containsFold(want.Names, byID[m.id].Name) {
			named++
		} else {
			sc.miss("entity %s named %s", m.name, byID[m.id].Name)
		}
		if len(want.Fields) > 0 {
			have := fieldNames(byID[m.id])
			ok := overlap(have, want.Fields)
			sc.r.EntityFields.add(metric(ok, len(have), len(want.Fields)))
		}
	}
	for _, want := range sc.e.Entities {
		if !taken[want.Name] {
			sc.miss("entity %s not found", want.Name)
			if len(want.Fields) > 0 {
				sc.r.EntityFields.add(metric(0, 0, len(want.Fields)))
			}
		}
	}
	sc.r.Entities = metric(correct, len(matches)+sc.countWrong(), len(sc.e.Entities))
	sc.r.EntityNames = metric(named, correct, correct)

	// Relations between scored entities.
	want := map[string]bool{}
	for _, en := range sc.e.Entities {
		for _, r := range en.Relations {
			want[en.Name+" "+r.Kind+" "+r.Target] = true
		}
	}
	var rc, rp int
	seen := map[string]bool{}
	for _, en := range sc.x.Entities {
		from, ok := sc.entity[en.ID]
		if !ok {
			continue
		}
		for _, rel := range en.Relations {
			to, ok := sc.entity[rel.Target]
			if !ok {
				continue
			}
			key := from + " " + rel.Kind + " " + to
			if seen[key] {
				continue
			}
			seen[key] = true
			rp++
			if from != "" && to != "" && want[key] {
				rc++
			} else {
				sc.miss("relation %s %s %s is not expected", orNone(from), rel.Kind, orNone(to))
			}
		}
	}
	for k := range want {
		if !seen[k] {
			sc.miss("relation %s not found", k)
		}
	}
	sc.r.Relations = metric(rc, rp, len(want))
}

// countWrong counts the scored entities that match no expected entity.
func (sc *scorer) countWrong() int {
	n := 0
	for _, name := range sc.entity {
		if name == "" {
			n++
		}
	}
	return n
}

// aside reports whether everything an entity was built from is a family
// the expected IR labels background or presentation.
func (sc *scorer) aside(en ir.Entity) bool {
	fams := map[string]bool{}
	for _, f := range en.Identity {
		fams[familyID(f.Ref)] = true
	}
	for _, f := range en.Fields {
		for _, s := range f.Sources {
			base, _, _ := strings.Cut(s.Ref, "#")
			_, id, _ := ir.SplitRef(base)
			fam, _, _ := strings.Cut(id, "/")
			fams[fam] = true
		}
	}
	if len(fams) == 0 {
		return false
	}
	for f := range fams {
		if r := sc.role[f]; r != candidates.RoleBackground && r != candidates.RolePresentation {
			return false
		}
	}
	return true
}

func (sc *scorer) expectedEntity(name string) ExpectedEntity {
	for _, en := range sc.e.Entities {
		if en.Name == name {
			return en
		}
	}
	return ExpectedEntity{}
}

// operations matches each IR operation to an expected operation and
// scores roles, merges, inputs and outputs.
func (sc *scorer) operations() {
	inOp := map[string]string{}
	for _, op := range sc.e.Operations {
		for _, f := range op.Families {
			inOp[f] = op.Name
		}
	}
	type match struct {
		op     ir.Operation
		want   ExpectedOperation
		weight float64
	}
	var matches []match
	var wrong int
	mergeExpected := 0
	for _, op := range sc.e.Operations {
		n := len(op.Families)
		mergeExpected += n * (n - 1) / 2
	}
	var mergeCorrect, mergePredicted int
	for _, op := range sc.x.Operations {
		fams := make([]string, len(op.SourceFamilies))
		labelled := true
		for i, f := range op.SourceFamilies {
			fams[i] = familyID(f)
			labelled = labelled && sc.role[fams[i]] != ""
		}
		var mine []string
		for _, f := range fams {
			if inOp[f] != "" {
				mine = append(mine, f)
			}
		}
		sort.Strings(mine)
		for i := range mine {
			for j := i + 1; j < len(mine); j++ {
				mergePredicted++
				if inOp[mine[i]] == inOp[mine[j]] {
					mergeCorrect++
				} else {
					sc.miss("operation %s merges fam:%s (%s) with fam:%s (%s)", op.Name, mine[i], inOp[mine[i]], mine[j], inOp[mine[j]])
				}
			}
		}
		// An operation belongs to the expected operation most of its
		// families come from; among several, the closest is the match and
		// the rest split it.
		best, weight := ExpectedOperation{}, 0.0
		for _, want := range sc.e.Operations {
			if 2*overlap(fams, want.Families) < len(fams) {
				continue
			}
			if j := jaccard(fams, want.Families); j > weight {
				best, weight = want, j
			}
		}
		switch {
		case weight > 0:
			matches = append(matches, match{op, best, weight})
		case labelled:
			wrong++
			sc.miss("operation %s over %s matches no expected operation", op.Name, strings.Join(fams, ", "))
		default:
			sc.r.Unscored++
		}
	}
	sc.r.OperationMerges = metric(mergeCorrect, mergePredicted, mergeExpected)

	sort.SliceStable(matches, func(i, j int) bool { return matches[i].weight > matches[j].weight })
	taken := map[string]bool{}
	var correct int
	for _, m := range matches {
		if taken[m.want.Name] {
			wrong++
			sc.miss("operation %s splits expected %s", m.op.Name, m.want.Name)
			continue
		}
		taken[m.want.Name] = true
		role := sc.hyp[m.op.Hypothesis].CandidateID
		if role == m.want.Role {
			correct++
		} else {
			wrong++
			sc.miss("operation %s (%s) has role %s, expected %s", m.op.Name, m.want.Name, role, m.want.Role)
		}
		sc.inputs(m.op, m.want)
		sc.outputs(m.op, m.want)
	}
	for _, want := range sc.e.Operations {
		if !taken[want.Name] {
			sc.miss("operation %s not found", want.Name)
			sc.inputs(ir.Operation{}, want)
			sc.outputs(ir.Operation{}, want)
		}
	}
	sc.r.Operations = metric(correct, correct+wrong, len(sc.e.Operations))
}

// inputs scores required inputs by the slots they fill.
func (sc *scorer) inputs(op ir.Operation, want ExpectedOperation) {
	expected := map[string]bool{}
	for _, in := range want.Required {
		for _, s := range in.Slots {
			expected[s] = true
		}
	}
	var correct, predicted int
	for _, in := range op.Inputs {
		if !in.Required {
			continue
		}
		for _, s := range in.Slots {
			predicted++
			if expected[s.Ref] {
				correct++
			} else {
				sc.miss("operation %s requires %s (%s), not expected", want.Name, in.Name, s.Ref)
			}
		}
	}
	if correct < len(expected) && op.ID != "" {
		sc.miss("operation %s requires %d of %d expected input slots", want.Name, correct, len(expected))
	}
	sc.r.RequiredInputs.add(metric(correct, predicted, len(expected)))
}

// outputs scores the entities an operation returns.
func (sc *scorer) outputs(op ir.Operation, want ExpectedOperation) {
	expected := map[string]bool{}
	for _, o := range want.Outputs {
		expected[fmt.Sprint(o.Entity, o.Many)] = true
	}
	var correct, predicted int
	seen := map[string]bool{}
	for _, o := range op.Outputs {
		name, scored := sc.entity[o.Entity]
		if o.Entity == "" || !scored {
			continue
		}
		key := fmt.Sprint(name, o.Many)
		if seen[key] {
			continue
		}
		seen[key] = true
		predicted++
		if name != "" && expected[key] {
			correct++
		} else {
			sc.miss("operation %s returns %s (many %v), not expected", want.Name, orNone(name), o.Many)
		}
	}
	sc.r.Outputs.add(metric(correct, predicted, len(expected)))
}

// prerequisites counts the run's claimed prerequisites on the labelled
// consumers that no accept rule covers.
func (sc *scorer) prerequisites(ps []ir.PrerequisiteHypothesis) {
	consumers := setOf(sc.e.Prerequisites.Consumers)
	var p Prereqs
	for _, pre := range ps {
		if pre.Status == ir.StatusRejected || !consumers[familyID(pre.ConsumerFamily)] {
			continue
		}
		switch pre.Kind {
		case candidates.PrereqBackground:
			p.Background++
			continue
		case decide.ChoiceUnknown, "":
			continue
		}
		p.Predicted++
		if !sc.accepted(pre) {
			p.FalsePositives++
			sc.miss("prerequisite %s: %s %s from fam:%s to fam:%s is not accepted", pre.ID, pre.Kind, pre.Carrier, familyID(pre.ProducerFamily), familyID(pre.ConsumerFamily))
		}
	}
	p.Rate = ratio(p.FalsePositives, p.Predicted)
	sc.r.Prerequisites = p
}

func (sc *scorer) accepted(pre ir.PrerequisiteHypothesis) bool {
	for _, a := range sc.e.Prerequisites.Accept {
		if a.Carrier != pre.Carrier || !contains(a.Kinds, pre.Kind) {
			continue
		}
		if len(a.Producers) == 0 || contains(a.Producers, familyID(pre.ProducerFamily)) {
			return true
		}
	}
	return false
}

func (sc *scorer) abstention(decs []decide.Decision) {
	var a Abstention
	for _, d := range decs {
		if d.Model.ID == "rule" {
			continue
		}
		a.Tasks++
		abstained := false
		switch {
		case d.Status == decide.StatusUnresolved:
			a.Unresolved++
			abstained = true
		case d.ChoiceID == decide.ChoiceUnknown:
			a.Unknown++
			abstained = true
		}
		if abstained && d.TaskType == decide.TypeClassifyFamily && sc.role[familyID(d.Subject)] != "" {
			a.OnLabelled++
		}
	}
	a.Rate = ratio(a.Unknown+a.Unresolved, a.Tasks)
	sc.r.Abstention = a
}

func identityRefs(en ir.Entity) []string {
	out := make([]string, len(en.Identity))
	for i, f := range en.Identity {
		out[i] = f.Ref
	}
	return out
}

func fieldNames(en ir.Entity) []string {
	out := make([]string, len(en.Fields))
	for i, f := range en.Fields {
		out[i] = f.Name
	}
	return out
}

func overlap(a, b []string) int {
	in := setOf(b)
	n := 0
	for _, s := range a {
		if in[s] {
			n++
			delete(in, s)
		}
	}
	return n
}

func jaccard(a, b []string) float64 {
	union := setOf(a)
	for _, s := range b {
		union[s] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(overlap(a, b)) / float64(len(union))
}

func setOf(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
