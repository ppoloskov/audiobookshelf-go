package user_progress

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const batchSource = "server/controllers/MeController.js:297-320"
const batchPath = "/api/me/progress/batch/update"

func batchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r110", Invariant: "INV-R110-CONTRACT-001", Case: "normal", State: "initial", Assertion: "batch-mixed-entries",
			Sources: []string{"server/routers/ApiRouter.js:184", batchSource, createSource},
			Claim:   "PATCH /api/me/progress/batch/update answers 200, stores the valid entries (book, episode, finished book with episodeId null) with the single-update rules, skips unknown items and a podcast without episode, and emits one user_updated with all stored rows to the caller only.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				before := time.Now()
				resp := e.Patch(t, who.token, batchPath, []map[string]any{
					{"libraryItemId": e.IDs["saga1"], "currentTime": 10, "duration": 60, "progress": 10.0 / 60},
					{"libraryItemId": unknownID, "currentTime": 1},
					{"libraryItemId": e.IDs["podcast"], "currentTime": 1},
					{"libraryItemId": e.IDs["podcast"], "episodeId": e.IDs["ep1"], "currentTime": 6, "duration": 24, "progress": 0.25},
					{"libraryItemId": e.IDs["novel"], "episodeId": nil, "isFinished": true},
					{"libraryItemId": e.IDs["podcast"], "episodeId": unknownID, "currentTime": 1},
				})
				wantStatus(t, resp, http.StatusOK, "batch")
				after := time.Now()
				if n := len(progressRows(t, e, who.id)); n != 3 {
					t.Fatalf("want 3 stored rows, got %d", n)
				}
				book := progressRow(t, e, who.id, saga1Book)
				requireRow(t, book, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 10, progress: 10.0 / 60})
				episode := progressRow(t, e, who.id, e.IDs["ep1"])
				requireRow(t, episode, who.id, e.IDs["ep1"], wantRow{item: e.IDs["podcast"], mediaType: "podcastEpisode", podcastID: podcastRow, duration: 24, current: 6, progress: 0.25})
				novel := progressRow(t, e, who.id, novelBook)
				requireRow(t, novel, who.id, novelBook, wantRow{item: e.IDs["novel"], mediaType: "book", progress: 1, finished: true, finishedAt: "now", before: before, after: after})

				ev := sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
				user := decodeUserUpdated(t, ev)
				byItem := progressByItem(user.MediaProgress)
				if len(user.MediaProgress) != 3 {
					t.Fatalf("user_updated carries %d progress objects, want 3", len(user.MediaProgress))
				}
				requireAPIMatchesRow(t, byItem[saga1Book], book, who.id, e.IDs["saga1"], "")
				requireAPIMatchesRow(t, byItem[e.IDs["ep1"]], episode, who.id, e.IDs["podcast"], e.IDs["ep1"])
				requireAPIMatchesRow(t, byItem[novelBook], novel, who.id, e.IDs["novel"], "")
				sockets.nobody(t, mark, "user_item_progress_updated")
			},
		},
		{
			Requirement: "route-r110", Invariant: "INV-R110-CONTRACT-001", Case: "normal", State: "initial", Assertion: "batch-mark-finished-as-clients-send-it",
			Sources: []string{batchSource, "go-backend/testmachine/contracts/clients/absorb.md"},
			Claim:   "A batch of {libraryItemId, isFinished:true} entries (the form clients send) creates finished rows for books and finishes existing unfinished rows without touching their position.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				before := time.Now()
				wantStatus(t, e.Patch(t, who.token, batchPath, []map[string]any{
					{"libraryItemId": e.IDs["saga1"], "isFinished": true},
					{"libraryItemId": e.IDs["saga2"], "isFinished": true},
				}), http.StatusOK, "batch")
				after := time.Now()
				requireRow(t, progressRow(t, e, who.id, saga1Book), who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 1, finished: true, finishedAt: "now", before: before, after: after})
				requireRow(t, progressRow(t, e, who.id, saga2Book), who.id, saga2Book, wantRow{item: e.IDs["saga2"], mediaType: "book", progress: 1, finished: true, finishedAt: "now", before: before, after: after})
			},
		},
		{
			Requirement: "route-r110", Invariant: "INV-R110-CONTRACT-001", Case: "edge", State: "repeat", Assertion: "batch-repeat-changes-nothing-but-still-emits",
			Sources: []string{batchSource},
			Claim:   "Sending the same batch again leaves the rows unchanged (updatedAt included) and still emits user_updated, because an entry was accepted.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				batch := []map[string]any{{"libraryItemId": e.IDs["saga1"], "currentTime": 10, "duration": 60, "progress": 10.0 / 60}}
				wantStatus(t, e.Patch(t, who.token, batchPath, batch), http.StatusOK, "first batch")
				first := progressRow(t, e, who.id, saga1Book)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				nextMillisecond()
				wantStatus(t, e.Patch(t, who.token, batchPath, batch), http.StatusOK, "second batch")
				sameRow(t, "repeat", first, progressRow(t, e, who.id, saga1Book))
				sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
			},
		},
		{
			Requirement: "route-r110", Invariant: "INV-R110-CONTRACT-001", Case: "edge", State: "changed", Assertion: "batch-entries-apply-in-order",
			Sources: []string{batchSource, applySource},
			Claim:   "Entries apply in order on the same row: a later entry for the same item updates what the earlier one created, and a per-entry lastUpdate becomes that row's updatedAt.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				wantStatus(t, e.Patch(t, who.token, batchPath, []map[string]any{
					{"libraryItemId": e.IDs["saga1"], "currentTime": 10, "duration": 60, "progress": 10.0 / 60},
					{"libraryItemId": e.IDs["saga1"], "currentTime": 20, "progress": 20.0 / 60, "lastUpdate": 1700000000000},
				}), http.StatusOK, "batch")
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 20, progress: 20.0 / 60})
				if got, _ := text(row, "updatedAt"); got != "2023-11-14 22:13:20.000 +00:00" {
					t.Fatalf("updatedAt = %q, want the entry's lastUpdate", got)
				}
				if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.LastUpdate != 1700000000000 {
					t.Fatalf("lastUpdate = %d", got.LastUpdate)
				}
			},
		},
		{
			Requirement: "route-r110", Invariant: "INV-R110-ERROR-001", Case: "error", State: "failure", Assertion: "batch-rejections-change-nothing",
			Sources: []string{batchSource},
			Claim:   "A batch that is empty, an object or missing answers 400; a batch of only invalid entries answers 200; none stores a row or emits an event.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				for _, body := range []any{[]any{}, map[string]any{}, map[string]any{"libraryItemId": e.IDs["saga1"], "currentTime": 3}} {
					wantStatus(t, e.Patch(t, who.token, batchPath, body), http.StatusBadRequest, "unusable payload")
				}
				wantStatus(t, e.Do(t, harness.Request{Method: http.MethodPatch, Path: batchPath, Token: who.token}), http.StatusBadRequest, "no body")
				wantStatus(t, e.Patch(t, who.token, batchPath, []map[string]any{
					{"libraryItemId": unknownID, "currentTime": 1},
					{"libraryItemId": e.IDs["podcast"], "currentTime": 1},
					{"libraryItemId": e.IDs["podcast"], "episodeId": unknownID},
				}), http.StatusOK, "only invalid entries")
				requireNoRows(t, e, who.id)
				sockets.nobody(t, mark, "user_updated")
			},
		},
		{
			Requirement: "route-r110", Invariant: "INV-R110-ERROR-001", Case: "error", State: "failure", Assertion: "batch-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/Server.js:160", batchSource},
			Claim:   "PATCH /api/me/progress/batch/update answers 401 without a token and with an unknown bearer.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodPatch, batchPath, []map[string]any{{"libraryItemId": e.IDs["saga1"], "isFinished": true}})
			},
		},
	}
}
