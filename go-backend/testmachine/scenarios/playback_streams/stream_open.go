package playback_streams

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// saga1BookID is the books.id of "The Beginning".
const saga1BookID = "5e33de11-3e94-4c51-87d8-d06d2928ac38"

// openScenarios cover the stream_open event (Stream.toJSON) of a transcoded
// session: payload, audience (every socket of the owning user), one event per
// stream and the completion path of a short item.
func openScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN", "normal", "initial", "stream-open-payload-and-audience",
			[]string{streamCheckSrc, streamStartSrc, streamPayloadSrc, socketClientSrc, webSocketSrc},
			"Starting a transcoded book emits stream_open once on both sockets of the owning user with the stream document (id, userId, expanded book, null episode, segment length 6, playlist path, HLS URI, start time, segment start number, completion flag) and on no admin or other-user socket.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "open-user")
				setProgress(t, e, user, e.IDs["saga1"], 40, saga1Duration)
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "open-initial")
				open := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")

				if open.ID != doc.ID || open.UserID != user.ID {
					t.Fatalf("stream_open ids: %+v", open)
				}
				if open.LibraryItem.ID != e.IDs["saga1"] || open.LibraryItem.MediaType != "book" ||
					open.LibraryItem.Media.ID != saga1BookID || open.LibraryItem.Media.Metadata.Title != "The Beginning" {
					t.Fatalf("stream_open libraryItem: %+v", open.LibraryItem)
				}
				if open.Episode != nil {
					t.Fatalf("a book stream must carry no episode: %+v", open.Episode)
				}
				if open.SegmentLength != segmentLength || open.StartTime != 40 || open.SegmentStartNumber != 1 {
					t.Fatalf("stream_open position: %+v", open)
				}
				requireEqual(t, "playlist path", open.PlaylistPath, filepath.Join(e.Install, "metadata", "streams", doc.ID, "output.m3u8"))
				requireEqual(t, "client playlist uri", open.ClientPlaylistURI, hlsPath(doc.ID, "output.m3u8"))
				if open.IsTranscodeComplete {
					t.Fatal("stream_open is emitted before the transcode is complete")
				}

				// The event is sent once per stream, not on later segment checks.
				a.owner1.None(t, a.owner1.Mark(), "stream_open", 1*time.Second, nil)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN", "edge", "initial", "stream-open-podcast-episode",
			[]string{streamPayloadSrc, streamStartSrc, sessionManager},
			"A transcoded podcast episode carries the expanded episode in stream_open: the payload names the episode id, the podcast item's media type and the episode playback URI.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "open-podcast-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["podcast"], e.IDs["ep3"], "open-podcast")
				open := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				if open.Episode == nil || open.Episode.ID != e.IDs["ep3"] {
					t.Fatalf("stream_open episode %+v, want episode %s", open.Episode, e.IDs["ep3"])
				}
				if open.LibraryItem.ID != e.IDs["podcast"] || open.LibraryItem.MediaType != "podcast" {
					t.Fatalf("stream_open podcast item: %+v", open.LibraryItem)
				}
				requireEqual(t, "podcast playlist uri", open.ClientPlaylistURI, hlsPath(doc.ID, "output.m3u8"))
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN-EMIT-001", "normal", "repeat", "stream-open-per-new-session",
			[]string{streamCheckSrc, sessionManager},
			"Starting playback again on the same device replaces the open session (the old one is no longer open) and emits a new stream_open carrying the new session id, so every started stream announces itself exactly once.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "open-repeat-user")
				a := watch(t, e, user, "finisher")
				first := startStream(t, e, user, e.IDs["saga2"], "", "open-repeat")
				initial := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				if initial.ID != first.ID {
					t.Fatalf("first stream_open id %s, want %s", initial.ID, first.ID)
				}
				a.reset()
				second := startStream(t, e, user, e.IDs["saga2"], "", "open-repeat")
				again := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				if again.ID != second.ID || again.ID == initial.ID {
					t.Fatalf("second stream_open id %s, want the new session %s", again.ID, second.ID)
				}
				requireStatus(t, e.Get(t, user.Token, "/api/session/"+first.ID), 404, "replaced session")
				closeQuietly(t, e, user.Token, second.ID)
			}),

		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN", "edge", "initial", "stream-open-short-item-completion",
			[]string{streamStartSrc, streamPayloadSrc},
			"A three-second item finishes transcoding before the first segment check and still emits stream_open with the full stream document and isTranscodeComplete false.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "open-short-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["disposable"], "", "open-short")
				open := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				if open.ID != doc.ID || open.SegmentStartNumber != 0 || open.IsTranscodeComplete {
					t.Fatalf("short stream_open: %+v", open)
				}
				requireEqual(t, "short playlist uri", open.ClientPlaylistURI, hlsPath(doc.ID, "output.m3u8"))
				waitSegment(t, e, doc.ID, 0)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN-EMIT-001", "normal", "changed", "stream-open-after-previous-stream-closed",
			[]string{streamCheckSrc, streamCloseSrc},
			"After a stream was closed, the next playback of the same user emits stream_open for the new session only.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "open-changed-user")
				a := watch(t, e, user, "finisher")
				first := startStream(t, e, user, e.IDs["saga1"], "", "open-changed-1")
				requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				closeQuietly(t, e, user.Token, first.ID)
				a.reset()
				second := startStream(t, e, user, e.IDs["saga1"], "", "open-changed-2")
				open := requireOnlyOwner[streamOpenDoc](t, a, "stream_open")
				if open.ID != second.ID {
					t.Fatalf("stream_open after a close has id %s, want %s", open.ID, second.ID)
				}
				closeQuietly(t, e, user.Token, second.ID)
			}),

		scenario("socket-outgoing-stream-open", "INV-SOCKET-OUTGOING-STREAM-OPEN-EMIT-001", "error", "failure", "stream-open-omitted-for-failed-stream",
			[]string{streamStartSrc, streamCloseSrc},
			"A transcode that fails on its input emits stream_error and no stream_open for that session.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Open")
				user := createUser(t, e, "open-failure-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, item, "", "open-failure")
				eventFirst[errorDoc](t, a.owner1, a.marks[0], "stream_error", 15*time.Second)
				eventFirst[errorDoc](t, a.owner2, a.marks[1], "stream_error", 15*time.Second)
				a.settleAll(t)
				for i, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
					if got := eventsNamed[streamOpenDoc](t, s, a.marks[i], "stream_open"); len(got) != 0 {
						t.Fatalf("a failed stream emitted stream_open: %+v", got)
					}
				}
				closeQuietly(t, e, user.Token, doc.ID)
			}),
	}
}

// brokenItem creates an audiobook from a valid mp3 and then replaces the file
// content with garbage (watchers are off, so the item row keeps its audio
// metadata). A forced transcode of it fails on the input.
func brokenItem(t *testing.T, e *harness.Env, title string) string {
	t.Helper()
	quietWatchers(t, e)
	id := newAudiobook(t, e, title, title+".mp3", fixtureMP3(t, e))
	path := filepath.Join(itemFolder(t, e, id), title+".mp3")
	if err := os.WriteFile(path, []byte("this is not an audio stream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}
