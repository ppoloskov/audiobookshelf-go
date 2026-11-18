package playlists

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func rollbackScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "error", State: "failure", Assertion: "create-rolls-back-link-write",
			Sources: []string{"server/controllers/PlaylistController.js:86-125"},
			Claim:   "A storage failure while creating the second ordered link rolls back the new playlist and links; the same request succeeds after storage recovers.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				callCreate := func() harness.Response {
					return e.Post(t, e.Token(t, "listener"), "/api/playlists", map[string]any{
						"name": "Storage recovery", "libraryId": e.IDs["library"], "items": []any{book(fx.novel), book(fx.saga1)},
					})
				}
				assertRollbackAndRetry(t, e, callCreate, fx.novel, fx.saga1)
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-CONTRACT-001", Case: "error", State: "failure", Assertion: "collection-copy-rolls-back-link-write",
			Sources: []string{"server/controllers/PlaylistController.js:532-566"},
			Claim:   "Failure to store the second link in a collection copy leaves the playlist tables unchanged; retry creates an ordered playlist.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				id := makeCollection(t, e, "Storage recovery copy", fx.novel, fx.saga1)
				assertRollbackAndRetry(t, e, func() harness.Response {
					return fromCollection(t, e, e.Token(t, "listener"), id)
				}, fx.novel, fx.saga1)
			},
		},
	}
}

// The trigger exists only in this disposable fixture. Aborting a link insert
// after the playlist insert distinguishes an actual rollback from input validation.
func assertRollbackAndRetry(t *testing.T, e *harness.Env, request func() harness.Response, ids ...string) {
	t.Helper()
	e.Stop(t)
	e.Exec(t, "CREATE TRIGGER reject_second_link BEFORE INSERT ON playlistMediaItems WHEN NEW.\"order\" = 2 BEGIN SELECT RAISE(ABORT, 'fixture link write failed'); END")
	restore := func() {
		e.Stop(t)
		e.Exec(t, "DROP TRIGGER IF EXISTS reject_second_link")
		e.Restart(t)
	}
	t.Cleanup(func() {
		if restore != nil {
			restore()
		}
	})
	e.Restart(t)
	socket := e.Socket(t, e.Token(t, "listener"))
	mark := socket.Mark()
	unchanged(t, e, func() { wantStatus(t, request(), http.StatusInternalServerError) })
	socket.Emit(t, "ping")
	socket.Wait(t, mark, "pong", eventWait, nil)
	for _, event := range socket.Events()[mark:] {
		if strings.HasPrefix(event.Name, "playlist_") {
			t.Fatalf("failed playlist transaction emitted %s", event.Name)
		}
	}
	restore()
	restore = nil
	p := decode(t, request())
	wantRow(t, e, p)
	wantBookLinks(t, e, p.ID, ids...)
	sameStrings(t, "retry item order", p.refs(), ids)
}
