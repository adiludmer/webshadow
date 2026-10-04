package score

import (
	"strings"
	"testing"

	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	v, err := DecodeJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func ptr[T any](v T) *T { return &v }

func TestEqual(t *testing.T) {
	tests := []struct {
		name, want, got string
		norm            scenario.Normalizer
		match           bool
		reason          string
	}{
		{"same number", `15000000`, `15000000`, scenario.Normalizer{}, true, ""},
		{"number written differently", `15000000`, `1.5e7`, scenario.Normalizer{}, true, ""},
		{"numeric string", `15000000`, `"15000000"`, scenario.Normalizer{}, true, ""},
		{"different number", `15000000`, `1500000`, scenario.Normalizer{}, false, "1500000 != 15000000"},
		{"currency string without normalizer", `15000000`, `"$15,000,000"`, scenario.Normalizer{}, false, "not a number"},
		{"currency string", `15000000`, `"$15,000,000"`, scenario.Normalizer{Currency: true}, true, ""},
		{"currency with code", `1299`, `"USD 1,299.00"`, scenario.Normalizer{Currency: true}, true, ""},
		{"within tolerance", `1299`, `1299.004`, scenario.Normalizer{NumericTolerance: ptr(0.01)}, true, ""},
		{"outside tolerance", `1299`, `1299.5`, scenario.Normalizer{NumericTolerance: ptr(0.01)}, false, "1299.5 != 1299"},
		{"exact string", `"USD"`, `"USD"`, scenario.Normalizer{}, true, ""},
		{"case differs", `"USD"`, `"usd"`, scenario.Normalizer{}, false, `"usd" != "USD"`},
		{"case folded", `"USD"`, `" usd "`, scenario.Normalizer{Case: "upper", Whitespace: "trim"}, true, ""},
		{"whitespace collapsed", `"Zen Book 14"`, `" Zen   Book 14"`, scenario.Normalizer{Whitespace: "collapse"}, true, ""},
		{"number as string id", `"123"`, `123`, scenario.Normalizer{}, true, ""},
		{"bool", `true`, `true`, scenario.Normalizer{}, true, ""},
		{"bool from string", `true`, `"Yes"`, scenario.Normalizer{}, true, ""},
		{"wrong bool", `true`, `false`, scenario.Normalizer{}, false, "false != true"},
		{"not a bool", `true`, `1`, scenario.Normalizer{}, false, "not a boolean"},
		{"url", `"https://shop.test/p/123"`, `"HTTPS://Shop.test/p/123/#top"`, scenario.Normalizer{URL: true}, true, ""},
		{"date", `"2026-03-05"`, `"March 5, 2026"`, scenario.Normalizer{Date: "2006-01-02"}, true, ""},
		{"wrong date", `"2026-03-05"`, `"2026-03-06"`, scenario.Normalizer{Date: "2006-01-02"}, false, "2026-03-06 != 2026-03-05"},
		{"not a date", `"2026-03-05"`, `"soon"`, scenario.Normalizer{Date: "2006-01-02"}, false, "not a date"},
		{"ordered list", `["a", "b"]`, `["a", "b"]`, scenario.Normalizer{}, true, ""},
		{"list out of order", `["a", "b"]`, `["b", "a"]`, scenario.Normalizer{}, false, "item 0"},
		{"unordered list", `["a", "b", "a"]`, `["a", "a", "b"]`, scenario.Normalizer{Unordered: true}, true, ""},
		{"unordered mismatch", `["a", "b", "a"]`, `["a", "b", "b"]`, scenario.Normalizer{Unordered: true}, false, "no match for item 2"},
		{"list length", `["a"]`, `["a", "b"]`, scenario.Normalizer{}, false, "2 items, want 1"},
		{"object", `{"w": 14, "ram": [32]}`, `{"ram": [32.0], "w": 14}`, scenario.Normalizer{}, true, ""},
		{"object differs", `{"w": 14}`, `{"w": 15}`, scenario.Normalizer{}, false, "objects differ"},
		{"null", `null`, `null`, scenario.Normalizer{}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, _ := DecodeJSON([]byte(tt.want))
			got, err := DecodeJSON([]byte(tt.got))
			if err != nil {
				t.Fatal(err)
			}
			match, reason := equal(want, got, tt.norm)
			if match != tt.match || !strings.Contains(reason, tt.reason) {
				t.Errorf("equal = %v, %q; want %v, %q", match, reason, tt.match, tt.reason)
			}
		})
	}
}

func TestFields(t *testing.T) {
	eval := scenario.Evaluation{
		Type:           "fields",
		RequiredFields: []string{"amount", "currency"},
		Normalizers:    map[string]scenario.Normalizer{"currency": {Case: "upper"}},
	}
	expected := obj(t, `{"amount": 15000000, "currency": "USD", "note": "ignored"}`)

	res := Fields(expected, obj(t, `{"amount": 15000000, "currency": "usd", "source": "feed"}`), eval)
	if !res.Pass || len(res.Fields) != 2 || strings.Join(res.Extra, ",") != "source" {
		t.Errorf("pass with extra: %+v", res)
	}

	res = Fields(expected, obj(t, `{"currency": "EUR"}`), eval)
	if res.Pass || res.Fields[0].Reason != "missing" || res.Fields[1].Match {
		t.Errorf("missing and wrong: %+v", res)
	}

	strict := eval
	strict.AllowExtraFields = ptr(false)
	res = Fields(expected, obj(t, `{"amount": 15000000, "currency": "USD", "source": "feed"}`), strict)
	if res.Pass {
		t.Errorf("extra field should fail when disallowed: %+v", res)
	}
}

func TestDecodeJSONRejectsTrailingData(t *testing.T) {
	if _, err := DecodeJSON([]byte(`{"a": 1} {"b": 2}`)); err == nil {
		t.Error("trailing value accepted")
	}
}
