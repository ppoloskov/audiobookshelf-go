// Package jsonvalue contains small coercions for values decoded from JSON.
package jsonvalue

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
