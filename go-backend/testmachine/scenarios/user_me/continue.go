package user_me

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	continueRoute  = "server/routers/ApiRouter.js:182"
	continueSource = "server/controllers/MeController.js:570-587"
	eventWait      = 5 * time.Second
	eventWindow    = 700 * time.Millisecond
)

// removeFromContinue calls GET /api/me/progress/:id/remove-from-continue-listening.
func removeFromContinue(e *harness.Env, t *testing.T, token, id string) harness.Response {
	t.Helper()
	return e.Get(t, token, "/api/me/progress/"+id+"/remove-from-continue-listening")
}

// finisherHidden reads the stored hide flag of the finisher's seeded progress row.
func finisherHidden(t *testing.T, e *harness.Env) int64 {
	t.Helper()
	rows := e.Rows(t, "SELECT hideFromContinueListening AS hidden FROM mediaProgresses WHERE id = ?", finisherProgress)
	if len(rows) != 1 {
		t.Fatalf("finisher's progress row missing: %v", rows)
	}
	hidden, ok := rows[0]["hidden"].(int64)
	if !ok {
		t.Fatalf("hide flag is not an integer: %v", rows[0])
	}
	return hidden
}

// isUserUpdatedFor matches a user_updated payload of the user id.
func isUserUpdatedFor(userID string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var user struct {
			ID string `json:"id"`
		}
		return len(args) > 0 && json.Unmarshal(args[0], &user) == nil && user.ID == userID
	}
}

// requireSeededHidden requires the finisher's seeded progress in the list to
// be the seeded row with only hideFromContinueListening set and lastUpdate
// moved forward by the save, and every other entry to stay visible.
func requireSeededHidden(t *testing.T, e *harness.Env, what string, list []progressJSON) {
	t.Helper()
	found := false
	for _, progress := range list {
		if progress.ID != finisherProgress {
			if progress.HideFromContinueListening {
				t.Fatalf("%s: only the selected progress may be hidden: %+v", what, progress)
			}
			continue
		}
		found = true
		if progress.LastUpdate <= finisherSaga1Progress(e).LastUpdate {
			t.Fatalf("%s: lastUpdate must move forward: %+v", what, progress)
		}
		progress.LastUpdate = finisherSaga1Progress(e).LastUpdate
		want := finisherSaga1Progress(e)
		want.HideFromContinueListening = true
		equalJSON(t, what, progress, want)
	}
	if !found {
		t.Fatalf("%s: the progress is missing: %+v", what, list)
	}
}

func continueScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "error", State: "initial", Assertion: "remove-continue-rejections",
			Sources: []string{continueRoute, continueSource},
			Claim:   "GET /api/me/progress/:id/remove-from-continue-listening answers 401 without a token and 404 for an unknown progress id and for another user's progress id, hiding nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/progress/"+finisherProgress+"/remove-from-continue-listening")
				wantStatus(t, removeFromContinue(e, t, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown id")
				wantStatus(t, removeFromContinue(e, t, e.Token(t, "listener"), finisherProgress), http.StatusNotFound, "another user's progress")
				wantStatus(t, removeFromContinue(e, t, e.Token(t, "root"), finisherProgress), http.StatusNotFound, "root is not special")
				if finisherHidden(t, e) != 0 {
					t.Fatal("rejected requests must not hide the progress")
				}
			},
		},
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "edge", State: "initial", Assertion: "remove-continue-needs-progress-id-not-item-id",
			Sources: []string{continueSource, "server/routers/ApiRouter.js:183"},
			Claim:   "The id of GET /api/me/progress/:id/remove-from-continue-listening is the media progress id: a library item id answers 404 and hides nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				wantStatus(t, removeFromContinue(e, t, e.Token(t, "finisher"), e.IDs["saga1"]), http.StatusNotFound, "library item id")
				if finisherHidden(t, e) != 0 {
					t.Fatal("the progress must stay visible")
				}
			},
		},
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "error", State: "failure", Assertion: "remove-continue-rejected-no-event",
			Sources: []string{continueSource},
			Claim:   "A rejected remove-from-continue-listening request leaves the progress row unchanged and sends no user_updated event to the owner.",
			Run: func(t *testing.T, e *harness.Env) {
				owner := e.Socket(t, e.Token(t, "finisher"))
				mark := owner.Mark()
				wantStatus(t, removeFromContinue(e, t, e.Token(t, "listener"), finisherProgress), http.StatusNotFound, "another user's progress")
				owner.None(t, mark, "user_updated", eventWindow, nil)
				if finisherHidden(t, e) != 0 {
					t.Fatal("the progress must stay visible")
				}
			},
		},
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "normal", State: "initial", Assertion: "remove-continue-hides-and-emits",
			Sources: []string{continueRoute, continueSource, "server/SocketAuthority.js:168"},
			Claim:   "GET /api/me/progress/:id/remove-from-continue-listening sets hideFromContinueListening on the caller's row, answers 200 with the caller's browser view showing it hidden, and emits user_updated to the caller's sockets only.",
			Run: func(t *testing.T, e *harness.Env) {
				owner := e.Socket(t, e.Token(t, "finisher"))
				listener := e.Socket(t, e.Token(t, "listener"))
				admin := e.Socket(t, e.Token(t, "root"))
				ownerMark, listenerMark, adminMark := owner.Mark(), listener.Mark(), admin.Mark()
				resp := removeFromContinue(e, t, e.Token(t, "finisher"), finisherProgress)
				wantStatus(t, resp, http.StatusOK, "hide")
				var user userJSON
				resp.JSON(t, &user)
				if user.ID != finisherID || user.Username != "trial-finisher" {
					t.Fatalf("response must be the caller's browser view: %s", resp.Body)
				}
				requireSeededHidden(t, e, "mediaProgress", user.MediaProgress)
				if finisherHidden(t, e) != 1 {
					t.Fatal("the row must be hidden")
				}
				event := owner.Wait(t, ownerMark, "user_updated", eventWait, isUserUpdatedFor(finisherID))
				var pushed userJSON
				if err := json.Unmarshal(event.Args[0], &pushed); err != nil {
					t.Fatal(err)
				}
				requireSeededHidden(t, e, "event mediaProgress", pushed.MediaProgress)
				listener.None(t, listenerMark, "user_updated", eventWindow, isUserUpdatedFor(finisherID))
				admin.None(t, adminMark, "user_updated", eventWindow, isUserUpdatedFor(finisherID))
			},
		},
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "remove-continue-repeat-is-quiet",
			Sources: []string{continueSource},
			Claim:   "Repeating remove-from-continue-listening on an already hidden progress answers 200 with the same browser view, leaves the complete stored progress row and lastUpdate unchanged, keeps it hidden and emits no event.",
			Run: func(t *testing.T, e *harness.Env) {
				owner := e.Socket(t, e.Token(t, "finisher"))
				var before userJSON
				getJSON(t, e, e.Token(t, "finisher"), "/api/me", &before)
				requireSeededHidden(t, e, "before repeat", before.MediaProgress)
				storedBefore := e.Rows(t, "SELECT * FROM mediaProgresses WHERE id = ?", finisherProgress)
				if len(storedBefore) != 1 {
					t.Fatalf("expected one stored progress before repeat: %v", storedBefore)
				}
				mark := owner.Mark()
				resp := removeFromContinue(e, t, e.Token(t, "finisher"), finisherProgress)
				wantStatus(t, resp, http.StatusOK, "repeat")
				var user userJSON
				resp.JSON(t, &user)
				requireSeededHidden(t, e, "mediaProgress", user.MediaProgress)
				equalJSON(t, "browser view unchanged by repeat", user, before)
				equalJSON(t, "stored progress unchanged by repeat", e.Rows(t, "SELECT * FROM mediaProgresses WHERE id = ?", finisherProgress), storedBefore)
				owner.None(t, mark, "user_updated", eventWindow, isUserUpdatedFor(finisherID))
				if finisherHidden(t, e) != 1 {
					t.Fatal("the row must stay hidden")
				}
			},
		},
		{
			Requirement: "route-r108", Invariant: "INV-R108-CONTRACT-001", Case: "normal", State: "changed", Assertion: "remove-continue-visible-in-reads",
			Sources: []string{continueSource, "server/models/MediaProgress.js:155-176"},
			Claim:   "After the hide, GET /api/me, /api/me/progress and /api/me/progress/:libraryItemId show hideFromContinueListening true for the finisher while the listener's progress of the same item stays visible.",
			Run: func(t *testing.T, e *harness.Env) {
				finisher := e.Token(t, "finisher")
				var me userJSON
				getJSON(t, e, finisher, "/api/me", &me)
				requireSeededHidden(t, e, "me", me.MediaProgress)
				var list struct {
					MediaProgress []progressJSON `json:"mediaProgress"`
				}
				getJSON(t, e, finisher, "/api/me/progress", &list)
				requireSeededHidden(t, e, "progress list", list.MediaProgress)
				requireSeededHidden(t, e, "item progress", []progressJSON{getProgress(t, e, finisher, "/api/me/progress/"+e.IDs["saga1"])})
				listenerView := getProgress(t, e, e.Token(t, "listener"), "/api/me/progress/"+e.IDs["saga1"])
				if listenerView.HideFromContinueListening {
					t.Fatal("the listener's progress must stay visible")
				}
			},
		},
	}
}
