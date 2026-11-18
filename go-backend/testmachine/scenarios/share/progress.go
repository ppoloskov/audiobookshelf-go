package share

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// progressRequest patches the public share progress.
func progressRequest(t *testing.T, e *harness.Env, slug, cookie string, body any) harness.Response {
	t.Helper()
	header := map[string]string{}
	if cookie != "" {
		header = cookieHeader(cookie)
	}
	return e.Do(t, harness.Request{Method: http.MethodPatch, Path: publicSharePath(slug) + "/progress", Header: header, JSON: body})
}

// publicProgressScenarios covers route-r224: PATCH /public/share/:slug/progress.
func publicProgressScenarios() []harness.Scenario {
	const req = "route-r224"
	const inv = "INV-R224-CONTRACT-001"
	readTime := func(t *testing.T, e *harness.Env, slug, cookie string) float64 {
		value, _ := openPublicSession(t, e, slug, nil, cookie)
		return value.PlaybackSession.CurrentTime
	}
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "progress-persists-in-share-session",
			Sources: []string{"server/controllers/ShareController.js:285-315", "server/controllers/ShareController.js:46-70"},
			Claim:   "PATCH progress answers 204 with no body for the share session cookie and the next slug read returns the same session id with the new currentTime, without persisting a playbackSessions row.",
			Run: func(t *testing.T, e *harness.Env) {
				before, cookie := openPublicSession(t, e, novelSlug, nil, "")
				sessionID := before.PlaybackSession.ID
				rowsBefore := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				resp := progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": 12.5})
				wantStatus(t, resp, http.StatusNoContent, "progress patch")
				if len(resp.Body) != 0 {
					t.Fatalf("204 must have no body, got %d bytes", len(resp.Body))
				}
				after, _ := openPublicSession(t, e, novelSlug, nil, cookie)
				if after.PlaybackSession.ID != sessionID || after.PlaybackSession.CurrentTime != 12.5 || after.PlaybackSession.Duration != 45 {
					t.Fatalf("progress not preserved on the session: %+v", after.PlaybackSession)
				}
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); rows != rowsBefore {
					t.Fatalf("share progress must not persist playbackSessions rows: %d -> %d", rowsBefore, rows)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "progress-clamps-and-repeats",
			Sources: []string{"server/controllers/ShareController.js:287-303", "server/controllers/ShareController.js:310-314"},
			Claim:   "A repeated identical progress patch changes nothing; a numeric string is accepted, zero is stored, and a currentTime beyond the duration clamps to the session duration.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				for _, value := range []any{7, 7} {
					wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": value}), http.StatusNoContent, "repeat progress")
				}
				if got := readTime(t, e, novelSlug, cookie); got != 7 {
					t.Fatalf("repeated progress currentTime = %v, want 7", got)
				}
				wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": "5.5"}), http.StatusNoContent, "numeric string progress")
				if got := readTime(t, e, novelSlug, cookie); got != 5.5 {
					t.Fatalf("numeric string progress currentTime = %v, want 5.5", got)
				}
				wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": 0}), http.StatusNoContent, "zero progress")
				if got := readTime(t, e, novelSlug, cookie); got != 0 {
					t.Fatalf("zero progress currentTime = %v", got)
				}
				wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": 9999}), http.StatusNoContent, "progress beyond duration")
				if got := readTime(t, e, novelSlug, cookie); got != 45 {
					t.Fatalf("progress must clamp to the duration 45, got %v", got)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "progress-rejects-invalid-time-and-session",
			Sources: []string{"server/controllers/ShareController.js:286-306"},
			Claim:   "Progress rejects a missing cookie with 404; rejects negative, null, absent or non-numeric currentTime with 400 before any share lookup; and answers 404 for an unknown slug or a foreign session.",
			Run: func(t *testing.T, e *harness.Env) {
				_, novelCookie := openPublicSession(t, e, novelSlug, nil, "")
				_, saga2Cookie := openPublicSession(t, e, saga2Slug, nil, "")
				wantStatus(t, progressRequest(t, e, novelSlug, "", map[string]any{"currentTime": 1}), http.StatusNotFound, "no cookie")

				invalid := []struct {
					name string
					body any
				}{
					{"negative", map[string]any{"currentTime": -1}},
					{"null", map[string]any{"currentTime": nil}},
					{"absent", map[string]any{}},
					{"text", map[string]any{"currentTime": "abc"}},
				}
				for _, test := range invalid {
					wantStatus(t, progressRequest(t, e, novelSlug, novelCookie, test.body), http.StatusBadRequest, test.name)
					wantStatus(t, progressRequest(t, e, "no-such-share", novelCookie, test.body), http.StatusBadRequest, test.name+" before share lookup")
				}
				wantStatus(t, progressRequest(t, e, "no-such-share", novelCookie, map[string]any{"currentTime": 1}), http.StatusNotFound, "unknown slug")
				wantStatus(t, progressRequest(t, e, novelSlug, saga2Cookie, map[string]any{"currentTime": 1}), http.StatusNotFound, "foreign session")
				wantStatus(t, progressRequest(t, e, novelSlug, "00000000-0000-4000-8000-000000000000", map[string]any{"currentTime": 1}), http.StatusNotFound, "unknown session")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "progress-belongs-to-the-current-session",
			Sources: []string{"server/controllers/ShareController.js:48-68", "server/controllers/ShareController.js:299-306"},
			Claim:   "After the cookie switches to another share, progress for the first share is rejected with 404, the new share's session keeps its own progress, and returning to the first share starts at zero.",
			Run: func(t *testing.T, e *harness.Env) {
				first, cookie := openPublicSession(t, e, novelSlug, nil, "")
				wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": 20}), http.StatusNoContent, "novel progress")
				if got := readTime(t, e, novelSlug, cookie); got != 20 {
					t.Fatalf("novel progress = %v", got)
				}
				switched, _ := openPublicSession(t, e, saga2Slug, nil, cookie)
				if switched.PlaybackSession.CurrentTime != 0 {
					t.Fatalf("new saga2 session must start at zero: %+v", switched.PlaybackSession)
				}
				wantStatus(t, progressRequest(t, e, novelSlug, cookie, map[string]any{"currentTime": 5}), http.StatusNotFound, "progress of the closed session")
				wantStatus(t, progressRequest(t, e, saga2Slug, cookie, map[string]any{"currentTime": 9}), http.StatusNoContent, "saga2 progress")
				if got := readTime(t, e, saga2Slug, cookie); got != 9 {
					t.Fatalf("saga2 progress = %v", got)
				}
				if first.PlaybackSession.ID == switched.PlaybackSession.ID {
					t.Fatal("switching shares must create a new session")
				}
				back, _ := openPublicSession(t, e, novelSlug, nil, cookie)
				if back.PlaybackSession.CurrentTime != 0 {
					t.Fatalf("recreated novel session must start at zero, got %v", back.PlaybackSession.CurrentTime)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "progress-denies-after-expiry",
			Sources: []string{"server/controllers/ShareController.js:296-306", "server/managers/ShareManager.js:149-165"},
			Claim:   "A progress write is accepted before a share expires and rejected with 404 after the scheduled removal, with no persisted progress row.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-progress-expiry", mediaIDForItem(t, e, fixtureOf(e).disposable), time.Now().Add(3000*time.Millisecond).UnixMilli(), true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				wantStatus(t, progressRequest(t, e, created.Slug, cookie, map[string]any{"currentTime": 2}), http.StatusNoContent, "progress before expiry")
				harness.Eventually(t, 15*time.Second, "progress share expiry", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				wantStatus(t, progressRequest(t, e, created.Slug, cookie, map[string]any{"currentTime": 3}), http.StatusNotFound, "progress after expiry")
				wantStatus(t, publicGet(t, e, created.Slug, nil, cookie, nil), http.StatusNotFound, "slug after expiry")
			},
		},
	}
}
