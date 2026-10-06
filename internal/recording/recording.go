// Package recording defines the on-disk recording that `webshadow browser`
// produces and reads it back. A recording is one directory per session:
//
//	<root>/<session id>/
//	  session.json   session metadata, rewritten when the session stops
//	  http.jsonl     one HTTPExchange per line, in completion order
//	  browser.jsonl  one BrowserEvent per line, in arrival order
//	  bodies/        request and response bodies, named <sha256>.bin
//
// The format records facts only: bytes as they crossed the proxy and
// browser events as Chromium reported them. Both streams are stamped by one
// session Clock, so a later analyzer can merge them without running the
// browser or the proxy.
package recording

import (
	"encoding/json"
	"time"
)

// FormatVersion changes whenever a field changes meaning.
const FormatVersion = 1

// Session status values.
const (
	StatusRecording = "recording"
	StatusComplete  = "complete"
)

// File names inside a recording directory.
const (
	SessionFile = "session.json"
	HTTPFile    = "http.jsonl"
	BrowserFile = "browser.jsonl"
	BodiesDir   = "bodies"
)

// Session is the content of session.json.
type Session struct {
	FormatVersion int        `json:"format_version"`
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`

	Proxy   *ProxyInfo   `json:"proxy,omitempty"`
	Browser *BrowserInfo `json:"browser,omitempty"`

	Exchanges     int `json:"exchanges"`
	BrowserEvents int `json:"browser_events"`
}

// ProxyInfo describes the proxy that recorded the session.
type ProxyInfo struct {
	Addr string `json:"addr"`
	// CAFingerprint is the SHA-256 of the CA certificate, never its key.
	CAFingerprint string `json:"ca_fingerprint,omitempty"`
}

// BrowserInfo describes the browser the session launched.
type BrowserInfo struct {
	Version    string   `json:"version"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Profile    string   `json:"profile"`
}

// Stamp is a point on the session timeline. T orders events; Wall is for
// people reading the recording.
type Stamp struct {
	// T is nanoseconds since the session started, from a monotonic clock.
	T    int64     `json:"t"`
	Wall time.Time `json:"wall"`
}

// Header is one HTTP header as it crossed the proxy, in original order.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Body is a request or response body stored out of line. The bytes are
// exactly what crossed the wire after transfer decoding (chunking is undone),
// with any content coding such as gzip left in place.
type Body struct {
	// Ref is the body file relative to the recording directory.
	Ref             string `json:"ref"`
	Size            int64  `json:"size"`
	SHA256          string `json:"sha256"`
	ContentType     string `json:"content_type,omitempty"`
	ContentEncoding string `json:"content_encoding,omitempty"`
}

// Request is the request half of an exchange.
type Request struct {
	Method   string   `json:"method"`
	Scheme   string   `json:"scheme"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	URL      string   `json:"url"`
	Protocol string   `json:"protocol"`
	Headers  []Header `json:"headers"`
	Body     *Body    `json:"body,omitempty"`
}

// Response is the response half of an exchange.
type Response struct {
	Status   int      `json:"status"`
	Protocol string   `json:"protocol"`
	Headers  []Header `json:"headers"`
	Body     *Body    `json:"body,omitempty"`
}

// Timing holds session times in nanoseconds; zero means the point was not
// reached.
type Timing struct {
	RequestStart  int64 `json:"request_start"`
	ResponseStart int64 `json:"response_start,omitempty"`
	ResponseEnd   int64 `json:"response_end,omitempty"`
}

// HTTPExchange is one request and its response, or the error that ended it.
type HTTPExchange struct {
	ID           string `json:"id"`
	Seq          uint64 `json:"seq"`
	SessionID    string `json:"session_id"`
	ConnectionID string `json:"connection_id"`

	StartedAt   Stamp `json:"started_at"`
	CompletedAt Stamp `json:"completed_at"`

	Request  Request   `json:"request"`
	Response *Response `json:"response,omitempty"`
	Timing   Timing    `json:"timing"`
	Error    string    `json:"error,omitempty"`
}

// BrowserEvent is one event from the browser: a CDP event or an
// interaction reported by the injected instrumentation.
type BrowserEvent struct {
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	SessionID string `json:"session_id"`
	Timestamp Stamp  `json:"timestamp"`

	TargetID string `json:"target_id,omitempty"`
	FrameID  string `json:"frame_id,omitempty"`
	// Type names the event, such as "Page.frameNavigated" for a CDP event
	// or "interaction.click" for an instrumented one.
	Type    string          `json:"type"`
	PageURL string          `json:"page_url,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
