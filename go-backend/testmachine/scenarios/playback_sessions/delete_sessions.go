package playback_sessions

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeDelete       = "server/routers/ApiRouter.js:235"
	deleteSrc         = "server/controllers/SessionController.js:186-196"
	routeBatchDelete  = "server/routers/ApiRouter.js:237"
	batchDeleteSrc    = "server/controllers/SessionController.js:213-249"
	sessionMiddleware = "server/controllers/SessionController.js:313-326"
)

// deleteScenarios covers DELETE /api/sessions/:id and POST /api/sessions/batch/delete.
func deleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r141", Invariant: "INV-R141-CONTRACT-001", Case: "normal", State: "initial", Assertion: "sessions-delete-stored-session",
			Sources: []string{routeDelete, deleteSrc, sessionMiddleware, "client/components/modals/ListeningSessionModal.vue:190-200"},
			Claim:   "DELETE /api/sessions/:id as an admin answers 200 with no body and removes that stored session only, keeping other sessions, its progress and the media.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-delete-one", map[string]any{})
				keep := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-delete-keep", 3)
				gone := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-delete-gone", 4)
				keepRow, progress := sessionRow(t, e, keep.ID), progressRow(t, e, user.ID, saga1BookID)
				if progress == nil || userSessionTotal(t, e, root, user.ID) != 2 {
					t.Fatalf("setup: two stored sessions and a progress expected")
				}
				requireStatus(t, e.Delete(t, root, "/api/sessions/"+gone.ID), http.StatusOK, "DELETE /api/sessions/:id")
				if sessionRow(t, e, gone.ID) != nil {
					t.Fatal("the deleted session is still stored")
				}
				requireEqual(t, "the other stored session", sessionRow(t, e, keep.ID), keepRow)
				requireEqual(t, "progress", progressRow(t, e, user.ID, saga1BookID), progress)
				if total := userSessionTotal(t, e, root, user.ID); total != 1 {
					t.Fatalf("history total after the delete: %d", total)
				}
				if page := history(t, e, root, "?user="+user.ID); findHistory(page, gone.ID) != nil {
					t.Fatal("the deleted session is still in the history")
				}
				if row := e.Rows(t, "SELECT id FROM libraryItems WHERE id = ?", e.IDs["saga1"]); len(row) != 1 {
					t.Fatal("deleting a session must keep the library item")
				}
			},
		},
		{
			Requirement: "route-r141", Invariant: "INV-R141-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "sessions-delete-repeat-not-found",
			Sources: []string{routeDelete, deleteSrc},
			Claim:   "Deleting the same stored session again answers 404 and changes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-delete-repeat", map[string]any{})
				doc := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-delete-repeat", 3)
				requireStatus(t, e.Delete(t, root, "/api/sessions/"+doc.ID), http.StatusOK, "first delete")
				keep := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-delete-repeat-keep", 2)
				keepRow, progress := sessionRow(t, e, keep.ID), progressRow(t, e, user.ID, saga1BookID)
				requireStatus(t, e.Delete(t, root, "/api/sessions/"+doc.ID), http.StatusNotFound, "second delete")
				requireEqual(t, "the remaining session", sessionRow(t, e, keep.ID), keepRow)
				requireEqual(t, "progress", progressRow(t, e, user.ID, saga1BookID), progress)
				if userSessionTotal(t, e, root, user.ID) != 1 {
					t.Fatal("a repeated delete must change no row")
				}
			},
		},
		{
			Requirement: "route-r141", Invariant: "INV-R141-CONTRACT-001", Case: "normal", State: "changed", Assertion: "sessions-delete-open-session",
			Sources: []string{routeDelete, deleteSrc, sessionMiddleware, "server/objects/PlaybackSession.js:62-92"},
			Claim:   "Deleting an open session answers 200, closes it without a user_session_closed event, removes its stored row and leaves its progress.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-delete-open", map[string]any{})
				own := e.Socket(t, user.Token)
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-delete-open", "audio/mpeg"))
				syncSession(t, e, user.Token, doc.ID, syncBody(30, 3))
				waitListening(t, e, doc.ID, 3)
				progress := progressRow(t, e, user.ID, saga1BookID)
				mark := own.Mark()
				requireStatus(t, e.Delete(t, root, "/api/sessions/"+doc.ID), http.StatusOK, "delete an open session")
				if _, status := getSession(t, e, user.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("the deleted open session must be gone, GET answered %d", status)
				}
				if contains(openSessionIDs(t, e), doc.ID) {
					t.Fatal("the deleted open session is still listed as open")
				}
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+doc.ID+"/sync", syncBody(31, 1)), http.StatusNotFound, "sync after the delete")
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusNotFound, "close after the delete")
				if sessionRow(t, e, doc.ID) != nil {
					t.Fatal("the stored row of the deleted session must be gone")
				}
				requireEqual(t, "progress of the deleted session", progressRow(t, e, user.ID, saga1BookID), progress)
				settle(t, own)
				if ids := closedEvents(t, own, mark); len(ids) != 0 {
					t.Fatalf("deleting a session must not announce user_session_closed: %v", ids)
				}
			},
		},
		{
			Requirement: "route-r141", Invariant: "INV-R141-CONTRACT-001", Case: "edge", State: "initial", Assertion: "sessions-delete-episode-session",
			Sources: []string{routeDelete, deleteSrc, "server/models/PlaybackSession.js:74-106"},
			Claim:   "An episode session is deleted like a book session: its row and history entry go, its episode progress and other users' sessions stay.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				listener := fixtureActor(t, e, "listener")
				user := createUser(t, e, "ps-delete-episode", map[string]any{})
				other := createStoredSession(t, e, listener, e.IDs["saga1"], "", "ps-delete-episode-other", 2)
				otherRow := sessionRow(t, e, other.ID)
				doc := createStoredSession(t, e, user, e.IDs["podcast"], e.IDs["ep2"], "ps-delete-episode", 3)
				progress := progressRow(t, e, user.ID, e.IDs["ep2"])
				if progress == nil {
					t.Fatal("setup: the episode progress is missing")
				}
				requireStatus(t, e.Delete(t, root, "/api/sessions/"+doc.ID), http.StatusOK, "delete an episode session")
				if sessionRow(t, e, doc.ID) != nil || findHistory(history(t, e, root, "?user="+user.ID), doc.ID) != nil {
					t.Fatal("the deleted episode session must be gone")
				}
				requireEqual(t, "episode progress", progressRow(t, e, user.ID, e.IDs["ep2"]), progress)
				requireEqual(t, "another user's session", sessionRow(t, e, other.ID), otherRow)
			},
		},
		{
			Requirement: "route-r141", Invariant: "INV-R141-CONTRACT-001", Case: "error", State: "failure", Assertion: "sessions-delete-refusals-change-nothing",
			Sources: []string{routeDelete, sessionMiddleware},
			Claim:   "DELETE /api/sessions/:id is 401 without a valid token, 403 for a user without the delete permission (even for their own session), 404 for an unknown or malformed id, and changes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				listener := fixtureActor(t, e, "listener")
				user := createUser(t, e, "ps-delete-refuse", map[string]any{})
				doc := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-delete-refuse", 3)
				stored, progress := sessionRow(t, e, doc.ID), progressRow(t, e, user.ID, saga1BookID)
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				for _, c := range []struct {
					name, token, id string
					status          int
				}{
					{"no token", "", doc.ID, http.StatusUnauthorized},
					{"invalid token", "not-a-token", doc.ID, http.StatusUnauthorized},
					{"listener without delete", listener.Token, doc.ID, http.StatusForbidden},
					{"unknown for a user without delete", listener.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"unknown", root, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"malformed", root, "not-a-session", http.StatusNotFound},
				} {
					requireStatus(t, e.Delete(t, c.token, "/api/sessions/"+c.id), c.status, "DELETE: "+c.name)
				}
				requireEqual(t, "stored session", sessionRow(t, e, doc.ID), stored)
				requireEqual(t, "progress", progressRow(t, e, user.ID, saga1BookID), progress)
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != rows {
					t.Fatalf("refused deletes removed %d rows", rows-got)
				}
			},
		},
		{
			Requirement: "route-r143", Invariant: "INV-R143-CONTRACT-001", Case: "normal", State: "initial", Assertion: "sessions-batch-delete-stored",
			Sources: []string{routeBatchDelete, batchDeleteSrc, "client/pages/config/sessions.vue:297-312"},
			Claim:   "POST /api/sessions/batch/delete as an admin answers 200 with no body and removes exactly the listed stored sessions, keeping the others.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-batch", map[string]any{})
				first := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-batch-1", 3)
				second := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-batch-2", 4)
				keep := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-batch-3", 5)
				keepRow := sessionRow(t, e, keep.ID)
				requireStatus(t, e.Post(t, root, "/api/sessions/batch/delete", map[string]any{"sessions": []string{first.ID, second.ID}}), http.StatusOK, "POST /api/sessions/batch/delete")
				if sessionRow(t, e, first.ID) != nil || sessionRow(t, e, second.ID) != nil {
					t.Fatal("a listed session is still stored")
				}
				requireEqual(t, "the unlisted session", sessionRow(t, e, keep.ID), keepRow)
				if total := userSessionTotal(t, e, root, user.ID); total != 1 {
					t.Fatalf("history total after the batch delete: %d", total)
				}
			},
		},
		{
			Requirement: "route-r143", Invariant: "INV-R143-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "sessions-batch-delete-repeat",
			Sources: []string{routeBatchDelete, batchDeleteSrc},
			Claim:   "Repeating the same batch delete answers 200 and changes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-batch-repeat", map[string]any{})
				keep := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-batch-repeat-keep", 3)
				gone := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-batch-repeat-gone", 4)
				body := map[string]any{"sessions": []string{gone.ID}}
				requireStatus(t, e.Post(t, root, "/api/sessions/batch/delete", body), http.StatusOK, "first batch")
				keepRow := sessionRow(t, e, keep.ID)
				requireStatus(t, e.Post(t, root, "/api/sessions/batch/delete", body), http.StatusOK, "repeated batch")
				requireEqual(t, "the remaining session", sessionRow(t, e, keep.ID), keepRow)
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE userId = ?", user.ID); rows != 1 {
					t.Fatalf("a repeated batch must change no row, got %d", rows)
				}
			},
		},
		{
			Requirement: "route-r143", Invariant: "INV-R143-CONTRACT-001", Case: "normal", State: "changed", Assertion: "sessions-batch-delete-open",
			Sources: []string{routeBatchDelete, batchDeleteSrc, "server/controllers/SessionController.js:236-243"},
			Claim:   "A batch that lists an open session removes the open session without a user_session_closed event and deletes its stored row.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-batch-open", map[string]any{})
				own := e.Socket(t, user.Token)
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-batch-open", "audio/mpeg"))
				syncSession(t, e, user.Token, doc.ID, syncBody(28, 3))
				waitListening(t, e, doc.ID, 3)
				progress := progressRow(t, e, user.ID, saga1BookID)
				mark := own.Mark()
				requireStatus(t, e.Post(t, root, "/api/sessions/batch/delete", map[string]any{"sessions": []string{doc.ID}}), http.StatusOK, "batch delete of an open session")
				if contains(openSessionIDs(t, e), doc.ID) {
					t.Fatal("the open session is still listed as open")
				}
				if _, status := getSession(t, e, user.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("the deleted open session must be gone, GET answered %d", status)
				}
				if sessionRow(t, e, doc.ID) != nil {
					t.Fatal("the stored row of the open session must be gone")
				}
				requireEqual(t, "progress of the deleted session", progressRow(t, e, user.ID, saga1BookID), progress)
				settle(t, own)
				if ids := closedEvents(t, own, mark); len(ids) != 0 {
					t.Fatalf("a batch delete must not announce user_session_closed: %v", ids)
				}
			},
		},
		{
			Requirement: "route-r143", Invariant: "INV-R143-CONTRACT-001", Case: "edge", State: "initial", Assertion: "sessions-batch-delete-duplicates",
			Sources: []string{routeBatchDelete, batchDeleteSrc},
			Claim:   "A batch with a duplicated id and an unknown valid id answers 200 and removes the listed session only.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-batch-dupe", map[string]any{})
				gone := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-batch-dupe-gone", 3)
				keep := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-batch-dupe-keep", 4)
				keepRow := sessionRow(t, e, keep.ID)
				resp := e.Post(t, root, "/api/sessions/batch/delete", map[string]any{"sessions": []string{gone.ID, gone.ID, "00000000-0000-4000-8000-000000000000"}})
				requireStatus(t, resp, http.StatusOK, "batch delete with duplicates")
				if sessionRow(t, e, gone.ID) != nil {
					t.Fatal("the listed session is still stored")
				}
				requireEqual(t, "the unlisted session", sessionRow(t, e, keep.ID), keepRow)
			},
		},
		{
			Requirement: "route-r143", Invariant: "INV-R143-CONTRACT-001", Case: "error", State: "failure", Assertion: "sessions-batch-delete-refusals",
			Sources: []string{routeBatchDelete, batchDeleteSrc},
			Claim:   "POST /api/sessions/batch/delete is 401 without a token, 403 for a non-admin (checked before validation), 400 for a missing, empty or non-uuid sessions array, and removes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				listener := fixtureActor(t, e, "listener")
				user := createUser(t, e, "ps-batch-refuse", map[string]any{})
				keep := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-batch-refuse", 3)
				keepRow := sessionRow(t, e, keep.ID)
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				for _, c := range []struct {
					name, token string
					body        any
					status      int
				}{
					{"no token", "", map[string]any{"sessions": []string{keep.ID}}, http.StatusUnauthorized},
					{"invalid token", "not-a-token", map[string]any{"sessions": []string{keep.ID}}, http.StatusUnauthorized},
					{"non-admin", listener.Token, map[string]any{}, http.StatusForbidden},
					{"non-admin with a valid body", listener.Token, map[string]any{"sessions": []string{keep.ID}}, http.StatusForbidden},
					{"missing sessions", root, map[string]any{}, http.StatusBadRequest},
					{"empty sessions", root, map[string]any{"sessions": []string{}}, http.StatusBadRequest},
					{"non-array sessions", root, map[string]any{"sessions": "not-a-list"}, http.StatusBadRequest},
					{"non-uuid entry", root, map[string]any{"sessions": []string{keep.ID, "not-a-uuid"}}, http.StatusBadRequest},
				} {
					requireStatus(t, e.Post(t, c.token, "/api/sessions/batch/delete", c.body), c.status, "batch delete: "+c.name)
				}
				requireEqual(t, "the listed session", sessionRow(t, e, keep.ID), keepRow)
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != rows {
					t.Fatalf("refused batch deletes removed %d rows", rows-got)
				}
			},
		},
	}
}
