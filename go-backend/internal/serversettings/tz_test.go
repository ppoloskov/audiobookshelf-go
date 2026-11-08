package serversettings

import "testing"

// HostTimeZone mirrors upstream ServerSettings.getHostTimeZone; the replacement
// must never answer Go's unnamed "Local" zone or an empty string.
func TestHostTimeZone(t *testing.T) {
	got := HostTimeZone()
	if got == "" || got == "Local" {
		t.Fatalf("HostTimeZone returned %q", got)
	}
}

// Expected values are what Node 22 prints for TZ=<input>
// (Intl.DateTimeFormat().resolvedOptions().timeZone), see icu_zones_gen.go.
func TestICUZoneName(t *testing.T) {
	cases := map[string]string{
		"Etc/UTC":       "UTC",
		"Zulu":          "UTC",
		"GMT":           "+00:00",
		"US/Pacific":    "America/Los_Angeles",
		"Asia/Kolkata":  "Asia/Calcutta",
		"Asia/Calcutta": "Asia/Calcutta",
		"Europe/London": "Europe/London",
		"UTC":           "UTC",
		"Not/AZone":     "Not/AZone",
	}
	for in, want := range cases {
		if got := icuZoneName(in); got != want {
			t.Errorf("icuZoneName(%q) = %q, want %q", in, got, want)
		}
	}
}
