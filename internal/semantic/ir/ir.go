// Package ir defines the Agent Interface IR that semantic discovery
// produces: entities, operations and the hypotheses behind them, each tied
// to the clustering evidence that supports it.
//
// Every assertion here is a hypothesis with a status. A status of
// mechanically_supported means the deterministic checks passed, not that
// the meaning a model proposed is true. Internal ids are content hashes kept
// apart from display names, so renaming an entity never changes what refers
// to it.
package ir

// SchemaVersion is the IR schema version. Readers refuse a revision written
// with another version rather than guessing at field meanings.
const SchemaVersion = "webshadow.ir/v1"

// HypothesisStatus is where an assertion stands.
type HypothesisStatus string

// Statuses an assertion can have.
const (
	// StatusProposed is a candidate a model or generator offered that no
	// check has passed yet.
	StatusProposed HypothesisStatus = "proposed"
	// StatusMechanicallySupported means every deterministic check passed.
	// It is not semantic truth.
	StatusMechanicallySupported HypothesisStatus = "mechanically_supported"
	// StatusReviewed means a person confirmed it.
	StatusReviewed HypothesisStatus = "reviewed"
	// StatusRejected means a check or a person refuted it.
	StatusRejected HypothesisStatus = "rejected"
	// StatusSuperseded means a later revision replaced it.
	StatusSuperseded HypothesisStatus = "superseded"
)

// Statuses lists every valid status.
var Statuses = []HypothesisStatus{
	StatusProposed, StatusMechanicallySupported, StatusReviewed, StatusRejected, StatusSuperseded,
}

// Valid reports whether s is a known status.
func (s HypothesisStatus) Valid() bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// HypothesisKind says what a hypothesis asserts.
type HypothesisKind string

// Kinds of hypothesis, one per decision task family in the spec.
const (
	KindFamilyRole   HypothesisKind = "family_role"
	KindEntity       HypothesisKind = "entity"
	KindEntityName   HypothesisKind = "entity_name"
	KindIdentity     HypothesisKind = "identity"
	KindRelation     HypothesisKind = "relation"
	KindPrerequisite HypothesisKind = "prerequisite"
	KindOperation    HypothesisKind = "operation"
)

// Kinds lists every valid kind.
var Kinds = []HypothesisKind{
	KindFamilyRole, KindEntity, KindEntityName, KindIdentity, KindRelation, KindPrerequisite, KindOperation,
}

// Valid reports whether k is a known kind.
func (k HypothesisKind) Valid() bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

// CheckResult is the outcome of one deterministic check on a hypothesis.
type CheckResult struct {
	Check  string `json:"check"`
	Passed bool   `json:"passed"`
	// Detail is a short machine-written explanation, such as a count of
	// mismatches; it never holds raw secrets.
	Detail string `json:"detail,omitempty"`
	// EvidenceRefs are the observations the check examined or the
	// counterexamples it found.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// ConfidenceRecord summarizes what a hypothesis rests on. It is counts, not
// a probability: a model's self-reported confidence is never stored here.
type ConfidenceRecord struct {
	// Supporting and Contradicting count observations for and against.
	Supporting    int `json:"supporting"`
	Contradicting int `json:"contradicting"`
	// Agreement counts independent decisions that chose the same candidate.
	Agreement int `json:"agreement,omitempty"`
	// Unresolved counts decisions that ended without a valid answer.
	Unresolved int `json:"unresolved,omitempty"`
}

// Hypothesis is one assertion with its evidence and status.
type Hypothesis struct {
	ID   string         `json:"id"`
	Kind HypothesisKind `json:"kind"`
	// SubjectRefs are what the hypothesis is about: families, entities,
	// fields or operations.
	SubjectRefs []string `json:"subject_refs"`
	// CandidateID is the choice that was selected, such as "item_read".
	CandidateID  string           `json:"candidate_id"`
	EvidenceRefs []string         `json:"evidence_refs"`
	ModelRunID   string           `json:"model_run_id,omitempty"`
	Status       HypothesisStatus `json:"status"`
	Checks       []CheckResult    `json:"checks,omitempty"`
	Confidence   ConfidenceRecord `json:"confidence"`
	// Supersedes names the hypotheses this one replaced.
	Supersedes []string `json:"supersedes,omitempty"`
}

// HypothesisRef is how the IR lists a hypothesis: its id, kind and status.
// The full record lives in the run's hypotheses ledger.
type HypothesisRef struct {
	ID     string           `json:"id"`
	Kind   HypothesisKind   `json:"kind"`
	Status HypothesisStatus `json:"status"`
}

// FieldRef is a typed path into one response variant or request slot, such
// as "fam:0c1d.../var:989b.../body.entity.asin" or "fam:.../query.k".
type FieldRef struct {
	Ref  string `json:"ref"`
	Type string `json:"type"`
}

// EntityField is one field of an entity and where it was observed.
type EntityField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Sources are the response paths this field was read from.
	Sources []FieldRef `json:"sources"`
	// Observations and Nulls count how often the field was present and
	// how often it was null across those sources.
	Observations int `json:"observations"`
	Nulls        int `json:"nulls,omitempty"`
}

// RelationRef links one entity to another.
type RelationRef struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	// Kind is "contains" for nesting inside one response or "references"
	// for a value that flows across families.
	Kind string `json:"kind"`
	// Cardinality is a hypothesis such as "one" or "many".
	Cardinality string   `json:"cardinality"`
	FieldPaths  []string `json:"field_paths"`
	Hypothesis  string   `json:"hypothesis"`
}

// Entity is a kind of object the site exposes, independent of the
// endpoints that return it.
type Entity struct {
	ID string `json:"id"`
	// Name is a display name; Aliases are other names it was given.
	Name         string        `json:"name"`
	Aliases      []string      `json:"aliases,omitempty"`
	Identity     []FieldRef    `json:"identity,omitempty"`
	Fields       []EntityField `json:"fields,omitempty"`
	Relations    []RelationRef `json:"relations,omitempty"`
	EvidenceRefs []string      `json:"evidence_refs"`
	Hypothesis   string        `json:"hypothesis"`
}

// InputField is one argument of an operation and the request slots it
// fills.
type InputField struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Required bool       `json:"required"`
	Slots    []FieldRef `json:"slots"`
}

// OutputField is one result of an operation and the response paths it is
// read from.
type OutputField struct {
	Name    string     `json:"name"`
	Type    string     `json:"type"`
	Entity  string     `json:"entity,omitempty"`
	Many    bool       `json:"many,omitempty"`
	Sources []FieldRef `json:"sources"`
}

// PrerequisiteHypothesis is protocol state an operation may need: a value
// some earlier family produced and the operation's requests carried. It is
// a correlation, never proven necessity.
type PrerequisiteHypothesis struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ProducerFamily string `json:"producer_family,omitempty"`
	ConsumerFamily string `json:"consumer_family"`
	// Carrier is how the value travelled: cookie, redirect, header, query,
	// path or body.
	Carrier string `json:"carrier"`
	// Scope is request, tab, navigation, browser_session or unknown.
	Scope        string           `json:"scope"`
	EvidenceRefs []string         `json:"evidence_refs"`
	Caveats      []string         `json:"caveats,omitempty"`
	Status       HypothesisStatus `json:"status"`
	Hypothesis   string           `json:"hypothesis"`
}

// Operation is an agent-facing capability, which may need several request
// families.
type Operation struct {
	ID             string                   `json:"id"`
	Name           string                   `json:"name"`
	Inputs         []InputField             `json:"inputs,omitempty"`
	Outputs        []OutputField            `json:"outputs,omitempty"`
	SourceFamilies []string                 `json:"source_families"`
	Preconditions  []PrerequisiteHypothesis `json:"preconditions,omitempty"`
	EvidenceRefs   []string                 `json:"evidence_refs"`
	Status         HypothesisStatus         `json:"status"`
	Hypothesis     string                   `json:"hypothesis"`
}

// InterfaceIR is one revision of the Agent Interface IR.
type InterfaceIR struct {
	SchemaVersion string `json:"schema_version"`
	Revision      string `json:"revision"`
	// Parent is the revision this one was patched from; empty for the
	// first.
	Parent string `json:"parent,omitempty"`
	// Evidence names the clustering results this revision draws on.
	Evidence   []string        `json:"evidence"`
	Entities   []Entity        `json:"entities"`
	Operations []Operation     `json:"operations"`
	Hypotheses []HypothesisRef `json:"hypotheses"`
}

// New returns an empty IR over the given clustering results.
func New(evidence ...string) *InterfaceIR {
	return &InterfaceIR{
		SchemaVersion: SchemaVersion,
		Evidence:      append([]string{}, evidence...),
		Entities:      []Entity{},
		Operations:    []Operation{},
		Hypotheses:    []HypothesisRef{},
	}
}
