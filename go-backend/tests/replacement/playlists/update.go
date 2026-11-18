package playlists

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

func update(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPatch, "/api/playlists/"+id, body)
}

func updateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r092", Invariant: "INV-R092-SAMPLED", Case: "normal", State: "initial", Assertion: "patch-renames-and-sets-description",
			Sources: []string{"server/controllers/PlaylistController.js:166-256"},
			Claim:   "PATCH by the owner changes name (tags stripped) and description, answers the expanded playlist and persists both in the row.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-editor")
				p := createAs(t, e, token, "Before", book(fx.novel))
				// updatedAt has millisecond resolution: let the clock pass the value read before.
				harness.Eventually(t, 5*time.Second, "clock past the created updatedAt", func() bool { return time.Now().UnixMilli() > p.LastUpdate })
				got := decode(t, update(t, e, token, p.ID, map[string]any{"name": "<i>After</i>", "description": "Now described"}))
				if got.ID != p.ID || got.Name != "After" || got.Description == nil || *got.Description != "Now described" || got.UserID != p.UserID || got.CreatedAt != p.CreatedAt {
					t.Fatalf("updated payload wrong: %+v", got)
				}
				if got.LastUpdate <= p.LastUpdate {
					t.Fatalf("a changing PATCH must move lastUpdate forward: %d -> %d", p.LastUpdate, got.LastUpdate)
				}
				if row := playlistRow(t, e, p.ID); millis(t, row["updatedAt"]) != got.LastUpdate {
					t.Fatalf("row updatedAt %v must equal the payload lastUpdate %d", row["updatedAt"], got.LastUpdate)
				}
				sameStrings(t, "items", got.refs(), []string{fx.novel})
				wantRow(t, e, got)
				wantRow(t, e, fetchAs(t, e, token, p.ID))
			},
		},
		{
			Requirement: "route-r092", Invariant: "INV-R092-ORDER", Case: "normal", State: "changed", Assertion: "patch-reorders-book-items",
			Sources: []string{"server/controllers/PlaylistController.js:203-247"},
			Claim:   "PATCH with items in a new order renumbers the persisted links 1..n in that order and the response and later reads list them in it.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-editor")
				p := createAs(t, e, token, "Reorder", book(fx.saga1), book(fx.novel), book(fx.ebook))
				got := decode(t, update(t, e, token, p.ID, map[string]any{"items": []any{book(fx.ebook), book(fx.saga1), book(fx.novel)}}))
				sameStrings(t, "response", got.refs(), []string{fx.ebook, fx.saga1, fx.novel})
				wantBookLinks(t, e, p.ID, fx.ebook, fx.saga1, fx.novel)
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.ebook, fx.saga1, fx.novel})
				if got.Name != "Reorder" {
					t.Fatalf("name must not change: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r092", Invariant: "INV-R092-ORDER", Case: "edge", State: "changed", Assertion: "patch-reorders-episode-items",
			Sources: []string{"server/controllers/PlaylistController.js:223-247"},
			Claim:   "PATCH reorders the episodes of a podcast playlist, identified by episode id, and persists the new order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-editor")
				p := createPodcastAs(t, e, token, "Reorder episodes", episode(fx.podcast, fx.ep1), episode(fx.podcast, fx.ep2), episode(fx.podcast, fx.ep3))
				got := decode(t, update(t, e, token, p.ID, map[string]any{"items": []any{episode(fx.podcast, fx.ep3), episode(fx.podcast, fx.ep1), episode(fx.podcast, fx.ep2)}}))
				sameStrings(t, "response", got.refs(), []string{ref(fx.podcast, fx.ep3), ref(fx.podcast, fx.ep1), ref(fx.podcast, fx.ep2)})
				wantEpisodeLinks(t, e, p.ID, fx.ep3, fx.ep1, fx.ep2)
			},
		},
		{
			Requirement: "route-r092", Invariant: "INV-R092-SAMPLED", Case: "edge", State: "repeat", Assertion: "patch-same-values-and-empty-body-change-nothing",
			Sources: []string{"server/controllers/PlaylistController.js:183-201", "server/controllers/PlaylistController.js:252-255"},
			Claim:   "PATCH with the current values, an empty body, an empty name or an empty description answers 200 with the playlist unchanged (an empty description cannot clear it).",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-editor")
				p := decode(t, call(t, e, token, http.MethodPost, "/api/playlists", map[string]any{
					"libraryId": e.IDs["library"], "name": "Stable", "description": "Kept", "items": []any{book(fx.novel)},
				}))
				unchanged(t, e, func() {
					for _, body := range []any{
						map[string]any{"name": "Stable", "description": "Kept"},
						map[string]any{},
						map[string]any{"name": ""},
						map[string]any{"description": ""},
						map[string]any{"items": []any{}},
					} {
						got := decode(t, update(t, e, token, p.ID, body))
						if got.Name != "Stable" || got.Description == nil || *got.Description != "Kept" || got.LastUpdate != p.LastUpdate {
							t.Fatalf("body %v changed the playlist: %+v", body, got)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r092", Invariant: "INV-R092-VALIDATE", Case: "error", State: "failure", Assertion: "patch-rejects-invalid-bodies",
			Sources: []string{"server/controllers/PlaylistController.js:168-181", "server/controllers/PlaylistController.js:206-221"},
			Claim:   "PATCH answers 400 for libraryId or userId in the body, a non-string name or description, items that are not an array or lack libraryItemId, unknown items and a changed item count, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-editor")
				p := createAs(t, e, token, "Strict", book(fx.novel), book(fx.saga1))
				unchanged(t, e, func() {
					for _, tc := range []struct {
						what string
						body any
					}{
						{"libraryId", map[string]any{"libraryId": e.IDs["podcastLibrary"]}},
						{"userId", map[string]any{"userId": e.IDs["listener"]}},
						{"numeric name", map[string]any{"name": 5}},
						{"numeric description", map[string]any{"description": 5}},
						{"items not array", map[string]any{"items": "x"}},
						{"item without id", map[string]any{"items": []any{map[string]any{}}}},
						{"numeric episode id", map[string]any{"items": []any{map[string]any{"libraryItemId": fx.novel, "episodeId": 5}}}},
						{"unknown item", map[string]any{"items": []any{book(fx.novel), book("no-such-item")}}},
						{"fewer items", map[string]any{"items": []any{book(fx.novel)}}},
						{"more items", map[string]any{"items": []any{book(fx.novel), book(fx.saga1), book(fx.ebook)}}},
					} {
						if resp := update(t, e, token, p.ID, tc.body); resp.Status != http.StatusBadRequest {
							t.Fatalf("%s: status %d, want 400; body %.200s", tc.what, resp.Status, resp.Body)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r092", Invariant: "INV-R092-ACCESS", Case: "error", State: "failure", Assertion: "patch-rejects-foreign-unknown-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "PATCH answers 401 without a token, 404 for an unknown id, 403 for another user (an administrator included) and 404 after the owner lost library access, changing nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				r := revokedUser(t, e)
				path := e.IDs["playlist"]
				body := map[string]any{"name": "Hijacked"}
				unchanged(t, e, func() {
					wantStatus(t, update(t, e, "", path, body), http.StatusUnauthorized)
					wantStatus(t, update(t, e, e.Token(t, "listener"), "no-such-playlist", body), http.StatusNotFound)
					wantStatus(t, update(t, e, e.Token(t, "finisher"), path, body), http.StatusForbidden)
					wantStatus(t, update(t, e, e.Token(t, "root"), path, body), http.StatusForbidden)
					wantStatus(t, update(t, e, r.token, r.bookPlaylist.ID, body), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r093", Invariant: "INV-R093-SAMPLED", Case: "normal", State: "initial", Assertion: "delete-removes-playlist-and-links",
			Sources: []string{"server/controllers/PlaylistController.js:265-272"},
			Claim:   "DELETE by the owner answers 200 and removes the playlist row and its links, leaving the library items; the playlist then reads 404 and leaves the lists.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-deleter")
				p := createAs(t, e, token, "Doomed", book(fx.saga1), book(fx.novel))
				items := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				resp := call(t, e, token, http.MethodDelete, "/api/playlists/"+p.ID, nil)
				wantStatus(t, resp, http.StatusOK)
				noPlaylistRow(t, e, p.ID)
				if got := e.Count(t, "SELECT COUNT(*) FROM libraryItems"); got != items {
					t.Fatalf("library items changed: %d -> %d", items, got)
				}
				wantStatus(t, e.Get(t, token, "/api/playlists/"+p.ID), http.StatusNotFound)
				if hasPlaylist(list(t, e, token), p.ID) {
					t.Fatal("deleted playlist still listed")
				}
			},
		},
		{
			Requirement: "route-r093", Invariant: "INV-R093-SAMPLED", Case: "edge", State: "repeat", Assertion: "delete-twice-second-is-404",
			Sources: []string{"server/controllers/PlaylistController.js:575-580"},
			Claim:   "Deleting a playlist that is already gone answers 404 and changes nothing; an empty playlist can be deleted too.",
			Run: func(t *testing.T, e *harness.Env) {
				token := plainUser(t, e, "pl-deleter")
				p := createAs(t, e, token, "Empty doomed")
				wantStatus(t, call(t, e, token, http.MethodDelete, "/api/playlists/"+p.ID, nil), http.StatusOK)
				noPlaylistRow(t, e, p.ID)
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, token, http.MethodDelete, "/api/playlists/"+p.ID, nil), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r093", Invariant: "INV-R093-SAMPLED", Case: "normal", State: "changed", Assertion: "delete-after-edits-removes-all-links",
			Sources: []string{"server/controllers/PlaylistController.js:265-272"},
			Claim:   "A playlist that gained items after creation is deleted with all its links, and other playlists of the owner stay.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-deleter")
				keep := createAs(t, e, token, "Keep", book(fx.novel))
				p := createAs(t, e, token, "Grown", book(fx.saga1))
				wantStatus(t, call(t, e, token, http.MethodPost, "/api/playlists/"+p.ID+"/batch/add", map[string]any{"items": []any{book(fx.novel), book(fx.ebook)}}), http.StatusOK)
				wantStatus(t, call(t, e, token, http.MethodDelete, "/api/playlists/"+p.ID, nil), http.StatusOK)
				noPlaylistRow(t, e, p.ID)
				wantBookLinks(t, e, keep.ID, fx.novel)
			},
		},
		{
			Requirement: "route-r093", Invariant: "INV-R093-ACCESS", Case: "error", State: "failure", Assertion: "delete-rejects-foreign-unknown-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "DELETE answers 401 without a token, 404 for an unknown id, 403 for another user or an administrator and 404 after the owner lost library access; nothing is deleted.",
			Run: func(t *testing.T, e *harness.Env) {
				r := revokedUser(t, e)
				path := "/api/playlists/" + e.IDs["playlist"]
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, "", http.MethodDelete, path, nil), http.StatusUnauthorized)
					wantStatus(t, call(t, e, e.Token(t, "listener"), http.MethodDelete, "/api/playlists/no-such-playlist", nil), http.StatusNotFound)
					wantStatus(t, call(t, e, e.Token(t, "finisher"), http.MethodDelete, path, nil), http.StatusForbidden)
					wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodDelete, path, nil), http.StatusForbidden)
					wantStatus(t, call(t, e, r.token, http.MethodDelete, "/api/playlists/"+r.bookPlaylist.ID, nil), http.StatusNotFound)
				})
			},
		},
	}
}
