package playlists

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func addItem(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/playlists/"+id+"/item", body)
}

// removeItem deletes /api/playlists/:id/item/:libraryItemId[/:episodeId].
func removeItem(t *testing.T, e *harness.Env, token, id, libraryItemID, episodeID string) harness.Response {
	t.Helper()
	path := "/api/playlists/" + id + "/item/" + libraryItemID
	if episodeID != "" {
		path += "/" + episodeID
	}
	return call(t, e, token, http.MethodDelete, path, nil)
}

func itemScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r094", Invariant: "INV-R094-CONTRACT-001", Case: "normal", State: "initial", Assertion: "add-item-appends-book",
			Sources: []string{"server/controllers/PlaylistController.js:283-338"},
			Claim:   "POST /item appends a book as the last item: the response lists it last with its expanded library item, the link is persisted with order n+1, and the playlist row's lastUpdate stays.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createAs(t, e, token, "Append", book(fx.saga1))
				got := decode(t, addItem(t, e, token, p.ID, book(fx.novel)))
				sameStrings(t, "response", got.refs(), []string{fx.saga1, fx.novel})
				if last := got.Items[1]; last.LibraryItem.ID != fx.novel || last.LibraryItem.MediaType != "book" {
					t.Fatalf("added item must be expanded: %+v", last)
				}
				wantBookLinks(t, e, p.ID, fx.saga1, fx.novel)
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.novel})
				if got.LastUpdate != p.LastUpdate {
					t.Fatalf("adding an item must not touch the playlist row: lastUpdate %d -> %d", p.LastUpdate, got.LastUpdate)
				}
				wantRowTimeUnchanged(t, e, p)
			},
		},
		{
			Requirement: "route-r094", Invariant: "INV-R094-CONTRACT-001", Case: "edge", State: "changed", Assertion: "add-item-appends-episode",
			Sources: []string{"server/controllers/PlaylistController.js:297-336"},
			Claim:   "POST /item with libraryItemId and episodeId appends an episode to a podcast playlist, with the expanded episode in the response and a podcastEpisode link persisted.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createPodcastAs(t, e, token, "Append episode", episode(fx.podcast, fx.ep1))
				got := decode(t, addItem(t, e, token, p.ID, episode(fx.podcast, fx.ep3)))
				sameStrings(t, "response", got.refs(), []string{ref(fx.podcast, fx.ep1), ref(fx.podcast, fx.ep3)})
				if last := got.Items[1]; last.Episode == nil || last.Episode.Title != "Third Time" || last.LibraryItem.MediaType != "podcast" {
					t.Fatalf("added episode must be expanded: %+v", last)
				}
				wantEpisodeLinks(t, e, p.ID, fx.ep1, fx.ep3)
			},
		},
		{
			Requirement: "route-r094", Invariant: "INV-R094-VALIDATE", Case: "error", State: "repeat", Assertion: "add-item-duplicate-is-400",
			Sources: []string{"server/controllers/PlaylistController.js:304-308"},
			Claim:   "Adding a book or episode that is already in the playlist answers 400 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				books := createAs(t, e, token, "Dup books", book(fx.novel))
				casts := createPodcastAs(t, e, token, "Dup casts", episode(fx.podcast, fx.ep1))
				unchanged(t, e, func() {
					wantStatus(t, addItem(t, e, token, books.ID, book(fx.novel)), http.StatusBadRequest)
					wantStatus(t, addItem(t, e, token, casts.ID, episode(fx.podcast, fx.ep1)), http.StatusBadRequest)
				})
			},
		},
		{
			Requirement: "route-r094", Invariant: "INV-R094-VALIDATE", Case: "error", State: "failure", Assertion: "add-item-rejects-invalid-items",
			Sources: []string{"server/controllers/PlaylistController.js:286-302"},
			Claim:   "POST /item answers 400 without libraryItemId, for an unknown item, an item of another library, an episodeId on a book, a podcast without episodeId and an unknown episode, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				books := createAs(t, e, token, "Strict books", book(fx.novel))
				casts := createPodcastAs(t, e, token, "Strict casts", episode(fx.podcast, fx.ep1))
				unchanged(t, e, func() {
					for _, tc := range []struct {
						what string
						id   string
						body any
					}{
						{"no libraryItemId", books.ID, map[string]any{}},
						{"unknown item", books.ID, book("no-such-item")},
						{"podcast item in book playlist", books.ID, episode(fx.podcast, fx.ep2)},
						{"book item in podcast playlist", casts.ID, book(fx.saga1)},
						{"episodeId on book", books.ID, episode(fx.saga1, fx.ep2)},
						{"podcast without episodeId", casts.ID, book(fx.podcast)},
						{"unknown episode", casts.ID, episode(fx.podcast, "no-such-episode")},
					} {
						if resp := addItem(t, e, token, tc.id, tc.body); resp.Status != http.StatusBadRequest {
							t.Fatalf("%s: status %d, want 400; body %.200s", tc.what, resp.Status, resp.Body)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r094", Invariant: "INV-R094-ACCESS", Case: "error", State: "failure", Assertion: "add-item-rejects-foreign-unknown-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "POST /item answers 401 without a token, 404 for an unknown playlist, 403 for another user or an administrator, and 404 for the owner after library access was revoked, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				r := revokedUser(t, e)
				unchanged(t, e, func() {
					wantStatus(t, addItem(t, e, "", e.IDs["playlist"], book(fx.saga1)), http.StatusUnauthorized)
					wantStatus(t, addItem(t, e, e.Token(t, "listener"), "no-such-playlist", book(fx.saga1)), http.StatusNotFound)
					wantStatus(t, addItem(t, e, e.Token(t, "finisher"), e.IDs["playlist"], book(fx.saga1)), http.StatusForbidden)
					wantStatus(t, addItem(t, e, e.Token(t, "root"), e.IDs["playlist"], book(fx.saga1)), http.StatusForbidden)
					wantStatus(t, addItem(t, e, r.token, r.bookPlaylist.ID, book(fx.saga1)), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-CONTRACT-001", Case: "normal", State: "initial", Assertion: "remove-item-renumbers-the-rest",
			Sources: []string{"server/controllers/PlaylistController.js:347-385"},
			Claim:   "DELETE /item/:libraryItemId removes a book from the middle: the response and the persisted links keep the others in order, renumbered 1..n, and the playlist row's lastUpdate stays.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createAs(t, e, token, "Shrink", book(fx.saga1), book(fx.novel), book(fx.ebook))
				got := decode(t, removeItem(t, e, token, p.ID, fx.novel, ""))
				sameStrings(t, "response", got.refs(), []string{fx.saga1, fx.ebook})
				wantBookLinks(t, e, p.ID, fx.saga1, fx.ebook)
				wantRow(t, e, fetchAs(t, e, token, p.ID))
				if got.LastUpdate != p.LastUpdate {
					t.Fatalf("removing an item must not touch the playlist row: lastUpdate %d -> %d", p.LastUpdate, got.LastUpdate)
				}
				wantRowTimeUnchanged(t, e, p)
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-CONTRACT-001", Case: "edge", State: "changed", Assertion: "remove-episode-by-episode-id",
			Sources: []string{"server/controllers/PlaylistController.js:351-355"},
			Claim:   "DELETE /item/:libraryItemId/:episodeId removes that episode, renumbers the rest, and without the episode id the podcast item is not found (404).",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createPodcastAs(t, e, token, "Shrink episodes", episode(fx.podcast, fx.ep1), episode(fx.podcast, fx.ep2), episode(fx.podcast, fx.ep3))
				unchanged(t, e, func() {
					wantStatus(t, removeItem(t, e, token, p.ID, fx.podcast, ""), http.StatusNotFound)
				})
				got := decode(t, removeItem(t, e, token, p.ID, fx.podcast, fx.ep1))
				sameStrings(t, "response", got.refs(), []string{ref(fx.podcast, fx.ep2), ref(fx.podcast, fx.ep3)})
				wantEpisodeLinks(t, e, p.ID, fx.ep2, fx.ep3)
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-EMPTY", Case: "edge", State: "changed", Assertion: "remove-last-item-deletes-playlist",
			Sources: []string{"server/controllers/PlaylistController.js:373-384"},
			Claim:   "Removing the last item answers 200 with the now item-less playlist, keeping its original lastUpdate, and deletes the playlist and its links.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createAs(t, e, token, "Last one", book(fx.novel))
				got := decode(t, removeItem(t, e, token, p.ID, fx.novel, ""))
				if got.ID != p.ID || len(got.Items) != 0 || got.LastUpdate != p.LastUpdate {
					t.Fatalf("response must be the emptied playlist with its original lastUpdate: %+v", got)
				}
				noPlaylistRow(t, e, p.ID)
				wantStatus(t, e.Get(t, token, "/api/playlists/"+p.ID), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-CONTRACT-001", Case: "error", State: "repeat", Assertion: "remove-item-twice-second-is-404",
			Sources: []string{"server/controllers/PlaylistController.js:356-358"},
			Claim:   "Removing an item that is not (or no longer) in the playlist answers 404 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-items")
				p := createAs(t, e, token, "Twice removed", book(fx.saga1), book(fx.novel))
				wantStatus(t, removeItem(t, e, token, p.ID, fx.novel, ""), http.StatusOK)
				unchanged(t, e, func() {
					wantStatus(t, removeItem(t, e, token, p.ID, fx.novel, ""), http.StatusNotFound)
					wantStatus(t, removeItem(t, e, token, p.ID, fx.ebook, ""), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-ACCESS", Case: "error", State: "failure", Assertion: "remove-item-rejects-foreign-unknown-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "DELETE /item answers 401 without a token, 404 for an unknown playlist, 403 for another user or an administrator, and 404 for the owner after library access was revoked, and removes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				r := revokedUser(t, e)
				unchanged(t, e, func() {
					wantStatus(t, removeItem(t, e, "", e.IDs["playlist"], fx.novel, ""), http.StatusUnauthorized)
					wantStatus(t, removeItem(t, e, e.Token(t, "listener"), "no-such-playlist", fx.novel, ""), http.StatusNotFound)
					wantStatus(t, removeItem(t, e, e.Token(t, "finisher"), e.IDs["playlist"], fx.novel, ""), http.StatusForbidden)
					wantStatus(t, removeItem(t, e, e.Token(t, "root"), e.IDs["playlist"], fx.novel, ""), http.StatusForbidden)
					wantStatus(t, removeItem(t, e, r.token, r.bookPlaylist.ID, fx.novel, ""), http.StatusNotFound)
				})
			},
		},
	}
}
