package identity

import (
	"testing"
	"time"
)

func TestNullableDateScanReadsEveryStoredSpelling(t *testing.T) {
	const want = int64(1704067200123)
	for _, text := range []string{
		"2024-01-01 00:00:00.123 +00:00",
		"2024-01-01T00:00:00.123Z",
		"2024-01-01 00:00:00.123",
		"2024-01-01 02:00:00.123 +02:00",
	} {
		var d nullableDate
		if err := d.Scan(text); err != nil || !d.Valid || d.Time.UnixMilli() != want {
			t.Errorf("Scan(%q) = %v valid=%v err=%v, want %d", text, d.Time, d.Valid, err, want)
		}
	}
	var d nullableDate
	if err := d.Scan(nil); err != nil || d.Valid {
		t.Errorf("Scan(nil) valid=%v err=%v, want invalid without error", d.Valid, err)
	}
	if err := d.Scan("nope"); err == nil {
		t.Error("Scan(nope) must fail")
	}
	at := time.UnixMilli(want)
	if err := d.Scan(at); err != nil || !d.Valid || !d.Time.Equal(at) {
		t.Errorf("Scan(time.Time) = %v valid=%v err=%v", d.Time, d.Valid, err)
	}
}
