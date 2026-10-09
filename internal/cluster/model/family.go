package model

// ObservationRef points at one exchange. Exchange ids are only unique within
// a session, so the session is part of the reference.
type ObservationRef struct {
	SessionID  string `json:"session_id"`
	ExchangeID string `json:"exchange_id"`
	Seq        uint64 `json:"seq"`
}

// Key is the reference as one string, for maps.
func (r ObservationRef) Key() string { return r.SessionID + "/" + r.ExchangeID }

// Ref returns the reference to an observation.
func (o *Observation) Ref() ObservationRef {
	return ObservationRef{SessionID: o.SessionID, ExchangeID: o.ExchangeID, Seq: o.Seq}
}

// Less orders references by session, then by recorded sequence, which is
// the order every list of references is written in.
func (r ObservationRef) Less(o ObservationRef) bool {
	if r.SessionID != o.SessionID {
		return r.SessionID < o.SessionID
	}
	if r.Seq != o.Seq {
		return r.Seq < o.Seq
	}
	return r.ExchangeID < o.ExchangeID
}

// RouteSegment is one position of a path template: a literal that every
// member had, or a slot whose members' values all shared one character
// class.
type RouteSegment struct {
	Literal string `json:"literal,omitempty"`
	// Slot is the slot's name, such as "slot_1", when the position varies.
	Slot string `json:"slot,omitempty"`
	// Class is the character class every value at a slot shared, such as
	// "id" or "int". It describes characters, never meaning.
	Class string `json:"class,omitempty"`
}

// Slot is a structurally stable position whose observed values may vary: a
// path slot, a query key or a request body field.
type Slot struct {
	Name string `json:"name"`
	// Location is where the slot sits, such as "path.2", "query.q" or
	// "body.items[].id".
	Location string `json:"location"`
	// Type is the character class for a path slot and the scalar type for a
	// query or body slot.
	Type string `json:"type"`
	// Observations counts the family members that carried this slot, so an
	// optional query key or body field shows as fewer than the family's
	// total.
	Observations int `json:"observations"`
	Cardinality  int `json:"cardinality"`
	// Examples are up to MaxExamples distinct values, picked in hash order
	// so the choice does not depend on which recording came first. Full
	// values stay in the observations.
	Examples []string `json:"examples"`
}

// MaxExamples bounds the representative values a slot keeps.
const MaxExamples = 5

// ResponseVariant is one kind of response a family returned: one status,
// one media type and one structural shape.
type ResponseVariant struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Media  string `json:"media,omitempty"`
	// Error is set instead of a status when the exchange ended in a
	// transport error.
	Error string `json:"error,omitempty"`
	Shape Shape  `json:"shape"`
	Count int    `json:"count"`
	// Examples are the first MaxVariantExamples members in reference order.
	Examples []ObservationRef `json:"examples"`
}

// MaxVariantExamples bounds the example exchanges a variant keeps.
const MaxVariantExamples = 3

// RequestFamily is a set of observations that look like instances of one
// protocol operation: same method, host, path template, query shape and
// request body shape. Responses do not split a family; they become its
// variants.
type RequestFamily struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Host   string `json:"host"`
	// PathTemplate is the route for people, such as
	// "/products/{slot_1}". Route holds the same positions as data.
	PathTemplate string         `json:"path_template"`
	Route        []RouteSegment `json:"route"`
	// RouteID is shared by every family with the same method, host and path
	// template, so families that differ only in query or body shape can be
	// shown side by side.
	RouteID string `json:"route_id"`

	QueryShape  Shape  `json:"query_shape"`
	BodyKind    string `json:"body_kind"`
	RequestBody Shape  `json:"request_body"`

	Slots            []Slot            `json:"slots,omitempty"`
	Observations     []ObservationRef  `json:"observations"`
	ResponseVariants []ResponseVariant `json:"response_variants"`
	// Static is set when every response was a static asset by media type.
	Static bool `json:"static,omitempty"`
}
