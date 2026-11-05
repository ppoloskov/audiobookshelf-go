package jsonvalue

import (
	"encoding/json"
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
