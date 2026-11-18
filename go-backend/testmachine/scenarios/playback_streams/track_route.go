package playback_streams

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// saga1Files are the three audio files of "The Beginning".
var saga1Files = []string{"01 - Part 1.mp3", "02 - Part 2.mp3", "03 - Part 3.mp3"}

// saga1AudioPath is the media folder of "The Beginning".
const saga1AudioPath = "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning"

// trackScenarios cover GET /public/session/:id/track/:index of a direct-play
// session as the web and mobile players request it.
func trackScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r225", "INV-R225-FILE-001", "normal", "initial", "public-track-direct-play-file",
			[]string{publicRouterSrc, getTrackSrc, audioTrackSrc},
			"GET /public/session/:id/track/1 of a direct-play book serves the first audio file as audio/mpeg with its exact bytes, without a bearer token; an m4b track answers audio/mp4.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startDirect(t, e, listener, e.IDs["saga1"], "", "track-initial", "audio/mpeg")

				resp := e.Get(t, "", trackPath(doc.ID, 1))
				requireStatus(t, resp, http.StatusOK, "track 1")
				requireEqual(t, "track content type", resp.Header.Get("Content-Type"), "audio/mpeg")
				requireEqual(t, "track accept-ranges", resp.Header.Get("Accept-Ranges"), "bytes")
				want := mediaFile(t, e, filepath.Join(saga1AudioPath, saga1Files[0]))
				if string(resp.Body) != string(want) {
					t.Fatalf("track %d bytes differ from %s: %d vs %d", 1, saga1Files[0], len(resp.Body), len(want))
				}

				novel := startDirect(t, e, listener, e.IDs["novel"], "", "track-novel", "audio/mp4")
				m4b := e.Get(t, "", trackPath(novel.ID, 1))
				requireStatus(t, m4b, http.StatusOK, "m4b track")
				requireEqual(t, "m4b content type", m4b.Header.Get("Content-Type"), "audio/mp4")
				wantM4B := mediaFile(t, e, filepath.Join("audiobooks", "Ada Example", "Single File Novel", "Single File Novel.m4b"))
				if string(m4b.Body) != string(wantM4B) {
					t.Fatal("m4b track bytes differ from the file")
				}

				closeQuietly(t, e, listener.Token, doc.ID)
				closeQuietly(t, e, listener.Token, novel.ID)
			}),

		scenario("route-r225", "INV-R225-RANGE-001", "edge", "repeat", "public-track-ranges-and-repeat",
			[]string{getTrackSrc, audioTrackSrc, webPlayerSrc},
			"Every track index serves its own file, a byte range answers 206 with exactly those bytes and a repeated request returns the same bytes.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startDirect(t, e, listener, e.IDs["saga1"], "", "track-range", "audio/mpeg")
				for index, name := range saga1Files {
					resp := e.Get(t, "", trackPath(doc.ID, index+1))
					requireStatus(t, resp, http.StatusOK, fmt.Sprintf("track %d", index+1))
					want := mediaFile(t, e, filepath.Join(saga1AudioPath, name))
					if string(resp.Body) != string(want) {
						t.Fatalf("track %d does not serve %s", index+1, name)
					}
				}

				full := e.Get(t, "", trackPath(doc.ID, 1))
				partial := e.Do(t, harness.Request{Path: trackPath(doc.ID, 1), Header: map[string]string{"Range": "bytes=2000-3999"}})
				requireStatus(t, partial, http.StatusPartialContent, "track range")
				requireEqual(t, "track content range", partial.Header.Get("Content-Range"), fmt.Sprintf("bytes 2000-3999/%d", len(full.Body)))
				if string(partial.Body) != string(full.Body[2000:4000]) {
					t.Fatal("track range returned other bytes")
				}

				second := mediaFile(t, e, filepath.Join(saga1AudioPath, saga1Files[1]))
				repeat := e.Do(t, harness.Request{Path: trackPath(doc.ID, 2), Header: map[string]string{"Range": "bytes=0-1023"}})
				requireStatus(t, repeat, http.StatusPartialContent, "track repeat range")
				if string(repeat.Body) != string(second[:1024]) {
					t.Fatalf("repeated range returned %d bytes, want the first 1024 of %s", len(repeat.Body), saga1Files[1])
				}

				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r225", "INV-R225-REJECT-001", "error", "failure", "public-track-rejections-change-nothing",
			[]string{getTrackSrc},
			"An unparsable index answers 400, a missing track and an unknown session answer 404, and the existing track keeps being served unchanged.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startDirect(t, e, listener, e.IDs["saga1"], "", "track-reject", "audio/mpeg")
				requireStatus(t, e.Get(t, "", fmt.Sprintf("/public/session/%s/track/abc", doc.ID)), http.StatusBadRequest, "unparsable index")
				// The index is parsed before the session is looked up: an
				// unparsable index on an unknown session is 400, not 404.
				requireStatus(t, e.Get(t, "", "/public/session/00000000-0000-4000-8000-000000000000/track/abc"), http.StatusBadRequest, "unparsable index of an unknown session")
				requireStatus(t, e.Get(t, "", trackPath(doc.ID, 0)), http.StatusNotFound, "book index 0")
				requireStatus(t, e.Get(t, "", trackPath(doc.ID, 4)), http.StatusNotFound, "index above the tracks")
				requireStatus(t, e.Get(t, "", trackPath("00000000-0000-4000-8000-000000000000", 1)), http.StatusNotFound, "unknown session")

				after := e.Get(t, "", trackPath(doc.ID, 1))
				requireStatus(t, after, http.StatusOK, "track after rejections")
				want := mediaFile(t, e, filepath.Join(saga1AudioPath, saga1Files[0]))
				if string(after.Body) != string(want) {
					t.Fatal("a rejected request changed the served track")
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r225", "INV-R225-PODCAST-001", "edge", "initial", "public-track-podcast-index-zero-fallback",
			[]string{getTrackSrc, "server/controllers/SessionController.js:305-310"},
			"For a podcast episode session index 0 and index 1 both serve the episode file, so a client that passes either number plays it.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startDirect(t, e, listener, e.IDs["podcast"], e.IDs["ep3"], "track-podcast", "audio/mpeg")
				want := mediaFile(t, e, filepath.Join("podcasts", "Fixture Cast", "episode-3.mp3"))
				for _, index := range []int{0, 1} {
					resp := e.Get(t, "", trackPath(doc.ID, index))
					requireStatus(t, resp, http.StatusOK, fmt.Sprintf("podcast index %d", index))
					requireEqual(t, "podcast track content type", resp.Header.Get("Content-Type"), "audio/mpeg")
					if string(resp.Body) != string(want) {
						t.Fatalf("podcast index %d does not serve episode-3.mp3", index)
					}
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r225", "INV-R225-TRANSCODE-001", "normal", "changed", "public-track-transcode-redirects",
			[]string{getTrackSrc, playMethodSrc, hlsRouterSrc},
			"A track request of a transcoded session answers 302 with the HLS playlist as location, which then serves the playlist.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startStream(t, e, listener, e.IDs["saga1"], "", "track-transcode")
				resp := e.Get(t, "", trackPath(doc.ID, 1))
				requireStatus(t, resp, http.StatusFound, "transcode track")
				requireEqual(t, "redirect location", resp.Header.Get("Location"), hlsPath(doc.ID, "output.m3u8"))
				requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusOK, "redirected playlist")
				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r225", "INV-R225-FILE-001", "edge", "changed", "public-track-x-accel-redirect",
			[]string{getTrackSrc, "server/Server.js:86"},
			"With USE_X_ACCEL set, a direct-play track request answers 204 with an X-Accel-Redirect header holding the configured prefix and the percent-encoded audio file path instead of the bytes.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: map[string]string{"USE_X_ACCEL": "/protected"}})
				listener := fixtureActor(t, own, "listener")
				doc := startDirect(t, own, listener, own.IDs["saga1"], "", "track-x-accel", "audio/mpeg")
				resp := own.Get(t, "", trackPath(doc.ID, 1))
				requireStatus(t, resp, http.StatusNoContent, "x-accel track")
				location := resp.Header.Get("X-Accel-Redirect")
				want := "/protected" + strings.ReplaceAll(mediaPath(own, filepath.Join(saga1AudioPath, saga1Files[0])), " ", "%20")
				requireEqual(t, "x-accel location", location, want)
				if len(resp.Body) != 0 {
					t.Fatalf("x-accel track returned %d bytes", len(resp.Body))
				}
				closeQuietly(t, own, listener.Token, doc.ID)
			}),

		scenario("route-r225", "INV-R225-CLOSED-001", "normal", "changed", "public-track-gone-after-close",
			[]string{getTrackSrc, sessionManager},
			"After the session is closed a track request answers 404.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startDirect(t, e, listener, e.IDs["saga1"], "", "track-closed", "audio/mpeg")
				requireStatus(t, e.Get(t, "", trackPath(doc.ID, 1)), http.StatusOK, "open track")
				closeQuietly(t, e, listener.Token, doc.ID)
				requireStatus(t, e.Get(t, "", trackPath(doc.ID, 1)), http.StatusNotFound, "track after close")
			}),
	}
}
