package capture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
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
	// StripBodies drops response bodies of these classes (the capture keeps
	// the item and its headers). Use it to shrink captures to what agents read.
	StripBodies []Class
	// DropClasses removes items of these classes from the capture entirely.
	DropClasses []Class
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
	Params         int
	BodyFields     int
	StrippedBodies int
	DroppedEntries int
}

func (r SanitizeReport) String() string {
	return fmt.Sprintf("%d items kept, %d dropped: %d headers, %d params and %d body fields redacted; %d bodies stripped",
		r.Entries, r.DroppedEntries, r.Headers, r.Params, r.BodyFields, r.StrippedBodies)
}

// Sanitize reads a Burp XML export, redacts it according to cfg and writes
// it back as a Burp XML export. Response bodies are stored decoded, with
// their transfer and content coding headers removed, so later stages and
// reviewers read plain text.
func Sanitize(r io.Reader, w io.Writer, cfg SanitizeConfig) (SanitizeReport, error) {
	exp, err := readExport(r)
	if err != nil {
		return SanitizeReport{}, err
	}
	s := sanitizer{cfg: cfg}
	kept := make([]item, 0, len(exp.items))
	for i, it := range exp.items {
		e, err := convert(i, it)
		if err != nil {
			return s.report, fmt.Errorf("item %d: %w", i, err)
		}
		if slices.Contains(cfg.DropClasses, e.Class) {
			s.report.DroppedEntries++
			continue
		}
		s.item(&it, &e, slices.Contains(cfg.StripBodies, e.Class))
		s.report.Entries++
		kept = append(kept, it)
	}
	exp.items = kept
	return s.report, exp.write(w)
}

type sanitizer struct {
	cfg    SanitizeConfig
	report SanitizeReport
}

const strippedComment = "response body removed by webshadow sanitize"

func (s *sanitizer) item(it *item, e *Entry, stripBody bool) {
	it.URL.Text = s.url(it.URL.Text)
	it.Path.Text = s.url(it.Path.Text)

	req := splitMessage(mustRaw(it.Request))
	if method, target, ok := strings.Cut(req.start, " "); ok {
		if target, proto, ok := strings.Cut(target, " "); ok {
			req.start = method + " " + s.url(target) + " " + proto
		}
	}
	s.headers(req.headers)
	if len(req.body) > 0 {
		req.body = []byte(s.body(string(req.body), e.RequestMIME))
		setLength(req.headers, len(req.body))
	}
	it.Request = encode(req.bytes())

	rawResp := mustRaw(it.Response)
	if len(rawResp) == 0 {
		return
	}
	resp := splitMessage(rawResp)
	s.headers(resp.headers)
	if decodable(resp.headers) {
		resp.body = e.ResponseBody
		resp.headers = without(resp.headers, "Content-Encoding", "Transfer-Encoding")
		switch {
		case len(resp.body) == 0:
		case stripBody:
			resp.body = nil
			it.Comment = strippedComment
			s.report.StrippedBodies++
		case utf8.Valid(resp.body):
			resp.body = []byte(s.body(string(resp.body), e.ResponseMIME))
		}
		setLength(resp.headers, len(resp.body))
	}
	raw := resp.bytes()
	it.Response = encode(raw)
	it.ResponseLength = fmt.Sprint(len(raw))
}

// mustRaw decodes a message that convert has already decoded once.
func mustRaw(m message) []byte {
	raw, _ := m.raw()
	return raw
}

func encode(raw []byte) message {
	return message{Base64: true, Data: base64.StdEncoding.EncodeToString(raw)}
}

func without(hs []Header, names ...string) []Header {
	out := hs[:0:0]
	for _, h := range hs {
		drop := false
		for _, n := range names {
			if strings.EqualFold(h.Name, n) {
				drop = true
			}
		}
		if !drop {
			out = append(out, h)
		}
	}
	return out
}

// setLength updates a Content-Length header, if there is one, after the
// body changed.
func setLength(hs []Header, n int) {
	for i := range hs {
		if strings.EqualFold(hs[i].Name, "Content-Length") {
			hs[i].Value = fmt.Sprint(n)
		}
	}
}

func (s *sanitizer) headers(hs []Header) {
	for i, h := range hs {
		switch {
		case matches(h.Name, s.cfg.Headers, s.cfg.HeadersContains):
			if h.Value != Redacted {
				hs[i].Value = Redacted
				s.report.Headers++
			}
		case strings.EqualFold(h.Name, "referer") || strings.EqualFold(h.Name, "location"):
			// These carry URLs whose query strings need the same redaction.
			hs[i].Value = s.url(h.Value)
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
// cookie and auth headers whose value is not the redaction placeholder.
// An empty result means the trace passes the benchmark's sanitization check.
func Unsanitized(t *Trace) []string {
	var found []string
	check := func(seq int, where string, hs []Header) {
		for _, h := range hs {
			switch strings.ToLower(h.Name) {
			case "cookie", "set-cookie", "authorization", "proxy-authorization":
				if h.Value != Redacted && h.Value != "" {
					found = append(found, fmt.Sprintf("item %d: %s header %s", seq, where, h.Name))
				}
			}
		}
	}
	for _, e := range t.Entries {
		check(e.Sequence, "request", e.RequestHeaders)
		check(e.Sequence, "response", e.ResponseHeaders)
	}
	return found
}
