package dbtime

import (
	"testing"
	"time"
)

func TestFormatUsesUTCAndMillisecondPrecision(t *testing.T) {
	value := Format(time.Date(2025, 3, 4, 5, 6, 7, 123456789, time.FixedZone("offset", 2*60*60)))
	if value != "2025-03-04 03:06:07.123 +00:00" {
		t.Fatalf("Format() = %q", value)
	}
}
