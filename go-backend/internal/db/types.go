package db

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// timeLayout is the Sequelize DATETIME text written to SQLite, always in UTC.
const timeLayout = "2006-01-02 15:04:05.000 -07:00"

// timeLayouts are the DATETIME spellings found in upstream-written and
// Go-written databases: Sequelize's own layout, its variants without
// milliseconds or offset, and RFC 3339. Text without an offset is UTC.
var timeLayouts = []string{
	timeLayout,
	"2006-01-02 15:04:05.999999999 -07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	time.RFC3339Nano,
}

// Time is a SQLite DATETIME column. It reads every spelling the upstream
// server and this one have stored, and writes timeLayout in UTC. The zero value
// is SQL NULL in both directions. In JSON it is epoch milliseconds, or null.
type Time struct{ time.Time }

// Now is the current time truncated to the millisecond, the precision stored.
func Now() Time { return Time{time.Now().UTC().Truncate(time.Millisecond)} }

// FromMillis converts Unix milliseconds to a Time.
func FromMillis(ms int64) Time { return Time{time.UnixMilli(ms).UTC()} }

// Millis is the Unix milliseconds of t. It is meaningless for the zero Time;
// check IsZero first.
func (t Time) Millis() int64 { return t.UnixMilli() }

// Scan implements sql.Scanner. It accepts NULL (zero Time), time.Time (what the
// driver returns for columns it can parse), text and bytes in any stored
// spelling, text of digits only (epoch milliseconds), and int64 or float64
// epoch milliseconds. Unrecognised text is an error: a stored date that cannot
// be read must not be silently rewritten as NULL.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*t = Time{}
	case time.Time:
		*t = Time{v.UTC()}
	case int64:
		*t = FromMillis(v)
	case float64:
		*t = FromMillis(int64(v))
	case string:
		return t.scanText(v)
	case []byte:
		return t.scanText(string(v))
	default:
		return fmt.Errorf("db.Time: cannot scan %T", src)
	}
	return nil
}

func (t *Time) scanText(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		*t = Time{}
		return nil
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil {
		*t = FromMillis(ms)
		return nil
	}
	for _, layout := range timeLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			*t = Time{parsed.UTC()}
			return nil
		}
	}
	return fmt.Errorf("db.Time: unrecognized timestamp %q", text)
}

// Value implements driver.Valuer. It returns a string, never time.Time, so the
// stored text is exactly timeLayout. The zero Time is NULL.
func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.UTC().Format(timeLayout), nil
}

// MarshalJSON writes epoch milliseconds, or null for the zero Time.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return strconv.AppendInt(nil, t.UnixMilli(), 10), nil
}

// UnmarshalJSON reads epoch milliseconds (integer or fractional) or null.
func (t *Time) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*t = Time{}
		return nil
	}
	var ms float64
	if err := json.Unmarshal(data, &ms); err != nil {
		return fmt.Errorf("db.Time: %w", err)
	}
	*t = FromMillis(int64(ms))
	return nil
}

// JSON is a JSON-typed TEXT column holding a document of type T.
//
// Scan reads NULL, empty text and malformed JSON as the zero T without error:
// one corrupt cell must not fail a whole listing.
//
// Value writes compact JSON as a string, never []byte, because the driver binds
// []byte as a BLOB and that would corrupt a TEXT column. A nil slice, nil map,
// nil pointer, nil interface or empty json.RawMessage is written as NULL, so a
// column that was NULL reads and writes back as NULL when T is one of those
// types; any other T (a struct, a number) is always written as JSON text.
type JSON[T any] struct{ V T }

// Scan implements sql.Scanner for NULL, string and []byte sources.
func (j *JSON[T]) Scan(src any) error {
	var text []byte
	switch v := src.(type) {
	case nil:
	case string:
		text = []byte(v)
	case []byte:
		text = v
	default:
		return fmt.Errorf("db.JSON: cannot scan %T", src)
	}
	var value T
	text = bytes.TrimSpace(text)
	if len(text) > 0 && json.Unmarshal(text, &value) != nil {
		var zero T
		value = zero // malformed or mistyped JSON reads as the zero value
	}
	j.V = value
	return nil
}

// Value implements driver.Valuer; see JSON for the NULL rule.
func (j JSON[T]) Value() (driver.Value, error) {
	switch v := any(j.V).(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if len(v) == 0 {
			return nil, nil
		}
	}
	rv := reflect.ValueOf(j.V)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
	}
	// No HTML escaping: JSON.stringify does not escape <, > and &, and upstream
	// filters search these columns with LIKE.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(j.V); err != nil {
		return nil, fmt.Errorf("db.JSON: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// MarshalJSON delegates to V.
func (j JSON[T]) MarshalJSON() ([]byte, error) { return json.Marshal(j.V) }

// UnmarshalJSON delegates to V.
func (j *JSON[T]) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &j.V) }

// Bool is a TINYINT(1) column. NULL reads as false and false writes as 0, so a
// Go rewrite turns a NULL flag into 0, which upstream reads as the same falsy
// value.
type Bool bool

// Scan implements sql.Scanner for NULL, int64, float64 and bool sources.
func (b *Bool) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*b = false
	case int64:
		*b = v != 0
	case float64:
		*b = v != 0
	case bool:
		*b = Bool(v)
	default:
		return fmt.Errorf("db.Bool: cannot scan %T", src)
	}
	return nil
}

// Value implements driver.Valuer, writing int64 0 or 1.
func (b Bool) Value() (driver.Value, error) {
	if b {
		return int64(1), nil
	}
	return int64(0), nil
}

// Ptr returns a pointer to v, for nullable columns.
func Ptr[T any](v T) *T { return &v }

// Val returns *p, or the zero T when p is nil.
func Val[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
