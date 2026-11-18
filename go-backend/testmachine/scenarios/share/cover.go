package share

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// coverRequest reads the public share cover.
func coverRequest(t *testing.T, e *harness.Env, slug, cookie string, header map[string]string) harness.Response {
	t.Helper()
	merged := map[string]string{}
	for key, value := range header {
		merged[key] = value
	}
	if cookie != "" {
		merged["Cookie"] = cookieName + "=" + cookie
	}
	return e.Do(t, harness.Request{Path: publicSharePath(slug) + "/cover", Header: merged})
}

// publicCoverScenarios covers route-r222: GET /public/share/:slug/cover.
func publicCoverScenarios() []harness.Scenario {
	const req = "route-r222"
	const inv = "INV-R222-CONTRACT-001"
	novelCover := func(t *testing.T, e *harness.Env) []byte {
		return metadataFile(t, e, "items/"+fixtureOf(e).novel+"/cover.jpg")
	}
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "cover-serves-share-cover-image",
			Sources: []string{"server/controllers/ShareController.js:140-178", "server/objects/PlaybackSession.js:59-116"},
			Claim:   "The cookie-bound cover route returns the novel's cover JPEG bytes with image/jpeg, an ETag, Last-Modified and byte ranges, never a path outside the session's own cover.",
			Run: func(t *testing.T, e *harness.Env) {
				value, cookie := openPublicSession(t, e, novelSlug, nil, "")
				if value.PlaybackSession.CoverPath == nil {
					t.Fatal("novel session must expose a coverPath")
				}
				file := novelCover(t, e)
				resp := coverRequest(t, e, novelSlug, cookie, nil)
				wantStatus(t, resp, http.StatusOK, "novel cover")
				if got := resp.Header.Get("Content-Type"); got != "image/jpeg" {
					t.Fatalf("Content-Type = %q, want image/jpeg", got)
				}
				if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
					t.Fatalf("Accept-Ranges = %q, want bytes", got)
				}
				if resp.Header.Get("ETag") == "" || resp.Header.Get("Last-Modified") == "" {
					t.Fatalf("cover response must carry cache validators: %v", resp.Header)
				}
				if !bytes.Equal(resp.Body, file) {
					t.Fatalf("cover body differs from %s (%d vs %d bytes)", *value.PlaybackSession.CoverPath, len(resp.Body), len(file))
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "cover-repeats-and-answers-conditional",
			Sources: []string{"server/controllers/ShareController.js:172-177", "server/controllers/ShareController.js:140-160"},
			Claim:   "Repeated cover reads return identical bytes and ETag; If-None-Match with that ETag returns 304 with no body, while a different tag returns the image again.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				first := coverRequest(t, e, novelSlug, cookie, nil)
				wantStatus(t, first, http.StatusOK, "first cover read")
				etag := first.Header.Get("ETag")
				second := coverRequest(t, e, novelSlug, cookie, nil)
				wantStatus(t, second, http.StatusOK, "repeat cover read")
				if second.Header.Get("ETag") != etag || !bytes.Equal(second.Body, first.Body) {
					t.Fatal("repeated cover reads must be identical")
				}
				notModified := coverRequest(t, e, novelSlug, cookie, map[string]string{"If-None-Match": etag})
				wantStatus(t, notModified, http.StatusNotModified, "conditional cover read")
				if len(notModified.Body) != 0 {
					t.Fatalf("304 must have no body, got %d bytes", len(notModified.Body))
				}
				changed := coverRequest(t, e, novelSlug, cookie, map[string]string{"If-None-Match": `"not-the-etag"`})
				wantStatus(t, changed, http.StatusOK, "cover with a stale validator")
				if !bytes.Equal(changed.Body, first.Body) {
					t.Fatal("stale validator must return the image")
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "cover-denies-missing-session-and-no-cover",
			Sources: []string{"server/controllers/ShareController.js:141-165", "server/controllers/ShareController.js:162-166"},
			Claim:   "The cover route answers 404 without image bytes for no cookie, an unknown slug, a cookie of another share or an unknown session, and for an item whose session has no cover path.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				cases := []struct {
					name, slug, cookie string
				}{
					{"no cookie", novelSlug, ""},
					{"unknown slug", "no-such-share", cookie},
					{"foreign share", saga2Slug, cookie},
					{"unknown session", novelSlug, "00000000-0000-4000-8000-000000000000"},
				}
				for _, test := range cases {
					header := map[string]string{}
					if test.cookie != "" {
						header = cookieHeader(test.cookie)
					}
					resp := e.Do(t, harness.Request{Path: publicSharePath(test.slug) + "/cover", Header: header})
					wantStatus(t, resp, http.StatusNotFound, test.name)
					if len(resp.Body) > 100 {
						t.Fatalf("%s: denial must be a short message, got %d bytes", test.name, len(resp.Body))
					}
				}
				created := mustCreateShare(t, e, "share-slug-no-cover", mediaIDForItem(t, e, fx.ebook), 0, false)
				_, noCoverCookie := openPublicSession(t, e, created.Slug, nil, "")
				missing := coverRequest(t, e, created.Slug, noCoverCookie, nil)
				wantStatus(t, missing, http.StatusNotFound, "session without cover")
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "cleanup cover share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "cover-closes-with-share-deletion",
			Sources: []string{"server/managers/ShareManager.js:149-165", "server/controllers/ShareController.js:145-160"},
			Claim:   "A saga share's cover is served while the share is open and the same cookie gets 404 after the share is deleted.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-cover-life", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, false)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				open := coverRequest(t, e, created.Slug, cookie, nil)
				wantStatus(t, open, http.StatusOK, "cover while open")
				if got := open.Header.Get("Content-Type"); got != "image/jpeg" {
					t.Fatalf("saga cover Content-Type = %q", got)
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "delete cover share")
				closed := coverRequest(t, e, created.Slug, cookie, nil)
				wantStatus(t, closed, http.StatusNotFound, "cover after deletion")
				if len(closed.Body) > 100 {
					t.Fatalf("deleted cover denial must be a short message, got %d bytes", len(closed.Body))
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "cover-denies-after-expiry",
			Sources: []string{"server/controllers/ShareController.js:146-148", "server/managers/ShareManager.js:114-133"},
			Claim:   "After a share expires the cover route returns 404 for its old cookie and no image bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-cover-expiry", mediaIDForItem(t, e, fixtureOf(e).saga1), time.Now().Add(3000*time.Millisecond).UnixMilli(), false)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				before := coverRequest(t, e, created.Slug, cookie, nil)
				wantStatus(t, before, http.StatusOK, "cover before expiry")
				harness.Eventually(t, 15*time.Second, "cover share expiry", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				after := coverRequest(t, e, created.Slug, cookie, nil)
				wantStatus(t, after, http.StatusNotFound, "cover after expiry")
				if len(after.Body) > 100 {
					t.Fatalf("expired cover denial must be a short message, got %d bytes", len(after.Body))
				}
			},
		},
	}
}
