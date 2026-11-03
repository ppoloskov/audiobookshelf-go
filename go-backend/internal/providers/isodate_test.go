package providers

import "testing"

func TestIsoDateConvertsStoredDatetimeToRFC3339(t *testing.T) {
	cases := map[string]string{
		"2024-01-01 00:00:00.123 +00:00": "2024-01-01T00:00:00.123Z",
		"2024-01-01 02:00:00.5 +02:00":   "2024-01-01T00:00:00.5Z",
		"2024-01-01T00:00:00Z":           "2024-01-01T00:00:00Z",
		"2024-01-01 00:00:00 +00:00":     "2024-01-01T00:00:00Z",
		"not a date":                     "not a date",
		"":                               "",
	}
	for in, want := range cases {
		if got := isoDate(in); got != want {
			t.Errorf("isoDate(%q) = %q, want %q", in, got, want)
		}
	}
}
