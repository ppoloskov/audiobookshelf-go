package server

import (
	"mime"
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func (s *suite) spaScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r014", Invariant: "INV-ROUTE-R014-SPA", Case: "normal", State: "stateless", Assertion: "spa-client-routes-serve-entry",
			Sources: []string{"server/Server.js:410-437"},
			Claim:   "Without REACT_CLIENT_PATH the bundled Nuxt client is served: / and all 21 listed client routes (including the optional bookshelf and series segments) answer 200 text/html with the same entry document, without authentication.",
			Run: func(t *testing.T, e *harness.Env) {
				entry := e.Get(t, "", "/")
				requireStatus(t, entry, http.StatusOK, "GET /")
				if !strings.HasPrefix(entry.Header.Get("Content-Type"), "text/html") || len(entry.Body) == 0 {
					t.Fatalf("GET / = %q body %.80q, want the HTML entry document", entry.Header.Get("Content-Type"), entry.Body)
				}
				library, item := e.IDs["library"], e.IDs["saga1"]
				for _, path := range []string{
					"/item/" + item,
					"/author/" + item,
					"/audiobook/" + item + "/chapters",
					"/audiobook/" + item + "/edit",
					"/audiobook/" + item + "/manage",
					"/library/" + library,
					"/library/" + library + "/search",
					"/library/" + library + "/bookshelf",
					"/library/" + library + "/bookshelf/2",
					"/library/" + library + "/authors",
					"/library/" + library + "/narrators",
					"/library/" + library + "/stats",
					"/library/" + library + "/series",
					"/library/" + library + "/series/2",
					"/library/" + library + "/podcast/search",
					"/library/" + library + "/podcast/latest",
					"/library/" + library + "/podcast/download-queue",
					"/config/users/" + e.IDs["listener"],
					"/config/users/" + e.IDs["listener"] + "/sessions",
					"/config/item-metadata-utils/" + item,
					"/collection/" + e.IDs["collection"],
					"/playlist/" + e.IDs["playlist"],
					"/share/a-shared-slug",
				} {
					resp := e.Get(t, "", path)
					requireStatus(t, resp, http.StatusOK, "GET "+path)
					if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
						t.Fatalf("GET %s content type = %q, want text/html", path, resp.Header.Get("Content-Type"))
					}
					if string(resp.Body) != string(entry.Body) {
						t.Fatalf("GET %s did not serve the same client entry document as /", path)
					}
				}
			},
		},
		{
			Requirement: "route-r014", Invariant: "INV-ROUTE-R014-SPA", Case: "error", State: "stateless", Assertion: "spa-missing-asset-404-no-listing",
			Sources: []string{"server/Server.js:412-414", "server/Server.js:432-437"},
			Claim:   "An existing client asset is served with its file type, while a missing asset and a directory path answer 404 and never list or fall back to the entry document.",
			Run: func(t *testing.T, e *harness.Env) {
				asset := e.Get(t, "", "/favicon.ico")
				requireStatus(t, asset, http.StatusOK, "GET /favicon.ico")
				mediaType, _, err := mime.ParseMediaType(asset.Header.Get("Content-Type"))
				if err != nil || mediaType != "image/x-icon" {
					t.Fatalf("GET /favicon.ico Content-Type = %q (%v), want image/x-icon", asset.Header.Get("Content-Type"), err)
				}
				if len(asset.Body) == 0 || strings.Contains(string(asset.Body), `id="__nuxt"`) {
					t.Fatalf("GET /favicon.ico body %.40q, want the icon bytes rather than the entry document", asset.Body)
				}
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
			Requirement: "route-r014", Invariant: "INV-ROUTE-R014-SPA", Case: "edge", State: "stateless", Assertion: "spa-dynamic-variants-and-unknown-client-path",
			Sources: []string{"server/Server.js:416-437"},
			Claim:   "A client route with a query string and the optional bookshelf segment serve the entry document, while an unlisted client-shaped path answers 404 and is not rewritten into the SPA.",
			Run: func(t *testing.T, e *harness.Env) {
				entry := e.Get(t, "", "/")
				requireStatus(t, entry, http.StatusOK, "GET /")
				variants := []harness.Request{
					{Path: "/library/" + e.IDs["library"] + "/search", Query: map[string][]string{"q": {"beginning"}}},
					{Path: "/library/" + e.IDs["library"] + "/bookshelf"},
					{Path: "/library/" + e.IDs["library"] + "/bookshelf/2"},
					{Path: "/library/" + e.IDs["library"] + "/series"},
				}
				for _, request := range variants {
					resp := e.Do(t, request)
					requireStatus(t, resp, http.StatusOK, "GET "+request.Path)
					if string(resp.Body) != string(entry.Body) {
						t.Fatalf("GET %s did not serve the client entry document", request.Path)
					}
				}
				requireStatus(t, e.Get(t, "", "/library/"+e.IDs["library"]+"/not-a-route"), http.StatusNotFound, "GET an unlisted client path")
			},
		},
	}
}

func (s *suite) unmatchedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r015", Invariant: "INV-ROUTE-R015-ORDER", Case: "normal", State: "stateless", Assertion: "unmatched-api-404-with-token",
			Sources: []string{"server/Server.js:346-355", "server/Server.js:437-467"},
			Claim:   "With a valid token, an unmatched /api path answers 404 with an error body, not the client entry document.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, path := range []string{"/api/not-a-route", "/api/library-items"} {
					resp := e.Get(t, root(t, e), path)
					requireStatus(t, resp, http.StatusNotFound, "GET "+path)
					if strings.Contains(string(resp.Body), `id="__nuxt"`) {
						t.Fatalf("GET %s was converted into the client HTML", path)
					}
				}
			},
		},
		{
			Requirement: "route-r015", Invariant: "INV-ROUTE-R015-ORDER", Case: "error", State: "stateless", Assertion: "unmatched-api-unauthenticated-401",
			Sources: []string{"server/Server.js:354", "server/Auth.js:45-52"},
			Claim:   "An unmatched /api path without a token is rejected by the API auth gate with 401 before route matching answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Get(t, "", "/api/not-a-route"), http.StatusUnauthorized, "GET unmatched /api without a token")
			},
		},
		{
			Requirement: "route-r015", Invariant: "INV-ROUTE-R015-ORDER", Case: "edge", State: "stateless", Assertion: "registered-routes-not-shadowed",
			Sources: []string{"server/Server.js:346-437", "server/Server.js:452"},
			Claim:   "Registered routes and the API auth gate answer before the terminal handler: a known API route succeeds, an unknown method on it is 404, and a public route stays public.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Get(t, root(t, e), "/api/me"), http.StatusOK, "GET /api/me")
				requireStatus(t, e.Do(t, harness.Request{Method: "DELETE", Path: "/api/libraries", Token: root(t, e)}), http.StatusNotFound, "DELETE /api/libraries")
				requireStatus(t, e.Get(t, "", "/ping"), http.StatusOK, "GET /ping")
				// The API router is mounted behind auth: an unknown route is 401
				// unauthenticated and 404 authenticated, never a client page.
				requireStatus(t, e.Get(t, "", "/api/backups"), http.StatusUnauthorized, "GET /api/backups without a token")
			},
		},
	}
}
