package server

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	capacitorOrigin = "capacitor://localhost"
	corsMethods     = "GET, POST, PATCH, PUT, DELETE, OPTIONS"
)

// requireCORS asserts the exact CORS grant upstream sets for a permitted origin.
func requireCORS(t *testing.T, resp harness.Response, origin, what string) {
	t.Helper()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("%s: Access-Control-Allow-Origin = %q, want %q", what, got, origin)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != corsMethods {
		t.Fatalf("%s: Access-Control-Allow-Methods = %q, want %q", what, got, corsMethods)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "*" {
		t.Fatalf("%s: Access-Control-Allow-Headers = %q, want *", what, got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("%s: Access-Control-Allow-Credentials = %q, want true", what, got)
	}
}

// requireNoCORS asserts that no CORS grant was added.
func requireNoCORS(t *testing.T, resp harness.Response, what string) {
	t.Helper()
	for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Allow-Credentials"} {
		if value := resp.Header.Get(header); value != "" {
			t.Fatalf("%s: unexpected %s = %q", what, header, value)
		}
	}
}

func (s *suite) corsScenarios() []harness.Scenario {
	cover := func(e *harness.Env) string { return "/api/items/" + e.IDs["saga1"] + "/cover" }
	return []harness.Scenario{
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "normal", State: "initial", Assertion: "cors-mobile-cover-and-ebook-origins",
			Sources: []string{"server/Server.js:270-295", "client/components/covers/BookCover.vue", "go-backend/testmachine/contracts/mobile-client-audit.md"},
			Claim:   "The mobile item cover and ebook routes answer the Capacitor and localhost origins with the exact upstream CORS grant (echoed origin, all methods, any headers, credentials) while serving the requested content.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, origin := range []string{capacitorOrigin, "http://localhost"} {
					image := e.Do(t, harness.Request{Path: cover(e), Header: map[string]string{"Origin": origin}})
					requireStatus(t, image, http.StatusOK, "GET item cover with Origin "+origin)
					if len(image.Body) == 0 {
						t.Fatalf("GET item cover with Origin %s returned no image", origin)
					}
					requireCORS(t, image, origin, "item cover")
				}
				ebook := e.Do(t, harness.Request{Path: "/api/items/" + e.IDs["ebook"] + "/ebook", Token: root(t, e), Header: map[string]string{"Origin": capacitorOrigin}})
				requireStatus(t, ebook, http.StatusOK, "GET item ebook with the Capacitor origin")
				if len(ebook.Body) == 0 {
					t.Fatal("GET item ebook returned no file")
				}
				requireCORS(t, ebook, capacitorOrigin, "item ebook")
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "error", State: "failure", Assertion: "cors-unlisted-origin-and-path",
			Sources: []string{"server/Server.js:285-294", "server/Server.js:349-355"},
			Claim:   "An unlisted origin on the mobile cover path and a permitted origin on a non-mobile API path receive no CORS grant; the cover is still served but the API stays 401 without credentials.",
			Run: func(t *testing.T, e *harness.Env) {
				image := e.Do(t, harness.Request{Path: cover(e), Header: map[string]string{"Origin": "https://evil.test"}})
				requireStatus(t, image, http.StatusOK, "item cover from an unlisted origin")
				if len(image.Body) == 0 {
					t.Fatal("item cover from an unlisted origin returned no image")
				}
				requireNoCORS(t, image, "item cover from an unlisted origin")
				library := e.Do(t, harness.Request{Path: "/api/libraries", Header: map[string]string{"Origin": capacitorOrigin}})
				requireStatus(t, library, http.StatusUnauthorized, "GET /api/libraries without a token")
				requireNoCORS(t, library, "non-mobile API path")
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "edge", State: "repeat", Assertion: "cors-preflight-mobile-cover",
			Sources: []string{"server/Server.js:270-295"},
			Claim:   "OPTIONS on the mobile cover path is answered 200 OK with the CORS grant for the two mobile origins, and is not answered for an unlisted origin or without an origin.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, origin := range []string{capacitorOrigin, "http://localhost"} {
					resp := e.Do(t, harness.Request{Method: "OPTIONS", Path: cover(e), Header: map[string]string{"Origin": origin, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}})
					requireStatus(t, resp, http.StatusOK, "OPTIONS item cover for "+origin)
					if string(resp.Body) != "OK" {
						t.Fatalf("OPTIONS item cover body = %q, want OK", resp.Body)
					}
					requireCORS(t, resp, origin, "item cover preflight")
				}
				for _, headers := range []map[string]string{
					{"Origin": "https://evil.test", "Access-Control-Request-Method": "GET"},
					{"Access-Control-Request-Method": "GET"},
				} {
					// OPTIONS is not an authentication exception, so the API auth
					// gate answers before any route or default 404.
					resp := e.Do(t, harness.Request{Method: "OPTIONS", Path: cover(e), Header: headers})
					requireStatus(t, resp, http.StatusUnauthorized, "OPTIONS item cover without a permitted origin")
					requireNoCORS(t, resp, "OPTIONS item cover without a permitted origin")
				}
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH-ROUTE-001", Case: "normal", State: "initial", Assertion: "basepath-and-legacy-routes",
			Sources: []string{"server/Server.js:323-334", "server/Server.js:346-355"},
			Claim:   "Every capability route answers under the configured /audiobookshelf prefix and, through the legacy rewrite, under its bare path with the same status and body, including status, ping and static client files.",
			Run: func(t *testing.T, e *harness.Env) {
				token := root(t, e)
				pairs := []struct {
					prefixed harness.Request
					legacy   harness.Request
					status   int
				}{
					{harness.Request{Path: "/status"}, harness.Request{Path: "/status", Raw: true}, http.StatusOK},
					{harness.Request{Path: "/ping"}, harness.Request{Path: "/ping", Raw: true}, http.StatusOK},
					{harness.Request{Path: "/api/libraries", Token: token}, harness.Request{Path: "/api/libraries", Token: token, Raw: true}, http.StatusOK},
					{harness.Request{Path: "/favicon.ico"}, harness.Request{Path: "/favicon.ico", Raw: true}, http.StatusOK},
				}
				for _, pair := range pairs {
					prefixed := e.Do(t, pair.prefixed)
					legacy := e.Do(t, pair.legacy)
					requireStatus(t, prefixed, pair.status, "prefixed "+pair.prefixed.Path)
					requireStatus(t, legacy, pair.status, "legacy "+pair.legacy.Path)
					if string(prefixed.Body) != string(legacy.Body) {
						t.Fatalf("%s and its legacy form differ", pair.prefixed.Path)
					}
				}
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "edge", State: "repeat", Assertion: "cors-mobile-cover-repeated",
			Sources: []string{"server/Server.js:270-295"},
			Claim:   "Repeated cover requests from the Capacitor origin receive the same completed image and the same CORS grant each time.",
			Run: func(t *testing.T, e *harness.Env) {
				var first []byte
				for i := range 3 {
					resp := e.Do(t, harness.Request{Path: cover(e), Header: map[string]string{"Origin": capacitorOrigin}})
					requireStatus(t, resp, http.StatusOK, "repeated item cover")
					requireCORS(t, resp, capacitorOrigin, "repeated item cover")
					if i == 0 {
						first = resp.Body
						continue
					}
					if string(resp.Body) != string(first) {
						t.Fatalf("cover bytes changed on request %d", i+1)
					}
				}
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "edge", State: "changed", Assertion: "cors-configured-origin-after-settings-change",
			Sources: []string{"server/Server.js:285-294", "server/controllers/MiscController.js:139-170"},
			Claim:   "After an admin adds an origin to allowedOrigins, that origin receives the CORS grant on an ordinary API path while an unrelated origin does not; clearing the setting removes the grant again.",
			Run: func(t *testing.T, e *harness.Env) {
				const origin = "http://cors.test"
				setAllowedOrigins := func(origins []string) {
					t.Helper()
					resp := e.Do(t, harness.Request{Method: "PATCH", Path: "/api/settings", Token: root(t, e), JSON: map[string]any{"allowedOrigins": origins}})
					requireStatus(t, resp, http.StatusOK, "PATCH /api/settings allowedOrigins")
				}
				setAllowedOrigins([]string{origin})
				defer setAllowedOrigins([]string{})
				allowed := e.Do(t, harness.Request{Path: "/api/libraries", Token: root(t, e), Query: map[string][]string{"cors": {"allowed"}}, Header: map[string]string{"Origin": origin}})
				requireStatus(t, allowed, http.StatusOK, "GET /api/libraries from a configured origin")
				requireCORS(t, allowed, origin, "configured origin")
				other := e.Do(t, harness.Request{Path: "/api/libraries", Token: root(t, e), Query: map[string][]string{"cors": {"other"}}, Header: map[string]string{"Origin": "https://other.test"}})
				requireStatus(t, other, http.StatusOK, "GET /api/libraries from an unconfigured origin")
				requireNoCORS(t, other, "unconfigured origin")
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH", Case: "edge", State: "force", Assertion: "cors-allow-cors-force-override",
			Sources: []string{"server/Server.js:71", "server/Server.js:270-295"},
			Claim:   "ALLOW_CORS=1 forces the CORS grant for any origin on any path, including a preflight on the ebook path that is not a mobile cover path.",
			Run: func(t *testing.T, e *harness.Env) {
				forced := harness.Start(t, harness.Options{Env: map[string]string{"ALLOW_CORS": "1"}})
				defer forced.Stop(t)
				const origin = "https://force.test"
				library := forced.Do(t, harness.Request{Path: "/api/libraries", Token: root(t, forced), Header: map[string]string{"Origin": origin}})
				requireStatus(t, library, http.StatusOK, "forced GET /api/libraries")
				requireCORS(t, library, origin, "ALLOW_CORS GET")
				ebook := forced.Do(t, harness.Request{Method: "OPTIONS", Path: "/api/items/" + forced.IDs["ebook"] + "/ebook", Header: map[string]string{"Origin": origin, "Access-Control-Request-Method": "GET"}})
				requireStatus(t, ebook, http.StatusOK, "forced OPTIONS ebook")
				requireCORS(t, ebook, origin, "ALLOW_CORS preflight")
			},
		},
		{
			Requirement: "protocol-cors-basepath", Invariant: "INV-PROTOCOL-CORS-BASEPATH-PUBLIC-001", Case: "error", State: "failure", Assertion: "public-image-exception-boundary",
			Sources: []string{"server/Auth.js:21", "server/Auth.js:36-52", "server/routers/ApiRouter.js:100-110", "server/controllers/LibraryItemController.js:401-433", "server/controllers/LibraryItemController.js:1121-1154"},
			Claim:   "The unauthenticated cover and author-image exceptions never expose an ebook: cover reads work without a token, while ebook downloads answer 401 even with the mobile CORS grant pre-added, and unknown image ids answer 404.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Get(t, "", cover(e)), http.StatusOK, "public cover read")
				for _, request := range []harness.Request{
					{Path: "/api/items/" + e.IDs["ebook"] + "/ebook"},
					{Path: "/api/items/" + e.IDs["ebook"] + "/ebook", Token: "not-a-token"},
				} {
					resp := e.Do(t, request)
					requireStatus(t, resp, http.StatusUnauthorized, "unauthenticated ebook download")
					if len(resp.Body) != 0 && resp.Body[0] == 'P' {
						t.Fatalf("unauthenticated ebook download returned file bytes: %.40q", resp.Body)
					}
				}
				requireStatus(t, e.Get(t, "", "/api/items/00000000-0000-4000-8000-000000000000/cover"), http.StatusNotFound, "unknown cover id")
				requireStatus(t, e.Get(t, "", "/api/authors/00000000-0000-4000-8000-000000000000/image"), http.StatusNotFound, "unknown author image id")
			},
		},
	}
}
