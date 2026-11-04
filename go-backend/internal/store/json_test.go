package store

import (
	"reflect"
	"testing"
)

func TestDecodeJSONSlice(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "strings", raw: ` ["one","two"] `, want: []string{"one", "two"}},
		{name: "empty", want: []string{}},
		{name: "null", raw: "null", want: []string{}},
		{name: "whitespace null", raw: "  null \n", want: []string{}},
		{name: "malformed", raw: "[", want: []string{}},
		{name: "partial type error", raw: `["one",2,"three"]`, want: []string{"one", "", "three"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DecodeJSONSlice[string]([]byte(test.raw))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("DecodeJSONSlice = %#v, want %#v", got, test.want)
			}
			if got == nil {
				t.Fatal("DecodeJSONSlice returned nil slice")
			}
		})
	}
}

func TestDecodeJSONSliceMaps(t *testing.T) {
	got := DecodeJSONSlice[map[string]any]([]byte(`[{"first":"kept"},4,{"last":"kept"}]`))
	want := []map[string]any{{"first": "kept"}, nil, {"last": "kept"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeJSONSlice maps = %#v, want %#v", got, want)
	}
}

func TestDecodeJSONObject(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{name: "object", raw: ` {"name":"Ada","active":true} `, want: map[string]any{"name": "Ada", "active": true}},
		{name: "empty", want: map[string]any{}},
		{name: "null", raw: "null", want: map[string]any{}},
		{name: "whitespace null", raw: " \nnull\t", want: map[string]any{}},
		{name: "malformed", raw: "{", want: map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DecodeJSONObject([]byte(test.raw))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("DecodeJSONObject = %#v, want %#v", got, test.want)
			}
			if got == nil {
				t.Fatal("DecodeJSONObject returned nil map")
			}
		})
	}

	got := DecodeJSONObject(nil)
	got["mutable"] = true
	if got["mutable"] != true {
		t.Fatal("empty JSONObject map is not mutable")
	}
}
