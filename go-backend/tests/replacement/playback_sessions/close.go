package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	routeClose  = "server/routers/ApiRouter.js:243"
	closeSource = "server/managers/PlaybackSessionManager.js:376-387"
	saveSource  = "server/managers/PlaybackSessionManager.js:389-399"
)

// audience is the sockets used to watch one session: both sockets of the
// owner, an admin and another user.
type audience struct {
	own1, own2, admin, other *harness.Socket
	marks                    [4]int
}

func watch(t *testing.T, e *harness.Env, owner actor) *audience {
	t.Helper()
	other := "finisher"
	if owner.Name == "trial-finisher" {
		other = "listener"
	}
	a := &audience{
		own1: e.Socket(t, owner.Token), own2: e.Socket(t, owner.Token),
		admin: e.Socket(t, e.Token(t, "root")), other: e.Socket(t, e.Token(t, other)),
	}
	a.reset()
	return a
}

func (a *audience) reset() {
	a.marks = [4]int{a.own1.Mark(), a.own2.Mark(), a.admin.Mark(), a.other.Mark()}
}

// requireClosedOnce requires user_session_closed with the session id on both
// owner sockets, once, and on no other socket.
func (a *audience) requireClosedOnce(t *testing.T, id string) {
	t.Helper()
	for i, s := range []*harness.Socket{a.own1, a.own2} {
		s.Wait(t, a.marks[i], "user_session_closed", 5*time.Second, nil)
		requireEqual(t, "user_session_closed on an owner socket", closedEvents(t, s, a.marks[i]), []string{id})
	}
	settle(t, a.admin)
	settle(t, a.other)
	if ids := closedEvents(t, a.admin, a.marks[2]); len(ids) != 0 {
		t.Fatalf("admin socket got user_session_closed %v", ids)
	}
	if ids := closedEvents(t, a.other, a.marks[3]); len(ids) != 0 {
		t.Fatalf("another user's socket got user_session_closed %v", ids)
	}
}

// requireNoClosed requires that nobody got user_session_closed.
func (a *audience) requireNoClosed(t *testing.T) {
	t.Helper()
	for i, s := range []*harness.Socket{a.own1, a.own2, a.admin, a.other} {
		settle(t, s)
		if ids := closedEvents(t, s, a.marks[i]); len(ids) != 0 {
			t.Fatalf("socket %d got user_session_closed %v", i, ids)
		}
	}
}

func closeScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r148", Invariant: "INV-R148-CLOSE-001", Case: "normal", State: "initial", Assertion: "session-close-saves-and-announces",
			Sources: []string{routeClose, "server/controllers/SessionController.js:107-112", closeSource, saveSource},
			Claim:   "POST /api/session/:id/close with a final sync stores the session with the total listening time, moves the progress, removes the open session, answers the owner's sockets with user_session_closed and the admins with one user_stream_update.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-1", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(30, 2))
				waitListening(t, e, doc.ID, 2)
				a.reset()
				resp := e.Post(t, listener.Token, "/api/session/"+doc.ID+"/close", syncBody(33, 1.5))
				requireStatus(t, resp, http.StatusOK, "close")
				read, status := getSession(t, e, listener.Token, doc.ID)
				if status != http.StatusNotFound {
					t.Fatalf("closed session must not be open: %d %+v", status, read)
				}
				if contains(openSessionIDs(t, e), doc.ID) {
					t.Fatal("closed session is still listed as open")
				}
				stored := waitListening(t, e, doc.ID, 3.5)
				requireSessionRow(t, stored, doc, 3.5, 33)
				progress := progressRow(t, e, listener.ID, saga1BookID)
				requireNear(t, "progress currentTime", number(t, progress["currentTime"]), 33)
				event := waitProgressEvent(t, a.own1, a.marks[0], doc.ID)
				requireProgressRow(t, event.Data, progress)
				a.requireClosedOnce(t, doc.ID)
				waitStreamUpdates(t, a.admin, a.marks[2], 1)
				settle(t, a.admin)
				updates := streamUpdates(t, a.admin, a.marks[2])
				if len(updates) != 1 {
					t.Fatalf("close must emit exactly one user_stream_update, got %d", len(updates))
				}
				if updates[0].ID != listener.ID || updates[0].Session == nil || updates[0].Session.ID != doc.ID {
					t.Fatalf("user_stream_update payload: %+v", updates[0])
				}
				for _, s := range []*harness.Socket{a.own1, a.other} {
					settle(t, s)
				}
				if got := streamUpdates(t, a.own1, a.marks[0]); len(got) != 0 {
					t.Fatalf("a non-admin socket got user_stream_update: %v", got)
				}
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-CLOSE-001", Case: "edge", State: "initial", Assertion: "session-close-without-listening-stores-nothing",
			Sources: []string{routeClose, "server/controllers/SessionController.js:107-112", closeSource, saveSource},
			Claim:   "Closing a session that never listened, with an empty body, stores no session row and touches no progress, but still removes the session and sends user_session_closed.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-2", "audio/mpeg"))
				progress := progressRow(t, e, listener.ID, saga1BookID)
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				a.reset()
				closeSession(t, e, listener.Token, doc.ID, map[string]any{})
				if _, status := getSession(t, e, listener.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("closed session still open: %d", status)
				}
				a.requireClosedOnce(t, doc.ID)
				if sessionRow(t, e, doc.ID) != nil || e.Count(t, "SELECT COUNT(*) FROM playbackSessions") != rows {
					t.Fatal("a session without listening time must not be stored")
				}
				requireEqual(t, "progress", progressRow(t, e, listener.ID, saga1BookID), progress)
				if got := progressEvents(t, a.own1, a.marks[0]); len(got) != 0 {
					t.Fatalf("no progress event expected: %v", got)
				}
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-CLOSE-001", Case: "edge", State: "changed", Assertion: "session-close-empty-keeps-synced-row",
			Sources: []string{routeClose, "server/controllers/SessionController.js:107-112", closeSource, saveSource},
			Claim:   "Closing with an empty body after a sync keeps the synced row as stored and does not touch the progress.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				own := e.Socket(t, listener.Token)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-3", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(34, 2.25))
				stored := waitListening(t, e, doc.ID, 2.25)
				progress := progressRow(t, e, listener.ID, saga1BookID)
				mark := own.Mark()
				closeSession(t, e, listener.Token, doc.ID, map[string]any{})
				own.Wait(t, mark, "user_session_closed", 5*time.Second, nil)
				requireEqual(t, "stored row", sessionRow(t, e, doc.ID), stored)
				requireEqual(t, "progress", progressRow(t, e, listener.ID, saga1BookID), progress)
				if got := progressEvents(t, own, mark); len(got) != 0 {
					t.Fatalf("an empty close must not emit progress: %v", got)
				}
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-CLOSE-001", Case: "normal", State: "repeat", Assertion: "session-close-twice-and-sync-after-close",
			Sources: []string{routeClose, "server/controllers/SessionController.js:313-326", closeSource},
			Claim:   "A session closes once: a second close or a later sync answers 404 and changes no row, progress or event.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-4", "audio/mpeg"))
				closeSession(t, e, listener.Token, doc.ID, syncBody(35, 3))
				stored := waitListening(t, e, doc.ID, 3)
				progress := progressRow(t, e, listener.ID, saga1BookID)
				a.requireClosedOnce(t, doc.ID)
				a.reset()
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+doc.ID+"/close", syncBody(40, 9)), http.StatusNotFound, "second close")
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+doc.ID+"/sync", syncBody(40, 9)), http.StatusNotFound, "sync after close")
				a.requireNoClosed(t)
				requireEqual(t, "stored row", sessionRow(t, e, doc.ID), stored)
				requireEqual(t, "progress", progressRow(t, e, listener.ID, saga1BookID), progress)
				if got := progressEvents(t, a.own1, a.marks[0]); len(got) != 0 {
					t.Fatalf("no progress event expected: %v", got)
				}
				if got := streamUpdates(t, a.admin, a.marks[2]); len(got) != 0 {
					t.Fatalf("the rejected requests must not emit user_stream_update: %d", len(got))
				}
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-CLOSE-001", Case: "normal", State: "changed", Assertion: "session-close-by-admin",
			Sources: []string{routeClose, "server/controllers/SessionController.js:313-326", "client/components/modals/ListeningSessionModal.vue:210"},
			Claim:   "An admin can close another user's open session, as the web client's session dialog does; the stored session is kept and user_session_closed goes to the session owner's sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-5", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(36, 2))
				stored := waitListening(t, e, doc.ID, 2)
				a.reset()
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusOK, "admin close")
				if _, status := getSession(t, e, listener.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("session closed by the admin is still open: %d", status)
				}
				a.requireClosedOnce(t, doc.ID)
				requireEqual(t, "stored row", sessionRow(t, e, doc.ID), stored)
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-AUTH-001", Case: "error", State: "failure", Assertion: "session-close-refusals-change-nothing",
			Sources: []string{routeClose, "server/controllers/SessionController.js:313-326"},
			Claim:   "Close is refused with 401 unauthenticated, 404 for an unknown session (before the owner check) and 403 for another non-admin user, and the session stays open without any event.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				a := watch(t, e, listener)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-close-6", "audio/mpeg"))
				read := mustGetSession(t, e, listener.Token, doc.ID)
				a.reset()
				for _, c := range []struct {
					name, token, id string
					status          int
				}{
					{"no token", "", doc.ID, http.StatusUnauthorized},
					{"invalid token", "not-a-token", doc.ID, http.StatusUnauthorized},
					{"unknown session", listener.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"unknown session of another user", finisher.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"another user's session", finisher.Token, doc.ID, http.StatusForbidden},
				} {
					requireStatus(t, e.Post(t, c.token, "/api/session/"+c.id+"/close", syncBody(5, 7)), c.status, "close: "+c.name)
				}
				requireEqual(t, "open session", mustGetSession(t, e, listener.Token, doc.ID), read)
				a.requireNoClosed(t)
				if got := streamUpdates(t, a.admin, a.marks[2]); len(got) != 0 {
					t.Fatalf("refused closes must not emit user_stream_update: %d", len(got))
				}
				if sessionRow(t, e, doc.ID) != nil {
					t.Fatal("a refused close must not store the session")
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
	}
}
