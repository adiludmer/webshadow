// Package score compares a reader's answer with a scenario's expected
// answer.
package score

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

// Field is the verdict on one required field.
type Field struct {
	Name     string `json:"name"`
	Expected any    `json:"expected"`
	Got      any    `json:"got"`
	Match    bool   `json:"match"`
	// Reason explains a mismatch.
	Reason string `json:"reason,omitempty"`
}

// Result is the verdict on a whole answer.
type Result struct {
	Pass   bool    `json:"pass"`
	Fields []Field `json:"fields"`
	// Extra lists answer fields beyond the required ones, which fail the
	// answer when the scenario disallows them.
	Extra []string `json:"extra,omitempty"`
}

// DecodeJSON parses JSON keeping numbers as json.Number, which is what
// Fields expects.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

// Fields scores answer against expected under the scenario's evaluation:
// every required field must match after its normalizers, and extra fields
// fail the answer only when allow_extra_fields is false. Values should come
// from DecodeJSON.
func Fields(expected, answer map[string]any, eval scenario.Evaluation) Result {
	res := Result{Pass: true}
	required := map[string]bool{}
	for _, name := range eval.RequiredFields {
		required[name] = true
		f := Field{Name: name, Expected: expected[name]}
		got, ok := answer[name]
		switch {
		case !ok:
			f.Reason = "missing"
		default:
			f.Got = got
			f.Match, f.Reason = equal(expected[name], got, eval.Normalizers[name])
		}
		if !f.Match {
			res.Pass = false
		}
		res.Fields = append(res.Fields, f)
	}
	for name := range answer {
		if !required[name] {
			res.Extra = append(res.Extra, name)
		}
	}
	sort.Strings(res.Extra)
	if len(res.Extra) > 0 && !eval.AllowsExtraFields() {
		res.Pass = false
	}
	return res
}

// equal compares one value under a normalizer and explains a mismatch.
func equal(want, got any, n scenario.Normalizer) (bool, string) {
	switch w := want.(type) {
	case json.Number, float64:
		wf, _ := toFloat(w, false)
		gf, ok := toFloat(got, n.Currency)
		if !ok {
			return false, fmt.Sprintf("not a number: %v", got)
		}
		tol := 0.0
		if n.NumericTolerance != nil {
			tol = *n.NumericTolerance
		}
		if math.Abs(wf-gf) <= tol+1e-9*math.Max(1, math.Abs(wf)) {
			return true, ""
		}
		return false, fmt.Sprintf("%v != %v", formatFloat(gf), formatFloat(wf))
	case string:
		gs, ok := got.(string)
		if !ok {
			if num, isNum := got.(json.Number); isNum {
				gs = num.String()
			} else {
				return false, fmt.Sprintf("not a string: %v", got)
			}
		}
		if n.Date != "" {
			return equalDate(w, gs, n.Date)
		}
		if n.URL {
			if normURL(w) == normURL(gs) {
				return true, ""
			}
			return false, fmt.Sprintf("%q != %q", gs, w)
		}
		if normString(w, n) == normString(gs, n) {
			return true, ""
		}
		return false, fmt.Sprintf("%q != %q", gs, w)
	case bool:
		gb, ok := got.(bool)
		if !ok {
			if s, isStr := got.(string); isStr {
				gb, ok = parseBool(s)
			}
		}
		if !ok {
			return false, fmt.Sprintf("not a boolean: %v", got)
		}
		if gb == w {
			return true, ""
		}
		return false, fmt.Sprintf("%v != %v", gb, w)
	case []any:
		ga, ok := got.([]any)
		if !ok {
			return false, fmt.Sprintf("not a list: %v", got)
		}
		if len(ga) != len(w) {
			return false, fmt.Sprintf("%d items, want %d", len(ga), len(w))
		}
		if n.Unordered {
			return equalUnordered(w, ga, n)
		}
		for i := range w {
			if ok, why := equal(w[i], ga[i], n); !ok {
				return false, fmt.Sprintf("item %d: %s", i, why)
			}
		}
		return true, ""
	case nil:
		if got == nil {
			return true, ""
		}
		return false, fmt.Sprintf("%v != null", got)
	default:
		// Objects compare structurally, with numbers compared by value.
		if reflect.DeepEqual(canonical(want), canonical(got)) {
			return true, ""
		}
		return false, "objects differ"
	}
}

func equalUnordered(want, got []any, n scenario.Normalizer) (bool, string) {
	used := make([]bool, len(got))
	for i, w := range want {
		found := false
		for j, g := range got {
			if used[j] {
				continue
			}
			if ok, _ := equal(w, g, n); ok {
				used[j], found = true, true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("no match for item %d (%v)", i, w)
		}
	}
	return true, ""
}

// toFloat reads a number from a JSON number or a numeric string. With
// currency set, symbols, codes, spaces and thousands separators are
// stripped from strings first, so "$15,000,000" reads as 15000000.
func toFloat(v any, currency bool) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case string:
		s := strings.TrimSpace(x)
		if currency {
			s = strings.Map(func(r rune) rune {
				switch {
				case r >= '0' && r <= '9', r == '.', r == '-':
					return r
				default:
					return -1
				}
			}, s)
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func normString(s string, n scenario.Normalizer) string {
	switch n.Whitespace {
	case "trim":
		s = strings.TrimSpace(s)
	case "collapse":
		s = strings.Join(strings.Fields(s), " ")
	}
	switch n.Case {
	case "lower":
		s = strings.ToLower(s)
	case "upper":
		s = strings.ToUpper(s)
	}
	return s
}

// normURL lowercases scheme and host and drops a trailing slash and a
// fragment.
func normURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// Layouts tried for an answer date that does not match the scenario's.
var answerDateLayouts = []string{time.RFC3339, "2006-01-02", "2006/01/02", "January 2, 2006", "Jan 2, 2006", "2 January 2006", "02.01.2006"}

func equalDate(want, got, layout string) (bool, string) {
	w, err := time.Parse(layout, strings.TrimSpace(want))
	if err != nil {
		return false, fmt.Sprintf("expected date %q does not match layout %q", want, layout)
	}
	for _, l := range append([]string{layout}, answerDateLayouts...) {
		if g, err := time.Parse(l, strings.TrimSpace(got)); err == nil {
			if g.Year() == w.Year() && g.YearDay() == w.YearDay() {
				return true, ""
			}
			return false, fmt.Sprintf("%s != %s", g.Format("2006-01-02"), w.Format("2006-01-02"))
		}
	}
	return false, fmt.Sprintf("not a date: %q", got)
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes":
		return true, true
	case "false", "no":
		return false, true
	}
	return false, false
}

// canonical turns json.Numbers into float64 so equal numbers compare equal
// however they were written.
func canonical(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = canonical(x[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = canonical(e)
		}
		return out
	}
	return v
}
