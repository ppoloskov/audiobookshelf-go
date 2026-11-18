package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	syncSource  = "server/managers/PlaybackSessionManager.js:373-411"
	routeSync   = "server/routers/ApiRouter.js:242"
	progressSrc = "server/models/MediaProgress.js:190-263"
)

func syncScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r147", Invariant: "INV-R147-CONTRACT-001", Case: "normal", State: "initial", Assertion: "session-sync-updates-progress-and-session",
			Sources: []string{routeSync, "server/controllers/SessionController.js:95-101", syncSource, progressSrc, "server/objects/PlaybackSession.js:181-186,240-249"},
			Claim:   "POST /api/session/:id/sync adds the listened time to the open session, moves the user's progress to the reported position, stores the session row, and tells only the user's sockets with user_item_progress_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				own, admin := e.Socket(t, listener.Token), e.Socket(t, e.Token(t, "root"))
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sync-1", "audio/mpeg"))
				before := progressRow(t, e, listener.ID, saga1BookID)
				marks := [2]int{own.Mark(), admin.Mark()}
				syncSession(t, e, listener.Token, doc.ID, syncBody(30, 2.5))
				row := progressRow(t, e, listener.ID, saga1BookID)
				if row["id"] != before["id"] || !near(number(t, row["currentTime"]), 30) || !near(number(t, row["duration"]), saga1Duration) ||
					!near(progressFraction(t, row), 30/saga1Duration) || row["isFinished"].(int64) != 0 || row["finishedAt"] != nil ||
					dbTime(t, row["updatedAt"]) <= dbTime(t, before["updatedAt"]) || row["createdAt"] != before["createdAt"] {
					t.Fatalf("progress row after sync: %v (before %v)", row, before)
				}
				event := waitProgressEvent(t, own, marks[0], doc.ID)
				if event.ID != row["id"] || event.DeviceDescription != "Pixel 8 SDK 34 / v0.10.0" {
					t.Fatalf("progress event envelope: %+v", event)
				}
				requireProgressRow(t, event.Data, row)
				if event.Data.LibraryItemID != e.IDs["saga1"] || event.Data.EpisodeID != nil {
					t.Fatalf("progress event data: %+v", event.Data)
				}
				read := mustGetSession(t, e, listener.Token, doc.ID)
				if read.TimeListening == nil || *read.TimeListening != 2.5 || read.CurrentTime != 30 || read.UpdatedAt <= doc.UpdatedAt || read.StartTime != doc.StartTime {
					t.Fatalf("session after sync: listening %v current %v updated %d (was %d) start %v", read.TimeListening, read.CurrentTime, read.UpdatedAt, doc.UpdatedAt, read.StartTime)
				}
				stored := waitListening(t, e, doc.ID, 2.5)
				requireSessionRow(t, stored, doc, 2.5, 30)
				if dbTime(t, stored["updatedAt"]) != read.UpdatedAt {
					t.Fatalf("stored updatedAt %v must be the session's %d", stored["updatedAt"], read.UpdatedAt)
				}
				me := e.Get(t, listener.Token, "/api/me/progress/"+e.IDs["saga1"])
				requireStatus(t, me, http.StatusOK, "GET progress")
				var served progressDoc
				me.JSON(t, &served)
				requireProgressRow(t, served, row)
				settle(t, admin)
				if got := progressEvents(t, admin, marks[1]); len(got) != 0 {
					t.Fatalf("an admin socket must not get the user's progress event: %v", got)
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "session-sync-repeat-accumulates",
			Sources: []string{routeSync, syncSource, "server/objects/PlaybackSession.js:240-249"},
			Claim:   "Every sync adds its timeListened to the session total (an identical repeat adds again), moves the position and the stored row, and a sync without listening time moves only the position.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sync-2", "audio/mpeg"))
				row := syncAndRead(t, e, listener, doc, syncBody(31, 2))
				stamp := dbTime(t, row["updatedAt"])
				row = syncAndRead(t, e, listener, doc, syncBody(31, 2))
				if dbTime(t, row["updatedAt"]) != stamp {
					t.Fatalf("a repeated sync with the same position changes nothing in the progress: updatedAt %d then %v", stamp, row["updatedAt"])
				}
				stored := waitListening(t, e, doc.ID, 4)
				row = syncAndRead(t, e, listener, doc, map[string]any{"currentTime": 36.5, "timeListened": 0})
				if dbTime(t, row["updatedAt"]) <= stamp {
					t.Fatalf("a new position must move progress updatedAt: %d then %v", stamp, row["updatedAt"])
				}
				requireNear(t, "progress currentTime", number(t, row["currentTime"]), 36.5)
				requireNear(t, "progress duration from the session", number(t, row["duration"]), saga1Duration)
				read := mustGetSession(t, e, listener.Token, doc.ID)
				if read.TimeListening == nil || *read.TimeListening != 4 || read.CurrentTime != 36.5 {
					t.Fatalf("session after a sync without listening time: %v %v", read.TimeListening, read.CurrentTime)
				}
				stored = waitListening(t, e, doc.ID, 4)
				harness.Eventually(t, 5*time.Second, "row currentTime 36.5", func() bool {
					stored = sessionRow(t, e, doc.ID)
					return near(number(t, stored["currentTime"]), 36.5)
				})
				if e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE id = ?", doc.ID) != 1 {
					t.Fatal("one session must have exactly one row")
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-CONTRACT-001", Case: "edge", State: "initial", Assertion: "session-sync-duration-payload",
			Sources: []string{syncSource, "server/models/MediaProgress.js:219-238"},
			Claim:   "A sync's duration replaces the progress duration when sent and the session duration is used when it is absent, while the progress ratio is always the position over the session duration.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sync-3", "audio/mpeg"))
				row := syncAndRead(t, e, listener, doc, map[string]any{"currentTime": 20, "timeListened": 1, "duration": 100})
				requireNear(t, "payload duration", number(t, row["duration"]), 100)
				requireNear(t, "ratio over the session duration", progressFraction(t, row), 20/saga1Duration)
				row = syncAndRead(t, e, listener, doc, map[string]any{"currentTime": 21, "timeListened": 1})
				requireNear(t, "session duration when absent", number(t, row["duration"]), saga1Duration)
				requireNear(t, "ratio", progressFraction(t, row), 21/saga1Duration)
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-FINISH-001", Case: "normal", State: "changed", Assertion: "session-sync-finish-by-time-remaining",
			Sources: []string{syncSource, "server/models/MediaProgress.js:219-248"},
			Claim:   "A sync within the library's markAsFinishedTimeRemaining of the end marks the progress finished with a finish time and ratio 1, and a later sync far from the end clears it.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-finish", map[string]any{})
				socket := e.Socket(t, user.Token)
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-finish-1", "audio/mpeg"))
				syncAndRead(t, e, user, doc, syncBody(10, 1))          // the first sync creates the progress row
				row := syncAndRead(t, e, user, doc, syncBody(50.5, 1)) // 9.69 s remain, limit 10
				if row["isFinished"].(int64) != 1 || row["finishedAt"] == nil || !near(progressFraction(t, row), 1) || !near(number(t, row["currentTime"]), 50.5) {
					t.Fatalf("9.7 s from the end must finish: %v", row)
				}
				mark := socket.Mark()
				row = syncAndRead(t, e, user, doc, syncBody(49.5, 1)) // 10.69 s remain
				if row["isFinished"].(int64) != 0 || row["finishedAt"] != nil || !near(progressFraction(t, row), 49.5/saga1Duration) {
					t.Fatalf("10.7 s from the end must not stay finished: %v", row)
				}
				event := waitProgressEvent(t, socket, mark, doc.ID)
				requireProgressRow(t, event.Data, row)
				if event.Data.IsFinished {
					t.Fatal("event must report the unfinished state")
				}
				row = syncAndRead(t, e, user, doc, syncBody(55, 1))
				if row["isFinished"].(int64) != 1 || row["finishedAt"] == nil {
					t.Fatalf("5.2 s from the end must finish again: %v", row)
				}
				finishedAt := dbTime(t, row["finishedAt"])
				if finishedAt < doc.StartedAt {
					t.Fatalf("finishedAt %d before the session start %d", finishedAt, doc.StartedAt)
				}
				closeQuietly(t, e, user.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-FINISH-001", Case: "edge", State: "changed", Assertion: "session-sync-finish-by-library-percent",
			Sources: []string{syncSource, "server/models/MediaProgress.js:224-238", "server/controllers/LibraryController.js:100-112"},
			Claim:   "With markAsFinishedPercentComplete set on the library the percentage rule replaces the time-remaining rule in both directions.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				setPercent := func(value any) {
					requireStatus(t, e.Patch(t, root, "/api/libraries/"+e.IDs["library"], map[string]any{"settings": map[string]any{"markAsFinishedPercentComplete": value}}), http.StatusOK, "set library finish percent")
				}
				t.Cleanup(func() { setPercent(nil) })
				user := createUser(t, e, "ps-percent", map[string]any{})
				doc := play(t, e, user.Token, e.IDs["saga1"], "", androidBody("ps-percent-1", "audio/mpeg"))
				syncAndRead(t, e, user, doc, syncBody(10, 1)) // the first sync creates the progress row
				setPercent(95)
				row := syncAndRead(t, e, user, doc, syncBody(55, 1)) // 91.4 percent, 5 s remain
				if row["isFinished"].(int64) != 0 {
					t.Fatalf("91 percent with a 95 percent rule must not finish: %v", row)
				}
				setPercent(50)
				row = syncAndRead(t, e, user, doc, syncBody(31, 1)) // 51.5 percent, 29 s remain
				if row["isFinished"].(int64) != 1 || row["finishedAt"] == nil {
					t.Fatalf("51 percent with a 50 percent rule must finish: %v", row)
				}
				row = syncAndRead(t, e, user, doc, syncBody(29, 1)) // 48.2 percent
				if row["isFinished"].(int64) != 0 || row["finishedAt"] != nil {
					t.Fatalf("48 percent must not stay finished: %v", row)
				}
				setPercent(nil)
				row = syncAndRead(t, e, user, doc, syncBody(55, 1))
				if row["isFinished"].(int64) != 1 {
					t.Fatalf("after clearing the percentage the time rule applies again: %v", row)
				}
				closeQuietly(t, e, user.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r147", Invariant: "INV-R147-AUTH-001", Case: "error", State: "failure", Assertion: "session-sync-refusals-change-nothing",
			Sources: []string{routeSync, "server/controllers/SessionController.js:313-326"},
			Claim:   "Sync is refused with 401 unauthenticated, 404 for an unknown or closed session (before the owner check), 403 for another non-admin user, and changes neither progress, session nor events.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				socket := e.Socket(t, listener.Token)
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-sync-refuse", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, syncBody(32, 1))
				waitListening(t, e, doc.ID, 1)
				progress := progressRow(t, e, listener.ID, saga1BookID)
				finisherProgress := progressRow(t, e, finisher.ID, saga1BookID)
				stored := sessionRow(t, e, doc.ID)
				read := mustGetSession(t, e, listener.Token, doc.ID)
				mark := socket.Mark()
				closed := play(t, e, listener.Token, e.IDs["saga2"], "", androidBody("ps-sync-refuse-2", "audio/mpeg"))
				closeQuietly(t, e, listener.Token, closed.ID)
				refused := []struct {
					name, token, id string
					status          int
				}{
					{"no token", "", doc.ID, http.StatusUnauthorized},
					{"invalid token", "not-a-token", doc.ID, http.StatusUnauthorized},
					{"unknown session", listener.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"unknown session of another user", finisher.Token, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
					{"seeded stored session is not open", listener.Token, "193bf2e2-70c3-4b91-99e2-40bc7398812a", http.StatusNotFound},
					{"closed session", listener.Token, closed.ID, http.StatusNotFound},
					{"another user's session", finisher.Token, doc.ID, http.StatusForbidden},
				}
				for _, c := range refused {
					requireStatus(t, e.Post(t, c.token, "/api/session/"+c.id+"/sync", syncBody(5, 7)), c.status, "sync: "+c.name)
				}
				requireEqual(t, "listener progress", progressRow(t, e, listener.ID, saga1BookID), progress)
				requireEqual(t, "finisher progress", progressRow(t, e, finisher.ID, saga1BookID), finisherProgress)
				requireEqual(t, "open session", mustGetSession(t, e, listener.Token, doc.ID), read)
				requireEqual(t, "stored session", sessionRow(t, e, doc.ID), stored)
				if got := progressEvents(t, socket, mark); len(got) != 0 {
					t.Fatalf("refused syncs must emit no progress event: %v", got)
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
	}
}
