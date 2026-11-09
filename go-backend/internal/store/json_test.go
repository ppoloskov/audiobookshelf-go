package store

import (
	"reflect"
	"testing"
)

// Persisted JSON columns are written by upstream and by hand; decoding never
// fails and always yields a non-nil, mutable value.
func TestDecodeJSONSliceAndObject(t *testing.T) {
	for _, raw := range []string{"", "null", " null\n", "[", `"text"`} {
		got := DecodeJSONSlice[string]([]byte(raw))
		if got == nil || len(got) != 0 {
			t.Errorf("DecodeJSONSlice(%q) = %#v, want empty non-nil", raw, got)
		}
	}
	if got := DecodeJSONSlice[string]([]byte(` ["one","two"] `)); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Errorf("DecodeJSONSlice = %#v", got)
	}
	for _, raw := range []string{"", "null", " \nnull\t", "{"} {
		got := DecodeJSONObject([]byte(raw))
		if got == nil || len(got) != 0 {
			t.Errorf("DecodeJSONObject(%q) = %#v, want empty non-nil", raw, got)
		}
		got["mutable"] = true
	}
	want := map[string]any{"name": "Ada", "active": true}
	if got := DecodeJSONObject([]byte(` {"name":"Ada","active":true} `)); !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeJSONObject = %#v", got)
	}
}
