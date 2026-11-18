package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeLocalAll = "server/routers/ApiRouter.js:239"
	localAllSrc   = "server/managers/PlaybackSessionManager.js:110-142,144-273"
)

const (
	localAllA = "3d2e7a8b-4c1e-4d2f-8d9a-1b2c3d4e5f01"
	localAllB = "3d2e7a8b-4c1e-4d2f-8d9a-1b2c3d4e5f02"
	localAllC = "3d2e7a8b-4c1e-4d2f-8d9a-1b2c3d4e5f03"
	localAllD = "3d2e7a8b-4c1e-4d2f-8d9a-1b2c3d4e5f04"
)

// localAllScenarios covers POST /api/session/local-all, the offline batch.
func localAllScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r145", Invariant: "INV-R145-CONTRACT-001", Case: "normal", State: "initial", Assertion: "local-all-reports-each-session",
			Sources: []string{routeLocalAll, localAllSrc, "go-backend/testmachine/contracts/clients/official-app.md:80"},
			Claim:   "POST /api/session/local-all answers {results} with one {id, success, progressSynced} entry per input in request order, stores each session and moves each item's progress.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-localall", map[string]any{})
				started := localUploadStart()
				bookUpdated, episodeUpdated := started.Add(30*time.Second), started.Add(40*time.Second)
				book := localBody(localAllA, e.IDs["saga1"], "", started.UnixMilli(), bookUpdated.UnixMilli(), 25, 8)
				episode := localBody(localAllB, e.IDs["podcast"], e.IDs["ep1"], started.UnixMilli(), episodeUpdated.UnixMilli(), 12, 5)
				requested := time.Now()
				results := postLocalAll(t, e, user.Token, []map[string]any{book, episode}, "ps-localall-device")
				requireEqual(t, "local-all results", results, []localResult{
					{ID: localAllA, Success: true, ProgressSynced: ptrOf(true)},
					{ID: localAllB, Success: true, ProgressSynced: ptrOf(true)},
				})
				row := localSessionRow(t, e, localAllA)
				requireLocalSession(t, row, localAllA, user.ID, e.IDs["saga1"], started.UnixMilli(), bookUpdated.UnixMilli(), 25, 8)
				progress := progressRow(t, e, user.ID, saga1BookID)
				requireLocalProgressValues(t, progress, user.ID, saga1BookID, 25, saga1Duration)
				if dbTime(t, progress["updatedAt"]) < requested.UnixMilli()-1000 {
					t.Fatalf("progress of a new row: %v", progress)
				}

				episodeRow := localSessionRow(t, e, localAllB)
				if episodeRow == nil || episodeRow["mediaItemId"] != e.IDs["ep1"] || episodeRow["mediaItemType"] != "podcastEpisode" || episodeRow["libraryId"] != e.IDs["podcastLibrary"] {
					t.Fatalf("episode offline session: %v", episodeRow)
				}
				if episodeRow["displayTitle"] != "Offline Title" || episodeRow["displayAuthor"] != "Offline Author" || !near(number(t, episodeRow["duration"]), ep1Duration) ||
					!near(number(t, episodeRow["currentTime"]), 12) || !near(number(t, episodeRow["timeListening"]), 5) {
					t.Fatalf("episode offline session values: %v", episodeRow)
				}
				episodeProgress := progressRow(t, e, user.ID, e.IDs["ep1"])
				if episodeProgress == nil || episodeProgress["mediaItemType"] != "podcastEpisode" || !near(number(t, episodeProgress["currentTime"]), 12) || !near(progressFraction(t, episodeProgress), 12/ep1Duration) {
					t.Fatalf("episode progress: %v", episodeProgress)
				}
			},
		},
		{
			Requirement: "route-r145", Invariant: "INV-R145-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "local-all-repeat-keeps-ids",
			Sources: []string{routeLocalAll, localAllSrc, "server/managers/PlaybackSessionManager.js:153-166"},
			Claim:   "A repeated batch keeps one session per id with the same result id, and a legacy play_local_ id maps to one generated server id that a later upload reuses.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-localall-repeat", map[string]any{})
				started := localUploadStart()
				first := postLocalAll(t, e, user.Token, []map[string]any{localBody(localAllC, e.IDs["saga1"], "", started.UnixMilli(), started.Add(30*time.Second).UnixMilli(), 20, 6)}, "ps-localall-repeat")
				requireEqual(t, "first result", first, []localResult{{ID: localAllC, Success: true, ProgressSynced: ptrOf(true)}})
				second := postLocalAll(t, e, user.Token, []map[string]any{localBody(localAllC, e.IDs["saga1"], "", started.UnixMilli(), started.Add(60*time.Second).UnixMilli(), 24, 9)}, "ps-localall-repeat")
				requireEqual(t, "repeat result id", second[0].ID, localAllC)
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", localAllC); rows != 1 {
					t.Fatalf("a repeated batch must keep one row, got %d", rows)
				}
				row := localSessionRow(t, e, localAllC)
				if !near(number(t, row["currentTime"]), 24) || !near(number(t, row["timeListening"]), 9) {
					t.Fatalf("repeat batch values: %v", row)
				}

				legacyBody := localBody("play_local_ps-localall-1", e.IDs["saga2"], "", started.UnixMilli(), started.Add(30*time.Second).UnixMilli(), 10, 4)
				mapped := postLocalAll(t, e, user.Token, []map[string]any{legacyBody}, "ps-localall-legacy")
				if len(mapped) != 1 || !mapped[0].Success || mapped[0].ID == "play_local_ps-localall-1" || len(mapped[0].ID) != 36 {
					t.Fatalf("a play_local_ id must map to one server id: %+v", mapped)
				}
				legacyBody["currentTime"] = 14
				legacyBody["timeListening"] = 6
				again := postLocalAll(t, e, user.Token, []map[string]any{legacyBody}, "ps-localall-legacy")
				requireEqual(t, "mapped id is stable", again[0].ID, mapped[0].ID)
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", mapped[0].ID); rows != 1 {
					t.Fatalf("the mapped session must have one row, got %d", rows)
				}
				if row := localSessionRow(t, e, mapped[0].ID); !near(number(t, row["currentTime"]), 14) || !near(number(t, row["timeListening"]), 6) {
					t.Fatalf("mapped session values: %v", row)
				}
			},
		},
		{
			Requirement: "route-r145", Invariant: "INV-R145-CONTRACT-001", Case: "normal", State: "changed", Assertion: "local-all-partial-failure-and-conflict",
			Sources: []string{routeLocalAll, localAllSrc, "server/managers/PlaybackSessionManager.js:196-273"},
			Claim:   "A batch with one unknown item still answers 200 with a failure result for that entry and stores only the valid session; when the stored progress is newer the entry succeeds with progressSynced false and leaves the progress.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-localall-partial", map[string]any{})
				started := localUploadStart()
				unknown := "00000000-0000-4000-8000-000000000000"
				updated := started.Add(30 * time.Second)
				good := localBody(localAllD, e.IDs["saga1"], "", started.UnixMilli(), updated.UnixMilli(), 25, 8)
				bad := localBody("ps-localall-unknown", unknown, "", started.UnixMilli(), updated.UnixMilli(), 10, 4)
				results := postLocalAll(t, e, user.Token, []map[string]any{good, bad}, "ps-localall-partial")
				if len(results) != 2 || results[1].ID != "ps-localall-unknown" || results[1].Success || results[1].ProgressSynced != nil || results[1].Error == nil || *results[1].Error != "Media item not found" {
					t.Fatalf("partial failure results: %+v", results)
				}
				requireEqual(t, "the valid entry", results[0], localResult{ID: localAllD, Success: true, ProgressSynced: ptrOf(true)})
				if localSessionRow(t, e, "ps-localall-unknown") != nil {
					t.Fatal("the failed entry must store no session")
				}
				if localSessionRow(t, e, localAllD) == nil {
					t.Fatal("the valid entry must store its session")
				}

				requireStatus(t, e.Patch(t, user.Token, "/api/me/progress/"+e.IDs["saga1"], map[string]any{"currentTime": 55, "duration": saga1Duration}), http.StatusOK, "set the newer progress")
				newer := progressRow(t, e, user.ID, saga1BookID)
				stale := postLocalAll(t, e, user.Token, []map[string]any{localBody(localAllD, e.IDs["saga1"], "", started.UnixMilli(), started.Add(10*time.Second).UnixMilli(), 30, 5)}, "ps-localall-partial")
				if len(stale) != 1 || !stale[0].Success || stale[0].ProgressSynced == nil || *stale[0].ProgressSynced {
					t.Fatalf("an entry older than the progress reports progressSynced false: %+v", stale)
				}
				requireEqual(t, "progress newer than the batch", progressRow(t, e, user.ID, saga1BookID), newer)
				if row := localSessionRow(t, e, localAllD); !near(number(t, row["currentTime"]), 30) || !near(number(t, row["timeListening"]), 5) {
					t.Fatalf("the session row still records the batch: %v", row)
				}
			},
		},
		{
			Requirement: "route-r145", Invariant: "INV-R145-CONTRACT-001", Case: "edge", State: "initial", Assertion: "local-all-empty-and-duplicates",
			Sources: []string{routeLocalAll, localAllSrc},
			Claim:   "A batch without sessions answers {results: []}; a batch that lists the same session id twice answers two success entries for that id (the second with progressSynced false because the first created the progress with the server time) and stores one row with the last session values.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-localall-edge", map[string]any{})
				resp := e.Post(t, user.Token, "/api/session/local-all", map[string]any{"deviceInfo": map[string]any{"deviceId": "ps-localall-empty"}})
				requireStatus(t, resp, http.StatusOK, "local-all without sessions")
				var empty struct {
					Results []localResult `json:"results"`
				}
				resp.JSON(t, &empty)
				if empty.Results == nil || len(empty.Results) != 0 {
					t.Fatalf("a batch without sessions must answer an empty results array: %s", resp.Body)
				}
				started := localUploadStart()
				first := localBody(localAllA, e.IDs["saga1"], "", started.UnixMilli(), started.Add(30*time.Second).UnixMilli(), 15, 4)
				second := localBody(localAllA, e.IDs["saga1"], "", started.UnixMilli(), started.Add(60*time.Second).UnixMilli(), 18, 7)
				results := postLocalAll(t, e, user.Token, []map[string]any{first, second}, "ps-localall-dupe")
				// The first entry creates the progress with the server time, so the
				// second, older client timestamp reports progressSynced false.
				requireEqual(t, "duplicate entries", results, []localResult{
					{ID: localAllA, Success: true, ProgressSynced: ptrOf(true)},
					{ID: localAllA, Success: true, ProgressSynced: ptrOf(false)},
				})
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", localAllA); rows != 1 {
					t.Fatalf("a duplicate id must store one row, got %d", rows)
				}
				row := localSessionRow(t, e, localAllA)
				if !near(number(t, row["currentTime"]), 18) || !near(number(t, row["timeListening"]), 7) || dbTime(t, row["updatedAt"]) != started.Add(60*time.Second).UnixMilli() {
					t.Fatalf("the last duplicate wins for the session row: %v", row)
				}
				requireLocalProgressValues(t, progressRow(t, e, user.ID, saga1BookID), user.ID, saga1BookID, 15, saga1Duration)
			},
		},
		{
			Requirement: "route-r145", Invariant: "INV-R145-CONTRACT-001", Case: "error", State: "failure", Assertion: "local-all-failures-change-nothing",
			Sources: []string{routeLocalAll, localAllSrc},
			Claim:   "A batch whose entries are all unknown answers 200 with one failure result per entry and stores nothing, and an unauthenticated batch is 401.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-localall-bad", map[string]any{})
				started := localUploadStart()
				unknownItem, unknownEpisode := "00000000-0000-4000-8000-000000000000", "00000000-0000-4000-8000-000000000001"
				sessions, progresses := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"), e.Count(t, "SELECT COUNT(*) FROM mediaProgresses")
				results := postLocalAll(t, e, user.Token, []map[string]any{
					localBody("ps-localall-bad-1", unknownItem, "", started.UnixMilli(), started.Add(10*time.Second).UnixMilli(), 10, 4),
					localBody("ps-localall-bad-2", e.IDs["podcast"], unknownEpisode, started.UnixMilli(), started.Add(10*time.Second).UnixMilli(), 5, 2),
				}, "ps-localall-bad")
				if len(results) != 2 || results[0].Success || results[1].Success || results[0].Error == nil || results[1].Error == nil {
					t.Fatalf("all-failure results: %+v", results)
				}
				requireStatus(t, e.Post(t, "", "/api/session/local-all", map[string]any{"sessions": []any{}}), http.StatusUnauthorized, "unauthenticated batch")
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != sessions {
					t.Fatalf("the failed batch stored %d sessions", got-sessions)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses"); got != progresses {
					t.Fatalf("the failed batch stored %d progress rows", got-progresses)
				}
			},
		},
	}
}
