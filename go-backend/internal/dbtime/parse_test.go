package dbtime

import (
	"testing"
	"time"
)

func TestParseAcceptsStoredSpellings(t *testing.T) {
	want := time.Date(2026, 11, 2, 8, 8, 50, 224000000, time.UTC)
	for _, value := range []string{"2026-11-02 08:08:50.224 +00:00", "2026-11-02 08:08:50.224+00:00", "2026-11-02 08:08:50.224", "2026-11-02T08:08:50.224Z", Format(want)} {
		got, ok := Parse(value)
		if !ok || !got.Equal(want) {
			t.Errorf("Parse(%q) = %v, %v; want %v", value, got, ok, want)
		}
	}
	if _, ok := Parse("not a date"); ok {
		t.Error("Parse accepted garbage")
	}
	if got := JSDate(want); got != "2026-11-02T08:08:50.224Z" {
		t.Errorf("JSDate = %q", got)
	}
}
