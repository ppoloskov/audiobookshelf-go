package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeStreamEvent = "server/managers/PlaybackSessionManager.js:361"
	routeCloseEvent  = "server/managers/PlaybackSessionManager.js:428-431"
	emitterSrc       = "server/SocketAuthority.js:68-87"
)

// userProfile reads the browser profile of a user as an admin.
func userProfile(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/users/"+id)
	requireStatus(t, resp, http.StatusOK, "GET /api/users/:id")
	return resp.Object(t)
}

// waitOneStreamUpdate requires exactly one user_stream_update since mark.
func waitOneStreamUpdate(t *testing.T, s *harness.Socket, mark int, what string) streamUpdate {
	t.Helper()
	harness.Eventually(t, 5*time.Second, what, func() bool {
		return len(streamUpdates(t, s, mark)) >= 1
	})
	settle(t, s)
	updates := streamUpdates(t, s, mark)
	if len(updates) != 1 {
		t.Fatalf("%s: got %d user_stream_update events", what, len(updates))
	}
	return updates[0]
}

// requireNoStreamUpdates requires that no user_stream_update arrived since mark.
func requireNoStreamUpdates(t *testing.T, s *harness.Socket, mark int, what string) {
	t.Helper()
	settle(t, s)
	if updates := streamUpdates(t, s, mark); len(updates) != 0 {
		t.Fatalf("%s: unexpected user_stream_update: %+v", what, updates)
	}
}

// socketScenarios covers the two session socket events of this packet:
// user_session_closed and user_stream_update.
func socketScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "normal", State: "initial", Assertion: "socket-session-closed-on-close",
			Sources: []string{routeCloseEvent, emitterSrc, closeSource},
			Claim:   "Closing a session emits user_session_closed once with the session id on every socket of the owner, and on no admin or other user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-1", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(30, 2))
				waitListening(t, e, doc.ID, 2)
				a.reset()
				closeSession(t, e, listener.Token, doc.ID, syncBody(31, 1))
				a.requireClosedOnce(t, doc.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "normal", State: "repeat", Assertion: "socket-session-closed-not-repeated",
			Sources: []string{routeCloseEvent, emitterSrc, sessionMiddleware},
			Claim:   "A repeated close or a sync after the close is rejected with 404 and emits no further user_session_closed.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-2", "audio/mpeg"))
				closeSession(t, e, listener.Token, doc.ID, syncBody(31, 2))
				a.requireClosedOnce(t, doc.ID)
				a.reset()
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusNotFound, "second close")
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+doc.ID+"/sync", syncBody(31, 2)), http.StatusNotFound, "sync after the close")
				a.requireNoClosed(t)
			},
		},
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "normal", State: "changed", Assertion: "socket-session-closed-by-admin",
			Sources: []string{routeCloseEvent, emitterSrc, "client/components/modals/ListeningSessionModal.vue:210"},
			Claim:   "An admin closing another user's session announces user_session_closed to the owner's sockets only, not to the admin's own socket.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-3", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(33, 2))
				waitListening(t, e, doc.ID, 2)
				a.reset()
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusOK, "admin close")
				a.requireClosedOnce(t, doc.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "error", State: "failure", Assertion: "socket-session-closed-refused",
			Sources: []string{routeCloseEvent, sessionMiddleware},
			Claim:   "Refused closes (401 without a token, 404 for an unknown session, 403 for another user) emit no user_session_closed on any socket and leave the session open.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-4", "audio/mpeg"))
				read := mustGetSession(t, e, listener.Token, doc.ID)
				a.reset()
				for _, c := range []struct {
					name, token, id string
					status          int
				}{
					{"no token", "", doc.ID, http.StatusUnauthorized},
					{"unknown session", listener.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"another user", finisher.Token, doc.ID, http.StatusForbidden},
				} {
					requireStatus(t, e.Post(t, c.token, "/api/session/"+c.id+"/close", map[string]any{}), c.status, "close: "+c.name)
				}
				a.requireNoClosed(t)
				requireEqual(t, "the refused session", mustGetSession(t, e, listener.Token, doc.ID), read)
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "edge", State: "changed", Assertion: "socket-session-closed-on-replacement",
			Sources: []string{"server/managers/PlaybackSessionManager.js:282-288", routeCloseEvent, emitterSrc},
			Claim:   "A play that replaces the session of the same device announces user_session_closed with the previous id once, on the owner's sockets only.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				first := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-5", "audio/mpeg"))
				syncSession(t, e, listener.Token, first.ID, syncBody(20, 2))
				waitListening(t, e, first.ID, 2)
				a.reset()
				second := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-5", "audio/mpeg"))
				a.requireClosedOnce(t, first.ID)
				mustGetSession(t, e, listener.Token, second.ID)
				closeQuietly(t, e, listener.Token, second.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-session-closed", Invariant: "INV-SOCKET-OUTGOING-USER-SESSION-CLOSED", Case: "edge", State: "initial", Assertion: "socket-session-closed-without-listening",
			Sources: []string{routeCloseEvent, closeSource, emitterSrc},
			Claim:   "Closing a session that never listened with an empty body still announces user_session_closed once and stores nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sc-6", "audio/mpeg"))
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				a.reset()
				closeSession(t, e, listener.Token, doc.ID, map[string]any{})
				a.requireClosedOnce(t, doc.ID)
				if sessionRow(t, e, doc.ID) != nil || e.Count(t, "SELECT COUNT(*) FROM playbackSessions") != rows {
					t.Fatal("a session without listening time must not be stored")
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "normal", State: "initial", Assertion: "socket-stream-update-on-play",
			Sources: []string{routeStreamEvent, emitterSrc, "server/models/User.js:584-594"},
			Claim:   "A play emits one user_stream_update to every admin socket with the user's id, username, type, lastSeen, createdAt and its session, and to no non-admin socket.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				root := e.Token(t, "root")
				admin1, admin2 := e.Socket(t, root), e.Socket(t, root)
				listener := fixtureActor(t, e, "listener")
				own := e.Socket(t, listener.Token)
				profile := userProfile(t, e, listener.ID)
				createdAt, _ := profile["createdAt"].(float64)
				lastSeen, ok := profile["lastSeen"].(float64)
				if !ok {
					t.Fatalf("the connected user has no lastSeen: %v", profile["lastSeen"])
				}
				marks := [3]int{admin1.Mark(), admin2.Mark(), own.Mark()}
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-1", "audio/mpeg"))
				update := waitOneStreamUpdate(t, admin1, marks[0], "play")
				if update.ID != listener.ID || update.Username != "trial-listener" || update.Type != "user" ||
					update.CreatedAt != int64(createdAt) || update.LastSeen == nil || *update.LastSeen != int64(lastSeen) {
					t.Fatalf("user_stream_update user fields: %+v", update)
				}
				// The payload session is toJSONForClient without the expanded item.
				want := doc
				want.LibraryItem = nil
				requireEqual(t, "user_stream_update session", update.Session, &want)
				if update.Session == nil || update.Session.LibraryItem != nil {
					t.Fatalf("user_stream_update session: %+v", update.Session)
				}
				second := waitOneStreamUpdate(t, admin2, marks[1], "play on the second admin socket")
				requireEqual(t, "both admin sockets get the same payload", second, update)
				requireNoStreamUpdates(t, own, marks[2], "a non-admin socket")
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "normal", State: "repeat", Assertion: "socket-stream-update-on-close-once",
			Sources: []string{routeCloseEvent, emitterSrc, closeSource},
			Claim:   "A close emits one user_stream_update carrying the closing session, and a repeated close emits none.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-2", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(30, 2))
				waitListening(t, e, doc.ID, 2)
				want := mustGetSession(t, e, listener.Token, doc.ID)
				want.LibraryItem = nil
				mark := root.Mark()
				closeSession(t, e, listener.Token, doc.ID, syncBody(31, 1))
				update := waitOneStreamUpdate(t, root, mark, "close")
				want.CurrentTime, want.TimeListening, want.UpdatedAt = 31, ptrOf(3.0), update.Session.UpdatedAt
				requireEqual(t, "user_stream_update of the close", update.Session, &want)
				mark = root.Mark()
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusNotFound, "second close")
				requireNoStreamUpdates(t, root, mark, "a rejected close")
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "normal", State: "changed", Assertion: "socket-stream-update-own-session-only",
			Sources: []string{routeStreamEvent, routeCloseEvent, "server/models/User.js:584-594"},
			Claim:   "Each user_stream_update carries the acting user's own session: closing the listener's session reports the listener's session and closing the finisher's reports the finisher's.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				first := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-3a", "audio/mpeg"))
				second := play(t, e, finisher.Token, e.IDs["saga2"], "", androidBody("ps-su-3b", "audio/mpeg"))
				mark := root.Mark()
				closeSession(t, e, listener.Token, first.ID, syncBody(25, 2))
				update := waitOneStreamUpdate(t, root, mark, "close of the listener's session")
				if update.ID != listener.ID || update.Username != "trial-listener" || update.Session == nil || update.Session.ID != first.ID || update.Session.ID == second.ID {
					t.Fatalf("listener close update: %+v", update)
				}
				mark = root.Mark()
				closeSession(t, e, finisher.Token, second.ID, syncBody(15, 2))
				update = waitOneStreamUpdate(t, root, mark, "close of the finisher's session")
				if update.ID != finisher.ID || update.Username != "trial-finisher" || update.Session == nil || update.Session.ID != second.ID {
					t.Fatalf("finisher close update: %+v", update)
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "error", State: "failure", Assertion: "socket-stream-update-refused",
			Sources: []string{routeStreamEvent, routeCloseEvent, startSource},
			Claim:   "A refused play and a refused close emit no user_stream_update on the admin sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				booksOnly := createUser(t, e, "ps-su-books", map[string]any{
					"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["library"]},
				})
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-4", "audio/mpeg"))
				defer closeQuietly(t, e, listener.Token, doc.ID)
				mark := root.Mark()
				requireStatus(t, e.Post(t, booksOnly.Token, "/api/items/"+e.IDs["podcast"]+"/play", androidBody("ps-su-4b", "audio/mpeg")), http.StatusForbidden, "forbidden play")
				requireStatus(t, e.Post(t, finisher.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusForbidden, "foreign close")
				requireNoStreamUpdates(t, root, mark, "refused play and close")
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "edge", State: "changed", Assertion: "socket-stream-update-on-replacement",
			Sources: []string{"server/managers/PlaybackSessionManager.js:282-288", routeStreamEvent, routeCloseEvent},
			Claim:   "Replacing the session of a device emits two user_stream_update events in order: the closing previous session, then the new session.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				root := e.Socket(t, e.Token(t, "root"))
				listener := fixtureActor(t, e, "listener")
				first := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-5", "audio/mpeg"))
				mark := root.Mark()
				second := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-5", "audio/mpeg"))
				harness.Eventually(t, 5*time.Second, "two user_stream_update events", func() bool {
					return len(streamUpdates(t, root, mark)) >= 2
				})
				settle(t, root)
				updates := streamUpdates(t, root, mark)
				if len(updates) != 2 || updates[0].Session == nil || updates[0].Session.ID != first.ID || updates[1].Session == nil || updates[1].Session.ID != second.ID {
					t.Fatalf("replacement updates: %+v", updates)
				}
				closeQuietly(t, e, listener.Token, second.ID)
			},
		},
		{
			Requirement: "socket-outgoing-user-stream-update", Invariant: "INV-SOCKET-OUTGOING-USER-STREAM-UPDATE", Case: "edge", State: "initial", Assertion: "socket-stream-update-empty-close",
			Sources: []string{routeCloseEvent, closeSource, emitterSrc},
			Claim:   "Closing a session that never listened still emits one user_stream_update whose session has zero listening time, and stores no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-su-6", "audio/mpeg"))
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				mark := root.Mark()
				closeSession(t, e, listener.Token, doc.ID, map[string]any{})
				update := waitOneStreamUpdate(t, root, mark, "close without listening")
				want := doc
				want.LibraryItem, want.TimeListening, want.UpdatedAt = nil, ptrOf(0.0), update.Session.UpdatedAt
				requireEqual(t, "user_stream_update of an empty close", update.Session, &want)
				if e.Count(t, "SELECT COUNT(*) FROM playbackSessions") != rows {
					t.Fatal("an empty close must store no session")
				}
			},
		},
	}
}
