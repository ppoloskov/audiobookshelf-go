package jsonvalue

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestString(t *testing.T) {
	for _, test := range []struct {
		name string
		in   any
		want string
	}{
		{name: "string", in: "value", want: "value"},
		{name: "empty string", in: "", want: ""},
		{name: "nil", in: nil, want: ""},
		{name: "zero", in: 0, want: ""},
		{name: "nonstring", in: true, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := String(test.in); got != test.want {
				t.Fatalf("String(%#v) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestTruthy(t *testing.T) {
	for _, test := range []struct {
		name string
		in   any
		want bool
	}{
		{name: "nil", in: nil, want: false},
		{name: "false", in: false, want: false},
		{name: "true", in: true, want: true},
		{name: "empty string", in: "", want: false},
		{name: "string", in: "0", want: true},
		{name: "float zero", in: float64(0), want: false},
		{name: "float nonzero", in: float64(1), want: true},
		{name: "int zero", in: 0, want: false},
		{name: "int64 zero", in: int64(0), want: false},
		{name: "json number zero", in: json.Number("0"), want: true},
		{name: "other value", in: []any{}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Truthy(test.in); got != test.want {
				t.Fatalf("Truthy(%#v) = %t, want %t", test.in, got, test.want)
			}
		})
	}
}

func TestStrings(t *testing.T) {
	for _, test := range []struct {
		name string
		in   any
		want []string
	}{
		{name: "decoded array", in: []any{"one", 2, "", false}, want: []string{"one", ""}},
		{name: "typed strings", in: []string{"one", ""}, want: []string{"one", ""}},
		{name: "other type", in: "one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Strings(test.in); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Strings(%#v) = %#v, want %#v", test.in, got, test.want)
			}
		})
	}
}

func TestArray(t *testing.T) {
	if got := Array([]any{1, "a"}); len(got) != 2 {
		t.Fatalf("Array(decoded array) = %#v", got)
	}
	for _, in := range []any{nil, "a", []string{"a"}, map[string]any{}} {
		if Array(in) != nil {
			t.Errorf("Array(%#v) must be nil", in)
		}
	}
}

func TestNumberAndFloat(t *testing.T) {
	for _, test := range []struct {
		name string
		in   any
		want float64
		ok   bool
	}{
		{"float64", 1.5, 1.5, true},
		{"json number", json.Number("12345678901"), 12345678901, true},
		{"int", 3, 3, true},
		{"int64", int64(-4), -4, true},
		{"bad json number", json.Number("abc"), 0, false},
		{"NaN", math.NaN(), 0, false},
		{"infinity", math.Inf(1), 0, false},
		{"numeric string is not a number", "5", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Number(test.in)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("Number(%#v) = %v, %v; want %v, %v", test.in, got, ok, test.want, test.ok)
			}
			if f := Float(test.in); f != test.want {
				t.Fatalf("Float(%#v) = %v, want %v", test.in, f, test.want)
			}
		})
	}
}

// Text is JavaScript's template-literal rendering: `${value}`.
func TestTextMatchesJavaScriptTemplateLiterals(t *testing.T) {
	for _, test := range []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{"plain", "plain"},
		{true, "true"},
		{false, "false"},
		{float64(5), "5"},
		{1.5, "1.5"},
		{[]any{float64(1), nil, "a"}, "1,,a"},
		{[]any{[]any{float64(1), float64(2)}, float64(3)}, "1,2,3"},
		{map[string]any{"a": 1}, "[object Object]"},
	} {
		if got := Text(test.in); got != test.want {
			t.Errorf("Text(%#v) = %q, want %q", test.in, got, test.want)
		}
	}
}
