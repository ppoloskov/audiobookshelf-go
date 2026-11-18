package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	flowPlay   = "server/controllers/LibraryItemController.js:437-472"
	flowSync   = "server/managers/PlaybackSessionManager.js:340-373"
	flowLocal  = "server/managers/PlaybackSessionManager.js:144-273"
	flowClose  = "server/managers/PlaybackSessionManager.js:376-399"
	flowClient = "go-backend/testmachine/contracts/clients/official-app.md:79-80,135-138"
)

const (
	flowLocalA = "4e3f8b9c-5d2f-4e3a-9e0b-2c3d4e5f6a01"
	flowLocalB = "4e3f8b9c-5d2f-4e3a-9e0b-2c3d4e5f6a02"
	flowLocalC = "4e3f8b9c-5d2f-4e3a-9e0b-2c3d4e5f6a03"
)

// flowScenarios covers the client playback journey: start, read, sync, close
// and the offline upload reconciliation.
func flowScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-001", Case: "normal", State: "initial", Assertion: "flow-online-session-lifecycle",
			Sources: []string{flowPlay, routeGet, flowSync, flowClose, flowClient},
			Claim:   "The online journey works as clients use it: play returns the session, GET reads that same start position, sync moves the position but not the start position, and close stores the session and moves the progress.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				user := createUser(t, e, "ps-flow", map[string]any{})
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-flow-1", "audio/mpeg"))
				requireSaga1Session(t, e, doc, user.ID, 0, 0, "exo-player")
				// GET answers the session the play response created.
				requireEqual(t, "GET returns the play-created session", mustGetSession(t, e, user.Token, doc.ID), doc)
				syncSession(t, e, user.Token, doc.ID, syncBody(30, 3))
				after := mustGetSession(t, e, user.Token, doc.ID)
				if after.ID != doc.ID || after.StartTime != doc.StartTime || after.CurrentTime != 30 || after.TimeListening == nil || *after.TimeListening != 3 {
					t.Fatalf("sync must move the position and keep the start position %v: %+v", doc.StartTime, after)
				}
				closeSession(t, e, user.Token, doc.ID, syncBody(32, 1))
				waitListening(t, e, doc.ID, 4)
				progress := progressRow(t, e, user.ID, saga1BookID)
				requireNear(t, "progress after the close", number(t, progress["currentTime"]), 32)
				requireNear(t, "progress ratio after the close", progressFraction(t, progress), 32/saga1Duration)
				if _, status := getSession(t, e, user.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("a closed session must no longer be readable: %d", status)
				}
			},
		},
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-001", Case: "normal", State: "repeat", Assertion: "flow-repeat-sync-and-resume",
			Sources: []string{flowSync, startSource, "server/managers/PlaybackSessionManager.js:282-288", flowClient},
			Claim:   "Repeated syncs accumulate the listening time, a new play on the same device resumes at the synced position and closes the previous session, and a second close answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				user := createUser(t, e, "ps-flow-repeat", map[string]any{})
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-flow-2", "audio/mpeg"))
				syncSession(t, e, user.Token, doc.ID, syncBody(31, 2))
				syncSession(t, e, user.Token, doc.ID, syncBody(31, 2))
				read := mustGetSession(t, e, user.Token, doc.ID)
				if read.CurrentTime != 31 || read.TimeListening == nil || *read.TimeListening != 4 || read.StartTime != 0 {
					t.Fatalf("repeated syncs: %+v", read)
				}
				again := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-flow-2", "audio/mpeg"))
				requireNear(t, "resume position of the new session", again.StartTime, 31)
				if again.ID == doc.ID {
					t.Fatal("the replacing play must create a new session")
				}
				if _, status := getSession(t, e, user.Token, doc.ID); status != http.StatusNotFound {
					t.Fatalf("the previous session must be closed, GET answered %d", status)
				}
				if row := sessionRow(t, e, doc.ID); row == nil || !near(number(t, row["timeListening"]), 4) || !near(number(t, row["currentTime"]), 31) {
					t.Fatalf("the replaced session must stay stored with its values: %v", row)
				}
				closeSession(t, e, user.Token, again.ID, syncBody(33, 1))
				requireStatus(t, e.Post(t, user.Token, "/api/session/"+again.ID+"/close", map[string]any{}), http.StatusNotFound, "second close")
				requireNear(t, "progress after the closes", number(t, progressRow(t, e, user.ID, saga1BookID)["currentTime"]), 33)
			},
		},
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-001", Case: "normal", State: "changed", Assertion: "flow-offline-then-online-resume",
			Sources: []string{flowLocal, startSource, flowSync, flowClient},
			Claim:   "An offline upload moves the progress, the next online play resumes there, the online close moves it on, and an older offline upload afterwards does not move it back.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-flow-offline", map[string]any{})
				started := localUploadStart()
				requireStatus(t, postLocal(t, e, user.Token, localBody(flowLocalA, e.IDs["saga1"], "", started.UnixMilli(), started.Add(30*time.Second).UnixMilli(), 42, 10)), http.StatusOK, "offline upload")
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-flow-3", "audio/mpeg"))
				requireNear(t, "online resume from the offline position", doc.StartTime, 42)
				syncSession(t, e, user.Token, doc.ID, syncBody(50, 2))
				closeSession(t, e, user.Token, doc.ID, syncBody(52, 1))
				waitListening(t, e, doc.ID, 3)
				progress := progressRow(t, e, user.ID, saga1BookID)
				requireNear(t, "progress after the online close", number(t, progress["currentTime"]), 52)

				older := localUploadStart().Add(-48 * time.Hour).UnixMilli()
				requireStatus(t, postLocal(t, e, user.Token, localBody(flowLocalB, e.IDs["saga1"], "", older-30000, older, 20, 6)), http.StatusOK, "older offline upload")
				requireEqual(t, "an older offline upload must not move the progress back", progressRow(t, e, user.ID, saga1BookID), progress)
			},
		},
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-001", Case: "normal", State: "failure", Assertion: "flow-failed-upload-then-retry",
			Sources: []string{flowLocal, localAllSrc, flowClient},
			Claim:   "A failed offline upload leaves the progress and the stored sessions untouched, and the client's next batch with a valid session stores it and moves the progress.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-flow-fail", map[string]any{})
				requireStatus(t, e.Patch(t, user.Token, "/api/me/progress/"+e.IDs["saga1"], map[string]any{"currentTime": 20, "duration": saga1Duration}), http.StatusOK, "set the progress")
				progress := progressRow(t, e, user.ID, saga1BookID)
				sessions := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				started := localUploadStart()
				unknown := "00000000-0000-4000-8000-000000000000"
				requireStatus(t, postLocal(t, e, user.Token, localBody(flowLocalA, unknown, "", started.UnixMilli(), started.Add(10*time.Second).UnixMilli(), 30, 8)), http.StatusInternalServerError, "failed upload")
				requireEqual(t, "progress after the failed upload", progressRow(t, e, user.ID, saga1BookID), progress)
				if e.Count(t, "SELECT COUNT(*) FROM playbackSessions") != sessions {
					t.Fatal("the failed upload stored a session")
				}
				newer := dbTime(t, progress["updatedAt"]) + 1000
				results := postLocalAll(t, e, user.Token, []map[string]any{
					localBody(flowLocalB, unknown, "", started.UnixMilli(), started.Add(20*time.Second).UnixMilli(), 25, 5),
					localBody(flowLocalC, e.IDs["saga1"], "", started.UnixMilli(), newer, 25, 5),
				}, "ps-flow-retry")
				if len(results) != 2 || results[0].Success || !results[1].Success {
					t.Fatalf("retry results: %+v", results)
				}
				requireLocalProgress(t, progressRow(t, e, user.ID, saga1BookID), user.ID, saga1BookID, 25, saga1Duration, newer)
			},
		},
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-001", Case: "error", State: "initial", Assertion: "flow-refusals-change-nothing",
			Sources: []string{flowPlay, flowLocal, routeSync, openSource},
			Claim:   "Every unauthenticated call of the journey is 401, a foreign user's sync and close are 403, and a closed session's sync is 404, without changing rows or progress.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-flow-refuse", "audio/mpeg"))
				defer closeQuietly(t, e, listener.Token, doc.ID)
				syncSession(t, e, listener.Token, doc.ID, syncBody(29, 1))
				progress := progressRow(t, e, listener.ID, saga1BookID)
				stored := sessionRow(t, e, doc.ID)
				read := mustGetSession(t, e, listener.Token, doc.ID)
				started := localUploadStart()
				for _, c := range []struct {
					name, method, path string
					body               any
					status             int
				}{
					{"play", http.MethodPost, "/api/items/" + e.IDs["saga1"] + "/play", androidBody("ps-flow-refused", "audio/mpeg"), http.StatusUnauthorized},
					{"local", http.MethodPost, "/api/session/local", localBody(flowLocalA, e.IDs["saga1"], "", started.UnixMilli(), started.Add(10*time.Second).UnixMilli(), 10, 5), http.StatusUnauthorized},
					{"local-all", http.MethodPost, "/api/session/local-all", map[string]any{"sessions": []any{}}, http.StatusUnauthorized},
					{"sync", http.MethodPost, "/api/session/" + doc.ID + "/sync", syncBody(5, 7), http.StatusUnauthorized},
				} {
					resp := e.Do(t, harness.Request{Method: c.method, Path: c.path, Token: "", JSON: c.body})
					requireStatus(t, resp, c.status, c.name)
				}
				requireStatus(t, e.Post(t, finisher.Token, "/api/session/"+doc.ID+"/sync", syncBody(5, 7)), http.StatusForbidden, "foreign sync")
				requireStatus(t, e.Post(t, finisher.Token, "/api/session/"+doc.ID+"/close", map[string]any{}), http.StatusForbidden, "foreign close")
				requireEqual(t, "progress after the refusals", progressRow(t, e, listener.ID, saga1BookID), progress)
				requireEqual(t, "stored session after the refusals", sessionRow(t, e, doc.ID), stored)
				requireEqual(t, "open session after the refusals", mustGetSession(t, e, listener.Token, doc.ID), read)
				closed := play(t, e, listener.Token, e.IDs["saga2"], "", androidBody("ps-flow-refuse-2", "audio/mpeg"))
				closeQuietly(t, e, listener.Token, closed.ID)
				requireStatus(t, e.Post(t, listener.Token, "/api/session/"+closed.ID+"/sync", syncBody(5, 7)), http.StatusNotFound, "sync after close")
			},
		},
		{
			Requirement: "flow-client-playback-session-sync", Invariant: "INV-FLOW-CLIENT-PLAYBACK-SESSION-SYNC-002", Case: "edge", State: "changed", Assertion: "flow-local-all-retry",
			Sources: []string{localAllSrc, "server/managers/PlaybackSessionManager.js:153-166", flowClient},
			Claim:   "A client retry of the same offline batch returns the same result ids and keeps one stored session per id, and a later batch appends its new session.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-flow-batch", map[string]any{})
				started := localUploadStart()
				batch := []map[string]any{
					localBody(flowLocalA, e.IDs["saga1"], "", started.UnixMilli(), started.Add(30*time.Second).UnixMilli(), 25, 8),
					localBody(flowLocalB, e.IDs["saga2"], "", started.UnixMilli(), started.Add(40*time.Second).UnixMilli(), 12, 4),
				}
				first := postLocalAll(t, e, user.Token, batch, "ps-flow-batch")
				if len(first) != 2 || first[0].ID != flowLocalA || first[1].ID != flowLocalB {
					t.Fatalf("first batch: %+v", first)
				}
				second := postLocalAll(t, e, user.Token, batch, "ps-flow-batch")
				requireEqual(t, "a retried batch keeps its ids", []string{second[0].ID, second[1].ID}, []string{first[0].ID, first[1].ID})
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id IN (?, ?)", flowLocalA, flowLocalB); rows != 2 {
					t.Fatalf("a retried batch must keep two rows, got %d", rows)
				}
				third := postLocalAll(t, e, user.Token, append(batch, localBody(flowLocalC, e.IDs["saga1"], "", started.UnixMilli(), started.Add(50*time.Second).UnixMilli(), 30, 10)), "ps-flow-batch")
				if len(third) != 3 || third[2].ID != flowLocalC {
					t.Fatalf("appending batch: %+v", third)
				}
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", flowLocalC); rows != 1 {
					t.Fatalf("the appended session must be stored once, got %d", rows)
				}
			},
		},
	}
}
