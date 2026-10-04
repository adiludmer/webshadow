// Package har parses, classifies and sanitizes HTTP Archive (HAR 1.2) files
// for the benchmark. Parsing produces a normalized Trace; sanitization
// rewrites the HAR document itself so every downstream consumer sees the
// redacted version.
package har

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Trace is the normalized view of a HAR file, one entry per request in
// file order.
type Trace struct {
	Entries []Entry
}

// Header is one HTTP header as recorded in the HAR.
type Header struct {
	Name  string
	Value string
}

// Entry is one request/response pair.
type Entry struct {
	Sequence     int // zero-based position in the HAR
	Method       string
	URL          string
	Host         string
	Path         string
	Query        url.Values
	Status       int
	ResourceType string // Chrome's _resourceType, when present
	Class        Class

	RequestHeaders  []Header
	RequestMIME     string
	RequestBody     []byte
	ResponseHeaders []Header
	ResponseMIME    string
	ResponseBody    []byte
	// Cookies counts entries left in the request and response cookie arrays.
	Cookies int
	// ResponseSize is the size the HAR reports for the response content,
	// which may be larger than ResponseBody when the body was not captured.
	ResponseSize int64

	StartedAt time.Time
	Duration  time.Duration
}

// RequestHeader returns the first request header with the given name,
// compared case-insensitively.
func (e *Entry) RequestHeader(name string) string { return findHeader(e.RequestHeaders, name) }

// ResponseHeader returns the first response header with the given name.
func (e *Entry) ResponseHeader(name string) string { return findHeader(e.ResponseHeaders, name) }

func findHeader(hs []Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// The raw types mirror the parts of HAR 1.2 the benchmark reads.
type rawDoc struct {
	Log *struct {
		Entries *[]rawEntry `json:"entries"`
	} `json:"log"`
}

type rawEntry struct {
	StartedDateTime string     `json:"startedDateTime"`
	Time            float64    `json:"time"`
	Request         rawRequest `json:"request"`
	Response        struct {
		Status  int               `json:"status"`
		Headers []rawHeader       `json:"headers"`
		Cookies []json.RawMessage `json:"cookies"`
		Content struct {
			Size     int64  `json:"size"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
	ResourceType string `json:"_resourceType"`
}

type rawRequest struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  []rawHeader       `json:"headers"`
	Cookies  []json.RawMessage `json:"cookies"`
	PostData *struct {
		MimeType string `json:"mimeType"`
		Text     string `json:"text"`
	} `json:"postData"`
}

type rawHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Parse reads a HAR document into a Trace. Errors carry no file name;
// callers add that context. Entries whose URL cannot be
// parsed are an error, since every later stage keys on host and path.
func Parse(r io.Reader) (*Trace, error) {
	var doc rawDoc
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if doc.Log == nil || doc.Log.Entries == nil {
		return nil, fmt.Errorf("missing log.entries array")
	}
	t := &Trace{Entries: make([]Entry, 0, len(*doc.Log.Entries))}
	for i, raw := range *doc.Log.Entries {
		e, err := convert(i, raw)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		t.Entries = append(t.Entries, e)
	}
	return t, nil
}

func convert(seq int, raw rawEntry) (Entry, error) {
	u, err := url.Parse(raw.Request.URL)
	if err != nil {
		return Entry{}, fmt.Errorf("bad url %q: %w", raw.Request.URL, err)
	}
	e := Entry{
		Sequence:        seq,
		Method:          strings.ToUpper(raw.Request.Method),
		URL:             raw.Request.URL,
		Host:            strings.ToLower(u.Host),
		Path:            u.EscapedPath(),
		Query:           u.Query(),
		Status:          raw.Response.Status,
		ResourceType:    raw.ResourceType,
		RequestHeaders:  headers(raw.Request.Headers),
		ResponseHeaders: headers(raw.Response.Headers),
		ResponseMIME:    raw.Response.Content.MimeType,
		ResponseSize:    raw.Response.Content.Size,
		Cookies:         len(raw.Request.Cookies) + len(raw.Response.Cookies),
		Duration:        time.Duration(raw.Time * float64(time.Millisecond)),
	}
	if e.Path == "" {
		e.Path = "/"
	}
	if raw.StartedDateTime != "" {
		if ts, err := time.Parse(time.RFC3339Nano, raw.StartedDateTime); err == nil {
			e.StartedAt = ts
		}
	}
	if pd := raw.Request.PostData; pd != nil {
		e.RequestMIME = pd.MimeType
		e.RequestBody = []byte(pd.Text)
	}
	if text := raw.Response.Content.Text; text != "" {
		if strings.EqualFold(raw.Response.Content.Encoding, "base64") {
			body, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return Entry{}, fmt.Errorf("response body is not valid base64: %w", err)
			}
			e.ResponseBody = body
		} else {
			e.ResponseBody = []byte(text)
		}
	}
	e.Class = Classify(&e)
	return e, nil
}

func headers(raw []rawHeader) []Header {
	hs := make([]Header, len(raw))
	for i, h := range raw {
		hs[i] = Header{h.Name, h.Value}
	}
	return hs
}
