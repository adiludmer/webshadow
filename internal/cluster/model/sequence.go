package model

// PartURL is a request's whole URL, the target of a redirect flow.
const PartURL = "url"

// Flow carriers. A carrier is the transport path a value took from an
// earlier response to a later request. It says how the value travelled,
// never what it was for: a cookie flow is not a login.
const (
	CarrierCookie   = "cookie"
	CarrierRedirect = "redirect"
	CarrierHeader   = "header"
	CarrierQuery    = "query"
	CarrierPath     = "path"
	CarrierBody     = "body"
)

// FlowExample is one observed instance of a value flow.
type FlowExample struct {
	From  ObservationRef `json:"from"`
	To    ObservationRef `json:"to"`
	Value string         `json:"value"`
}

// ValueFlow aggregates the times a value from one location of an earlier
// family's response reappeared at one location of a later family's request.
type ValueFlow struct {
	Carrier string   `json:"carrier"`
	From    Location `json:"from"`
	To      Location `json:"to"`
	// Matches counts the later family's observations that received a value
	// this way. Read it against the edge's OrderedCount.
	Matches int `json:"matches"`
	// Values counts the distinct values that flowed.
	Values int `json:"values"`
	// Flags holds the flags every flowing value shared, so a flow carrying
	// only booleans reads as low information while one carrying a real id
	// does not.
	Flags    []string      `json:"flags,omitempty"`
	Examples []FlowExample `json:"examples"`
}

// Timing summarizes the gaps, in nanoseconds, between the most recent
// earlier occurrence of an edge's first family and each occurrence of its
// second.
type Timing struct {
	Min    int64 `json:"min"`
	Median int64 `json:"median"`
	Max    int64 `json:"max"`
}

// SequenceEdge aggregates how often one family was observed before another
// across all traces. It is evidence of ordering and data flow, never of
// dependency: WithoutPredecessor keeps every time the second family occurred
// with no earlier first family, so a reader can see when the order was not
// needed.
type SequenceEdge struct {
	ID         string `json:"id"`
	FromFamily string `json:"from_family"`
	ToFamily   string `json:"to_family"`
	// ToObservations is how many times ToFamily occurred in all traces.
	ToObservations int `json:"to_observations"`
	// ObservedTogether counts ToFamily occurrences in traces that also
	// contain FromFamily, before or after.
	ObservedTogether int `json:"observed_together"`
	// OrderedCount counts ToFamily occurrences with an earlier FromFamily in
	// the same trace, however far back.
	OrderedCount int `json:"ordered_count"`
	// WithoutPredecessor counts ToFamily occurrences with no earlier
	// FromFamily in their trace, including traces with none at all.
	WithoutPredecessor int `json:"without_predecessor"`
	// Immediate counts ToFamily occurrences whose directly preceding step
	// was FromFamily.
	Immediate int `json:"immediate"`
	// SessionsWithBoth counts the traces containing both families.
	SessionsWithBoth int         `json:"sessions_with_both"`
	Timing           Timing      `json:"timing"`
	ValueFlows       []ValueFlow `json:"value_flows,omitempty"`
}
