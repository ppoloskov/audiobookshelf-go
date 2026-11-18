package user_me

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const progressSource = "server/controllers/MeController.js:239-246"

// getProgress reads GET /api/me/progress/:id/:episodeId? and decodes a 200.
func getProgress(t *testing.T, e *harness.Env, token, path string) progressJSON {
	t.Helper()
	var progress progressJSON
	getJSON(t, e, token, path, &progress)
	return progress
}

func progressScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r109", Invariant: "INV-R109-SAMPLED", Case: "normal", State: "stateless", Assertion: "progress-of-item-and-episode",
			Sources: []string{"server/routers/ApiRouter.js:183", progressSource, "server/models/User.js:716-722", "server/models/MediaProgress.js:155-176"},
			Claim:   "GET /api/me/progress/:libraryItemId returns the caller's progress of a book (in progress, finished, ebook-only) and /:libraryItemId/:episodeId the progress of a podcast episode, with every field of the old progress projection.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				all := listenerProgress(e)
				for _, c := range []struct{ path, id string }{
					{"/api/me/progress/" + e.IDs["saga1"], "b95b3028-3461-4306-803b-6e310140cd22"},
					{"/api/me/progress/" + e.IDs["novel"], "20ae1a03-2eac-4b4a-a595-6220a2f0b475"},
					{"/api/me/progress/" + e.IDs["ebook"], "c896481a-a447-4bf2-ab05-0f6a40906b03"},
					{"/api/me/progress/" + e.IDs["podcast"] + "/" + e.IDs["ep1"], "8b1b3f1b-dd6f-4809-ade0-eeef6ff56c98"},
					{"/api/me/progress/" + e.IDs["podcast"] + "/" + e.IDs["ep2"], "e83ed67e-8012-408d-a9a1-475e6fd83828"},
					{"/api/me/progress/" + e.IDs["podcast"] + "/" + e.IDs["ep3"], "3dc1ddba-4b1a-45fc-8b91-53e355fc52fc"},
				} {
					equalJSON(t, c.path, getProgress(t, e, token, c.path), all[c.id])
				}
			},
		},
		{
			Requirement: "route-r109", Invariant: "INV-R109-SAMPLED", Case: "edge", State: "stateless", Assertion: "progress-of-item-is-per-user",
			Sources: []string{progressSource, "server/models/User.js:716-722"},
			Claim:   "GET /api/me/progress/:libraryItemId returns the caller's own row when several users have progress for the item: the finisher sees its finished row, not the listener's.",
			Run: func(t *testing.T, e *harness.Env) {
				path := "/api/me/progress/" + e.IDs["saga1"]
				equalJSON(t, "finisher", getProgress(t, e, e.Token(t, "finisher"), path), finisherSaga1Progress(e))
				equalJSON(t, "listener", getProgress(t, e, e.Token(t, "listener"), path), listenerProgress(e)["b95b3028-3461-4306-803b-6e310140cd22"])
			},
		},
		{
			Requirement: "route-r109", Invariant: "INV-R109-SAMPLED", Case: "edge", State: "stateless", Assertion: "progress-reflects-write",
			Sources: []string{"server/controllers/MeController.js:263-277", progressSource},
			Claim:   "After PATCH /api/me/progress/:libraryItemId creates progress, GET /api/me/progress/:libraryItemId and the progress list return it with the written currentTime, duration, written progress and a fresh lastUpdate.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "finisher")
				path := "/api/me/progress/" + e.IDs["saga2"]
				wantStatus(t, e.Get(t, token, path), http.StatusNotFound, "no progress yet")
				wantStatus(t, e.Patch(t, token, path, map[string]any{"currentTime": 12, "duration": 30, "progress": 0.4, "isFinished": false}), http.StatusOK, "create progress")
				got := getProgress(t, e, token, path)
				if got.UserID != finisherID || got.LibraryItemID != e.IDs["saga2"] || got.MediaItemID != "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af" || got.MediaItemType != "book" ||
					got.EpisodeID != nil || got.CurrentTime != 12 || got.Duration != 30 || !near(got.Progress, 0.4) || got.IsFinished || got.HideFromContinueListening || got.FinishedAt != nil || got.LastUpdate <= 0 || got.StartedAt <= 0 {
					t.Fatalf("written progress wrong: %+v", got)
				}
				var list struct {
					MediaProgress []progressJSON `json:"mediaProgress"`
				}
				getJSON(t, e, token, "/api/me/progress", &list)
				wantProgressSet(t, "progress list after write", list.MediaProgress, map[string]progressJSON{
					finisherProgress: finisherSaga1Progress(e),
					got.ID:           got,
				})
			},
		},
		{
			Requirement: "route-r109", Invariant: "INV-R109-SAMPLED", Case: "error", State: "stateless", Assertion: "progress-of-item-not-found",
			Sources: []string{"server/Server.js:354", progressSource},
			Claim:   "GET /api/me/progress/:libraryItemId answers 401 without a token and 404 for an unknown id, for an item the caller has no progress on, and for an episode id that does not match the stored row.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/progress/"+e.IDs["saga1"])
				listener := e.Token(t, "listener")
				wantStatus(t, e.Get(t, listener, "/api/me/progress/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown id")
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/me/progress/"+e.IDs["saga1"]), http.StatusNotFound, "root without progress")
				wantStatus(t, e.Get(t, listener, "/api/me/progress/"+e.IDs["saga1"]+"/"+e.IDs["ep1"]), http.StatusNotFound, "episode id that does not match a book row")
				wantStatus(t, e.Get(t, listener, "/api/me/progress/"+e.IDs["podcast"]+"/"+e.IDs["saga1"]), http.StatusNotFound, "episode id of another item")
			},
		},
	}
}
