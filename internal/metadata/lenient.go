package metadata

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// The types below decode what a custom provider or a drifting public API
// sends. Their UnmarshalJSON never fails: a value of the wrong shape is left
// out. They are used only where the sender is not ours to fix.

// looseText is a string that also reads a number as its text, an object by its
// "value" and an array as its comma join.
type looseText string

func (t *looseText) UnmarshalJSON(data []byte) error {
	var value any
	_ = json.Unmarshal(data, &value) // malformed input reads as absent
	*t = looseText(textOf(value))
	return nil
}

func textOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case map[string]any:
		return textOf(v["value"])
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = textOf(item)
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// looseNumber is a number that also reads a numeric string; absent is 0.
type looseNumber float64

func (n *looseNumber) UnmarshalJSON(data []byte) error {
	var value any
	_ = json.Unmarshal(data, &value) // malformed input reads as absent
	switch v := value.(type) {
	case float64:
		*n = looseNumber(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			*n = looseNumber(f)
		}
	}
	return nil
}

// looseList is a comma separated string or an array of strings, trimmed and
// unique. An array with a non-string element reads as absent.
type looseList []string

func (l *looseList) UnmarshalJSON(data []byte) error {
	var value any
	_ = json.Unmarshal(data, &value) // malformed input reads as absent
	var parts []string
	switch v := value.(type) {
	case string:
		parts = strings.Split(v, ",")
	case []any:
		for _, part := range v {
			s, ok := part.(string)
			if !ok {
				return nil
			}
			parts = append(parts, s)
		}
	}
	*l = uniqueTrimmed(parts)
	return nil
}

// looseSeries keeps the entries that name a series.
type looseSeries []Series

func (s *looseSeries) UnmarshalJSON(data []byte) error {
	var entries []struct {
		Series   string
		Sequence looseText
	}
	_ = json.Unmarshal(data, &entries) // entries of another shape are skipped
	for _, entry := range entries {
		if entry.Series != "" {
			*s = append(*s, Series{Series: entry.Series, Sequence: string(entry.Sequence)})
		}
	}
	return nil
}
