package dbtime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseMillis reads a stored DATETIME text as Unix milliseconds. Surrounding
// space is ignored; empty text is absent (nil, nil); anything Parse does not
// recognize is an error.
func ParseMillis(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, ok := Parse(s)
	if !ok {
		return nil, fmt.Errorf("unrecognized timestamp %q", s)
	}
	n := t.UnixMilli()
	return &n, nil
}

// OptionalMillis converts a scanned DATETIME column value (NULL, text, a
// time.Time the driver decoded, or a number of epoch milliseconds) to Unix
// milliseconds. NULL is nil; an unsupported value type or unparsable text is
// an error.
func OptionalMillis(v any) (*int64, error) {
	var n int64
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		n = x
	case int:
		n = int64(x)
	case float64:
		n = int64(x)
	case []byte:
		return ParseMillis(string(x))
	case string:
		return ParseMillis(x)
	case time.Time:
		n = x.UnixMilli()
	default:
		return nil, fmt.Errorf("unsupported date value %T", v)
	}
	return &n, nil
}

// Millis converts a scanned DATETIME column value to a JSON value: Unix
// milliseconds as int64, or nil for NULL and for anything unrecognized. Unlike
// OptionalMillis it never fails, and text that is only digits counts as
// milliseconds already.
func Millis(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.UnixMilli()
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		return textMillis(string(x))
	case string:
		return textMillis(x)
	}
	return nil
}

func textMillis(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if t, ok := Parse(s); ok {
		return t.UnixMilli()
	}
	return nil
}

// ParseUnixMilli reads a stored DATETIME text as Unix milliseconds without
// trimming; ok is false when Parse does not recognize it.
func ParseUnixMilli(value string) (int64, bool) {
	t, ok := Parse(value)
	if !ok {
		return 0, false
	}
	return t.UnixMilli(), true
}

// Time converts a scanned DATETIME column value to a time.Time. A value that
// is neither a time.Time nor parsable text yields the zero time.
func Time(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		t, _ := Parse(x)
		return t
	case []byte:
		t, _ := Parse(string(x))
		return t
	}
	return time.Time{}
}
