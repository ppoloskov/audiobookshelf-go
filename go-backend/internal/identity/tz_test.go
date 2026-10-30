package identity

import "testing"

// hostTimeZone mirrors upstream ServerSettings.getHostTimeZone; the replacement
// must never answer Go's unnamed "Local" zone or an empty string.
func TestHostTimeZone(t *testing.T) {
	got := hostTimeZone()
	if got == "" || got == "Local" {
		t.Fatalf("hostTimeZone returned %q", got)
	}
}
