package identity

import (
	"testing"
	"time"
)

func TestNullableDateScan(t *testing.T) {
	when := time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC)
	for _, tc := range []struct {
		name      string
		in        any
		wantValid bool
		want      time.Time
		wantErr   bool
	}{
		{"nil is NULL", nil, false, time.Time{}, false},
		{"time.Time passes through", when, true, when, false},
		{"stored string", "2024-01-02 03:04:05.678 +00:00", true, when, false},
		{"stored bytes", []byte("2024-01-02 03:04:05.678 +00:00"), true, when, false},
		{"garbage string", "nope", false, time.Time{}, true},
		{"unsupported type", int64(5), false, time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := nullableDate{Time: time.Unix(1, 0), Valid: true}
			err := d.Scan(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Scan(%#v) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if d.Valid != tc.wantValid || !d.Time.Equal(tc.want) {
				t.Fatalf("Scan(%#v) = {%v %v}, want {%v %v}", tc.in, d.Time, d.Valid, tc.want, tc.wantValid)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2024-01-02 03:04:05.678 +00:00", time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC)},
		{"2024-01-01T00:00:00.123", time.Date(2024, 1, 1, 0, 0, 0, 123000000, time.UTC)}, // zone-less ISO
		{"2024-01-01T00:00:00", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"garbage", time.Time{}},
	} {
		if got := parseDate(tc.in); !got.Equal(tc.want) {
			t.Errorf("parseDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
