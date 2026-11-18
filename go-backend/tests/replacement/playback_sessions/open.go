package playback_sessions

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	routeGet   = "server/routers/ApiRouter.js:241"
	getSource  = "server/controllers/SessionController.js:152-156"
	openSource = "server/controllers/SessionController.js:313-326"
)

// removedSessions holds sessions opened on an item that was then deleted. The
// scenarios of the removed item run in order and share them on purpose: one
// deletion serves the sync, read and close checks.
var removedSessions struct {
	owner       actor
	read, close sessionDoc
	itemID      string
	progress    map[string]any
}

func openScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r146", Invariant: "INV-R146-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "session-get-open-session",
			Sources: []string{routeGet, getSource, "server/objects/PlaybackSession.js:62-92"},
			Claim:   "GET /api/session/:id returns the open session document with tracks and expanded item to its owner and to an admin, current with the latest sync.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-get-1", "audio/mpeg"))
				owner := mustGetSession(t, e, listener.Token, doc.ID)
				requireEqual(t, "owner read equals the play answer", owner, doc)
				requireSaga1Session(t, e, owner, listener.ID, doc.StartTime, 0, "exo-player")
				requireDirectTracks(t, e, owner.AudioTracks)
				admin := mustGetSession(t, e, e.Token(t, "root"), doc.ID)
				requireEqual(t, "admin read equals the owner read", admin, owner)
				syncSession(t, e, listener.Token, doc.ID, syncBody(41, 2))
				after := mustGetSession(t, e, listener.Token, doc.ID)
				if after.CurrentTime != 41 || after.TimeListening == nil || *after.TimeListening != 2 || after.ID != doc.ID {
					t.Fatalf("read after sync: %+v", after)
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r146", Invariant: "INV-R146-AUTH-001", Case: "error", State: "stateless", Assertion: "session-get-refusals",
			Sources: []string{routeGet, openSource},
			Claim:   "GET /api/session/:id is 401 unauthenticated, 404 for an unknown, stored-only or closed session (before the owner check) and 403 for another non-admin user.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-get-2", "audio/mpeg"))
				for _, c := range []struct {
					name, token, id string
					status          int
				}{
					{"no token", "", doc.ID, http.StatusUnauthorized},
					{"invalid token", "not-a-token", doc.ID, http.StatusUnauthorized},
					{"another user", finisher.Token, doc.ID, http.StatusForbidden},
					{"unknown", listener.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"unknown for another user", finisher.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"stored but not open", listener.Token, "193bf2e2-70c3-4b91-99e2-40bc7398812a", http.StatusNotFound},
					{"malformed id", listener.Token, "not-a-uuid", http.StatusNotFound},
				} {
					requireStatus(t, e.Get(t, c.token, "/api/session/"+c.id), c.status, "GET session: "+c.name)
				}
				closeQuietly(t, e, listener.Token, doc.ID)
				requireStatus(t, e.Get(t, listener.Token, "/api/session/"+doc.ID), http.StatusNotFound, "GET closed session")
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-FAILURE-001", Case: "error", State: "failure", Assertion: "session-sync-item-removed",
			Sources: []string{routeSync, "server/managers/PlaybackSessionManager.js:106-110,340-348"},
			Claim:   "When the session's library item has been deleted, sync answers 500 and changes neither the session nor any progress, and emits no progress event.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				owner := fixtureActor(t, e, "listener")
				item := e.IDs["disposable"]
				open := func(device string) sessionDoc {
					return play(t, e, owner.Token, item, "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": device}})
				}
				sync, read, closing := open("ps-removed-1"), open("ps-removed-2"), open("ps-removed-3")
				syncSession(t, e, owner.Token, sync.ID, map[string]any{"currentTime": 1, "timeListened": 1, "duration": 3.056327})
				stored := waitListening(t, e, sync.ID, 1)
				disposableBook := *sync.BookID
				requireStatus(t, e.Delete(t, root, "/api/items/"+item), http.StatusOK, "delete the item")
				progress := progressRow(t, e, owner.ID, disposableBook) // deleting the item removed its progress rows
				if progress != nil {
					t.Fatalf("progress of a deleted item must be gone: %v", progress)
				}
				socket := e.Socket(t, owner.Token)
				mark := socket.Mark()
				requireStatus(t, e.Post(t, owner.Token, "/api/session/"+sync.ID+"/sync", map[string]any{"currentTime": 2, "timeListened": 5, "duration": 3.056327}), http.StatusInternalServerError, "sync of a deleted item")
				requireEqual(t, "stored session", sessionRow(t, e, sync.ID), stored)
				requireEqual(t, "progress", progressRow(t, e, owner.ID, disposableBook), progress)
				if got := progressEvents(t, socket, mark); len(got) != 0 {
					t.Fatalf("failed sync emitted progress: %v", got)
				}
				same := mustGetSession(t, e, owner.Token, sync.ID)
				if same.TimeListening == nil || *same.TimeListening != 1 || same.CurrentTime != 1 {
					t.Fatalf("the failed sync must not change the open session: %+v", same)
				}
				closeQuietly(t, e, owner.Token, sync.ID)
				removedSessions.owner, removedSessions.read, removedSessions.close, removedSessions.itemID = owner, read, closing, item
				removedSessions.progress = progress
			},
		},
		{
			Requirement: "route-r146", Invariant: "INV-R146-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "session-get-item-removed",
			Sources: []string{routeGet, getSource, "server/objects/PlaybackSession.js:93-122"},
			Claim:   "GET /api/session/:id of a session whose item was deleted still answers 200 with the session fields and a null libraryItem.",
			Run: func(t *testing.T, e *harness.Env) {
				removed := removedSessions
				if removed.read.ID == "" {
					t.Fatal("scenario session-sync-item-removed must run first: it opens the sessions and deletes the item")
				}
				doc := mustGetSession(t, e, removed.owner.Token, removed.read.ID)
				if doc.LibraryItem != nil || doc.UserID != removed.owner.ID || doc.LibraryItemID == nil || *doc.LibraryItemID != removed.itemID || doc.DisplayTitle != removed.read.DisplayTitle || len(doc.AudioTracks) != len(removed.read.AudioTracks) {
					t.Fatalf("session of a deleted item: %+v", doc)
				}
				requireStatus(t, e.Get(t, e.Token(t, "root"), "/api/session/"+removed.read.ID), http.StatusOK, "admin read")
				closeQuietly(t, e, removed.owner.Token, removed.read.ID)
			},
		},
		{
			Requirement: "route-r148", Invariant: "INV-R148-FAILURE-001", Case: "error", State: "failure", Assertion: "session-close-item-removed",
			Sources: []string{routeClose, closeSource, "server/managers/PlaybackSessionManager.js:340-348"},
			Claim:   "Closing with a final sync after the item was deleted still answers 200 and closes the session with its event, but stores nothing and moves no progress.",
			Run: func(t *testing.T, e *harness.Env) {
				removed := removedSessions
				if removed.close.ID == "" {
					t.Fatal("scenario session-sync-item-removed must run first: it opens the sessions and deletes the item")
				}
				a := watch(t, e, removed.owner)
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				requireStatus(t, e.Post(t, removed.owner.Token, "/api/session/"+removed.close.ID+"/close", map[string]any{"currentTime": 2, "timeListened": 4, "duration": 3.056327}), http.StatusOK, "close")
				a.requireClosedOnce(t, removed.close.ID)
				if _, status := getSession(t, e, removed.owner.Token, removed.close.ID); status != http.StatusNotFound {
					t.Fatalf("session must be closed, GET answered %d", status)
				}
				if sessionRow(t, e, removed.close.ID) != nil || e.Count(t, "SELECT COUNT(*) FROM playbackSessions") != rows {
					t.Fatal("a failed final sync must not store the session")
				}
				requireEqual(t, "progress", progressRow(t, e, removed.owner.ID, *removed.close.BookID), removedSessions.progress)
				waitStreamUpdates(t, a.admin, a.marks[2], 1)
			},
		},
	}
}
