package playback_sessions

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeOpenSessions = "server/routers/ApiRouter.js:236"
	openSnapshotSrc   = "server/controllers/SessionController.js:122-147"
)

// openSessions reads GET /api/sessions/open.
func openSessions(t *testing.T, e *harness.Env, token string) openPage {
	t.Helper()
	resp := e.Get(t, token, "/api/sessions/open")
	requireStatus(t, resp, http.StatusOK, "GET /api/sessions/open")
	var page openPage
	resp.JSON(t, &page)
	return page
}

// findOpen returns the open session with the id, or nil.
func findOpen(page openPage, id string) *openSessionDoc {
	for i := range page.Sessions {
		if page.Sessions[i].ID == id {
			return &page.Sessions[i]
		}
	}
	return nil
}

// openSessionScenarios covers GET /api/sessions/open, the admin snapshot.
func openSessionScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r142", Invariant: "INV-R142-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "open-sessions-admin-snapshot",
			Sources: []string{routeOpenSessions, openSnapshotSrc, "client/pages/config/sessions.vue:499-507"},
			Claim:   "GET /api/sessions/open as an admin lists every open session in creation order as its toJSON plus user {id, username} (no expanded tracks or item), and shareSessions [].",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener, finisher := fixtureActor(t, e, "listener"), fixtureActor(t, e, "finisher")
				first := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-open-1", "audio/mpeg"))
				syncSession(t, e, listener.Token, first.ID, syncBody(30, 4))
				waitListening(t, e, first.ID, 4)
				second := play(t, e, finisher.Token, e.IDs["saga2"], "", androidBody("ps-open-2", "audio/mpeg"))
				syncSession(t, e, finisher.Token, second.ID, map[string]any{"currentTime": 25, "timeListened": 3})
				waitListening(t, e, second.ID, 3)
				page := openSessions(t, e, e.Token(t, "root"))
				if len(page.Sessions) != 2 || page.Sessions[0].ID != first.ID || page.Sessions[1].ID != second.ID {
					t.Fatalf("open session order: %+v", page.Sessions)
				}
				if page.ShareSessions == nil || len(page.ShareSessions) != 0 {
					t.Fatalf("shareSessions must be an empty array: %v", page.ShareSessions)
				}
				for _, c := range []struct {
					doc   sessionDoc
					user  actor
					time  float64
					pos   float64
					title string
				}{
					{first, listener, 4, 30, "The Beginning"},
					{second, finisher, 3, 25, "The Middle"},
				} {
					got := findOpen(page, c.doc.ID)
					if got == nil {
						t.Fatalf("open session %s is missing", c.doc.ID)
					}
					// The snapshot is toJSON plus user: no audio tracks and no
					// expanded library item, unlike GET /api/session/:id.
					want := mustGetSession(t, e, e.Token(t, "root"), c.doc.ID)
					want.AudioTracks, want.LibraryItem = nil, nil
					requireEqual(t, "open session equals the toJSON session", got.sessionDoc, want)
					if got.AudioTracks != nil || got.LibraryItem != nil {
						t.Fatalf("the open snapshot must not expand tracks or the item: %+v", got.sessionDoc)
					}
					requireEqual(t, "open session user", got.User, &minifiedUser{ID: c.user.ID, Username: c.user.Name})
					if got.CurrentTime != c.pos || got.TimeListening == nil || *got.TimeListening != c.time || got.DisplayTitle != c.title {
						t.Fatalf("open session fields: %+v", got)
					}
				}
				for _, c := range []struct {
					doc   sessionDoc
					token string
				}{{first, listener.Token}, {second, finisher.Token}} {
					closeQuietly(t, e, c.token, c.doc.ID)
				}
			},
		},
		{
			Requirement: "route-r142", Invariant: "INV-R142-CONTRACT-001", Case: "error", State: "stateless", Assertion: "open-sessions-refused",
			Sources: []string{routeOpenSessions, openSnapshotSrc},
			Claim:   "GET /api/sessions/open answers 404 to a non-admin user and 401 without a valid token, exposing no open-session data.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				finisher := fixtureActor(t, e, "finisher")
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-open-refuse", "audio/mpeg"))
				defer closeQuietly(t, e, listener.Token, doc.ID)
				for _, c := range []struct {
					name, token string
					status      int
				}{
					{"no token", "", http.StatusUnauthorized},
					{"invalid token", "not-a-token", http.StatusUnauthorized},
					{"listener", listener.Token, http.StatusNotFound},
					{"finisher", finisher.Token, http.StatusNotFound},
				} {
					resp := e.Get(t, c.token, "/api/sessions/open")
					requireStatus(t, resp, c.status, "GET /api/sessions/open: "+c.name)
					if body := string(resp.Body); containsToken(body, doc.ID) || containsToken(body, "shareSessions") {
						t.Fatalf("%s: the refusal leaked open sessions: %.200s", c.name, body)
					}
				}
			},
		},
		{
			Requirement: "route-r142", Invariant: "INV-R142-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "open-sessions-empty-and-synced",
			Sources: []string{routeOpenSessions, openSnapshotSrc, "server/objects/PlaybackSession.js:62-92"},
			Claim:   "With no open session the snapshot is {sessions: [], shareSessions: []}; a session's entry reflects each sync while it is open and disappears once it closes.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				empty := openSessions(t, e, e.Token(t, "root"))
				if empty.Sessions == nil || len(empty.Sessions) != 0 || empty.ShareSessions == nil || len(empty.ShareSessions) != 0 {
					t.Fatalf("empty snapshot: %+v", empty)
				}
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["saga2"], "", androidBody("ps-open-3", "audio/mpeg"))
				syncSession(t, e, listener.Token, doc.ID, map[string]any{"currentTime": 12, "timeListened": 2})
				page := openSessions(t, e, e.Token(t, "root"))
				got := findOpen(page, doc.ID)
				if len(page.Sessions) != 1 || got == nil || got.CurrentTime != 12 || got.TimeListening == nil || *got.TimeListening != 2 {
					t.Fatalf("snapshot after a sync: %+v", page.Sessions)
				}
				syncSession(t, e, listener.Token, doc.ID, map[string]any{"currentTime": 14, "timeListened": 1})
				got = findOpen(openSessions(t, e, e.Token(t, "root")), doc.ID)
				if got == nil || got.CurrentTime != 14 || got.TimeListening == nil || *got.TimeListening != 3 {
					t.Fatalf("snapshot after a second sync: %+v", got)
				}
				closeQuietly(t, e, listener.Token, doc.ID)
				if after := openSessions(t, e, e.Token(t, "root")); len(after.Sessions) != 0 {
					t.Fatalf("a closed session must leave the snapshot: %+v", after.Sessions)
				}
			},
		},
	}
}
