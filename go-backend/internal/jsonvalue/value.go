// Package jsonvalue contains small coercions for values decoded from JSON.
package jsonvalue

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func Strings(value any) []string {
	var out []string
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func String(value any) string {
	text, _ := value.(string)
	return text
}

func Truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return true
	}
}

// Array is a decoded JSON array (nil when value is not one).
func Array(value any) []any {
	a, _ := value.([]any)
	return a
}

// Number is the float64 of a decoded JSON number (json.Number and Go integers
// included); ok is false for anything else and for NaN and infinities.
func Number(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		n = f
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

// Text is how a JavaScript template literal renders a decoded JSON value.
func Text(value any) string {
	switch x := value.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return fmt.Sprint(x)
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", x), ".0")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = Text(e)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// Float is Number's value, 0 when value is not a finite number.
func Float(value any) float64 {
	n, _ := Number(value)
	return n
}
