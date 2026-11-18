package playback_streams

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// errorScenarios cover stream_error: its {id, error} payload and audience, the
// removed stream folder and the absence of the event for a healthy stream or
// after the failure.
func errorScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-error", "INV-SOCKET-OUTGOING-STREAM-ERROR", "normal", "failure", "stream-error-payload-and-audience",
			[]string{streamCloseSrc, streamStartSrc, socketClientSrc, webSocketSrc, webPlayerSrc},
			"A transcode that fails on its input emits stream_error once on both owner sockets with exactly {id: session id, error: non-empty text}, no stream_open and no stream_closed, removes the stream folder and leaves the HLS route at 404.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Error")
				user := createUser(t, e, "error-initial-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, item, "", "error-initial")

				first := requireOnlyOwner[map[string]any](t, a, "stream_error")
				if len(first) != 2 {
					t.Fatalf("stream_error payload has %d fields, want id and error: %v", len(first), first)
				}
				requireEqual(t, "stream_error id", first["id"], doc.ID)
				text, ok := first["error"].(string)
				if !ok || text == "" {
					t.Fatalf("stream_error error %v is not a non-empty string", first["error"])
				}
				if got := eventsNamed[streamOpenDoc](t, a.owner1, a.marks[0], "stream_open"); len(got) != 0 {
					t.Fatalf("a failed stream emitted stream_open: %+v", got)
				}
				if got := eventsNamed[string](t, a.owner1, a.marks[0], "stream_closed"); len(got) != 0 {
					t.Fatalf("a failed stream emitted stream_closed: %v", got)
				}
				harness.Eventually(t, 10*time.Second, "the stream folder to disappear", func() bool {
					return !streamFolderExists(e, doc.ID)
				})
				requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusNotFound, "playlist after the error")
			}),

		scenario("socket-outgoing-stream-error", "INV-SOCKET-OUTGOING-STREAM-ERROR-SILENT-001", "error", "initial", "stream-error-absent-on-success",
			[]string{streamStartSrc, streamCloseSrc},
			"A successful transcode that completes and streams its segments emits no stream_error.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "error-success-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "error-success")
				eventFirst[streamOpenDoc](t, a.owner1, a.marks[0], "stream_open", 15*time.Second)
				waitSegment(t, e, doc.ID, 0)
				a.settleAll(t)
				a.requireNobody(t, "stream_error", 500*time.Millisecond)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-error", "INV-SOCKET-OUTGOING-STREAM-ERROR-SILENT-001", "edge", "repeat", "stream-error-repeat-is-silent",
			[]string{streamCloseSrc, hlsRouterSrc},
			"Repeating the HLS file requests of a failed stream answers 404 and emits no second stream_error.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Error Repeat")
				user := createUser(t, e, "error-repeat-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, item, "", "error-repeat")
				eventFirst[errorDoc](t, a.owner1, a.marks[0], "stream_error", 15*time.Second)
				harness.Eventually(t, 10*time.Second, "the stream folder to disappear", func() bool {
					return !streamFolderExists(e, doc.ID)
				})
				a.reset()
				for i := 0; i < 2; i++ {
					requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusNotFound, "repeated playlist request")
					requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusNotFound, "repeated segment request")
				}
				a.requireNobody(t, "stream_error", 500*time.Millisecond)
			}),

		scenario("socket-outgoing-stream-error", "INV-SOCKET-OUTGOING-STREAM-ERROR-SILENT-001", "normal", "changed", "stream-error-targets-only-the-owner",
			[]string{streamCloseSrc, socketClientSrc},
			"A failed stream of one user reaches only that user's sockets; another user's running stream receives no stream_error and keeps serving segments.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Error Other")
				owner := createUser(t, e, "error-owner-user")
				bystander := createUser(t, e, "error-bystander-user")
				bystanderSocket := e.Socket(t, bystander.Token)
				bystanderMark := bystanderSocket.Mark()
				healthy := startStream(t, e, bystander, e.IDs["saga1"], "", "error-bystander")
				a := watch(t, e, owner, "finisher")
				broken := startStream(t, e, owner, item, "", "error-owner")
				eventFirst[errorDoc](t, a.owner1, a.marks[0], "stream_error", 15*time.Second)
				eventFirst[errorDoc](t, a.owner2, a.marks[1], "stream_error", 15*time.Second)
				settle(t, bystanderSocket)
				if got := eventsNamed[errorDoc](t, bystanderSocket, bystanderMark, "stream_error"); len(got) != 0 {
					t.Fatalf("another user's socket got stream_error: %+v", got)
				}
				waitSegment(t, e, healthy.ID, 0)
				closeQuietly(t, e, bystander.Token, healthy.ID)
				closeQuietly(t, e, owner.Token, broken.ID)
			}),
	}
}
