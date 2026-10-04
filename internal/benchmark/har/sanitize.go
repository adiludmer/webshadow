package har

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Redacted replaces every removed string value.
const Redacted = "REDACTED"

// SanitizeConfig controls what Sanitize removes. Name matching is
// case-insensitive; a name matches when it equals an entry or, for the
// *Contains lists, when it contains one.
type SanitizeConfig struct {
	Headers            []string // header names, exact
	HeadersContains    []string // header-name fragments
	Params             []string // query and form parameter names, exact
	ParamsContains     []string // parameter-name fragments
	BodyFields         []string // JSON object keys, exact
	BodyFieldsContains []string
	// StripBodies drops response bodies of these classes (the HAR keeps the
	// entry, headers and size). Use it to shrink captures to what agents read.
	StripBodies []Class
	// DropClasses removes entries of these classes from the HAR entirely.
	DropClasses []Class
	// TrimInitiators removes the call stacks Chrome records under
	// _initiator, keeping its type and URL. Stacks are often a quarter of a
	// trimmed capture and say nothing about the data a page received.
	TrimInitiators bool
}

// DefaultSanitizeConfig removes credentials, session material and common
// personal identifiers.
func DefaultSanitizeConfig() SanitizeConfig {
	return SanitizeConfig{
		Headers: []string{
			"cookie", "set-cookie", "authorization", "proxy-authorization",
			"x-api-key", "api-key", "x-auth-token", "x-access-token",
			"x-csrf-token", "x-xsrf-token", "csrf-token", "x-wp-nonce",
			"x-amz-security-token", "x-client-data", "x-browser-validation",
		},
		HeadersContains: []string{"token", "secret", "session", "apikey", "api-key"},
		Params: []string{
			"key", "api_key", "apikey", "access_token", "refresh_token", "id_token",
			"token", "auth", "password", "passwd", "secret", "signature", "sig",
			"session", "sessionid", "session_id", "sid", "csrf", "_csrf", "xsrf",
			"nonce", "_wpnonce", "code", "state", "email", "phone",
			"uid", "user_id", "userid", "cid", "client_id", "_ga", "_gid",
			"ui", "uifp", "sd", "visitor_id", "device_id",
		},
		ParamsContains: []string{"token", "secret", "password", "session", "apikey", "api_key"},
		BodyFields: []string{
			"password", "passwd", "secret", "token", "access_token", "refresh_token",
			"id_token", "api_key", "apikey", "authorization", "session", "sessionid",
			"session_id", "csrf", "csrf_token", "nonce", "email", "phone",
			"credit_card", "card_number", "cvv", "ssn",
			"uid", "user_id", "userid", "visitor_id", "device_id", "ui", "uifp", "sd",
		},
		BodyFieldsContains: []string{"password", "secret", "token"},
	}
}

// SanitizeReport counts what Sanitize changed.
type SanitizeReport struct {
	Entries        int
	Headers        int
	Cookies        int
	Params         int
	BodyFields     int
	StrippedBodies int
	DroppedEntries int
}

func (r SanitizeReport) String() string {
	return fmt.Sprintf("%d entries kept, %d dropped: %d headers, %d cookies, %d params and %d body fields redacted; %d bodies stripped",
		r.Entries, r.DroppedEntries, r.Headers, r.Cookies, r.Params, r.BodyFields, r.StrippedBodies)
}

// Sanitize reads a HAR document, redacts it according to cfg and writes the
// result as indented JSON. Fields the sanitizer does not know are kept.
func Sanitize(r io.Reader, w io.Writer, cfg SanitizeConfig) (SanitizeReport, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return SanitizeReport{}, fmt.Errorf("invalid JSON: %w", err)
	}
	log, ok := doc["log"].(map[string]any)
	if !ok {
		return SanitizeReport{}, fmt.Errorf("missing log object")
	}
	entries, _ := log["entries"].([]any)

	s := sanitizer{cfg: cfg}
	kept := make([]any, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		class := classifyRaw(entry)
		if slices.Contains(cfg.DropClasses, class) {
			s.report.DroppedEntries++
			continue
		}
		s.entry(entry, slices.Contains(cfg.StripBodies, class))
		s.report.Entries++
		kept = append(kept, entry)
	}
	if _, ok := log["entries"]; ok {
		log["entries"] = kept
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return s.report, err
	}
	return s.report, nil
}

type sanitizer struct {
	cfg    SanitizeConfig
	report SanitizeReport
}

func (s *sanitizer) entry(e map[string]any, stripBody bool) {
	if init, ok := e["_initiator"].(map[string]any); ok && s.cfg.TrimInitiators {
		delete(init, "stack")
	}
	if req, ok := e["request"].(map[string]any); ok {
		s.headers(req)
		s.cookies(req)
		if u, ok := req["url"].(string); ok {
			req["url"] = s.url(u)
		}
		s.nameValues(req["queryString"])
		if pd, ok := req["postData"].(map[string]any); ok {
			s.nameValues(pd["params"])
			if text, ok := pd["text"].(string); ok {
				mime, _ := pd["mimeType"].(string)
				pd["text"] = s.body(text, mime)
			}
		}
	}
	resp, ok := e["response"].(map[string]any)
	if !ok {
		return
	}
	s.headers(resp)
	s.cookies(resp)
	if loc, ok := resp["redirectURL"].(string); ok && loc != "" {
		resp["redirectURL"] = s.url(loc)
	}
	content, ok := resp["content"].(map[string]any)
	if !ok {
		return
	}
	text, _ := content["text"].(string)
	if text == "" {
		return
	}
	if stripBody {
		delete(content, "text")
		delete(content, "encoding")
		content["comment"] = "body removed by webshadow sanitize"
		s.report.StrippedBodies++
		return
	}
	if enc, _ := content["encoding"].(string); strings.EqualFold(enc, "base64") {
		return // binary bodies are not redacted field by field
	}
	mime, _ := content["mimeType"].(string)
	content["text"] = s.body(text, mime)
}

// classifyRaw classifies an undecoded HAR entry the same way Parse does.
func classifyRaw(e map[string]any) Class {
	var tmp Entry
	if req, ok := e["request"].(map[string]any); ok {
		if raw, ok := req["url"].(string); ok {
			if u, err := url.Parse(raw); err == nil {
				tmp.Host, tmp.Path = strings.ToLower(u.Host), u.EscapedPath()
			}
		}
	}
	tmp.ResourceType, _ = e["_resourceType"].(string)
	if resp, ok := e["response"].(map[string]any); ok {
		if content, ok := resp["content"].(map[string]any); ok {
			tmp.ResponseMIME, _ = content["mimeType"].(string)
		}
	}
	return Classify(&tmp)
}

func (s *sanitizer) headers(m map[string]any) {
	list, _ := m["headers"].([]any)
	for _, raw := range list {
		h, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := h["name"].(string)
		value, _ := h["value"].(string)
		switch {
		case matches(name, s.cfg.Headers, s.cfg.HeadersContains):
			if value != Redacted {
				h["value"] = Redacted
				s.report.Headers++
			}
		case strings.EqualFold(name, "referer") || strings.EqualFold(name, "location") || name == ":path":
			// These carry URLs whose query strings need the same redaction.
			h["value"] = s.url(value)
		}
	}
}

func (s *sanitizer) cookies(m map[string]any) {
	if list, ok := m["cookies"].([]any); ok && len(list) > 0 {
		s.report.Cookies += len(list)
		m["cookies"] = []any{}
	}
}

// nameValues redacts a HAR name/value list such as queryString.
func (s *sanitizer) nameValues(v any) {
	list, _ := v.([]any)
	for _, raw := range list {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := p["name"].(string)
		value, _ := p["value"].(string)
		switch {
		case matches(name, s.cfg.Params, s.cfg.ParamsContains):
			if _, has := p["value"]; has {
				p["value"] = Redacted
			}
		default:
			if decoded, err := url.QueryUnescape(value); err == nil && looksLikeJSON(decoded) {
				before := s.report.BodyFields
				if clean := s.body(decoded, "application/json"); s.report.BodyFields > before {
					if decoded == value {
						p["value"] = clean
					} else {
						p["value"] = url.QueryEscape(clean)
					}
				}
			}
		}
	}
}

// url redacts sensitive query parameters and userinfo in a URL, keeping
// the parameter order and every other byte of the query as recorded.
func (s *sanitizer) url(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	changed := false
	if u.User != nil {
		u.User = nil
		changed = true
	}
	if u.RawQuery != "" {
		if q, n := s.query(u.RawQuery); n > 0 {
			u.RawQuery = q
			s.report.Params += n
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return u.String()
}

// query redacts values in a raw query string and returns it with the number
// of values replaced.
func (s *sanitizer) query(raw string) (string, int) {
	parts := strings.Split(raw, "&")
	n := 0
	for i, part := range parts {
		key, value, hasValue := strings.Cut(part, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		if !hasValue || value == Redacted {
			continue
		}
		if matches(name, s.cfg.Params, s.cfg.ParamsContains) {
			parts[i] = key + "=" + Redacted
			n++
			continue
		}
		// A value can itself be URL-encoded JSON carrying identifiers.
		if decoded, err := url.QueryUnescape(value); err == nil && looksLikeJSON(decoded) {
			before := s.report.BodyFields
			if clean := s.body(decoded, "application/json"); s.report.BodyFields > before {
				parts[i] = key + "=" + url.QueryEscape(clean)
				n++
			}
		}
	}
	return strings.Join(parts, "&"), n
}

// body redacts a request or response body by MIME type: JSON keys by name,
// form-encoded parameters by name. Other bodies are returned unchanged.
func (s *sanitizer) body(text, mime string) string {
	mime = strings.ToLower(mime)
	if m := jsonpPattern.FindStringSubmatch(text); m != nil && strings.Contains(mime, "javascript") {
		return m[1] + s.body(m[2], "application/json") + m[3]
	}
	switch {
	case strings.Contains(mime, "json") || looksLikeJSON(text):
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil || dec.More() {
			return text
		}
		before := s.report.BodyFields
		v = s.jsonValue(v)
		if s.report.BodyFields == before {
			return text
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return text
		}
		return strings.TrimSuffix(buf.String(), "\n")
	case strings.Contains(mime, "x-www-form-urlencoded"):
		q, n := s.query(text)
		s.report.Params += n
		return q
	}
	return text
}

// jsonpPattern matches script responses that wrap one JSON value, either
// as a JSONP call, callback({...}); or as an assignment, name = {...};
var jsonpPattern = regexp.MustCompile(`(?s)^(\s*[\w.$]+\s*(?:\(|=)\s*)([\[{].*[\]}])(\s*\)?;?\s*)$`)

func looksLikeJSON(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// jsonValue walks a decoded JSON value and replaces the values of sensitive
// keys with placeholders of the same type, so the body keeps its shape.
func (s *sanitizer) jsonValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if matches(k, s.cfg.BodyFields, s.cfg.BodyFieldsContains) {
				x[k] = s.placeholder(x[k])
			} else {
				x[k] = s.jsonValue(x[k])
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = s.jsonValue(x[i])
		}
		return x
	}
	return v
}

func (s *sanitizer) placeholder(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == Redacted {
			return x
		}
		s.report.BodyFields++
		return Redacted
	case json.Number:
		if x != "0" {
			s.report.BodyFields++
		}
		return json.Number("0")
	case bool:
		if x {
			s.report.BodyFields++
		}
		return false
	case map[string]any:
		for k, val := range x {
			x[k] = s.placeholder(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = s.placeholder(x[i])
		}
		return x
	}
	return v
}

func matches(name string, exact, contains []string) bool {
	n := strings.ToLower(name)
	for _, e := range exact {
		if n == e {
			return true
		}
	}
	for _, c := range contains {
		if strings.Contains(n, c) {
			return true
		}
	}
	return false
}

// Unsanitized lists the credential material still present in a trace:
// cookie arrays, and cookie and auth headers whose value is not the redaction placeholder.
// An empty result means the trace passes the benchmark's sanitization check.
func Unsanitized(t *Trace) []string {
	var found []string
	check := func(seq int, where string, hs []Header) {
		for _, h := range hs {
			switch strings.ToLower(h.Name) {
			case "cookie", "set-cookie", "authorization", "proxy-authorization":
				if h.Value != Redacted && h.Value != "" {
					found = append(found, fmt.Sprintf("entry %d: %s header %s", seq, where, h.Name))
				}
			}
		}
	}
	for _, e := range t.Entries {
		if e.Cookies > 0 {
			found = append(found, fmt.Sprintf("entry %d: %d cookie(s)", e.Sequence, e.Cookies))
		}
		check(e.Sequence, "request", e.RequestHeaders)
		check(e.Sequence, "response", e.ResponseHeaders)
	}
	return found
}
