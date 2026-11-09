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

// A type error mid-array keeps what was decoded before it and still yields
// the later elements (encoding/json behaviour the libraryFiles/audioFiles
// columns rely on).
func TestDecodeJSONSliceKeepsValuesAroundATypeError(t *testing.T) {
	if got, want := DecodeJSONSlice[string]([]byte(`["one",2,"three"]`)), []string{"one", "", "three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("strings = %#v, want %#v", got, want)
	}
	got := DecodeJSONSlice[map[string]any]([]byte(`[{"first":"kept"},4,{"last":"kept"}]`))
	want := []map[string]any{{"first": "kept"}, nil, {"last": "kept"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("maps = %#v, want %#v", got, want)
	}
}

func TestStringPtrValueKeepsNilAsNull(t *testing.T) {
	if got := StringPtrValue(nil); got != nil {
		t.Errorf("StringPtrValue(nil) = %#v, want untyped nil (bound as SQL NULL)", got)
	}
	s := "x"
	if got := StringPtrValue(&s); got != "x" {
		t.Errorf("StringPtrValue(&x) = %#v, want x", got)
	}
	empty := ""
	if got := StringPtrValue(&empty); got != "" {
		t.Errorf("StringPtrValue(&empty) = %#v, want empty string kept", got)
	}
}
