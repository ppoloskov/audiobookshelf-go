package dbtime

import (
	"testing"
	"time"
)

const wantMillis = int64(1704067200123)

// The DATETIME spellings that appear in upstream-written and Go-written
// databases, all for the same instant.
var sameInstant = []string{
	"2024-01-01 00:00:00.123 +00:00",
	"2024-01-01 00:00:00.123456789 +00:00",
	"2024-01-01 00:00:00.123+00:00",
	"2024-01-01 00:00:00.123",
	"2024-01-01T00:00:00.123Z",
	"2024-01-01 02:00:00.123 +02:00",
}

func TestParseMillisAcceptsEveryStoredSpelling(t *testing.T) {
	for _, text := range sameInstant {
		got, err := ParseMillis(" " + text + "\n")
		if err != nil || got == nil {
			t.Fatalf("ParseMillis(%q) = %v, %v", text, got, err)
		}
		// The nine-digit spelling truncates to the same millisecond.
		if *got != wantMillis {
			t.Errorf("ParseMillis(%q) = %d, want %d", text, *got, wantMillis)
		}
	}
	for _, text := range []string{"2024-01-01 00:00:00 +00:00", "2024-01-01 00:00:00"} {
		got, err := ParseMillis(text)
		if err != nil || got == nil || *got != 1704067200000 {
			t.Errorf("ParseMillis(%q) = %v, %v", text, got, err)
		}
	}
}

func TestParseMillisEmptyIsAbsentAndGarbageIsAnError(t *testing.T) {
	if got, err := ParseMillis("  "); got != nil || err != nil {
		t.Fatalf("blank = %v, %v; want nil, nil", got, err)
	}
	if got, err := ParseMillis("yesterday"); got != nil || err == nil {
		t.Fatalf("garbage = %v, %v; want an error", got, err)
	}
	if _, err := ParseMillis("1704067200000"); err == nil {
		t.Fatal("digits are not a DATETIME for ParseMillis; Millis handles them")
	}
}

func TestOptionalMillisByValueType(t *testing.T) {
	at := time.UnixMilli(wantMillis)
	cases := []struct {
		name  string
		value any
		want  *int64
		fails bool
	}{
		{"nil", nil, nil, false},
		{"int64", int64(1234), ptr(1234), false},
		{"int", 1234, ptr(1234), false},
		{"float64", float64(1234), ptr(1234), false},
		{"text", "2024-01-01 00:00:00.123 +00:00", ptr(wantMillis), false},
		{"bytes", []byte("2024-01-01 00:00:00.123 +00:00"), ptr(wantMillis), false},
		{"time", at, ptr(wantMillis), false},
		{"empty", "", nil, false},
		{"bool", true, nil, true},
		{"garbage", "nope", nil, true},
	}
	for _, c := range cases {
		got, err := OptionalMillis(c.value)
		if (err != nil) != c.fails {
			t.Errorf("%s: err = %v, want failure %v", c.name, err, c.fails)
			continue
		}
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMillisIsLenient(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  any
	}{
		{"nil", nil, nil},
		{"time", time.UnixMilli(wantMillis), wantMillis},
		{"int64", int64(5), int64(5)},
		{"float64", float64(5.9), int64(5)},
		{"digits are already milliseconds", "1704067200000", int64(1704067200000)},
		{"text", "2024-01-01 00:00:00.123 +00:00", wantMillis},
		{"bytes", []byte("2024-01-01T00:00:00.123Z"), wantMillis},
		{"garbage", "nope", nil},
		{"padded text is not trimmed", " 2024-01-01 00:00:00.123 +00:00", nil},
		{"unsupported type", true, nil},
	}
	for _, c := range cases {
		if got := Millis(c.value); got != c.want {
			t.Errorf("%s: Millis(%#v) = %#v, want %#v", c.name, c.value, got, c.want)
		}
	}
}

func TestTime(t *testing.T) {
	at := time.UnixMilli(wantMillis)
	if got := Time(at); !got.Equal(at) {
		t.Fatalf("Time(time.Time) = %v", got)
	}
	if got := Time("2024-01-01 00:00:00.123 +00:00"); got.UnixMilli() != wantMillis {
		t.Fatalf("Time(text) = %v", got)
	}
	if got := Time([]byte("2024-01-01 00:00:00.123 +00:00")); got.UnixMilli() != wantMillis {
		t.Fatalf("Time(bytes) = %v", got)
	}
	if !Time("nope").IsZero() || !Time(7).IsZero() {
		t.Fatal("unparsable input must be the zero time")
	}
}

func ptr(n int64) *int64 { return &n }
