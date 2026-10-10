// Package bench scores an analysis run against a human-reviewed expected
// IR for the same clustering result, so model sizes and prompt versions
// can be compared on frozen evidence.
//
// The expected IR labels only what a reviewer is sure of. Families, slots
// and prerequisite consumers it leaves out count neither for nor against a
// run.
package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/semantic/input"
	"github.com/adiludmer/webshadow/internal/semantic/ir"
)

// ExpectedFile is the expected IR's name inside a clustering result's
// directory.
const ExpectedFile = "expected-ir.json"

// Expected is the reviewed ground truth for one benchmark case.
type Expected struct {
	Case     string   `json:"case"`
	Evidence string   `json:"evidence"`
	Review   string   `json:"review"`
	Notes    []string `json:"notes,omitempty"`
	// Roles lists family ids under the role a reviewer gives them.
	Roles    map[string][]string `json:"roles"`
	Entities []ExpectedEntity    `json:"entities"`
	// NotIdentity lists slots that look like keys but identify nothing,
	// so an entity built on them counts against a run.
	NotIdentity   []string            `json:"not_identity,omitempty"`
	Operations    []ExpectedOperation `json:"operations"`
	Prerequisites ExpectedPrereqs     `json:"prerequisites"`
}

// ExpectedEntity is one entity a run should find.
type ExpectedEntity struct {
	Name string `json:"name"`
	// Names are the names a run may give it.
	Names []string `json:"names"`
	// Identity lists every slot or response path that holds its key.
	Identity  []string           `json:"identity,omitempty"`
	Fields    []string           `json:"fields,omitempty"`
	Relations []ExpectedRelation `json:"relations,omitempty"`
}

// ExpectedRelation links an expected entity to another by name.
type ExpectedRelation struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// ExpectedOperation is one operation a run should synthesize.
type ExpectedOperation struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Families []string `json:"families"`
	// Required lists the inputs an agent must supply. Optional inputs a
	// run adds are not scored.
	Required []ExpectedInput  `json:"required,omitempty"`
	Outputs  []ExpectedOutput `json:"outputs,omitempty"`
}

// ExpectedInput is one required input and the slots it fills.
type ExpectedInput struct {
	Name  string   `json:"name"`
	Slots []string `json:"slots"`
}

// ExpectedOutput is an entity an operation returns.
type ExpectedOutput struct {
	Entity string `json:"entity"`
	Many   bool   `json:"many,omitempty"`
}

// ExpectedPrereqs says which prerequisites are real for the labelled
// consumers. A run's prerequisite on one of them that no rule accepts is
// a false positive.
type ExpectedPrereqs struct {
	Consumers []string       `json:"consumers"`
	Accept    []AcceptPrereq `json:"accept"`
}

// AcceptPrereq accepts prerequisites of the given carrier and kinds, from
// any producer unless Producers names them.
type AcceptPrereq struct {
	Carrier   string   `json:"carrier"`
	Producers []string `json:"producers,omitempty"`
	Kinds     []string `json:"kinds"`
	Why       string   `json:"why,omitempty"`
}

// Load reads an expected IR.
func Load(path string) (*Expected, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Expected
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &e, nil
}

// Validate checks that every family and slot the expected IR names exists
// in the clustering result, so a label cannot silently score nothing.
func (e *Expected) Validate(in *input.Input) error {
	var bad []string
	if e.Evidence != in.Manifest.ID {
		bad = append(bad, fmt.Sprintf("evidence %s, clustering result is %s", e.Evidence, in.Manifest.ID))
	}
	family := func(id string) {
		if !in.Has(ir.Ref(ir.RefFamily, id)) {
			bad = append(bad, "family "+id)
		}
	}
	located := locations(in)
	slot := func(ref string) {
		base, _, _ := strings.Cut(ref, "#")
		if !in.Has(base) || !located[ref] {
			bad = append(bad, "slot "+ref)
		}
	}
	seen := map[string]string{}
	for role, ids := range e.Roles {
		for _, id := range ids {
			family(id)
			if other, ok := seen[id]; ok {
				bad = append(bad, fmt.Sprintf("family %s labelled %s and %s", id, other, role))
			}
			seen[id] = role
		}
	}
	names := map[string]bool{}
	for _, en := range e.Entities {
		names[en.Name] = true
		for _, r := range en.Identity {
			slot(r)
		}
	}
	for _, en := range e.Entities {
		for _, r := range en.Relations {
			if !names[r.Target] {
				bad = append(bad, "relation to unknown entity "+r.Target)
			}
		}
	}
	for _, r := range e.NotIdentity {
		slot(r)
	}
	for _, op := range e.Operations {
		for _, f := range op.Families {
			family(f)
		}
		for _, in := range op.Required {
			for _, r := range in.Slots {
				slot(r)
			}
		}
		for _, o := range op.Outputs {
			if !names[o.Entity] {
				bad = append(bad, "output of unknown entity "+o.Entity)
			}
		}
	}
	for _, c := range e.Prerequisites.Consumers {
		family(c)
	}
	for _, a := range e.Prerequisites.Accept {
		for _, p := range a.Producers {
			family(p)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("expected IR does not match the clustering result:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}

// locations lists every request slot and every location a value link
// saw a value at, as family references.
func locations(in *input.Input) map[string]bool {
	out := map[string]bool{}
	for _, f := range in.Families {
		for _, s := range f.Slots {
			out[ir.Ref(ir.RefFamily, f.ID)+"#request."+s.Location] = true
		}
	}
	for _, l := range in.Links {
		for _, o := range l.Locations {
			out[ir.Ref(ir.RefFamily, o.FamilyID)+"#"+o.Location.String()] = true
		}
	}
	return out
}
