package shape

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// decode parses JSON the way the parity harness does (generic values).
func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func inferJSON(t *testing.T, samples ...string) *Skeleton {
	t.Helper()
	vals := make([]any, 0, len(samples))
	for _, s := range samples {
		vals = append(vals, decode(t, s))
	}
	return Infer(vals...)
}

func TestInferKinds(t *testing.T) {
	tests := []struct {
		name    string
		samples []string
		want    Kind
	}{
		{"object", []string{`{"a":1}`}, KindObject},
		{"array", []string{`[1]`}, KindArray},
		{"string", []string{`"x"`}, KindString},
		{"number", []string{`2.5`}, KindNumber},
		{"bool", []string{`true`}, KindBool},
		{"only null", []string{`null`, `null`}, KindNull},
		{"string and number conflict", []string{`"x"`, `1`}, KindAny},
		{"object and array conflict", []string{`{}`, `[]`}, KindAny},
		{"null mixed at top level", []string{`null`, `{}`}, KindAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferJSON(t, tc.samples...).Kind; got != tc.want {
				t.Fatalf("kind = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInferNoSamples(t *testing.T) {
	if s := Infer(); s != nil {
		t.Fatalf("Infer() = %+v, want nil", s)
	}
}

func TestInferOptionalKey(t *testing.T) {
	s := inferJSON(t, `{"id":"a","note":"x"}`, `{"id":"b"}`)
	if f := s.Keys["id"]; f.Optional {
		t.Fatalf("id is in every sample, got optional")
	}
	note := s.Keys["note"]
	if note == nil || !note.Optional || note.Nullable || note.Kind != KindString {
		t.Fatalf("note = %+v, want optional non-nullable string", note)
	}
}

func TestInferNullableKey(t *testing.T) {
	s := inferJSON(t, `{"expiresAt":null}`, `{"expiresAt":"2030-01-01"}`)
	f := s.Keys["expiresAt"]
	if !f.Nullable || f.Optional || f.Kind != KindString {
		t.Fatalf("expiresAt = %+v, want nullable string", f)
	}
}

func TestInferNullableAndOptionalKey(t *testing.T) {
	s := inferJSON(t, `{"x":null}`, `{}`, `{"x":3}`)
	f := s.Keys["x"]
	if !f.Nullable || !f.Optional || f.Kind != KindNumber {
		t.Fatalf("x = %+v, want optional nullable number", f)
	}
}

func TestInferOnlyNullKey(t *testing.T) {
	s := inferJSON(t, `{"x":null}`, `{"x":null}`)
	f := s.Keys["x"]
	if f.Kind != KindNull || f.Nullable || f.Optional {
		t.Fatalf("x = %+v, want plain null", f)
	}
}

func TestInferConflictingKeyBecomesAny(t *testing.T) {
	s := inferJSON(t, `{"x":1}`, `{"x":"1"}`)
	if f := s.Keys["x"]; f.Kind != KindAny {
		t.Fatalf("x = %+v, want any", f)
	}
}

func TestInferEmptyArrayHasNoElem(t *testing.T) {
	s := inferJSON(t, `[]`, `[]`)
	if s.Kind != KindArray || s.Elem != nil {
		t.Fatalf("got %+v, want array with nil elem", s)
	}
}

func TestInferEmptyArrayAddsNoElementsToOthers(t *testing.T) {
	s := inferJSON(t, `[]`, `[{"id":1}]`)
	if s.Elem == nil || s.Elem.Kind != KindObject || s.Elem.Keys["id"] == nil {
		t.Fatalf("elem = %+v, want object with id", s.Elem)
	}
}

func TestInferNestedArrayOfObjects(t *testing.T) {
	s := inferJSON(t,
		`{"shelves":[{"id":"a","entities":[{"id":"1","title":"t"}]}]}`,
		`{"shelves":[{"id":"b","entities":[{"id":"2"}, {"id":"3","title":null}]}]}`,
	)
	shelves := s.Keys["shelves"]
	if shelves == nil || shelves.Kind != KindArray || shelves.Elem == nil {
		t.Fatalf("shelves = %+v", shelves)
	}
	entities := shelves.Elem.Keys["entities"]
	if entities == nil || entities.Elem == nil {
		t.Fatalf("entities = %+v", entities)
	}
	title := entities.Elem.Keys["title"]
	if title == nil || !title.Optional || !title.Nullable || title.Kind != KindString {
		t.Fatalf("title = %+v, want optional nullable string", title)
	}
	if id := entities.Elem.Keys["id"]; id == nil || id.Optional || id.Kind != KindString {
		t.Fatalf("id = %+v, want required string", id)
	}
}

func TestInferNullElementInArrayBecomesAny(t *testing.T) {
	s := inferJSON(t, `[null, {"id":1}]`)
	if s.Elem == nil || s.Elem.Kind != KindAny {
		t.Fatalf("elem = %+v, want any", s.Elem)
	}
}

func TestInferSkeletonRoundTripsJSON(t *testing.T) {
	s := inferJSON(t, `{"a":null,"b":[{"c":"x"}]}`, `{"b":[]}`)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Skeleton
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, s) {
		t.Fatalf("round trip changed skeleton:\n%s", raw)
	}
}

func TestConforms(t *testing.T) {
	skel := inferJSON(t,
		`{"id":"a","count":1,"ok":true,"expiresAt":null,"note":"x","items":[{"id":"1","title":"t"}],"nothing":null}`,
		`{"id":"b","count":2,"ok":false,"expiresAt":"2030","items":[{"id":"2"}],"nothing":null}`,
	)
	tests := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "same shape",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":"2031","note":"y","items":[{"id":"9","title":"u"}],"nothing":null}`,
		},
		{
			name: "optional key may be absent",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":[],"nothing":null}`,
		},
		{
			name: "extra keys allowed",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":[{"id":"9","extra":[1]}],"nothing":null,"new":{"deep":true}}`,
		},
		{
			name: "nullable accepts null",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":[],"nothing":null}`,
		},
		{
			name: "required key missing",
			json: `{"count":3,"ok":true,"expiresAt":null,"items":[],"nothing":null}`,
			want: []string{"$.id: missing required key"},
		},
		{
			name: "wrong type at top level key",
			json: `{"id":3,"count":3,"ok":true,"expiresAt":null,"items":[],"nothing":null}`,
			want: []string{"$.id: want string, got number"},
		},
		{
			name: "null where not nullable",
			json: `{"id":null,"count":3,"ok":true,"expiresAt":null,"items":[],"nothing":null}`,
			want: []string{"$.id: want string, got null"},
		},
		{
			name: "nested missing key reports array path",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":[{"id":"1"},{"title":"no id"}],"nothing":null}`,
			want: []string{"$.items[1].id: missing required key"},
		},
		{
			name: "null-only key rejects a value",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":[],"nothing":5}`,
			want: []string{"$.nothing: want null, got number"},
		},
		{
			name: "array given object",
			json: `{"id":"c","count":3,"ok":true,"expiresAt":null,"items":{},"nothing":null}`,
			want: []string{"$.items: want array, got object"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Conforms(skel, decode(t, tc.json))
			if !equalStrings(got, tc.want) {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConformsKindAnyAndNilAcceptEverything(t *testing.T) {
	anySkel := &Skeleton{Kind: KindAny}
	for _, v := range []any{nil, 1.0, "x", []any{}, map[string]any{}} {
		if got := Conforms(anySkel, v); len(got) != 0 {
			t.Fatalf("any rejected %v: %q", v, got)
		}
		if got := Conforms(nil, v); len(got) != 0 {
			t.Fatalf("nil skeleton rejected %v: %q", v, got)
		}
	}
}

func TestConformsEmptyArraySkeletonAcceptsElements(t *testing.T) {
	skel := inferJSON(t, `[]`)
	if got := Conforms(skel, decode(t, `[1,"two"]`)); len(got) != 0 {
		t.Fatalf("elements of an element-less array were rejected: %q", got)
	}
}

func TestConformsNumberKinds(t *testing.T) {
	skel := &Skeleton{Kind: KindNumber}
	if got := Conforms(skel, 2.5); len(got) != 0 {
		t.Fatalf("float64 rejected: %q", got)
	}
	if got := Conforms(skel, json.Number("7")); len(got) != 0 {
		t.Fatalf("json.Number rejected: %q", got)
	}
	if got := Conforms(skel, "7"); len(got) != 1 || !strings.Contains(got[0], "want number, got string") {
		t.Fatalf("string accepted as number: %q", got)
	}
}

const (
	uuidA = "11111111-2222-4333-8444-555555555555"
	uuidB = "66666666-7777-4888-9999-aaaaaaaaaaaa"
)

func TestInferKindMapForUUIDKeys(t *testing.T) {
	tests := []struct {
		name    string
		samples []string
		want    Kind
	}{
		{"all keys are uuids", []string{`{"` + uuidA + `":{"a":1}}`}, KindMap},
		{"uuids across samples", []string{`{"` + uuidA + `":1}`, `{"` + uuidB + `":2}`}, KindMap},
		{"empty object next to a map", []string{`{"` + uuidA + `":1}`, `{}`}, KindMap},
		{"empty object alone", []string{`{}`}, KindObject},
		{"one key is not a uuid", []string{`{"` + uuidA + `":1,"name":2}`}, KindObject},
		{"fixed keys next to a map", []string{`{"` + uuidA + `":1}`, `{"name":2}`}, KindAny},
		{"map next to an array", []string{`{"` + uuidA + `":1}`, `[]`}, KindAny},
		{"map next to null", []string{`{"` + uuidA + `":1}`, `null`}, KindAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferJSON(t, tc.samples...).Kind; got != tc.want {
				t.Fatalf("kind = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInferMapValueSkeletonMergesAllValues(t *testing.T) {
	s := inferJSON(t,
		`{"`+uuidA+`":{"id":"x","title":"t"}}`,
		`{"`+uuidB+`":{"id":"y"}}`,
	)
	if s.Kind != KindMap || s.Elem == nil || s.Elem.Kind != KindObject {
		t.Fatalf("got %+v, want map of objects", s)
	}
	if id := s.Elem.Keys["id"]; id == nil || id.Optional {
		t.Fatalf("id = %+v, want required", id)
	}
	if title := s.Elem.Keys["title"]; title == nil || !title.Optional {
		t.Fatalf("title = %+v, want optional", title)
	}
}

func TestInferMapInsideObjectFollowsSamples(t *testing.T) {
	s := inferJSON(t,
		`{"items":{"`+uuidA+`":{"n":1}}}`,
		`{"items":{}}`,
	)
	items := s.Keys["items"]
	if items == nil || items.Kind != KindMap || items.Elem == nil || items.Elem.Keys["n"] == nil {
		t.Fatalf("items = %+v, want map with elem keys n", items)
	}
}

func TestConformsMap(t *testing.T) {
	skel := inferJSON(t,
		`{"`+uuidA+`":{"id":"a","size":1,"name":null}}`,
		`{"`+uuidA+`":{"id":"b","size":2,"name":"x"}}`,
	)
	tests := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "any uuid keys with conforming values",
			json: `{"` + uuidB + `":{"id":"b","size":2,"name":"n"}}`,
		},
		{
			name: "empty map",
			json: `{}`,
		},
		{
			name: "keys are not checked",
			json: `{"not-a-uuid":{"id":"z","size":3,"name":null}}`,
		},
		{
			name: "value missing a required key",
			json: `{"` + uuidB + `":{"size":2,"name":null}}`,
			want: []string{"$." + uuidB + ".id: missing required key"},
		},
		{
			name: "value of the wrong type",
			json: `{"` + uuidB + `":{"id":"b","size":"2","name":null}}`,
			want: []string{"$." + uuidB + ".size: want number, got string"},
		},
		{
			name: "map given an array",
			json: `[]`,
			want: []string{"$: want map, got array"},
		},
		{
			name: "map given null",
			json: `null`,
			want: []string{"$: want map, got null"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Conforms(skel, decode(t, tc.json))
			if !equalStrings(got, tc.want) {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMapSkeletonRoundTripsJSON(t *testing.T) {
	s := inferJSON(t, `{"`+uuidA+`":[1]}`, `{}`)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Skeleton
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, s) || back.Kind != KindMap || back.Elem == nil || back.Elem.Kind != KindArray {
		t.Fatalf("round trip = %s", raw)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
