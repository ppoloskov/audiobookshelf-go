package protocol

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/jpeg" // decode resized JPEG dimensions
	_ "image/png"  // decode resized PNG dimensions
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// progressDoc is the media-progress object Lissen caches.
type progressDoc struct {
	ID            string  `json:"id"`
	LibraryItemID string  `json:"libraryItemId"`
	CurrentTime   float64 `json:"currentTime"`
}

// progressPath is the per-item progress read of the mobile client.
func progressPath(itemID string) string {
	return "/api/me/progress/" + itemID
}

// mobileETagScenarios covers route-level conditional JSON reads.
func mobileETagScenarios() []harness.Scenario {
	const req = "flow-client-mobile-protocol"
	const inv = "INV-FLOW-CLIENT-MOBILE-ETAG-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "json-read-carries-an-etag-and-answers-304",
			Sources: []string{
				"server/routers/ApiRouter.js:176-186",
				"server/controllers/MeController.js:239-330",
				"go-backend/testmachine/contracts/mobile-client-audit.md:53",
			},
			Claim: "GET /api/me/progress/:itemId answers 200 with a weak ETag; repeating it with that exact If-None-Match answers 304 with no body and the same ETag; a stale If-None-Match answers 200 with the same progress value.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				e.Patch(t, token, progressPath(novelItemID), map[string]any{"currentTime": 0})
				first := e.Get(t, token, progressPath(novelItemID))
				wantStatus(t, first, http.StatusOK, "progress read")
				etag := first.Header.Get("ETag")
				if etag == "" {
					t.Fatalf("progress read has no ETag: %v", first.Header)
				}
				var doc progressDoc
				first.JSON(t, &doc)
				if doc.LibraryItemID != novelItemID || doc.CurrentTime != 0 {
					t.Fatalf("progress doc = %+v", doc)
				}
				notModified := e.Do(t, harness.Request{Path: progressPath(novelItemID), Token: token, Header: map[string]string{"If-None-Match": etag}})
				wantStatus(t, notModified, http.StatusNotModified, "conditional progress read")
				if len(notModified.Body) != 0 {
					t.Fatalf("304 carried %d body bytes", len(notModified.Body))
				}
				wantHeader(t, notModified, "ETag", etag, "304")
				stale := e.Do(t, harness.Request{Path: progressPath(novelItemID), Token: token, Header: map[string]string{"If-None-Match": `"stale"`}})
				wantStatus(t, stale, http.StatusOK, "stale validator")
				var repeated progressDoc
				stale.JSON(t, &repeated)
				if repeated.CurrentTime != doc.CurrentTime {
					t.Fatalf("stale validator returned %+v, want %+v", repeated, doc)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "progress-write-changes-the-validator-and-revalidates",
			Sources: []string{"server/routers/ApiRouter.js:176-186", "server/controllers/MeController.js:239-330"},
			Claim:   "After PATCH /api/me/progress/:itemId changes currentTime, a conditional read with the previous ETag answers 200 whose currentTime is the written value and whose ETag differs, and a conditional read with the new ETag answers 304.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				e.Patch(t, token, progressPath(novelItemID), map[string]any{"currentTime": 4})
				before := e.Get(t, token, progressPath(novelItemID))
				wantStatus(t, before, http.StatusOK, "progress before write")
				oldETag := before.Header.Get("ETag")
				wantStatus(t, e.Patch(t, token, progressPath(novelItemID), map[string]any{"currentTime": 12.5}), http.StatusOK, "progress write")
				after := e.Do(t, harness.Request{Path: progressPath(novelItemID), Token: token, Header: map[string]string{"If-None-Match": oldETag}})
				wantStatus(t, after, http.StatusOK, "old validator after write")
				var doc progressDoc
				after.JSON(t, &doc)
				if doc.CurrentTime != 12.5 {
					t.Fatalf("currentTime after write = %v, want 12.5", doc.CurrentTime)
				}
				if got := after.Header.Get("ETag"); got == "" || got == oldETag {
					t.Fatalf("ETag after write = %q, want a new validator", got)
				}
				notModified := e.Do(t, harness.Request{Path: progressPath(novelItemID), Token: token, Header: map[string]string{"If-None-Match": after.Header.Get("ETag")}})
				wantStatus(t, notModified, http.StatusNotModified, "new validator")
				if len(notModified.Body) != 0 {
					t.Fatalf("304 after write carried %d body bytes", len(notModified.Body))
				}
				e.Patch(t, token, progressPath(novelItemID), map[string]any{"currentTime": 0})
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "auth-failure-is-not-hidden-by-a-cached-validator",
			Sources: []string{"server/Server.js:347", "server/Auth.js:54-58", "server/routers/ApiRouter.js:176-186"},
			Claim:   "An unauthenticated read of the cached progress route with a valid If-None-Match answers 401 (never 304), carries no progress body, and creates no media-progress row.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				e.Patch(t, token, progressPath(novelItemID), map[string]any{"currentTime": 3})
				authed := e.Get(t, token, progressPath(novelItemID))
				wantStatus(t, authed, http.StatusOK, "authed progress read")
				etag := authed.Header.Get("ETag")
				before := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses")
				unauth := e.Do(t, harness.Request{Path: progressPath(e.IDs["saga1"]), Header: map[string]string{"If-None-Match": etag}})
				wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated conditional read")
				if bytes.Contains(unauth.Body, []byte(`"currentTime"`)) {
					t.Fatalf("401 body carried a progress document: %.200s", unauth.Body)
				}
				if after := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses"); after != before {
					t.Fatalf("rejected read changed media progress rows: %d -> %d", before, after)
				}
			},
		},
	}
}

// mobileCoverScenarios covers the cover query options the mobile clients send.
func mobileCoverScenarios() []harness.Scenario {
	const req = "flow-client-mobile-protocol"
	const inv = "INV-FLOW-CLIENT-MOBILE-COVER-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "cover-parameters-select-raw-resize-and-format",
			Sources: []string{
				"server/controllers/LibraryItemController.js:401-431",
				"server/managers/CacheManager.js:43-90",
				"server/utils/index.js:62-67,144-147",
				"server/routers/ApiRouter.js:112",
			},
			Claim: "With no cover parameters the response is a resized JPEG of the width sent; Accept: image/webp or format=webp selects image/webp with the WEBP signature; format=png selects a PNG whose decoded width is the requested width and format=jpeg a JPEG; raw=1 returns the original image/jpeg bytes and ignores width; a cached (non-raw) cover carries no ETag, so If-None-Match leaves it a 200.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				original := readFile(t, filepath.Join(e.Install, novelCoverPath))
				raw := e.Get(t, token, novelCoverURL()+"?raw=1")
				wantStatus(t, raw, http.StatusOK, "raw cover")
				wantHeader(t, raw, "Content-Type", "image/jpeg", "raw cover")
				wantBody(t, raw, original, "raw cover")
				rawWithWidth := e.Get(t, token, novelCoverURL()+"?raw=1&width=50&format=webp")
				wantStatus(t, rawWithWidth, http.StatusOK, "raw cover with width")
				wantHeader(t, rawWithWidth, "Content-Type", "image/jpeg", "raw cover with width")
				wantBody(t, rawWithWidth, original, "raw cover with width")

				cases := []struct {
					name, query string
					header      map[string]string
					mime        string
					width       int
				}{
					{"default jpeg", "?width=60", nil, "image/jpeg", 60},
					{"accept webp", "?width=61", map[string]string{"Accept": "image/webp,*/*"}, "image/webp", 0},
					{"format webp", "?width=62&format=webp", nil, "image/webp", 0},
					{"format png", "?width=63&format=png", nil, "image/png", 63},
					{"format jpeg", "?width=64&format=jpeg", nil, "image/jpeg", 64},
				}
				for _, test := range cases {
					resp := doFile(t, e, http.MethodGet, novelCoverURL()+test.query, token, test.header)
					wantStatus(t, resp, http.StatusOK, test.name)
					wantHeader(t, resp, "Content-Type", test.mime, test.name)
					if len(resp.Body) >= len(original) {
						t.Fatalf("%s: resized image is %d bytes, not smaller than %d", test.name, len(resp.Body), len(original))
					}
					switch test.mime {
					case "image/webp":
						if len(resp.Body) < 12 || string(resp.Body[:4]) != "RIFF" || string(resp.Body[8:12]) != "WEBP" {
							t.Fatalf("%s: body is not a WEBP image: %x", test.name, resp.Body[:min(12, len(resp.Body))])
						}
					case "image/jpeg", "image/png":
						config, format, err := image.DecodeConfig(bytes.NewReader(resp.Body))
						if err != nil {
							t.Fatalf("%s: decode image: %v", test.name, err)
						}
						if format != test.mime[len("image/"):] {
							t.Fatalf("%s: decoded format %q, want %q", test.name, format, test.mime[len("image/"):])
						}
						if config.Width != test.width {
							t.Fatalf("%s: decoded width %d, want %d", test.name, config.Width, test.width)
						}
					}
				}

				cached := e.Get(t, token, novelCoverURL()+"?width=60")
				wantStatus(t, cached, http.StatusOK, "cached cover")
				if etag := cached.Header.Get("ETag"); etag != "" {
					t.Fatalf("streamed cached cover unexpectedly has ETag %q", etag)
				}
				conditional := e.Do(t, harness.Request{Path: novelCoverURL() + "?width=60", Token: token, Header: map[string]string{"If-None-Match": `"any"`}})
				wantStatus(t, conditional, http.StatusOK, "conditional cached cover")
				if !bytes.Equal(conditional.Body, cached.Body) {
					t.Fatal("conditional cached cover changed the representation")
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "cover-rejects-invalid-format-and-unknown-item",
			Sources: []string{"server/controllers/LibraryItemController.js:411-431", "server/managers/CacheManager.js:43-53"},
			Claim:   "An unsupported cover format answers 400 and an unknown library item's raw cover answers 404, neither with image bytes nor a cache entry for the invalid request.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				badFormat := e.Get(t, token, novelCoverURL()+"?format=gif")
				wantStatus(t, badFormat, http.StatusBadRequest, "unsupported format")
				if bytes.HasPrefix(badFormat.Body, []byte{0xFF, 0xD8}) || bytes.HasPrefix(badFormat.Body, []byte("\x89PNG")) {
					t.Fatalf("400 response carried image bytes: %x", badFormat.Body[:min(8, len(badFormat.Body))])
				}
				unknown := e.Get(t, token, "/api/items/00000000-0000-4000-8000-000000000000/cover?raw=1")
				if unknown.Status != http.StatusNotFound && unknown.Status != http.StatusInternalServerError {
					t.Fatalf("unknown item raw cover: status = %d, want 404/500", unknown.Status)
				}
				var body map[string]any
				if json.Unmarshal(unknown.Body, &body) == nil && body != nil {
					t.Fatalf("unknown item raw cover returned JSON: %v", body)
				}
				noProgress := e.Get(t, token, progressPath(e.IDs["saga1"]))
				wantStatus(t, noProgress, http.StatusNotFound, "progress of an item without progress")
				if bytes.Contains(noProgress.Body, []byte(`"currentTime"`)) {
					t.Fatalf("404 progress body carried a record: %.200s", noProgress.Body)
				}
				missing := e.Patch(t, token, progressPath("00000000-0000-4000-8000-000000000000"), map[string]any{"currentTime": 1})
				wantStatus(t, missing, http.StatusNotFound, "progress write for an unknown item")
				if bytes.Contains(missing.Body, []byte(`"currentTime"`)) {
					t.Fatalf("404 progress write body carried a record: %.200s", missing.Body)
				}
			},
		},
	}
}
