package playback_streams

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// transcodeToAC3 encodes the fixture mp3 as a mono 96 kbit/s AC3 file with the
// configured ffmpeg, so the stream start path takes its AC3 bitrate/channel
// branch.
func transcodeToAC3(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		t.Fatal("FFMPEG_PATH is not set")
	}
	source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3")
	output := filepath.Join(t.TempDir(), "ac3-stream.ac3")
	command := exec.Command(ffmpeg, "-loglevel", "error", "-y", "-i", source, "-c:a", "ac3", "-b:a", "96k", "-ac", "1", output)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("encode AC3 fixture: %v: %s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// hlsScenarios cover GET /hls/:stream/:file: the generated playlist, the
// segments with their MIME type and byte ranges, the capability URL (no bearer
// token), the invalid-file rejections and the state after a reset or close.
func hlsScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r219", "INV-R219-FILE-001", "normal", "initial", "hls-playlist-and-first-segment",
			[]string{hlsRouterSrc, hlsPlaylistGen, serverRoutesSrc, webSocketSrc},
			"GET /hls/:stream/output.m3u8 serves the 11-segment VOD playlist of a transcoded book as application/vnd.apple.mpegurl, GET /hls/:stream/output-0.ts serves the segment as video/mp2t without a bearer token, and a byte range answers 206 with the requested prefix.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startStream(t, e, listener, e.IDs["saga1"], "", "hls-initial")

				playlist := getHLS(t, e, doc.ID, "output.m3u8", nil)
				requireStatus(t, playlist, http.StatusOK, "playlist")
				requireEqual(t, "playlist content type", playlist.Header.Get("Content-Type"), "application/vnd.apple.mpegurl")
				body := string(playlist.Body)
				for _, header := range []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:6", "#EXT-X-MEDIA-SEQUENCE:0", "#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST"} {
					if !strings.Contains(body, header) {
						t.Fatalf("playlist lacks %q:\n%s", header, body)
					}
				}
				files := playlistSegments(body)
				if len(files) != saga1Segments {
					t.Fatalf("playlist lists %d segments, want %d: %v", len(files), saga1Segments, files)
				}
				for i, file := range files {
					if got := segmentNumber(t, file); got != i {
						t.Fatalf("playlist segment %d is %q", i, file)
					}
				}

				waitSegment(t, e, doc.ID, 0)
				segment := getHLS(t, e, doc.ID, "output-0.ts", nil)
				requireStatus(t, segment, http.StatusOK, "first segment")
				requireEqual(t, "segment content type", segment.Header.Get("Content-Type"), "video/mp2t")
				if len(segment.Body) == 0 {
					t.Fatal("first segment is empty")
				}
				requireEqual(t, "segment accept-ranges", segment.Header.Get("Accept-Ranges"), "bytes")
				// The capability URL needs no bearer token; the owner's token works too.
				withToken := e.Do(t, harness.Request{Path: hlsPath(doc.ID, "output-0.ts"), Token: listener.Token})
				requireStatus(t, withToken, http.StatusOK, "segment with token")
				if string(withToken.Body) != string(segment.Body) {
					t.Fatal("segment bytes depend on the bearer token")
				}

				partial := getHLS(t, e, doc.ID, "output-0.ts", map[string]string{"Range": "bytes=0-99"})
				requireStatus(t, partial, http.StatusPartialContent, "range request")
				requireEqual(t, "content range", partial.Header.Get("Content-Range"), fmt.Sprintf("bytes 0-99/%d", len(segment.Body)))
				if len(partial.Body) != 100 || string(partial.Body) != string(segment.Body[:100]) {
					t.Fatalf("range returned %d bytes, want the first 100 of the segment", len(partial.Body))
				}

				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r219", "INV-R219-REJECT-001", "error", "failure", "hls-invalid-file-rejections-change-nothing",
			[]string{hlsRouterSrc, segmentParseSrc},
			"GET /hls/:stream/:file rejects an unsupported extension and a path outside the stream folder with 400 and an unknown stream with 404, without emitting stream_reset and without affecting a live stream.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener, "finisher")
				doc := startStream(t, e, listener, e.IDs["saga1"], "", "hls-reject")
				a.reset()

				requireStatus(t, getHLS(t, e, doc.ID, "output.txt", nil), http.StatusBadRequest, "text file")
				requireStatus(t, getHLS(t, e, doc.ID, "files.txt", nil), http.StatusBadRequest, "concat file")
				requireStatus(t, getHLS(t, e, doc.ID, "..%2Ffiles.txt", nil), http.StatusBadRequest, "path traversal")
				requireStatus(t, getHLS(t, e, "00000000-0000-4000-8000-000000000000", "output.m3u8", nil), http.StatusNotFound, "unknown stream")
				// The stream lookup precedes the file validation: an unknown
				// stream with an invalid file is 404, not 400.
				requireStatus(t, getHLS(t, e, "00000000-0000-4000-8000-000000000000", "files.txt", nil), http.StatusNotFound, "unknown stream with an invalid file")
				waitSegment(t, e, doc.ID, 0)
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusOK, "stream after rejections")
				a.requireNobody(t, "stream_reset", 500*time.Millisecond)

				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r219", "INV-R219-RANGE-001", "edge", "repeat", "hls-repeat-and-beyond-eof-range",
			[]string{hlsRouterSrc, webPlayerSrc},
			"Repeating a segment request returns the same bytes, a range inside the segment returns exactly those bytes, and a range starting after the end answers 416 with the full size.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startStream(t, e, listener, e.IDs["saga1"], "", "hls-range")
				first := waitSegment(t, e, doc.ID, 0)

				second := getHLS(t, e, doc.ID, "output-0.ts", nil)
				requireStatus(t, second, http.StatusOK, "repeated segment")
				if string(second.Body) != string(first) {
					t.Fatal("repeated segment request returned different bytes")
				}

				partial := getHLS(t, e, doc.ID, "output-0.ts", map[string]string{"Range": "bytes=1000-1999"})
				requireStatus(t, partial, http.StatusPartialContent, "inner range")
				requireEqual(t, "inner content range", partial.Header.Get("Content-Range"), fmt.Sprintf("bytes 1000-1999/%d", len(first)))
				if string(partial.Body) != string(first[1000:2000]) {
					t.Fatal("inner range returned other bytes")
				}

				beyond := getHLS(t, e, doc.ID, "output-0.ts", map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(first)+10)})
				requireStatus(t, beyond, http.StatusRequestedRangeNotSatisfiable, "range beyond end")
				requireEqual(t, "unsatisfied content range", beyond.Header.Get("Content-Range"), fmt.Sprintf("bytes */%d", len(first)))

				playlistAgain := getHLS(t, e, doc.ID, "output.m3u8", nil)
				requireStatus(t, playlistAgain, http.StatusOK, "repeated playlist")
				requireEqual(t, "repeated playlist body", string(playlistAgain.Body), string(waitPlaylist(t, e, doc.ID)))

				closeQuietly(t, e, listener.Token, doc.ID)
			}),

		scenario("route-r219", "INV-R219-RESET-001", "normal", "changed", "hls-backward-segment-restarts-stream",
			[]string{hlsRouterSrc, hlsStreamSrc, streamResetSrc, socketClientSrc},
			"Requesting output-1 of a session resumed at 44 s answers 404 and broadcasts stream_reset {startTime 6, streamId} to the watching owner, after which the retranscode from the beginning serves output-0 and output-1 non-empty.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "hls-reset-user")
				setProgress(t, e, user, e.IDs["novel"], 44, novelDuration)
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["novel"], "", "hls-reset")
				if doc.StartTime != 44 {
					t.Fatalf("start time %v, want the resume position 44", doc.StartTime)
				}
				// The playlist always lists every segment from 0; only the
				// transcode starts at segment 2.
				playlist := waitPlaylist(t, e, doc.ID)
				if len(playlistSegments(playlist)) != novelSegments {
					t.Fatalf("playlist lists %d segments, want %d", len(playlistSegments(playlist)), novelSegments)
				}
				harness.Eventually(t, 30*time.Second, "the first produced segment", func() bool {
					return getHLS(t, e, doc.ID, "output-2.ts", nil).Status == http.StatusOK
				})
				// Segment 1 is before the start number 2, so its reset start
				// time is 6 s – truthy, unlike the silent segment 0 reset.
				before := getHLS(t, e, doc.ID, "output-1.ts", nil)
				requireStatus(t, before, http.StatusNotFound, "segment before the start offset")
				payload := eventFirst[resetDoc](t, a.owner1, a.marks[0], "stream_reset", 10*time.Second)
				requireEqual(t, "stream_reset payload", payload, resetDoc{StartTime: segmentLength, StreamID: doc.ID})
				eventFirst[resetDoc](t, a.owner2, a.marks[1], "stream_reset", 10*time.Second)
				for _, number := range []int{0, 1} {
					after := waitSegment(t, e, doc.ID, number)
					if len(after) == 0 {
						t.Fatalf("retranscoded segment %d is empty", number)
					}
				}
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("route-r219", "INV-R219-FILE-001", "edge", "initial", "hls-ac3-item-transcodes",
			[]string{hlsRouterSrc, streamStartSrc},
			"An AC3 (.mka) audiobook is transcoded through the HLS path and serves a 200 playlist and a non-empty first segment.",
			func(t *testing.T, e *harness.Env) {
				quietWatchers(t, e)
				item := newAudiobook(t, e, "AC3 Stream", "AC3 Stream.mka", transcodeToAC3(t, e))
				user := createUser(t, e, "hls-ac3-user")
				doc := startStream(t, e, user, item, "", "hls-ac3")
				waitPlaylist(t, e, doc.ID)
				if len(waitSegment(t, e, doc.ID, 0)) == 0 {
					t.Fatal("AC3 transcode produced an empty first segment")
				}
				requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusOK, "AC3 playlist")
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("route-r219", "INV-R219-CLOSED-001", "edge", "changed", "hls-files-gone-after-close",
			[]string{hlsRouterSrc, streamCloseSrc, sessionRemoveSrc},
			"After the session is closed the playlist and the segments answer 404 and the stream folder no longer exists.",
			func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := startStream(t, e, listener, e.IDs["saga1"], "", "hls-closed")
				waitPlaylist(t, e, doc.ID)
				waitSegment(t, e, doc.ID, 0)
				if !streamFolderExists(e, doc.ID) {
					t.Fatal("a live stream must have a stream folder")
				}
				closeQuietly(t, e, listener.Token, doc.ID)
				harness.Eventually(t, 10*time.Second, "the stream folder to disappear", func() bool {
					return !streamFolderExists(e, doc.ID)
				})
				requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusNotFound, "playlist after close")
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusNotFound, "segment after close")
			}),
	}
}
