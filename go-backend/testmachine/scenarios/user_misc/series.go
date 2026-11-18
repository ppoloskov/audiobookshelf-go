package user_misc

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	removeSource = "server/controllers/MeController.js:526-543"
	readdSource  = "server/controllers/MeController.js:545-562"
	hideSource   = "server/models/User.js:920-945"
	seriesClient = "client/components/cards/LazyBookCard.vue:834"
	readdClient  = "client/components/app/BookShelfToolbar.vue:459"
)

// userRow returns users.updatedAt of a user.
func userUpdatedAt(t *testing.T, e *harness.Env, userID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT updatedAt FROM users WHERE id = ?", userID)
	if len(rows) != 1 {
		t.Fatalf("user %s has no row", userID)
	}
	return fmtString(rows[0]["updatedAt"])
}

func fmtString(value any) string {
	text, _ := value.(string)
	return text
}

func seriesScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r118", Invariant: "INV-R118-CONTRACT-001", Case: "normal", State: "initial", Assertion: "series-remove-hides",
			Sources: []string{"server/routers/ApiRouter.js:192", removeSource, hideSource, seriesClient},
			Claim:   "GET /api/me/series/:id/remove-from-continue-listening appends the existing series to the listener's stored hidden list, answers 200 with the caller's browser user view (identical to GET /api/me) and emits user_updated with that view to the caller only.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				ls := e.Socket(t, listener)
				rs := e.Socket(t, e.Token(t, "root"))
				fs := e.Socket(t, e.Token(t, "finisher"))
				before := userUpdatedAt(t, e, e.IDs["listener"])
				equalJSON(t, "hidden list before", hiddenSeries(t, e, e.IDs["listener"]), []any{})
				mark, rootMark, finisherMark := ls.Mark(), rs.Mark(), fs.Mark()

				resp := e.Get(t, listener, "/api/me/series/"+seriesID+"/remove-from-continue-listening")
				wantStatus(t, resp, http.StatusOK, "remove series from continue listening")
				body := anyJSON(t, resp)
				field := func(name string) any {
					object, _ := body.(map[string]any)
					return object[name]
				}
				if field("id") != e.IDs["listener"] || field("username") != "trial-listener" {
					t.Fatalf("response is not the listener's view: %s", resp.Body)
				}
				equalJSON(t, "response hidden list", field("seriesHideFromContinueListening"), []any{seriesID})
				equalJSON(t, "stored hidden list", hiddenSeries(t, e, e.IDs["listener"]), []any{seriesID})
				equalJSON(t, "response equals GET /api/me", anyJSON(t, e.Get(t, listener, "/api/me")), body)
				if after := userUpdatedAt(t, e, e.IDs["listener"]); after == before {
					t.Fatalf("a changed user row must move updatedAt forward: %s", after)
				}

				event := ls.Wait(t, mark, "user_updated", 5*time.Second, nil)
				if len(event.Args) != 1 {
					t.Fatalf("user_updated must carry one argument: %v", event.Args)
				}
				var payload any
				if err := json.Unmarshal(event.Args[0], &payload); err != nil {
					t.Fatalf("user_updated payload: %v", err)
				}
				equalJSON(t, "user_updated payload", payload, body)
				rs.None(t, rootMark, "user_updated", 500*time.Millisecond, nil)
				fs.None(t, finisherMark, "user_updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r118", Invariant: "INV-R118-IDEMPOTENT-001", Case: "normal", State: "repeat", Assertion: "series-remove-repeat",
			Sources: []string{removeSource, hideSource},
			Claim:   "Removing the already hidden series again answers 200 with the same single-entry hidden list, does not duplicate the id, leaves users.extraData and updatedAt unchanged and emits no user_updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				socket := e.Socket(t, listener)
				before := userUpdatedAt(t, e, e.IDs["listener"])
				mark := socket.Mark()
				resp := e.Get(t, listener, "/api/me/series/"+seriesID+"/remove-from-continue-listening")
				wantStatus(t, resp, http.StatusOK, "repeat remove")
				var body struct {
					SeriesHideFromContinueListening []string `json:"seriesHideFromContinueListening"`
				}
				resp.JSON(t, &body)
				equalJSON(t, "hidden list", body.SeriesHideFromContinueListening, []string{seriesID})
				equalJSON(t, "stored hidden list", hiddenSeries(t, e, e.IDs["listener"]), []any{seriesID})
				if after := userUpdatedAt(t, e, e.IDs["listener"]); after != before {
					t.Fatalf("no-op remove must not touch the user row: %s -> %s", before, after)
				}
				socket.None(t, mark, "user_updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r118", Invariant: "INV-R118-CONTRACT-001", Case: "edge", State: "changed", Assertion: "series-remove-other-user",
			Sources: []string{removeSource, hideSource},
			Claim:   "The finisher, who has no continue-listening progress on the series, can hide it too: their own view and row list it, while the listener's hidden list is unaffected and the response only shows the caller's list.",
			Run: func(t *testing.T, e *harness.Env) {
				finisher := e.Token(t, "finisher")
				resp := e.Get(t, finisher, "/api/me/series/"+seriesID+"/remove-from-continue-listening")
				wantStatus(t, resp, http.StatusOK, "finisher removes series")
				var body struct {
					ID                              string   `json:"id"`
					SeriesHideFromContinueListening []string `json:"seriesHideFromContinueListening"`
				}
				resp.JSON(t, &body)
				if body.ID != finisherID || len(body.SeriesHideFromContinueListening) != 1 || body.SeriesHideFromContinueListening[0] != seriesID {
					t.Fatalf("finisher view wrong: %s", resp.Body)
				}
				equalJSON(t, "finisher stored hidden list", hiddenSeries(t, e, finisherID), []any{seriesID})
				equalJSON(t, "listener hidden list", hiddenSeries(t, e, e.IDs["listener"]), []any{seriesID})
			},
		},
		{
			Requirement: "route-r118", Invariant: "INV-R118-ERRORS-001", Case: "error", State: "failure", Assertion: "series-remove-not-found",
			Sources: []string{removeSource, "server/routers/ApiRouter.js:192"},
			Claim:   "Removing an unknown series answers 404 and leaves both users' hidden lists unchanged; without a token the route answers 401.",
			Run: func(t *testing.T, e *harness.Env) {
				beforeListener := hiddenSeries(t, e, e.IDs["listener"])
				beforeFinisher := hiddenSeries(t, e, finisherID)
				for _, token := range []string{e.Token(t, "listener"), e.Token(t, "finisher")} {
					resp := e.Get(t, token, "/api/me/series/00000000-0000-4000-8000-000000000000/remove-from-continue-listening")
					wantStatus(t, resp, http.StatusNotFound, "unknown series")
				}
				equalJSON(t, "listener hidden list after 404", hiddenSeries(t, e, e.IDs["listener"]), beforeListener)
				equalJSON(t, "finisher hidden list after 404", hiddenSeries(t, e, finisherID), beforeFinisher)
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/series/"+seriesID+"/remove-from-continue-listening")
			},
		},
		{
			Requirement: "route-r119", Invariant: "INV-R119-CONTRACT-001", Case: "normal", State: "changed", Assertion: "series-readd-restores",
			Sources: []string{"server/routers/ApiRouter.js:193", readdSource, hideSource, readdClient},
			Claim:   "GET /api/me/series/:id/readd-to-continue-listening removes only that series from the listener's stored hidden list, answers 200 with the updated browser user view and emits user_updated to the caller only.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				ls := e.Socket(t, listener)
				fs := e.Socket(t, e.Token(t, "finisher"))
				before := userUpdatedAt(t, e, e.IDs["listener"])
				mark, finisherMark := ls.Mark(), fs.Mark()
				resp := e.Get(t, listener, "/api/me/series/"+seriesID+"/readd-to-continue-listening")
				wantStatus(t, resp, http.StatusOK, "readd series")
				body := anyJSON(t, resp)
				object, _ := body.(map[string]any)
				equalJSON(t, "response hidden list", object["seriesHideFromContinueListening"], []any{})
				equalJSON(t, "stored hidden list", hiddenSeries(t, e, e.IDs["listener"]), []any{})
				equalJSON(t, "response equals GET /api/me", anyJSON(t, e.Get(t, listener, "/api/me")), body)
				if after := userUpdatedAt(t, e, e.IDs["listener"]); after == before {
					t.Fatalf("a changed user row must move updatedAt forward: %s", after)
				}
				event := ls.Wait(t, mark, "user_updated", 5*time.Second, nil)
				if len(event.Args) != 1 {
					t.Fatalf("user_updated must carry one argument: %v", event.Args)
				}
				var payload any
				if err := json.Unmarshal(event.Args[0], &payload); err != nil {
					t.Fatalf("user_updated payload: %v", err)
				}
				equalJSON(t, "user_updated payload", payload, body)
				fs.None(t, finisherMark, "user_updated", 500*time.Millisecond, nil)
				equalJSON(t, "finisher hidden list untouched", hiddenSeries(t, e, finisherID), []any{seriesID})
			},
		},
		{
			Requirement: "route-r119", Invariant: "INV-R119-IDEMPOTENT-001", Case: "normal", State: "repeat", Assertion: "series-readd-repeat",
			Sources: []string{readdSource, hideSource},
			Claim:   "Readding the series when nothing is hidden answers 200 with an empty hidden list, leaves the row and updatedAt unchanged and emits no user_updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				socket := e.Socket(t, listener)
				before := userUpdatedAt(t, e, e.IDs["listener"])
				mark := socket.Mark()
				resp := e.Get(t, listener, "/api/me/series/"+seriesID+"/readd-to-continue-listening")
				wantStatus(t, resp, http.StatusOK, "repeat readd")
				var body struct {
					SeriesHideFromContinueListening []string `json:"seriesHideFromContinueListening"`
				}
				resp.JSON(t, &body)
				if body.SeriesHideFromContinueListening == nil || len(body.SeriesHideFromContinueListening) != 0 {
					t.Fatalf("hidden list must stay empty: %s", resp.Body)
				}
				if after := userUpdatedAt(t, e, e.IDs["listener"]); after != before {
					t.Fatalf("no-op readd must not touch the user row: %s -> %s", before, after)
				}
				socket.None(t, mark, "user_updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r119", Invariant: "INV-R119-CONTRACT-001", Case: "edge", State: "initial", Assertion: "series-readd-without-hidden-list",
			Sources: []string{readdSource},
			Claim:   "A newly created user who never hid a series gets 200 with an unchanged empty hidden list and no user_updated event when calling readd, so the route needs no hidden entry to exist.",
			Run: func(t *testing.T, e *harness.Env) {
				userID, token := newUser(t, e, map[string]any{"username": "misc-series-new", "type": "user"}, "misc-series-password")
				socket := e.Socket(t, token)
				before := userUpdatedAt(t, e, userID)
				mark := socket.Mark()
				resp := e.Get(t, token, "/api/me/series/"+seriesID+"/readd-to-continue-listening")
				wantStatus(t, resp, http.StatusOK, "readd for a user without a hidden list")
				equalJSON(t, "hidden list", hiddenSeries(t, e, userID), []any{})
				if after := userUpdatedAt(t, e, userID); after != before {
					t.Fatalf("no-op readd must not touch the user row: %s -> %s", before, after)
				}
				socket.None(t, mark, "user_updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r119", Invariant: "INV-R119-ERRORS-001", Case: "error", State: "failure", Assertion: "series-readd-not-found",
			Sources: []string{readdSource, "server/routers/ApiRouter.js:193"},
			Claim:   "Readding an unknown series answers 404 and leaves the stored hidden lists unchanged; without a token the route answers 401.",
			Run: func(t *testing.T, e *harness.Env) {
				before := hiddenSeries(t, e, e.IDs["listener"])
				resp := e.Get(t, e.Token(t, "listener"), "/api/me/series/00000000-0000-4000-8000-000000000000/readd-to-continue-listening")
				wantStatus(t, resp, http.StatusNotFound, "unknown series")
				equalJSON(t, "hidden list after 404", hiddenSeries(t, e, e.IDs["listener"]), before)
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/series/"+seriesID+"/readd-to-continue-listening")
			},
		},
	}
}
