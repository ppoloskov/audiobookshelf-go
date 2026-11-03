package userapi

import "testing"

func TestParseDateStringReadsEveryStoredSpelling(t *testing.T) {
	const want = int64(1704067200123)
	for _, text := range []string{
		"2024-01-01 00:00:00.123 +00:00",
		"2024-01-01T00:00:00.123Z",
		"2024-01-01T00:00:00.123",
		"2024-01-01 02:00:00.123 +02:00",
	} {
		if got := parseDateString(text); got.UnixMilli() != want {
			t.Errorf("parseDateString(%q) = %v (%d), want %d", text, got, got.UnixMilli(), want)
		}
	}
	for _, text := range []string{"", "nope"} {
		if got := parseDateString(text); !got.IsZero() {
			t.Errorf("parseDateString(%q) = %v, want the zero time", text, got)
		}
	}
}
