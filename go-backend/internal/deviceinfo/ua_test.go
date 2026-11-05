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
			b, bv, o, ov := ParseUserAgent(tc.UA)
			if b != tc.Browser || bv != tc.BrowserVersion || o != tc.OSName || ov != tc.OSVersion {
				t.Fatalf("got (%q,%q,%q,%q), want (%q,%q,%q,%q)", b, bv, o, ov, tc.Browser, tc.BrowserVersion, tc.OSName, tc.OSVersion)
			}
		})
	}
}
