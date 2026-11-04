package store

import "encoding/json"

// DecodeJSONSlice decodes JSON stored in persisted columns, retaining any
// values encoding/json decoded before a type error and returning an empty
// non-nil slice when the column is empty, null, or malformed.
func DecodeJSONSlice[T any](raw []byte) []T {
	values := make([]T, 0)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	if values == nil {
		return make([]T, 0)
	}
	return values
}

// DecodeJSONObject decodes a persisted JSON object and returns an empty,
// mutable map when the column is empty, null, or malformed.
func DecodeJSONObject(raw []byte) map[string]any {
	value := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &value)
	}
	if value == nil {
		return map[string]any{}
	}
	return value
}
