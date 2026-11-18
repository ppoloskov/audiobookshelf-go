package share

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// trackRequest reads one public share track.
func trackRequest(t *testing.T, e *harness.Env, slug string, index, cookie string, header map[string]string) harness.Response {
	t.Helper()
	merged := map[string]string{}
	for key, value := range header {
		merged[key] = value
	}
	if cookie != "" {
		merged["Cookie"] = cookieName + "=" + cookie
	}
	return e.Do(t, harness.Request{Path: publicSharePath(slug) + "/track/" + index, Header: merged})
}

// publicTrackScenarios covers route-r221: GET /public/share/:slug/track/:index.
func publicTrackScenarios() []harness.Scenario {
	const req = "route-r221"
	const inv = "INV-R221-CONTRACT-001"
	novelFile := func(t *testing.T, e *harness.Env) []byte {
		return mediaFile(t, e, libraryPath("audiobooks", "Ada Example", "Single File Novel", "Single File Novel.m4b"))
	}
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "track-serves-audio-file-with-range-support",
			Sources: []string{"server/controllers/ShareController.js:180-224", "server/utils/fileUtils.js:424-429", "server/controllers/ShareController.js:100-113"},
			Claim:   "The cookie-bound track route serves the exact novel audio file with audio/mp4, its byte length, Accept-Ranges and no auth header; the track index and contentUrl in the session point at the same file.",
			Run: func(t *testing.T, e *harness.Env) {
				value, cookie := openPublicSession(t, e, novelSlug, nil, "")
				file := novelFile(t, e)
				resp := trackRequest(t, e, novelSlug, "1", cookie, nil)
				wantStatus(t, resp, http.StatusOK, "novel track 1")
				if got := resp.Header.Get("Content-Type"); got != "audio/mp4" {
					t.Fatalf("Content-Type = %q, want audio/mp4", got)
				}
				if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(file)) {
					t.Fatalf("Content-Length = %q, want %d", got, len(file))
				}
				if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
					t.Fatalf("Accept-Ranges = %q, want bytes", got)
				}
				if !bytes.Equal(resp.Body, file) {
					t.Fatalf("track body differs from the media file (%d vs %d bytes)", len(resp.Body), len(file))
				}
				if len(value.PlaybackSession.AudioTracks) != 1 || value.PlaybackSession.AudioTracks[0].ContentURL != "/audiobookshelf/public/share/"+novelSlug+"/track/1" {
					t.Fatalf("session track contentUrl wrong: %+v", value.PlaybackSession.AudioTracks)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "track-repeats-and-honours-byte-ranges",
			Sources: []string{"server/controllers/ShareController.js:180-224", "server/controllers/ShareController.js:210-219"},
			Claim:   "A repeated track read returns the same bytes, a padded index (\"01\") addresses track 1, a closed byte range answers 206 with the exact slice, and a range at or past the end answers 416 with the file size.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				file := novelFile(t, e)
				first := trackRequest(t, e, novelSlug, "1", cookie, nil)
				repeat := trackRequest(t, e, novelSlug, "1", cookie, nil)
				wantStatus(t, repeat, http.StatusOK, "repeated track read")
				if !bytes.Equal(first.Body, repeat.Body) || !bytes.Equal(repeat.Body, file) {
					t.Fatal("repeated track reads must return the same media bytes")
				}
				padded := trackRequest(t, e, novelSlug, "01", cookie, nil)
				wantStatus(t, padded, http.StatusOK, "padded track index")
				if !bytes.Equal(padded.Body, file) {
					t.Fatal("padded index must address the same track")
				}

				partial := trackRequest(t, e, novelSlug, "1", cookie, map[string]string{"Range": "bytes=10-19"})
				wantStatus(t, partial, http.StatusPartialContent, "byte range")
				if got, want := partial.Header.Get("Content-Range"), "bytes 10-19/"+strconv.Itoa(len(file)); got != want {
					t.Fatalf("Content-Range = %q, want %q", got, want)
				}
				if !bytes.Equal(partial.Body, file[10:20]) {
					t.Fatalf("range body differs: %q vs %q", partial.Body, file[10:20])
				}
				last := trackRequest(t, e, novelSlug, "1", cookie, map[string]string{"Range": "bytes=5-"})
				wantStatus(t, last, http.StatusPartialContent, "open-ended range")
				if !bytes.Equal(last.Body, file[5:]) {
					t.Fatal("open-ended range body differs")
				}
				outside := trackRequest(t, e, novelSlug, "1", cookie, map[string]string{"Range": "bytes=" + strconv.Itoa(len(file)) + "-"})
				wantStatus(t, outside, http.StatusRequestedRangeNotSatisfiable, "range beyond the file")
				if got, want := outside.Header.Get("Content-Range"), "bytes */"+strconv.Itoa(len(file)); got != want {
					t.Fatalf("416 Content-Range = %q, want %q", got, want)
				}
				if len(outside.Body) > 1000 || (len(outside.Body) > 0 && bytes.HasPrefix(file, outside.Body)) {
					t.Fatalf("416 must not carry the media bytes, got %d", len(outside.Body))
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "track-denies-missing-session-and-unknown-index",
			Sources: []string{"server/controllers/ShareController.js:181-208"},
			Claim:   "The track route answers 404 with no media bytes when the cookie is absent, the slug is unknown, the cookie belongs to another share, or the index is unknown/non-numeric/negative.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				cases := []struct {
					name, path, cookie string
				}{
					{"no cookie", publicSharePath(novelSlug) + "/track/1", ""},
					{"unknown slug", publicSharePath("no-such-share") + "/track/1", cookie},
					{"foreign share", publicSharePath(saga2Slug) + "/track/1", cookie},
					{"unknown index", publicSharePath(novelSlug) + "/track/2", cookie},
					{"non-numeric index", publicSharePath(novelSlug) + "/track/abc", cookie},
					{"negative index", publicSharePath(novelSlug) + "/track/-1", cookie},
				}
				for _, test := range cases {
					header := map[string]string{}
					if test.cookie != "" {
						header = cookieHeader(test.cookie)
					}
					resp := e.Do(t, harness.Request{Path: test.path, Header: header})
					wantStatus(t, resp, http.StatusNotFound, test.name)
					if len(resp.Body) == 0 || len(resp.Body) > 100 {
						t.Fatalf("%s: denial body must be a short message, got %d bytes", test.name, len(resp.Body))
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "track-closes-with-share-deletion",
			Sources: []string{"server/controllers/ShareController.js:208-223", "server/managers/ShareManager.js:149-165"},
			Claim:   "A multi-track saga share serves every track by index, and deleting the share closes the session so the same cookie gets 404 from the track route.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				created := mustCreateShare(t, e, "share-slug-tracks", mediaIDForItem(t, e, fx.saga1), 0, true)
				session, cookie := openPublicSession(t, e, created.Slug, nil, "")
				if len(session.PlaybackSession.AudioTracks) != 3 {
					t.Fatalf("saga session must expose three tracks: %+v", session.PlaybackSession.AudioTracks)
				}
				want := map[int]string{
					1: "01 - Part 1.mp3",
					2: "02 - Part 2.mp3",
					3: "03 - Part 3.mp3",
				}
				for index, name := range want {
					resp := trackRequest(t, e, created.Slug, strconv.Itoa(index), cookie, nil)
					wantStatus(t, resp, http.StatusOK, "saga track "+name)
					if got := resp.Header.Get("Content-Type"); got != "audio/mpeg" {
						t.Fatalf("saga track %d Content-Type = %q, want audio/mpeg", index, got)
					}
					file := mediaFile(t, e, libraryPath("audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning", name))
					if !bytes.Equal(resp.Body, file) {
						t.Fatalf("saga track %d bytes differ", index)
					}
					track := session.PlaybackSession.AudioTracks[index-1]
					if track.Index != index || track.Title != name || track.StartOffset < 0 {
						t.Fatalf("track %d metadata wrong: %+v", index, track)
					}
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "delete saga share")
				closed := trackRequest(t, e, created.Slug, "2", cookie, nil)
				wantStatus(t, closed, http.StatusNotFound, "track after share deletion")
				if len(closed.Body) == 0 || len(closed.Body) > 100 {
					t.Fatalf("closed track denial must be a short message, got %d bytes", len(closed.Body))
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "track-denies-after-expiry",
			Sources: []string{"server/controllers/ShareController.js:181-188", "server/managers/ShareManager.js:114-133"},
			Claim:   "An open track session stops serving when its share expires: after the scheduled removal the same cookie gets 404 and no media bytes, and the share row is gone.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-track-expiry", mediaIDForItem(t, e, fixtureOf(e).disposable), time.Now().Add(3000*time.Millisecond).UnixMilli(), true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				before := trackRequest(t, e, created.Slug, "1", cookie, nil)
				wantStatus(t, before, http.StatusOK, "track before expiry")
				if got := before.Header.Get("Content-Type"); got != "audio/mpeg" {
					t.Fatalf("track before expiry Content-Type = %q", got)
				}
				harness.Eventually(t, 15*time.Second, "expiring share row to be removed", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				after := trackRequest(t, e, created.Slug, "1", cookie, nil)
				wantStatus(t, after, http.StatusNotFound, "track after expiry")
				if len(after.Body) > 100 {
					t.Fatalf("expired track denial must be a short message, got %d bytes", len(after.Body))
				}
			},
		},
	}
}
