package launch

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// staticScenarios prove the retained client is served by the same process at
// the configured base path, with no directory listing or asset fallback.
func staticScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "sub-static-client", Invariant: "INV-client-browser-001", Case: "normal", State: "stateless", Assertion: "client-index-and-assets-served",
			Sources: []string{"index.js:55-56", "server/Server.js:407-414"},
			Claim:   "The retained client serves the entry document, a known client route's fallback and a bundled asset without authentication at the configured base path.",
			Run: func(t *testing.T, e *harness.Env) {
				entry := e.Get(t, "", "/")
				requireStatus(t, entry, http.StatusOK, "GET /")
				if !strings.HasPrefix(entry.Header.Get("Content-Type"), "text/html") || len(entry.Body) == 0 {
					t.Fatalf("client entry = %q body %.80q", entry.Header.Get("Content-Type"), entry.Body)
				}
				if e.IDs["library"] == "" {
					t.Fatal("fixture library id is missing")
				}
				route := e.Get(t, "", "/library/"+e.IDs["library"])
				requireStatus(t, route, http.StatusOK, "GET a known client route")
				if string(route.Body) != string(entry.Body) {
					t.Fatal("a known client route did not serve the entry document")
				}
				asset := e.Get(t, "", "/favicon.ico")
				requireStatus(t, asset, http.StatusOK, "GET /favicon.ico")
				if len(asset.Body) == 0 || strings.HasPrefix(asset.Header.Get("Content-Type"), "text/html") {
					t.Fatalf("client asset = %q body %.40q", asset.Header.Get("Content-Type"), asset.Body)
				}
			},
		},
		{
			Requirement: "sub-static-client", Invariant: "INV-F06-REVIEW-CLOSURE-001", Case: "error", State: "stateless", Assertion: "client-missing-asset-no-fallback",
			Sources: []string{"server/Server.js:411-414", "server/Server.js:432-437"},
			Claim:   "A missing client asset or an asset directory path answers 404 without falling back to the entry document or listing the directory.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, path := range []string{"/_nuxt/does-not-exist.js", "/_nuxt/"} {
					resp := e.Get(t, "", path)
					requireStatus(t, resp, http.StatusNotFound, "GET "+path)
					if strings.Contains(string(resp.Body), `id="__nuxt"`) {
						t.Fatalf("GET %s fell back to the client entry document", path)
					}
					if strings.Contains(string(resp.Body), "0180c66.js") {
						t.Fatalf("GET %s listed the client asset directory", path)
					}
				}
			},
		},
		{
			Requirement: "sub-static-client", Invariant: "INV-F06-REVIEW-CLOSURE-001", Case: "edge", State: "stateless", Assertion: "client-prefixed-base-serves-index",
			Sources: []string{"index.js:50", "server/Server.js:55-63", "server/Server.js:323-334", "server/Server.js:407-414"},
			Claim:   "At a non-default ROUTER_BASE_PATH the client entry document and its bundled assets are served under that prefix.",
			Run: func(t *testing.T, e *harness.Env) {
				const custom = "/client-prefix"
				own := harness.Start(t, harness.Options{
					NoWait: true,
					Env:    map[string]string{"ROUTER_BASE_PATH": custom},
				})
				defer own.Stop(t)
				prefixed := own.URL + custom
				waitPing(t, "prefixed ping", prefixed+"/ping")
				entry := directGet(t, prefixed+"/")
				requireStatus(t, entry, http.StatusOK, "GET prefixed /")
				if !strings.HasPrefix(entry.Header.Get("Content-Type"), "text/html") || len(entry.Body) == 0 {
					t.Fatalf("prefixed client entry = %q body %.80q", entry.Header.Get("Content-Type"), entry.Body)
				}
				asset := directGet(t, prefixed+"/favicon.ico")
				requireStatus(t, asset, http.StatusOK, "GET prefixed /favicon.ico")
				if len(asset.Body) == 0 || strings.HasPrefix(asset.Header.Get("Content-Type"), "text/html") {
					t.Fatalf("prefixed client asset = %q body %.40q", asset.Header.Get("Content-Type"), asset.Body)
				}
			},
		},
	}
}
