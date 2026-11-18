package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeLocal = "server/routers/ApiRouter.js:238"
	localSrc   = "server/managers/PlaybackSessionManager.js:144-273"
)

const (
	localSessionA = "2c1d6f6a-3b0d-4b1c-9c7e-0a1b2c3d4e01"
	localSessionB = "2c1d6f6a-3b0d-4b1c-9c7e-0a1b2c3d4e02"
	localSessionC = "2c1d6f6a-3b0d-4b1c-9c7e-0a1b2c3d4e03"
	localSessionD = "2c1d6f6a-3b0d-4b1c-9c7e-0a1b2c3d4e04"
)

// localUploadOffline is the client timestamp the offline-upload scenarios use.
func localUploadStart() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }

// localScenarios covers POST /api/session/local, the offline session upload.
func localScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r144", Invariant: "INV-R144-CONTRACT-001", Case: "normal", State: "initial", Assertion: "session-local-creates-stored-session",
			Sources: []string{routeLocal, localSrc, "server/models/PlaybackSession.js:140-163", "go-backend/testmachine/contracts/clients/official-app.md:79"},
			Claim:   "POST /api/session/local with a mobile payload answers 200 with no body, stores the session under the client id with the client metadata and timestamps, overrides the caller and library ids, takes the library duration, and moves the caller's progress and emits its progress event.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-local", map[string]any{})
				started := localUploadStart()
				updated := started.Add(30 * time.Second)
				socket := e.Socket(t, user.Token)
				mark := socket.Mark()
				requested := time.Now()
				resp := postLocal(t, e, user.Token, localBody(localSessionA, e.IDs["saga1"], "", started.UnixMilli(), updated.UnixMilli(), 33, 10))
				requireStatus(t, resp, http.StatusOK, "POST /api/session/local")
				row := localSessionRow(t, e, localSessionA)
				requireLocalSession(t, row, localSessionA, user.ID, e.IDs["saga1"], started.UnixMilli(), updated.UnixMilli(), 33, 10)
				if row["date"] != "2026-05-01" || row["dayOfWeek"] != "Friday" {
					t.Fatalf("the client's initial date must be stored: %v %v", row["date"], row["dayOfWeek"])
				}
				progress := progressRow(t, e, user.ID, saga1BookID)
				requireLocalProgressValues(t, progress, user.ID, saga1BookID, 33, saga1Duration)
				// A new progress row is stamped with the server time, not the client one.
				if dbTime(t, progress["createdAt"]) < requested.UnixMilli()-1000 || dbTime(t, progress["updatedAt"]) < requested.UnixMilli()-1000 {
					t.Fatalf("progress timestamps of a new row: %v", progress)
				}
				if progress["isFinished"].(int64) != 0 {
					t.Fatalf("33 of 60 seconds must not be finished: %v", progress)
				}
				event := waitProgressEvent(t, socket, mark, localSessionA)
				if event.ID != progress["id"] || event.DeviceDescription != "Pixel 8 SDK 34 / v0.10.0" || event.Data.CurrentTime != 33 || event.Data.MediaItemID != saga1BookID || event.Data.LibraryItemID != e.IDs["saga1"] {
					t.Fatalf("progress event of the offline session: %+v", event)
				}
				requireProgressRow(t, event.Data, progress)
				if device := deviceRow(t, e, "ps-offline-android"); device["userId"] != user.ID || device["clientName"] != "Abs Android" {
					t.Fatalf("offline device row: %v", device)
				}
			},
		},
		{
			Requirement: "route-r144", Invariant: "INV-R144-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "session-local-repeat-keeps-one-session",
			Sources: []string{routeLocal, localSrc, "server/models/PlaybackSession.js:130-163"},
			Claim:   "Re-uploading the same offline session id updates its position, listening time and client timestamp in one row, keeps the initial metadata and duration, and recomputes the date from the new timestamp.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-local-repeat", map[string]any{})
				// The client's timestamps are derived from the stored progress so
				// the conflict rule is decided by data, not by the wall clock.
				requireStatus(t, e.Patch(t, user.Token, "/api/me/progress/"+e.IDs["saga1"], map[string]any{"currentTime": 5, "duration": saga1Duration}), http.StatusOK, "set the initial progress")
				first := dbTime(t, progressRow(t, e, user.ID, saga1BookID)["updatedAt"]) + 1000
				started := first - 30000
				second := first + 60000
				requireStatus(t, postLocal(t, e, user.Token, localBody(localSessionB, e.IDs["saga1"], "", started, first, 20, 5)), http.StatusOK, "first upload")
				requireLocalSession(t, localSessionRow(t, e, localSessionB), localSessionB, user.ID, e.IDs["saga1"], started, first, 20, 5)
				requireLocalProgress(t, progressRow(t, e, user.ID, saga1BookID), user.ID, saga1BookID, 20, saga1Duration, first)
				body := localBody(localSessionB, e.IDs["saga1"], "", started, second, 28, 8)
				body["displayTitle"] = "Renamed Offline Title"
				body["duration"] = 99
				body["mediaPlayer"] = "iina"
				requireStatus(t, postLocal(t, e, user.Token, body), http.StatusOK, "repeat upload")
				if rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", localSessionB); rows != 1 {
					t.Fatalf("a repeat upload must keep one row, got %d", rows)
				}
				row := localSessionRow(t, e, localSessionB)
				if row["displayTitle"] != "Offline Title" || row["displayAuthor"] != "Offline Author" || !near(number(t, row["duration"]), saga1Duration) || row["mediaPlayer"] != "exo-player" {
					t.Fatalf("a repeat upload must keep the initial metadata: %v", row)
				}
				if dbTime(t, row["createdAt"]) != started || dbTime(t, row["updatedAt"]) != second ||
					!near(number(t, row["currentTime"]), 28) || !near(number(t, row["timeListening"]), 8) {
					t.Fatalf("repeat upload values: %v", row)
				}
				stamp := time.UnixMilli(second).UTC()
				if row["date"] != stamp.Format("2006-01-02") || row["dayOfWeek"] != stamp.Format("Monday") {
					t.Fatalf("a repeat upload recomputes the date from the client timestamp: %v %v", row["date"], row["dayOfWeek"])
				}
				requireLocalProgress(t, progressRow(t, e, user.ID, saga1BookID), user.ID, saga1BookID, 28, saga1Duration, second)
			},
		},
		{
			Requirement: "route-r144", Invariant: "INV-R144-CONTRACT-001", Case: "normal", State: "changed", Assertion: "session-local-never-moves-newer-progress",
			Sources: []string{routeLocal, localSrc, "server/models/MediaProgress.js:252-265"},
			Claim:   "An offline session older than the stored progress is stored but leaves the progress untouched; a later upload with a newer client timestamp moves it forward.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-local-order", map[string]any{})
				requireStatus(t, e.Patch(t, user.Token, "/api/me/progress/"+e.IDs["saga1"], map[string]any{"currentTime": 50, "duration": saga1Duration}), http.StatusOK, "set the newer progress")
				newer := progressRow(t, e, user.ID, saga1BookID)
				if !near(number(t, newer["currentTime"]), 50) {
					t.Fatalf("setup progress: %v", newer)
				}
				older := utcMillis(2026, 5, 1, 12, 0, 30)
				requireStatus(t, postLocal(t, e, user.Token, localBody(localSessionC, e.IDs["saga1"], "", utcMillis(2026, 5, 1, 12, 0, 0), older, 30, 12)), http.StatusOK, "older upload")
				row := localSessionRow(t, e, localSessionC)
				if row == nil || !near(number(t, row["currentTime"]), 30) || !near(number(t, row["timeListening"]), 12) {
					t.Fatalf("the offline session is stored with its own position: %v", row)
				}
				requireEqual(t, "progress newer than the upload", progressRow(t, e, user.ID, saga1BookID), newer)

				fresh := dbTime(t, newer["updatedAt"]) + 1000
				requireStatus(t, postLocal(t, e, user.Token, localBody(localSessionC, e.IDs["saga1"], "", utcMillis(2026, 5, 1, 12, 0, 0), fresh, 40, 15)), http.StatusOK, "newer upload")
				moved := progressRow(t, e, user.ID, saga1BookID)
				if moved["id"] != newer["id"] {
					t.Fatalf("the newer upload must update the existing progress row: %v", moved)
				}
				requireLocalProgress(t, moved, user.ID, saga1BookID, 40, saga1Duration, fresh)
				row = localSessionRow(t, e, localSessionC)
				if !near(number(t, row["currentTime"]), 40) || !near(number(t, row["timeListening"]), 15) || dbTime(t, row["updatedAt"]) != fresh {
					t.Fatalf("the newer upload must update the session row: %v", row)
				}
			},
		},
		{
			Requirement: "route-r144", Invariant: "INV-R144-CONTRACT-001", Case: "edge", State: "initial", Assertion: "session-local-minimal-payload",
			Sources: []string{routeLocal, localSrc, "server/objects/PlaybackSession.js:196-224"},
			Claim:   "An offline session that omits the display fields, date, media metadata and play method is completed from the library item, derives its date from the client timestamp, stores a zero listening time as null and still creates the progress.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-local-min", map[string]any{})
				started := utcMillis(2026, 11, 12, 8, 0, 0)
				updated := started + 30000
				body := map[string]any{
					"id": localSessionD, "libraryItemId": e.IDs["saga1"], "mediaType": "book",
					"startTime": 0, "currentTime": 0, "timeListening": 0,
					"startedAt": started, "updatedAt": updated,
				}
				requireStatus(t, postLocal(t, e, user.Token, body), http.StatusOK, "minimal offline upload")
				row := localSessionRow(t, e, localSessionD)
				if row == nil {
					t.Fatal("a minimal offline session must be stored")
				}
				if row["displayTitle"] != "The Beginning" || row["displayAuthor"] != "Terry Fixture" || !near(number(t, row["duration"]), saga1Duration) {
					t.Fatalf("missing display fields must come from the item: %v", row)
				}
				if row["date"] != "2026-11-12" || row["dayOfWeek"] != "Thursday" || dbTime(t, row["updatedAt"]) != updated {
					t.Fatalf("missing date must be derived from the client timestamp: %v %v", row["date"], row["dayOfWeek"])
				}
				if row["timeListening"] != nil || !near(number(t, row["currentTime"]), 0) || row["playMethod"] != nil || row["mediaPlayer"] != nil {
					t.Fatalf("minimal offline session null fields: %v", row)
				}
				progress := progressRow(t, e, user.ID, saga1BookID)
				if progress == nil || !near(number(t, progress["currentTime"]), 0) || progressFraction(t, progress) != 0 || progress["isFinished"].(int64) != 0 {
					t.Fatalf("progress of a minimal offline session: %v", progress)
				}
			},
		},
		{
			Requirement: "route-r144", Invariant: "INV-R144-CONTRACT-001", Case: "error", State: "failure", Assertion: "session-local-unknown-item",
			Sources: []string{routeLocal, localSrc},
			Claim:   "An offline session of an unknown item or of an unknown episode of a podcast answers 500 and stores no session and no progress; an unauthenticated upload is 401.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-local-bad", map[string]any{})
				unknown := "00000000-0000-4000-8000-000000000000"
				sessions, progresses := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"), e.Count(t, "SELECT COUNT(*) FROM mediaProgresses")
				started, updated := localUploadStart().UnixMilli(), localUploadStart().Add(20*time.Second).UnixMilli()
				resp := postLocal(t, e, user.Token, localBody("ps-local-bad-1", unknown, "", started, updated, 10, 4))
				requireStatus(t, resp, http.StatusInternalServerError, "offline upload of an unknown item")
				resp = postLocal(t, e, user.Token, localBody("ps-local-bad-2", e.IDs["podcast"], unknown, started, updated, 10, 4))
				requireStatus(t, resp, http.StatusInternalServerError, "offline upload of an unknown episode")
				requireStatus(t, postLocal(t, e, "", localBody("ps-local-bad-3", e.IDs["saga1"], "", started, updated, 10, 4)), http.StatusUnauthorized, "unauthenticated offline upload")
				if localSessionRow(t, e, "ps-local-bad-1") != nil || localSessionRow(t, e, "ps-local-bad-2") != nil || localSessionRow(t, e, "ps-local-bad-3") != nil {
					t.Fatal("a failed offline upload must store no session")
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != sessions {
					t.Fatalf("failed offline uploads stored %d sessions", got-sessions)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses"); got != progresses {
					t.Fatalf("failed offline uploads stored %d progress rows", got-progresses)
				}
			},
		},
	}
}
