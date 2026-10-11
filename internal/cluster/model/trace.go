package model

// Page view triggers, from the request that started the page view. They
// come from request headers a server receives, never from the browser's
// own reports.
const (
	// TriggerTyped is a navigation the user started from outside any page:
	// a typed URL or a bookmark (Sec-Fetch-Site: none).
	TriggerTyped = "navigation.typed"
	// TriggerUser is a navigation that followed a user gesture in a page,
	// such as a click or Enter (Sec-Fetch-User: ?1).
	TriggerUser = "navigation.user"
	// TriggerRedirect is a navigation with no user gesture that the server
	// redirected, such as a sign-in bounce.
	TriggerRedirect = "navigation.redirect"
	// TriggerLink is any other navigation from a page with no user gesture,
	// such as a script setting location or a refresh.
	TriggerLink = "navigation.link"
	// TriggerHTML is a request with no Sec-Fetch headers that asked for and
	// got HTML, taken as a navigation.
	TriggerHTML = "navigation.html"
	// TriggerGap starts a page view after a quiet gap, for traffic with no
	// navigation signal at all.
	TriggerGap = "gap"
)

// Request kinds, from Sec-Fetch-Dest and Sec-Fetch-Mode.
const (
	// KindDocument is a main-frame navigation request.
	KindDocument = "document"
	// KindFetch is a fetch or XMLHttpRequest (Sec-Fetch-Dest: empty).
	KindFetch = "fetch"
	// KindWebSocket is a WebSocket handshake.
	KindWebSocket = "websocket"
)

// Trigger is the request that started a page view.
type Trigger struct {
	Kind       string `json:"kind"`
	ExchangeID string `json:"exchange_id"`
	Seq        uint64 `json:"seq"`
	T          int64  `json:"t"`
	URL        string `json:"url"`
}

// Navigation is one main-frame document load: its navigation request and
// any redirects the server answered it with, joined into one.
type Navigation struct {
	ID string `json:"id"`
	// URL is the document's URL after redirects.
	URL   string `json:"url,omitempty"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	// Document is the exchange that returned the document; Redirects are
	// the exchanges before it in its redirect chain, in order.
	Document   string   `json:"document"`
	Redirects  []string `json:"redirects,omitempty"`
	EpisodeIDs []string `json:"episode_ids"`
}

// Episode is one page view: the navigation that started it and the
// requests the page made. A subrequest belongs to the page view its Referer
// names, else to the latest one. Membership is context, not causation; each
// step's kind and offset are kept so a reader can judge.
type Episode struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	NavigationID string `json:"navigation_id,omitempty"`
	// Start is when its first request started and End when its last one
	// started.
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Trigger is nil for the traffic before the session's first page view.
	Trigger     *Trigger `json:"trigger,omitempty"`
	ExchangeIDs []string `json:"exchange_ids"`
	FamilyIDs   []string `json:"family_ids"`
	// FirstStep and LastStep are indices into the session trace's steps;
	// both are -1 for an episode with no traffic.
	FirstStep int `json:"first_step"`
	LastStep  int `json:"last_step"`
}

// TraceStep is one exchange in observed order.
type TraceStep struct {
	// T is when the request started, in session nanoseconds.
	T            int64          `json:"t"`
	Ref          ObservationRef `json:"ref"`
	Method       string         `json:"method"`
	URL          string         `json:"url"`
	FamilyID     string         `json:"family_id"`
	EpisodeID    string         `json:"episode_id"`
	NavigationID string         `json:"navigation_id,omitempty"`
	// SinceTrigger is T minus the episode trigger's time; zero for an
	// untriggered episode.
	SinceTrigger int64 `json:"since_trigger"`
	// Kind is what the request was for, from Sec-Fetch-Dest and
	// Sec-Fetch-Mode: document, fetch, script, image, iframe and so on.
	// It is empty when the request said nothing.
	Kind string `json:"kind,omitempty"`
}

// Trace is one session's exchanges in recorded order, each named by its
// family. It says only that these families were observed in this order.
type Trace struct {
	ID          string       `json:"id"`
	SessionID   string       `json:"session_id"`
	Navigations []Navigation `json:"navigations"`
	Steps       []TraceStep  `json:"steps"`
}
