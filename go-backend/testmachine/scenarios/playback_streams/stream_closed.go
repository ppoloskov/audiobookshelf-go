package playback_streams

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// closedScenarios cover stream_closed: its audience and single-argument
// payload, the close of a direct-play session, the repeated close, the rejected
// close and the removed stream folder.
func closedScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-closed", "INV-SOCKET-OUTGOING-STREAM-CLOSED", "normal", "initial", "stream-closed-on-session-close",
			[]string{streamCloseSrc, sessionManager, "server/routers/ApiRouter.js:243", socketClientSrc, webSocketSrc},
			"Closing a transcoded session with the official clients' final sync payload answers 200, persists the session row with its listening time, removes the stream folder and emits stream_closed with the session id once on both owner sockets and on no admin or other-user socket.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "closed-initial-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "closed-initial")
				waitSegment(t, e, doc.ID, 0)
				// A first sync creates the row; the final close sync updates it.
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+doc.ID+"/sync", map[string]any{"currentTime": 2.0, "timeListened": 2.0, "duration": saga1Duration}), http.StatusOK, "sync before close")
				a.reset()
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+doc.ID+"/close", map[string]any{"currentTime": 5.0, "timeListened": 5.0, "duration": saga1Duration}), http.StatusOK, "close with sync payload")
				id := requireOnlyOwner[string](t, a, "stream_closed")
				if id != doc.ID {
					t.Fatalf("stream_closed payload %q, want the session id %q", id, doc.ID)
				}
				rows := e.Rows(t, "SELECT timeListening FROM playbackSessions WHERE id = ?", doc.ID)
				if len(rows) != 1 || rows[0]["timeListening"] == nil {
					t.Fatalf("closing a streamed session with a final sync did not persist it: %v", rows)
				}
				harness.Eventually(t, 10*time.Second, "the stream folder to disappear", func() bool {
					return !streamFolderExists(e, doc.ID)
				})
				requireStatus(t, getHLS(t, e, doc.ID, "output.m3u8", nil), http.StatusNotFound, "playlist after close")
			}),

		scenario("socket-outgoing-stream-closed", "INV-SOCKET-OUTGOING-STREAM-CLOSED-SILENT-001", "error", "failure", "stream-closed-forbidden-close-changes-nothing",
			[]string{"server/routers/ApiRouter.js:243", "server/controllers/SessionController.js:344-352"},
			"Closing another user's session as a non-admin answers 403, emits no stream_closed on either user's sockets and leaves the stream serving its segments.",
			func(t *testing.T, e *harness.Env) {
				owner := createUser(t, e, "closed-forbidden-owner")
				intruder := createUser(t, e, "closed-forbidden-intruder")
				a := watch(t, e, owner, "finisher")
				doc := startStream(t, e, owner, e.IDs["saga1"], "", "closed-forbidden")
				waitSegment(t, e, doc.ID, 0)
				intruderSocket := e.Socket(t, intruder.Token)
				intruderMark := intruderSocket.Mark()
				a.reset()
				requireStatus(t, e.Post(t, intruder.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusForbidden, "forbidden close")
				a.requireNobody(t, "stream_closed", 500*time.Millisecond)
				settle(t, intruderSocket)
				if got := eventsNamed[string](t, intruderSocket, intruderMark, "stream_closed"); len(got) != 0 {
					t.Fatalf("the intruder's socket got stream_closed: %v", got)
				}
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusOK, "segment after the forbidden close")
				closeQuietly(t, e, owner.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-closed", "INV-SOCKET-OUTGOING-STREAM-CLOSED-SILENT-001", "edge", "changed", "stream-closed-not-for-direct-play",
			[]string{streamCloseSrc, sessionManager},
			"Closing a direct-play session emits no stream_closed (it has no stream) while the owner still receives user_session_closed.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "closed-direct-user")
				a := watch(t, e, user, "finisher")
				doc := startDirect(t, e, user, e.IDs["saga1"], "", "closed-direct", "audio/mpeg")
				a.reset()
				closeQuietly(t, e, user.Token, doc.ID)
				eventFirst[string](t, a.owner1, a.marks[0], "user_session_closed", 10*time.Second)
				a.settleAll(t)
				for i, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
					if got := eventsNamed[string](t, s, a.marks[i], "stream_closed"); len(got) != 0 {
						t.Fatalf("stream_closed for a direct-play session on socket %d: %v", i, got)
					}
				}
			}),

		scenario("socket-outgoing-stream-closed", "INV-SOCKET-OUTGOING-STREAM-CLOSED-SILENT-001", "normal", "repeat", "stream-closed-once",
			[]string{streamCloseSrc, sessionManager},
			"Closing an already closed session answers 404 and emits no second stream_closed.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "closed-repeat-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "closed-repeat")
				waitSegment(t, e, doc.ID, 0)
				a.reset()
				closeQuietly(t, e, user.Token, doc.ID)
				requireOnlyOwner[string](t, a, "stream_closed")
				a.reset()
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusNotFound, "second close")
				a.requireNobody(t, "stream_closed", 500*time.Millisecond)
			}),
	}
}
