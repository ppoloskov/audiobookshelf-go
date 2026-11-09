package serversettings

import "testing"

// HostTimeZone is the process zone as Node 22 names it (upstream
// ServerSettings.getHostTimeZone): TZ wins, mapped to ICU's spelling; with TZ
// unset it must still be a real name, never Go's unnamed "Local" or "".
func TestHostTimeZone(t *testing.T) {
	for tz, want := range map[string]string{"US/Pacific": "America/Los_Angeles", "Etc/UTC": "UTC", " Europe/London ": "Europe/London"} {
		t.Setenv("TZ", tz)
		if got := HostTimeZone(); got != want {
			t.Errorf("TZ=%q: HostTimeZone = %q, want %q", tz, got, want)
		}
	}
	t.Setenv("TZ", "")
	if got := HostTimeZone(); got == "" || got == "Local" {
		t.Errorf("TZ unset: HostTimeZone = %q", got)
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
