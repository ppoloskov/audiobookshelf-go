package playlists

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// libraryPlaylists reads GET /api/libraries/:id/playlists and returns the results.
func libraryPlaylists(t *testing.T, e *harness.Env, token, libraryID string) []playlist {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+libraryID+"/playlists")
	wantStatus(t, resp, http.StatusOK)
	var body struct {
		Results []playlist `json:"results"`
	}
	resp.JSON(t, &body)
	return body.Results
}

// makeCollection creates a collection as root and returns its id.
func makeCollection(t *testing.T, e *harness.Env, name string, books ...string) string {
	t.Helper()
	resp := call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": name, "books": books})
	wantStatus(t, resp, http.StatusOK)
	id, _ := resp.Object(t)["id"].(string)
	if id == "" {
		t.Fatalf("collection create without id: %s", resp.Body)
	}
	return id
}

func flowScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001", Case: "normal", State: "initial", Assertion: "flow-owner-lifecycle-from-collection",
			Sources: []string{"server/controllers/PlaylistController.js:511-567", "server/controllers/PlaylistController.js:394-454", "server/controllers/PlaylistController.js:166-256", "server/controllers/PlaylistController.js:347-385", "server/controllers/PlaylistController.js:265-272"},
			Claim:   "A user makes a playlist from a collection, adds books in a batch, reorders, removes one, sees the resulting state in both lists and the database, and deletes it.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-flow")
				colID := makeCollection(t, e, "Flow source", fx.saga1, fx.novel)
				p := decode(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/"+colID, nil))
				sameStrings(t, "from collection", p.refs(), []string{fx.saga1, fx.novel})
				got := decode(t, batchAdd(t, e, token, p.ID, book(fx.saga2), book(fx.saga1)))
				sameStrings(t, "after batch add", got.refs(), []string{fx.saga1, fx.novel, fx.saga2})
				got = decode(t, update(t, e, token, p.ID, map[string]any{"name": "Flow renamed", "items": []any{book(fx.saga2), book(fx.saga1), book(fx.novel)}}))
				sameStrings(t, "after reorder", got.refs(), []string{fx.saga2, fx.saga1, fx.novel})
				got = decode(t, removeItem(t, e, token, p.ID, fx.saga1, ""))
				sameStrings(t, "after remove", got.refs(), []string{fx.saga2, fx.novel})
				wantBookLinks(t, e, p.ID, fx.saga2, fx.novel)
				wantRow(t, e, fetchAs(t, e, token, p.ID))
				for _, listed := range [][]playlist{list(t, e, token), libraryPlaylists(t, e, token, e.IDs["library"])} {
					if len(listed) != 1 || listed[0].ID != p.ID || listed[0].Name != "Flow renamed" {
						t.Fatalf("list must show the one renamed playlist: %+v", listed)
					}
					sameStrings(t, "listed items", listed[0].refs(), []string{fx.saga2, fx.novel})
				}
				wantStatus(t, call(t, e, token, http.MethodDelete, "/api/playlists/"+p.ID, nil), http.StatusOK)
				noPlaylistRow(t, e, p.ID)
				if len(list(t, e, token)) != 0 {
					t.Fatal("list must be empty after delete")
				}
			},
		},
		{
			Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001", Case: "error", State: "failure", Assertion: "flow-other-users-cannot-touch-a-playlist",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "Another user and an administrator are refused (403) on every playlist route for the listener's fixture playlist, nothing changes, and the owner still reads it unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				id := e.IDs["playlist"]
				base := "/api/playlists/" + id
				requests := []struct {
					method, path string
					body         any
				}{
					{http.MethodGet, base, nil},
					{http.MethodPatch, base, map[string]any{"name": "Stolen"}},
					{http.MethodDelete, base, nil},
					{http.MethodPost, base + "/item", book(fx.saga1)},
					{http.MethodDelete, base + "/item/" + fx.novel, nil},
					{http.MethodPost, base + "/batch/add", map[string]any{"items": []any{book(fx.saga1)}}},
					{http.MethodPost, base + "/batch/remove", map[string]any{"items": []any{book(fx.novel)}}},
				}
				unchanged(t, e, func() {
					for _, who := range []string{"finisher", "root"} {
						for _, rq := range requests {
							if resp := call(t, e, e.Token(t, who), rq.method, rq.path, rq.body); resp.Status != http.StatusForbidden {
								t.Fatalf("%s %s %s: status %d, want 403", who, rq.method, rq.path, resp.Status)
							}
						}
					}
				})
				wantFixtureBookPlaylist(t, e, fetchAs(t, e, e.Token(t, "listener"), id))
			},
		},
		{
			Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001", Case: "edge", State: "changed", Assertion: "flow-lists-are-per-user",
			Sources: []string{"server/controllers/PlaylistController.js:138-144", "server/controllers/LibraryController.js:853-870"},
			Claim:   "Two users with same-named playlists each see only their own in both lists, an administrator sees neither, and deleting one user's playlist leaves the other's.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				alice, bob := plainUser(t, e, "pl-flow-alice"), plainUser(t, e, "pl-flow-bob")
				a := createAs(t, e, alice, "Shared name", book(fx.saga1))
				b := createAs(t, e, bob, "Shared name", book(fx.novel))
				for _, tc := range []struct {
					token     string
					mine, not string
				}{{alice, a.ID, b.ID}, {bob, b.ID, a.ID}} {
					for _, listed := range [][]playlist{list(t, e, tc.token), libraryPlaylists(t, e, tc.token, e.IDs["library"])} {
						if len(listed) != 1 || listed[0].ID != tc.mine {
							t.Fatalf("user must see only own playlist %s: %+v", tc.mine, listed)
						}
					}
				}
				if hasPlaylist(list(t, e, e.Token(t, "root")), a.ID) || hasPlaylist(libraryPlaylists(t, e, e.Token(t, "root"), e.IDs["library"]), b.ID) {
					t.Fatal("administrator must not see other users' playlists")
				}
				wantStatus(t, call(t, e, alice, http.MethodDelete, "/api/playlists/"+a.ID, nil), http.StatusOK)
				if len(list(t, e, alice)) != 0 || len(libraryPlaylists(t, e, bob, e.IDs["library"])) != 1 {
					t.Fatal("deleting one user's playlist must not affect the other")
				}
				wantBookLinks(t, e, b.ID, fx.novel)
			},
		},
		{
			Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001", Case: "normal", State: "repeat", Assertion: "flow-repeated-batch-operations-do-not-duplicate",
			Sources: []string{"server/controllers/PlaylistController.js:414-421", "server/controllers/PlaylistController.js:472-488"},
			Claim:   "Repeating a batch add adds nothing the second time and repeating a batch remove removes nothing more, so clients can retry safely.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-flow-repeat")
				p := createAs(t, e, token, "Retry", book(fx.saga1))
				for range 2 {
					got := decode(t, batchAdd(t, e, token, p.ID, book(fx.novel), book(fx.saga2)))
					sameStrings(t, "after add", got.refs(), []string{fx.saga1, fx.novel, fx.saga2})
					wantBookLinks(t, e, p.ID, fx.saga1, fx.novel, fx.saga2)
				}
				for range 2 {
					got := decode(t, batchRemove(t, e, token, p.ID, book(fx.novel)))
					sameStrings(t, "after remove", got.refs(), []string{fx.saga1, fx.saga2})
				}
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.saga2})
			},
		},
		{
			Requirement: "flow-client-list-ownership", Invariant: "INV-FLOW-CLIENT-LIST-OWNERSHIP-001", Case: "error", State: "changed", Assertion: "flow-collection-writes-denied-playlists-allowed",
			Sources: []string{"server/controllers/CollectionController.js:38-64", "server/routers/ApiRouter.js:159-168"},
			Claim:   "A user without update permission cannot create, change or delete collections, but can still make playlists, also from a collection, and the collection is unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-flow-noedit")
				colID := makeCollection(t, e, "Read only to users", fx.saga1, fx.novel)
				count := e.Count(t, "SELECT COUNT(*) FROM collections")
				path := "/api/collections/" + colID
				wantStatus(t, call(t, e, token, http.MethodPost, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": "No", "books": []string{fx.saga1}}), http.StatusForbidden)
				wantStatus(t, call(t, e, token, http.MethodPatch, path, map[string]any{"name": "Renamed"}), http.StatusForbidden)
				wantStatus(t, call(t, e, token, http.MethodPost, path+"/book", map[string]any{"id": fx.ebook}), http.StatusForbidden)
				wantStatus(t, call(t, e, token, http.MethodDelete, path, nil), http.StatusForbidden)
				if got := e.Count(t, "SELECT COUNT(*) FROM collections"); got != count {
					t.Fatalf("collections changed: %d -> %d", count, got)
				}
				if n := e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE collectionId = '"+colID+"'"); n != 2 {
					t.Fatalf("collection books changed: %d", n)
				}
				fromCollection := decode(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/"+colID, nil))
				sameStrings(t, "from collection", fromCollection.refs(), []string{fx.saga1, fx.novel})
				direct := createAs(t, e, token, "Direct", book(fx.saga2))
				wantBookLinks(t, e, direct.ID, fx.saga2)
			},
		},
	}
}
