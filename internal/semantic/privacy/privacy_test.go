package privacy

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

const links = `[
  {"value": "133-9148859-8608158-long", "occurrences": [
    {"family_id": "a", "location": {"side": "request", "part": "cookie", "pattern": "session-id"}},
    {"family_id": "b", "location": {"side": "request", "part": "query", "pattern": "sid"}}]},
  {"value": "en_US", "occurrences": [
    {"family_id": "a", "location": {"side": "request", "part": "cookie", "pattern": "lc-main"}}]},
  {"value": "Bearer abcdefgh12345678", "occurrences": [
    {"family_id": "a", "location": {"side": "request", "part": "header", "pattern": "Authorization"}}]},
  {"value": "B0H46QQ5KM-asin", "occurrences": [
    {"family_id": "a", "location": {"side": "response", "part": "text"}},
    {"family_id": "b", "location": {"side": "request", "part": "path", "pattern": "2"}}]}
]`

const sequences = `[
  {"id": "se_1", "value_flows": [
    {"carrier": "cookie", "from": {"side": "response", "part": "set-cookie", "pattern": "ubid-main"},
     "to": {"side": "request", "part": "cookie", "pattern": "ubid-main"},
     "examples": [{"value": "ubid-value-0001"}]}]}
]`

func TestRewriteTagsSecretsEverywhere(t *testing.T) {
	r := New("cl_1", decode(t, links), decode(t, sequences))
	doc := decode(t, `{
	  "a": "133-9148859-8608158-long",
	  "url": "https://shop.test/x?sid=133-9148859-8608158-long&k=tv",
	  "b": ["en_US", "B0H46QQ5KM-asin", "Bearer abcdefgh12345678", "ubid-value-0001"],
	  "jwt": "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	  "mail": "write to someone@example.com today",
	  "ubid-value-0001": 1
	}`)
	out := r.Rewrite(doc).(map[string]any)
	data, _ := json.Marshal(out)
	s := string(data)
	for _, leaked := range []string{"133-9148859", "abcdefgh12345678", "ubid-value-0001", "eyJhbGci", "someone@example.com"} {
		if strings.Contains(s, leaked) {
			t.Errorf("%q survived: %s", leaked, s)
		}
	}
	for _, kept := range []string{"en_US", "B0H46QQ5KM-asin", "k=tv", "write to "} {
		if !strings.Contains(s, kept) {
			t.Errorf("%q was redacted: %s", kept, s)
		}
	}
	tag := out["a"].(string)
	if !strings.HasPrefix(tag, "{{redacted:cookie.session-id:") || !IsTag(tag) {
		t.Errorf("tag = %q", tag)
	}
	if !strings.Contains(out["url"].(string), "sid="+tag+"&") {
		t.Errorf("substring not replaced with the same tag: %q", out["url"])
	}
	b := out["b"].([]any)
	if !strings.HasPrefix(b[2].(string), "{{redacted:header.Authorization:") {
		t.Errorf("authorization = %q", b[2])
	}
	if !strings.HasPrefix(b[3].(string), "{{redacted:set-cookie.ubid-main:") {
		t.Errorf("flowed cookie = %q", b[3])
	}
	st := r.Stats()
	if st.Secrets["cookie"] != 1 || st.Secrets["header"] != 1 || st.Secrets["set-cookie"] != 1 || st.Secrets["jwt"] != 1 || st.Secrets["email"] != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestTagsAreStablePerScopeAndIdempotent(t *testing.T) {
	a := New("cl_1", decode(t, links), nil)
	b := New("cl_1", decode(t, links), nil)
	c := New("cl_2", decode(t, links), nil)
	v := "133-9148859-8608158-long"
	if a.String(v) != b.String(v) {
		t.Error("same scope gave different tags")
	}
	if a.String(v) == c.String(v) {
		t.Error("different scopes gave the same tag")
	}
	tagged := a.String(v)
	// A redacted document read again is left alone.
	again := New("cl_1", decode(t, `[{"value": "`+tagged+`", "occurrences": [{"location": {"part": "cookie", "pattern": "session-id"}}]}]`), nil)
	if again.Secrets() != 0 || again.String(tagged) != tagged {
		t.Error("a tag was treated as a secret")
	}
}

func TestSecretLocation(t *testing.T) {
	for _, tc := range []struct {
		part, pattern string
		want          bool
	}{
		{"cookie", "anything", true},
		{"set-cookie", "x", true},
		{"header", "X-Csrf-Token", true},
		{"header", "Accept-Language", false},
		{"query", "session_id", true},
		{"query", "k", false},
		{"body", "items[].authToken", true},
		{"path", "session", false},
		{"text", "", false},
	} {
		_, got := secretLocation(map[string]any{"part": tc.part, "pattern": tc.pattern})
		if got != tc.want {
			t.Errorf("%s.%s: got %v", tc.part, tc.pattern, got)
		}
	}
}
