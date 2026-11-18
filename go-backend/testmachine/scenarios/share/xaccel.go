package share

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// encodedURIPath mirrors encodeUriPath in server/utils/fileUtils.js: the path is
// assigned to a URL pathname, which percent-encodes spaces and control bytes.
func encodedURIPath(path string) string {
	return (&url.URL{Path: path}).EscapedPath()
}

// acceleratedScenarios cover the X-Accel-Redirect branch of the cover and track
// routes on a dedicated server.
func acceleratedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r222", Invariant: "INV-R222-CONTRACT-001", Case: "edge", State: "initial", Assertion: "cover-x-accel-redirect",
			Sources: []string{"server/controllers/ShareController.js:168-172", "server/utils/fileUtils.js:442-448"},
			Claim:   "With USE_X_ACCEL set, the cover route answers 204 with an X-Accel-Redirect header naming the encoded cover path under the configured prefix and no body.",
			Run: func(t *testing.T, e *harness.Env) {
				server := harness.Start(t, harness.Options{Env: map[string]string{"USE_X_ACCEL": "/protected"}})
				value, cookie := openPublicSession(t, server, novelSlug, nil, "")
				if value.PlaybackSession.CoverPath == nil {
					t.Fatal("novel session must expose a cover path")
				}
				resp := coverRequest(t, server, novelSlug, cookie, nil)
				wantStatus(t, resp, http.StatusNoContent, "accelerated cover")
				want := encodedURIPath("/protected" + *value.PlaybackSession.CoverPath)
				if got := resp.Header.Get("X-Accel-Redirect"); got != want {
					t.Fatalf("X-Accel-Redirect = %q, want %q", got, want)
				}
				if len(resp.Body) != 0 {
					t.Fatalf("accelerated cover must have no body, got %d bytes", len(resp.Body))
				}
			},
		},
		{
			Requirement: "route-r221", Invariant: "INV-R221-CONTRACT-001", Case: "edge", State: "initial", Assertion: "track-x-accel-redirect",
			Sources: []string{"server/controllers/ShareController.js:198-205", "server/utils/fileUtils.js:442-448"},
			Claim:   "With USE_X_ACCEL set, the track route answers 204 with an X-Accel-Redirect header naming the encoded audio path of the requested index and no media body.",
			Run: func(t *testing.T, e *harness.Env) {
				server := harness.Start(t, harness.Options{Env: map[string]string{"USE_X_ACCEL": "/protected"}})
				value, cookie := openPublicSession(t, server, saga2Slug, nil, "")
				if len(value.PlaybackSession.AudioTracks) != 1 || value.PlaybackSession.AudioTracks[0].Metadata.Path == "" {
					t.Fatalf("saga2 session must expose one track path: %+v", value.PlaybackSession.AudioTracks)
				}
				resp := trackRequest(t, server, saga2Slug, "1", cookie, nil)
				wantStatus(t, resp, http.StatusNoContent, "accelerated track")
				want := encodedURIPath("/protected" + value.PlaybackSession.AudioTracks[0].Metadata.Path)
				if got := resp.Header.Get("X-Accel-Redirect"); got != want {
					t.Fatalf("X-Accel-Redirect = %q, want %q", got, want)
				}
				if len(resp.Body) != 0 {
					t.Fatalf("accelerated track must have no body, got %d bytes", len(resp.Body))
				}
			},
		},
	}
}
