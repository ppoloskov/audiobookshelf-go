package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

func deviceScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r059", Invariant: "INV-R059-REPLACE-001", Case: "normal", State: "repeat", Assertion: "play-same-device-closes-previous-session",
			Sources: []string{startSource, "server/managers/PlaybackSessionManager.js:282-288", "server/managers/PlaybackSessionManager.js:420-431"},
			Claim:   "A second play by the same user and device closes the previous session first: it is saved, no longer open, announced with user_session_closed to the owner's sockets only, and only the new session stays open.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				root := e.Socket(t, e.Token(t, "root"))
				own1, own2 := e.Socket(t, listener.Token), e.Socket(t, listener.Token)
				other := e.Socket(t, finisher.Token)
				first := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-replace-x", "audio/mpeg"))
				syncSession(t, e, listener.Token, first.ID, syncBody(33, 4.5))
				waitListening(t, e, first.ID, 4.5)
				marks := []int{root.Mark(), own1.Mark(), own2.Mark(), other.Mark()}
				second := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-replace-x", "audio/mpeg"))
				if second.ID == first.ID {
					t.Fatal("the second play must create a new session id")
				}
				requireNear(t, "resume position of the new session", second.StartTime, 33)
				if _, status := getSession(t, e, listener.Token, first.ID); status != http.StatusNotFound {
					t.Fatalf("previous session must be closed, GET answered %d", status)
				}
				mustGetSession(t, e, listener.Token, second.ID)
				open := openSessionIDs(t, e)
				if contains(open, first.ID) || !contains(open, second.ID) {
					t.Fatalf("open sessions after replacement: %v", open)
				}
				row := sessionRow(t, e, first.ID)
				if row == nil || !near(number(t, row["timeListening"]), 4.5) || !near(number(t, row["currentTime"]), 33) {
					t.Fatalf("previous session must stay stored with its listening: %v", row)
				}
				for i, s := range []*harness.Socket{own1, own2} {
					s.Wait(t, marks[i+1], "user_session_closed", 5*time.Second, nil)
					requireEqual(t, "closed ids on a socket of the owner", closedEvents(t, s, marks[i+1]), []string{first.ID})
				}
				settle(t, root)
				settle(t, other)
				if ids := closedEvents(t, root, marks[0]); len(ids) != 0 {
					t.Fatalf("admin socket must not receive user_session_closed: %v", ids)
				}
				if ids := closedEvents(t, other, marks[3]); len(ids) != 0 {
					t.Fatalf("another user must not receive user_session_closed: %v", ids)
				}
				closeQuietly(t, e, listener.Token, second.ID)
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-REPLACE-001", Case: "edge", State: "initial", Assertion: "play-other-device-keeps-session",
			Sources: []string{startSource, "server/managers/PlaybackSessionManager.js:282-288"},
			Claim:   "A play on another device or by another user leaves the earlier session open, and one user can hold several sessions at once.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				a := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-keep-a", "audio/mpeg"))
				b := play(t, e, listener.Token, e.IDs["saga2"], "", androidBody("ps-keep-b", "audio/mpeg"))
				c := play(t, e, finisher.Token, e.IDs["saga1"], "", androidBody("ps-keep-a", "audio/mpeg"))
				open := openSessionIDs(t, e)
				if len(open) != 3 || !contains(open, a.ID) || !contains(open, b.ID) || !contains(open, c.ID) {
					t.Fatalf("three sessions must be open, got %v", open)
				}
				for _, doc := range []sessionDoc{a, b} {
					mustGetSession(t, e, listener.Token, doc.ID)
				}
				mustGetSession(t, e, finisher.Token, c.ID)
				for _, doc := range []sessionDoc{a, b} {
					closeQuietly(t, e, listener.Token, doc.ID)
				}
				closeQuietly(t, e, finisher.Token, c.ID)
			},
		},
	}
}
