package model

// Occurrence is one sighting of a value: in which exchange, which family,
// and where in the exchange.
type Occurrence struct {
	Ref      ObservationRef `json:"ref"`
	FamilyID string         `json:"family_id"`
	Loc      Location       `json:"loc"`
}

// ValueOccurrence aggregates the sightings of one value at one location of
// one family, so a value repeated across many exchanges of a family is one
// entry with a count rather than one entry per exchange.
type ValueOccurrence struct {
	FamilyID string   `json:"family_id"`
	Location Location `json:"location"`
	Count    int      `json:"count"`
	// Examples are the first MaxVariantExamples exchanges in reference
	// order.
	Examples []ObservationRef `json:"examples"`
}

// ValueLink records that one exact value was seen in more than one family.
// It is a join discovered without being named: it says the value connects
// these locations, never what the value is.
type ValueLink struct {
	ID        string            `json:"id"`
	Value     string            `json:"value"`
	ValueType string            `json:"value_type"`
	Flags     []string          `json:"flags,omitempty"`
	FamilyIDs []string          `json:"family_ids"`
	Count     int               `json:"count"`
	Locations []ValueOccurrence `json:"occurrences"`
}
