// Package model holds the deterministic schemas the clustering pass
// produces. Every structure here is written to disk and hashed into
// identities, so field meanings are frozen per Version: an algorithm change
// that would move a value from one identity to another increments it rather
// than silently renaming families in old output.
//
// Nothing in this package, or anywhere under internal/cluster, decides what
// an observation means. A name like "search" or "token" never appears in
// clustering output unless the recorded protocol itself carried that string.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Version is the clustering schema and algorithm version. Family, link and
// edge identities hash it, so output from two versions never collides.
const Version = 1

// Body kinds. A kind says how far a body could be parsed, not what it is for.
const (
	BodyNone      = "none"
	BodyJSON      = "json"
	BodyForm      = "form"
	BodyMultipart = "multipart"
	BodyOpaque    = "opaque"
)

// Shape types. Opaque marks a body that was not parsed; Any is what two
// conflicting shapes merge into.
const (
	TypeNull      = "null"
	TypeBool      = "bool"
	TypeInteger   = "integer"
	TypeNumber    = "number"
	TypeString    = "string"
	TypeObject    = "object"
	TypeArray     = "array"
	TypeOpaque    = "opaque"
	TypeAny       = "any"
	TypeAbsent    = "absent"
	TypeMultipart = "multipart"
)

// Shape is the structure of a value with its own values removed. Object
// fields are always sorted by name and arrays carry one merged element
// shape, so two bodies that differ only in key order or in their values
// produce the same Shape and the same Fingerprint.
type Shape struct {
	Type   string  `json:"type"`
	Fields []Field `json:"fields,omitempty"`
	Elem   *Shape  `json:"elem,omitempty"`
	// Media and SizeClass describe an opaque body, which is the common case:
	// HTML, scripts, images and media are recorded by type and size class
	// rather than parsed.
	Media     string `json:"media,omitempty"`
	SizeClass string `json:"size_class,omitempty"`
}

// Field is one object field or form key.
type Field struct {
	Name  string `json:"name"`
	Shape Shape  `json:"shape"`
}

// Canonical renders the shape as the string its fingerprint hashes. It is
// stable across runs because fields are sorted on construction.
func (s Shape) Canonical() string {
	var b strings.Builder
	s.writeCanonical(&b)
	return b.String()
}

func (s Shape) writeCanonical(b *strings.Builder) {
	switch s.Type {
	case TypeObject, TypeMultipart:
		if s.Type == TypeMultipart {
			b.WriteString("multipart")
		}
		b.WriteByte('{')
		for i, f := range s.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(f.Name)
			b.WriteByte(':')
			f.Shape.writeCanonical(b)
		}
		b.WriteByte('}')
	case TypeArray:
		b.WriteByte('[')
		if s.Elem != nil {
			s.Elem.writeCanonical(b)
		}
		b.WriteByte(']')
	case TypeOpaque:
		b.WriteString("opaque(")
		b.WriteString(s.Media)
		b.WriteByte(',')
		b.WriteString(s.SizeClass)
		b.WriteByte(')')
	default:
		b.WriteString(s.Type)
	}
}

// Fingerprint identifies the shape. It covers the version, so a change to
// how shapes are derived cannot be mistaken for a change in the traffic.
func (s Shape) Fingerprint() string {
	return Hash("shape", s.Canonical())
}

// SortFields puts an object's fields in canonical order. Derivation calls
// it; callers that build a Shape by hand must too.
func SortFields(fields []Field) {
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
}

// Hash returns the 12 hex characters that identify a canonical form. The
// clustering version is mixed in, and so is kind, so a family and a link
// built from the same string get different ids.
func Hash(kind, canonical string) string {
	sum := sha256.Sum256([]byte("webshadow/cluster/v" + strconv.Itoa(Version) + "/" + kind + "\x00" + canonical))
	return hex.EncodeToString(sum[:6])
}

// Size classes bucket a body by length so that opaque bodies of the same
// kind do not each become their own response variant.
const (
	SizeEmpty  = "empty"
	SizeTiny   = "tiny"
	SizeSmall  = "small"
	SizeMedium = "medium"
	SizeLarge  = "large"
	SizeHuge   = "huge"
)

// SizeClass buckets n bytes.
func SizeClass(n int64) string {
	switch {
	case n <= 0:
		return SizeEmpty
	case n < 1<<10:
		return SizeTiny
	case n < 1<<14:
		return SizeSmall
	case n < 1<<18:
		return SizeMedium
	case n < 1<<22:
		return SizeLarge
	default:
		return SizeHuge
	}
}

// Sides of an exchange a value can sit on.
const (
	SideRequest  = "request"
	SideResponse = "response"
)

// Parts of an exchange a value can sit in. These are transport positions,
// never roles: Cookie is where a cookie was carried, not a claim that
// anything was authenticated.
const (
	PartPath      = "path"
	PartQuery     = "query"
	PartHeader    = "header"
	PartCookie    = "cookie"
	PartSetCookie = "set-cookie"
	PartBody      = "body"
	PartLocation  = "location"
	// PartText is a value found inside an unparsed text body, such as an
	// HTML page, by exact token match. It has no structural path.
	PartText = "text"
)

// Location is where a value was observed inside an exchange. Path is the
// concrete position, including array indices; Pattern is the same position
// with indices collapsed, so occurrences in different rows of one array
// aggregate. For path segments both are the segment index.
type Location struct {
	Side    string `json:"side"`
	Part    string `json:"part"`
	Path    string `json:"path,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// String renders a location the way evidence packs print it, for example
// "response.body.products[].asin" or "request.path.2".
func (l Location) String() string {
	s := l.Side + "." + l.Part
	if l.Pattern != "" {
		s += "." + l.Pattern
	}
	return s
}

// Value flags. Nothing is discarded for being low information: a flagged
// value stays in the index and in the links, and only the evidence pack
// decides whether it is worth showing.
const (
	// FlagLowInformation marks a value whose repetition is weak evidence on
	// its own: a boolean, a small number, a very short string, a media type.
	FlagLowInformation = "low_information"
	// FlagUbiquitous marks a value that occurs across many families, such as
	// a session cookie. Set by the value-index pass, not during
	// normalization.
	FlagUbiquitous = "ubiquitous"
)

// ValueRef is one scalar observed at one location, with the value kept as
// the text that crossed the wire.
type ValueRef struct {
	Value string   `json:"value"`
	Type  string   `json:"type"`
	Loc   Location `json:"loc"`
	Flags []string `json:"flags,omitempty"`
}

// HasFlag reports whether f is set.
func (v ValueRef) HasFlag(f string) bool {
	for _, got := range v.Flags {
		if got == f {
			return true
		}
	}
	return false
}

// Pair is a header, query parameter or cookie as observed, with the name
// canonicalized for comparison and the value left alone.
type Pair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Body is the structural record of one request or response body.
type Body struct {
	Kind      string `json:"kind"`
	Media     string `json:"media,omitempty"`
	Size      int64  `json:"size"`
	SizeClass string `json:"size_class,omitempty"`
	Shape     Shape  `json:"shape"`
	// Truncated is set when the body was parsed only as far as the value or
	// node budget allowed, so a missing field is not evidence of absence.
	Truncated bool `json:"truncated,omitempty"`
	// ParseError holds why a body that claimed to be JSON or a form was
	// recorded as opaque instead.
	ParseError string `json:"parse_error,omitempty"`
}

// Observation is one HTTP exchange reduced to canonical structure, with its
// values kept beside the structure rather than inside it. It always refers
// back to the exchange it came from, so later passes can fetch raw bytes.
type Observation struct {
	ExchangeID   string `json:"exchange_id"`
	SessionID    string `json:"session_id"`
	ConnectionID string `json:"connection_id,omitempty"`
	Seq          uint64 `json:"seq"`

	// RequestStart and ResponseStart are session nanoseconds from the
	// recording's monotonic clock; End is when the exchange completed.
	RequestStart  int64 `json:"request_start"`
	ResponseStart int64 `json:"response_start,omitempty"`
	End           int64 `json:"end"`

	Method       string   `json:"method"`
	Scheme       string   `json:"scheme"`
	Host         string   `json:"host"`
	PathSegments []string `json:"path_segments,omitempty"`
	// RawPath is the path as observed, for display and for redirect matching.
	RawPath string `json:"raw_path"`
	URL     string `json:"url"`

	Query          []Pair `json:"query,omitempty"`
	RequestHeaders []Pair `json:"request_headers,omitempty"`
	RequestCookies []Pair `json:"request_cookies,omitempty"`
	RequestBody    Body   `json:"request_body"`

	Status          int    `json:"status,omitempty"`
	ResponseHeaders []Pair `json:"response_headers,omitempty"`
	SetCookies      []Pair `json:"set_cookies,omitempty"`
	ResponseBody    Body   `json:"response_body"`
	// Error is the transport error that ended the exchange, if any; such an
	// exchange has no response.
	Error string `json:"error,omitempty"`

	// QueryShape is the query as a shape: one field per distinct key, sorted,
	// with the type of its values. It takes part in family identity.
	QueryShape Shape `json:"query_shape"`

	// Values are the scalars found in this exchange, in deterministic order
	// (side, part, pattern, path, value).
	Values []ValueRef `json:"values,omitempty"`
}

// Static reports whether this observation's response looks like a static
// asset: an image, font, stylesheet, script or media file. Such families are
// kept everywhere, including traces and sequence edges, and only the
// evidence pack collapses them, since they are most of a browser recording.
func (o *Observation) Static() bool { return StaticMedia(o.ResponseBody.Media) }

// StaticMedia reports whether a media type names a static asset.
func StaticMedia(media string) bool {
	switch {
	case media == "":
		return false
	case strings.HasPrefix(media, "image/"),
		strings.HasPrefix(media, "font/"),
		strings.HasPrefix(media, "audio/"),
		strings.HasPrefix(media, "video/"):
		return true
	}
	switch media {
	case "text/css",
		"text/javascript",
		"application/javascript",
		"application/x-javascript",
		"application/font-woff",
		"application/font-woff2",
		"application/vnd.ms-fontobject",
		"application/x-font-ttf":
		return true
	}
	return false
}
