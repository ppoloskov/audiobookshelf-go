package user_progress

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const removeSource = "server/controllers/MeController.js:253-267"

func removeScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r112", Invariant: "INV-R112-CONTRACT-001", Case: "normal", State: "initial", Assertion: "remove-own-progress",
			Sources: []string{"server/routers/ApiRouter.js:186", removeSource, "server/models/MediaProgress.js:41-47"},
			Claim:   "DELETE /api/me/progress/:id removes the caller's row (book and episode), leaves the caller's other rows, answers 200, makes GET progress 404 and emits user_updated without that progress to the caller only.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				patchOK(t, e, who, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"currentTime": 4, "duration": 24, "progress": 4.0 / 24})
				book, _ := text(progressRow(t, e, who.id, saga1Book), "id")
				episode, _ := text(progressRow(t, e, who.id, e.IDs["ep1"]), "id")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+book), http.StatusOK, "delete book progress")
				ev := sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
				user := decodeUserUpdated(t, ev)
				if len(user.MediaProgress) != 1 || user.MediaProgress[0].ID != episode {
					t.Fatalf("user_updated must list only the episode progress: %+v", user.MediaProgress)
				}
				if rows := progressRows(t, e, who.id); len(rows) != 1 || rows[0]["id"] != episode {
					t.Fatalf("only the episode row must remain: %v", rows)
				}
				wantStatus(t, e.Get(t, who.token, progressPath(e.IDs["saga1"], "")), http.StatusNotFound, "progress after delete")
				mark = sockets.mark()
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+episode), http.StatusOK, "delete episode progress")
				user = decodeUserUpdated(t, sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id)))
				if len(user.MediaProgress) != 0 || user.MediaProgress == nil {
					t.Fatalf("user_updated must carry an empty mediaProgress array: %+v", user.MediaProgress)
				}
				requireNoRows(t, e, who.id)
				wantStatus(t, e.Get(t, who.token, progressPath(e.IDs["podcast"], e.IDs["ep1"])), http.StatusNotFound, "episode progress after delete")
			},
		},
		{
			Requirement: "route-r112", Invariant: "INV-R112-CONTRACT-001", Case: "edge", State: "changed", Assertion: "remove-then-create-gets-new-id",
			Sources: []string{removeSource, createSource},
			Claim:   "After a delete a new PATCH creates a new row with a new id and a fresh startedAt; the deleted id answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := seedBook(t, e, who)
				id, _ := text(first, "id")
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+id), http.StatusOK, "delete")
				nextMillisecond()
				row := seedBook(t, e, who)
				if newID, _ := text(row, "id"); newID == id {
					t.Fatal("a recreated row must get a new id")
				}
				if !stamp(t, row, "createdAt").After(stamp(t, first, "createdAt")) {
					t.Fatalf("startedAt must be fresh: %v then %v", first["createdAt"], row["createdAt"])
				}
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+id), http.StatusNotFound, "deleted id again")
			},
		},
		{
			Requirement: "route-r112", Invariant: "INV-R112-ERROR-001", Case: "error", State: "failure", Assertion: "remove-rejections-change-nothing",
			Sources: []string{removeSource, "server/Server.js:354", "server/Server.js:160"},
			Claim:   "DELETE answers 401 unauthenticated and 404 for an unknown id, for another user's progress id and for a repeated delete, and removes no row and emits no event.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodDelete, "/api/me/progress/"+unknownID, nil)
				who, other := newActor(t, e), newActor(t, e)
				seedBook(t, e, other)
				otherID, _ := text(progressRow(t, e, other.id, saga1Book), "id")
				mine, _ := text(seedBook(t, e, who), "id")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+unknownID), http.StatusNotFound, "unknown id")
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+otherID), http.StatusNotFound, "another user's id")
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+"b95b3028-3461-4306-803b-6e310140cd22"), http.StatusNotFound, "seeded listener row")
				if n := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE id IN (?, ?, ?)", otherID, mine, "b95b3028-3461-4306-803b-6e310140cd22"); n != 3 {
					t.Fatalf("rejected deletes must keep all rows, %d of 3 remain", n)
				}
				sockets.nobody(t, mark, "user_updated")
			},
		},
	}
}
