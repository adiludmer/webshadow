package model

import "encoding/json"

// BrowserEventRef points at one recorded browser event and carries enough
// of it to read an episode without opening the recording.
type BrowserEventRef struct {
	EventID  string `json:"event_id"`
	Seq      uint64 `json:"seq"`
	Type     string `json:"type"`
	T        int64  `json:"t"`
	TargetID string `json:"target_id,omitempty"`
	FrameID  string `json:"frame_id,omitempty"`
	PageURL  string `json:"page_url,omitempty"`
	// Payload is the event's recorded payload, such as the element an
	// interaction touched. It is copied as recorded.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Navigation is a span of a session between two main-frame document loads.
type Navigation struct {
	ID         string           `json:"id"`
	URL        string           `json:"url,omitempty"`
	Start      int64            `json:"start"`
	End        int64            `json:"end"`
	Event      *BrowserEventRef `json:"event,omitempty"`
	EpisodeIDs []string         `json:"episode_ids"`
}

// Episode is the traffic around one browser trigger: from the trigger to
// the next one. It is temporal context, not a workflow. Membership means a
// request started in the window, not that the trigger caused it; each
// step's initiator and offset are kept so a reader can tell the difference.
type Episode struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	NavigationID string `json:"navigation_id,omitempty"`
	Start        int64  `json:"start"`
	End          int64  `json:"end"`
	// Trigger is nil for the traffic before the session's first trigger.
	Trigger     *BrowserEventRef `json:"trigger,omitempty"`
	ExchangeIDs []string         `json:"exchange_ids"`
	FamilyIDs   []string         `json:"family_ids"`
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
	// Initiator, FrameID and TargetID come from the browser's own report of
	// the request when one could be matched to the exchange.
	Initiator string `json:"initiator,omitempty"`
	FrameID   string `json:"frame_id,omitempty"`
	TargetID  string `json:"target_id,omitempty"`
}

// Trace is one session's exchanges in recorded order, each named by its
// family. It says only that these families were observed in this order.
type Trace struct {
	ID          string       `json:"id"`
	SessionID   string       `json:"session_id"`
	Navigations []Navigation `json:"navigations"`
	Steps       []TraceStep  `json:"steps"`
}
