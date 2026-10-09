// Package privacy replaces secret values in clustering output with stable
// equality tags before anything in semantic discovery reads it.
//
// A value is secret when the recording carried it somewhere credentials
// travel: a cookie, a Set-Cookie header, or a header, query key or body
// field whose name says auth, token, session, csrf and the like. Every
// occurrence of that exact value anywhere in the output then becomes the
// same tag, such as {{redacted:cookie.session-id:3f9a1c2b7d4e}}, so equality
// and flow between families survive while the value itself does not. JSON
// web tokens and email addresses are tagged wherever they appear.
//
// Tags are keyed by the scope a Redactor is built for, the clustering
// result id, so the same value in two results gets two unrelated tags.
package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// MinSecretLen is the shortest value treated as secret. Shorter values at
// secret locations, such as a currency or a locale cookie, carry no
// credential and are kept so they stay readable.
const MinSecretLen = 8

// minSubstringLen is the shortest secret also replaced inside longer
// strings, such as a URL or a flow example. Shorter ones are replaced only
// where they are the whole string, so a short value cannot clobber
// unrelated text.
const minSubstringLen = 16

var (
	tagPattern   = regexp.MustCompile(`^\{\{redacted:[^{}]+\}\}$`)
	jwtPattern   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
)

// sensitiveNames are substrings that mark a header, query key or body field
// as one that carries credentials or session state.
var sensitiveNames = []string{
	"auth", "token", "csrf", "xsrf", "session", "secret", "password", "passwd",
	"apikey", "api-key", "api_key", "signature", "credential", "cookie",
}

// IsTag reports whether s is a tag this package produced.
func IsTag(s string) bool { return tagPattern.MatchString(s) }

// Stats counts what a Redactor found and replaced. It never holds values.
type Stats struct {
	// Secrets counts the distinct secret values, by the kind of location
	// they were first found at.
	Secrets map[string]int `json:"secrets"`
	// Replaced counts the strings that were rewritten.
	Replaced int `json:"replaced"`
}

// Redactor rewrites values. Build one per clustering result with New.
type Redactor struct {
	key      []byte
	secrets  map[string]string // value -> tag
	replacer *strings.Replacer
	stats    Stats
}

// New builds a Redactor for one clustering result. links and sequences are
// the decoded links.json and sequences.json documents, as generic JSON;
// they are where secret values are found, by the locations they occurred
// at.
func New(scope string, links, sequences any) *Redactor {
	sum := sha256.Sum256([]byte("webshadow/privacy/v1\x00" + scope))
	r := &Redactor{key: sum[:], secrets: map[string]string{}, stats: Stats{Secrets: map[string]int{}}}
	labels := map[string]string{} // value -> smallest secret location
	note := func(value, label string) {
		if len(value) < MinSecretLen || IsTag(value) {
			return
		}
		if cur, ok := labels[value]; !ok || label < cur {
			labels[value] = label
		}
	}
	for _, link := range list(links) {
		m := obj(link)
		value := str(m["value"])
		for _, occ := range list(m["occurrences"]) {
			if label, ok := secretLocation(obj(obj(occ)["location"])); ok {
				note(value, label)
			}
		}
	}
	for _, edge := range list(sequences) {
		for _, flow := range list(obj(edge)["value_flows"]) {
			f := obj(flow)
			fromLabel, fromSecret := secretLocation(obj(f["from"]))
			toLabel, toSecret := secretLocation(obj(f["to"]))
			label := ""
			switch {
			case fromSecret:
				label = fromLabel
			case toSecret:
				label = toLabel
			case str(f["carrier"]) == "cookie":
				label = "cookie"
			default:
				continue
			}
			for _, ex := range list(f["examples"]) {
				note(str(obj(ex)["value"]), label)
			}
		}
	}
	values := make([]string, 0, len(labels))
	for v := range labels {
		values = append(values, v)
	}
	sort.Strings(values)
	var pairs []string
	for _, v := range values {
		label := labels[v]
		r.secrets[v] = r.tag(label, v)
		kind, _, _ := strings.Cut(label, ".")
		r.stats.Secrets[kind]++
		if len(v) >= minSubstringLen {
			pairs = append(pairs, v, r.secrets[v])
		}
	}
	if len(pairs) > 0 {
		r.replacer = strings.NewReplacer(pairs...)
	}
	return r
}

// tag renders the equality tag for a value.
func (r *Redactor) tag(label, value string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(value))
	return "{{redacted:" + label + ":" + hex.EncodeToString(mac.Sum(nil)[:6]) + "}}"
}

// Secrets returns how many distinct secret values the Redactor knows.
func (r *Redactor) Secrets() int { return len(r.secrets) }

// Stats returns what the Redactor has found and replaced so far.
func (r *Redactor) Stats() Stats {
	s := Stats{Secrets: map[string]int{}, Replaced: r.stats.Replaced}
	for k, n := range r.stats.Secrets {
		s.Secrets[k] = n
	}
	return s
}

// Rewrite returns v, a decoded generic JSON document, with every secret
// value replaced by its tag, in object keys as well as values. It rewrites
// maps and slices in place.
func (r *Redactor) Rewrite(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case []any:
		for i := range t {
			t[i] = r.Rewrite(t[i])
		}
		return t
	case map[string]any:
		for k, val := range t {
			nk := r.String(k)
			if nk != k {
				delete(t, k)
			}
			t[nk] = r.Rewrite(val)
		}
		return t
	default:
		return v
	}
}

// String redacts one string.
func (r *Redactor) String(s string) string {
	if s == "" || IsTag(s) {
		return s
	}
	out := s
	if tag, ok := r.secrets[s]; ok {
		out = tag
	} else {
		if r.replacer != nil && len(s) >= minSubstringLen {
			out = r.replacer.Replace(out)
		}
		out = jwtPattern.ReplaceAllStringFunc(out, func(m string) string {
			r.countKind(m, "jwt")
			return r.tag("jwt", m)
		})
		out = emailPattern.ReplaceAllStringFunc(out, func(m string) string {
			r.countKind(m, "email")
			return r.tag("email", m)
		})
	}
	if out != s {
		r.stats.Replaced++
	}
	return out
}

// countKind counts a pattern match as a secret the first time it is seen.
func (r *Redactor) countKind(value, kind string) {
	if _, ok := r.secrets[value]; ok {
		return
	}
	r.secrets[value] = r.tag(kind, value)
	r.stats.Secrets[kind]++
}

// secretLocation reports whether a clustering location is one that carries
// credentials, and the label its tag uses, such as "cookie.session-id" or
// "header.Authorization".
func secretLocation(loc map[string]any) (string, bool) {
	part := str(loc["part"])
	name := str(loc["pattern"])
	if name == "" {
		name = str(loc["path"])
	}
	label := part
	if name != "" {
		label += "." + name
	}
	switch part {
	case "cookie", "set-cookie":
		return label, true
	case "header", "query", "body":
		lower := strings.ToLower(name)
		for _, s := range sensitiveNames {
			if strings.Contains(lower, s) {
				return label, true
			}
		}
	}
	return "", false
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
