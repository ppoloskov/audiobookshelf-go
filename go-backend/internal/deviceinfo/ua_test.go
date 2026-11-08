package deviceinfo

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/ua.json
var uaCasesJSON []byte

type uaCase struct {
	Name           string `json:"name"`
	UA             string `json:"ua"`
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browserVersion"`
	OSName         string `json:"osName"`
	OSVersion      string `json:"osVersion"`
}

func TestParseUserAgent(t *testing.T) {
	var cases []uaCase
	if err := json.Unmarshal(uaCasesJSON, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := Parse(tc.UA)
			if got.Browser != tc.Browser || got.BrowserVersion != tc.BrowserVersion || got.OSName != tc.OSName || got.OSVersion != tc.OSVersion {
				t.Fatalf("got %+v, want (%q,%q,%q,%q)", got, tc.Browser, tc.BrowserVersion, tc.OSName, tc.OSVersion)
			}
		})
	}
}

func TestParseDeviceType(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) Version/16.0 Mobile/15E148 Safari/604.1": "mobile",
		"Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) Version/16.0 Safari/604.1":                        "tablet",
		"Mozilla/5.0 (Linux; Android 13; Pixel 7) Chrome/120.0 Mobile Safari/537.36":                     "mobile",
		"Mozilla/5.0 (Linux; Android 13; SM-X700) Chrome/120.0 Safari/537.36":                            "tablet",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0 Safari/537.36":                           "",
	} {
		if got := Parse(ua).DeviceType; got != want {
			t.Errorf("%q: got %q, want %q", ua, got, want)
		}
	}
}
