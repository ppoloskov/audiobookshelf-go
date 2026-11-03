// Package shape infers JSON type skeletons from sample responses and checks
// decoded JSON values against them. It is a test helper: stdlib only.
//
// A Skeleton describes the shape a client can rely on. Objects list their keys;
// a key absent from some samples is Optional, and a key that was null in some
// samples and typed in others is Nullable with that type. Arrays carry one
// element skeleton merged from every element of every sample. An object whose
// keys are all UUIDs is a Map: its keys are ids, so only the values have a
// shape, merged into one value skeleton. Conflicting non-null kinds merge to
// KindAny.
package shape

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// Kind names a JSON type, or KindAny for conflicting or unconstrained values.
type Kind string

const (
	KindObject Kind = "object"
	KindMap    Kind = "map"
	KindArray  Kind = "array"
	KindString Kind = "string"
	KindNumber Kind = "number"
	KindBool   Kind = "bool"
	KindNull   Kind = "null"
	KindAny    Kind = "any"
)

// Skeleton is the inferred shape of one JSON value.
type Skeleton struct {
	Kind Kind `json:"kind"`
	// Keys is set for KindObject.
	Keys map[string]*Field `json:"keys,omitempty"`
	// Elem is set for KindArray (nil when every sample array was empty) and for
	// KindMap (the merged skeleton of every value).
	Elem *Skeleton `json:"elem,omitempty"`
}

// Field is the skeleton of one object key.
type Field struct {
	Skeleton
	// Nullable: the key was null in some samples and Kind in others.
	Nullable bool `json:"nullable,omitempty"`
	// Optional: the key was absent from some object samples.
	Optional bool `json:"optional,omitempty"`
}

// uuidKey matches a UUID in the 8-4-4-4-12 hex form the database and the API use
// for ids.
var uuidKey = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isIDMap reports whether an object is keyed by ids: it has keys and every key
// is a UUID. An empty object is not a map; it says nothing about its keys.
func isIDMap(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if !uuidKey.MatchString(k) {
			return false
		}
	}
	return true
}

// Infer merges the samples into one skeleton. It returns nil when there are no
// samples; Conforms accepts any value against a nil skeleton.
//
// A null mixed with another kind where no key can carry it (the top level and
// array elements) merges to KindAny, so the skeleton never rejects a null the
// samples produced.
func Infer(samples ...any) *Skeleton {
	var s *Skeleton
	for _, v := range samples {
		s = mergeNonNil(s, of(v))
	}
	return s
}

// of returns the skeleton of a single decoded JSON value.
func of(v any) *Skeleton {
	switch t := v.(type) {
	case nil:
		return &Skeleton{Kind: KindNull}
	case bool:
		return &Skeleton{Kind: KindBool}
	case string:
		return &Skeleton{Kind: KindString}
	case []any:
		s := &Skeleton{Kind: KindArray}
		for _, e := range t {
			s.Elem = mergeNonNil(s.Elem, of(e))
		}
		return s
	case map[string]any:
		if isIDMap(t) {
			s := &Skeleton{Kind: KindMap}
			for _, e := range t {
				s.Elem = mergeNonNil(s.Elem, of(e))
			}
			return s
		}
		s := &Skeleton{Kind: KindObject, Keys: make(map[string]*Field, len(t))}
		for k, e := range t {
			s.Keys[k] = &Field{Skeleton: *of(e)}
		}
		return s
	}
	if valueKind(v) == string(KindNumber) {
		return &Skeleton{Kind: KindNumber}
	}
	return &Skeleton{Kind: KindAny}
}

func mergeNonNil(a, b *Skeleton) *Skeleton {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return mergeValue(a, b)
}

// mergeValue merges two skeletons of values in the same position where a null
// cannot be recorded as Nullable. Kinds that differ merge to KindAny.
func mergeValue(a, b *Skeleton) *Skeleton {
	if a.Kind == KindMap || b.Kind == KindMap {
		return mergeMap(a, b)
	}
	if a.Kind != b.Kind {
		return &Skeleton{Kind: KindAny}
	}
	switch a.Kind {
	case KindObject:
		return &Skeleton{Kind: KindObject, Keys: mergeKeys(a.Keys, b.Keys)}
	case KindArray:
		return &Skeleton{Kind: KindArray, Elem: mergeNonNil(a.Elem, b.Elem)}
	default:
		return &Skeleton{Kind: a.Kind}
	}
}

// mergeMap merges a map with a map, or with an object that has no keys (an
// empty object at a path whose other samples are id-keyed). Any other pairing
// is a conflict.
func mergeMap(a, b *Skeleton) *Skeleton {
	if !mapLike(a) || !mapLike(b) {
		return &Skeleton{Kind: KindAny}
	}
	return &Skeleton{Kind: KindMap, Elem: mergeNonNil(a.Elem, b.Elem)}
}

// mapLike reports whether a skeleton can be a map: a map, or an empty object.
func mapLike(s *Skeleton) bool {
	return s.Kind == KindMap || (s.Kind == KindObject && len(s.Keys) == 0)
}

// mergeKeys unions two key sets. A key present on one side only is Optional,
// because the other side's samples did not carry it.
func mergeKeys(a, b map[string]*Field) map[string]*Field {
	out := make(map[string]*Field, len(a)+len(b))
	for k, fa := range a {
		if fb, ok := b[k]; ok {
			out[k] = mergeField(fa, fb)
			continue
		}
		c := *fa
		c.Optional = true
		out[k] = &c
	}
	for k, fb := range b {
		if _, ok := a[k]; !ok {
			c := *fb
			c.Optional = true
			out[k] = &c
		}
	}
	return out
}

// mergeField merges the same key seen on both sides. A null on either side
// makes the key Nullable with the other side's type.
func mergeField(a, b *Field) *Field {
	out := &Field{
		Optional: a.Optional || b.Optional,
		Nullable: a.Nullable || b.Nullable,
	}
	aNull, bNull := a.Kind == KindNull, b.Kind == KindNull
	switch {
	case aNull && bNull:
		out.Skeleton = Skeleton{Kind: KindNull}
	case aNull:
		out.Skeleton = b.Skeleton
		out.Nullable = true
	case bNull:
		out.Skeleton = a.Skeleton
		out.Nullable = true
	default:
		out.Skeleton = *mergeValue(&a.Skeleton, &b.Skeleton)
	}
	if out.Kind == KindNull {
		out.Nullable = false
	}
	return out
}

// Conforms returns the problems found when v does not match s, one message per
// problem, each prefixed by its JSON path ("$" is the root). Extra object keys
// are allowed, and a map's keys are not checked (only its values). A nil
// skeleton accepts every value.
func Conforms(s *Skeleton, v any) []string {
	var problems []string
	check(s, v, "$", &problems)
	return problems
}

func check(s *Skeleton, v any, path string, out *[]string) {
	if s == nil || s.Kind == KindAny {
		return
	}
	if s.Kind == KindNull {
		if v != nil {
			*out = append(*out, fmt.Sprintf("%s: want null, got %s", path, valueKind(v)))
		}
		return
	}
	if v == nil {
		*out = append(*out, fmt.Sprintf("%s: want %s, got null", path, s.Kind))
		return
	}
	want := s.Kind
	if want == KindMap {
		want = KindObject // a map is a JSON object
	}
	if got := valueKind(v); got != string(want) {
		*out = append(*out, fmt.Sprintf("%s: want %s, got %s", path, s.Kind, got))
		return
	}
	switch s.Kind {
	case KindArray:
		for i, e := range v.([]any) {
			check(s.Elem, e, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case KindMap:
		obj := v.(map[string]any)
		for _, k := range sortedKeys(obj) {
			check(s.Elem, obj[k], path+"."+k, out)
		}
	case KindObject:
		obj := v.(map[string]any)
		keys := make([]string, 0, len(s.Keys))
		for k := range s.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f := s.Keys[k]
			child := path + "." + k
			e, present := obj[k]
			if !present {
				if !f.Optional {
					*out = append(*out, fmt.Sprintf("%s: missing required key", child))
				}
				continue
			}
			if e == nil && f.Nullable {
				continue
			}
			check(&f.Skeleton, e, child, out)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// valueKind names the JSON type of a decoded value.
func valueKind(v any) string {
	switch v.(type) {
	case nil:
		return string(KindNull)
	case bool:
		return string(KindBool)
	case string:
		return string(KindString)
	case []any:
		return string(KindArray)
	case map[string]any:
		return string(KindObject)
	case json.Number, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return string(KindNumber)
	default:
		return fmt.Sprintf("%T", v)
	}
}
