package playlists

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// createBody is a create request body for the fixture book library.
func createBody(e *harness.Env, name string, items ...map[string]any) map[string]any {
	return map[string]any{"libraryId": e.IDs["library"], "name": name, "items": items}
}

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "normal", State: "initial", Assertion: "create-book-playlist-keeps-order",
			Sources: []string{"server/controllers/PlaylistController.js:29-125", "server/models/Playlist.js:297-319"},
			Claim:   "POST /api/playlists creates a playlist owned by the caller with the items in request order; the row, the links (type book, order 1..n) and the response agree.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-creator")
				started := time.Now().UnixMilli()
				body := createBody(e, "Road trip", bookNull(fx.saga1), bookNull(fx.novel), bookNull(fx.ebook)) // episodeId: null as the web client sends it
				body["description"] = "For the car"
				p := decode(t, call(t, e, token, http.MethodPost, "/api/playlists", body))
				if p.ID == "" || p.Name != "Road trip" || p.Description == nil || *p.Description != "For the car" || p.UserID != userID(e, "pl-creator") || p.LibraryID != e.IDs["library"] {
					t.Fatalf("created playlist wrong: %+v", p)
				}
				if p.CreatedAt < started-1000 || p.LastUpdate < started-1000 {
					t.Fatalf("timestamps must be the request time: %+v", p)
				}
				sameStrings(t, "items", p.refs(), []string{fx.saga1, fx.novel, fx.ebook})
				for _, entry := range p.Items {
					if entry.LibraryItem.ID != entry.LibraryItemID || entry.LibraryItem.MediaType != "book" {
						t.Fatalf("item must carry the expanded library item: %+v", entry)
					}
				}
				wantRow(t, e, p)
				wantBookLinks(t, e, p.ID, fx.saga1, fx.novel, fx.ebook)
				sameStrings(t, "GET after create", fetchAs(t, e, token, p.ID).refs(), p.refs())
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "normal", State: "initial", Assertion: "create-podcast-playlist-episodes",
			Sources: []string{"server/controllers/PlaylistController.js:44-84", "server/controllers/PlaylistController.js:101-109"},
			Claim:   "A playlist whose items carry episodeId is a podcast playlist: links are of type podcastEpisode, hold the episode ids in order, and the response items carry the episodes.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-creator")
				p := createPodcastAs(t, e, token, "Episodes", episode(fx.podcast, fx.ep2), episode(fx.podcast, fx.ep1))
				sameStrings(t, "items", p.refs(), []string{ref(fx.podcast, fx.ep2), ref(fx.podcast, fx.ep1)})
				if p.Items[0].Episode == nil || p.Items[0].Episode.Title != "Second Outing" || p.Items[0].LibraryItem.MediaType != "podcast" {
					t.Fatalf("episode item wrong: %+v", p.Items[0])
				}
				wantRow(t, e, p)
				wantEpisodeLinks(t, e, p.ID, fx.ep2, fx.ep1)
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "edge", State: "initial", Assertion: "create-without-items-strips-tags",
			Sources: []string{"server/controllers/PlaylistController.js:33-35", "server/controllers/PlaylistController.js:44", "server/utils/htmlSanitizer.js:27-40"},
			Claim:   "A playlist may be created with no items (empty or absent) and its name is stripped of HTML tags; a missing description is stored as null.",
			Run: func(t *testing.T, e *harness.Env) {
				token := plainUser(t, e, "pl-creator")
				empty := decode(t, call(t, e, token, http.MethodPost, "/api/playlists", createBody(e, "<b>Plain</b> name")))
				if empty.Name != "Plain name" || empty.Description != nil || len(empty.Items) != 0 {
					t.Fatalf("empty playlist wrong: %+v", empty)
				}
				wantRow(t, e, empty)
				wantBookLinks(t, e, empty.ID)
				absent := decode(t, call(t, e, token, http.MethodPost, "/api/playlists", map[string]any{"libraryId": e.IDs["library"], "name": "No items key"}))
				if absent.Name != "No items key" || len(absent.Items) != 0 {
					t.Fatalf("absent items wrong: %+v", absent)
				}
				wantRow(t, e, absent)
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "normal", State: "repeat", Assertion: "create-repeat-makes-distinct-playlists",
			Sources: []string{"server/controllers/PlaylistController.js:89-97"},
			Claim:   "Posting the same body twice creates two playlists with different ids and identical content.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-creator")
				first := createAs(t, e, token, "Twice", book(fx.novel))
				second := createAs(t, e, token, "Twice", book(fx.novel))
				if first.ID == second.ID || first.Name != "Twice" || second.Name != first.Name || first.UserID != userID(e, "pl-creator") || second.UserID != first.UserID || first.LibraryID != e.IDs["library"] || second.LibraryID != first.LibraryID || first.Description != nil || second.Description != nil {
					t.Fatalf("repeat must create a second playlist: %+v %+v", first, second)
				}
				sameStrings(t, "first items", first.refs(), []string{fx.novel})
				sameStrings(t, "second items", second.refs(), []string{fx.novel})
				wantRow(t, e, first)
				wantRow(t, e, second)
				wantBookLinks(t, e, first.ID, fx.novel)
				wantBookLinks(t, e, second.ID, fx.novel)
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-SAMPLED", Case: "normal", State: "changed", Assertion: "create-visible-in-lists-of-owner-only",
			Sources: []string{"server/controllers/PlaylistController.js:138-144", "server/controllers/LibraryController.js:853-870"},
			Claim:   "A created playlist appears in the owner's GET /api/playlists and library list, and in nobody else's.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				owner := plainUser(t, e, "pl-visible")
				p := createAs(t, e, owner, "Visible", book(fx.saga1))
				if !hasPlaylist(list(t, e, owner), p.ID) || hasPlaylist(list(t, e, e.Token(t, "root")), p.ID) || hasPlaylist(list(t, e, e.Token(t, "listener")), p.ID) {
					t.Fatal("playlist must be listed for its owner only")
				}
				var body struct {
					Results []playlist `json:"results"`
				}
				e.Get(t, owner, "/api/libraries/"+e.IDs["library"]+"/playlists").JSON(t, &body)
				if len(body.Results) != 1 || body.Results[0].ID != p.ID {
					t.Fatalf("library list wrong: %+v", body.Results)
				}
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-VALIDATE", Case: "error", State: "failure", Assertion: "create-rejects-invalid-bodies",
			Sources: []string{"server/controllers/PlaylistController.js:33-84"},
			Claim:   "Create answers 400 for a missing, empty or tag-only name, a missing libraryId, a non-string description, an item without libraryItemId, an episodeId on a book, a podcast item without episodeId, unknown items or episodes and items of another library, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-creator")
				podcastBody := func(items ...map[string]any) map[string]any {
					return map[string]any{"libraryId": e.IDs["podcastLibrary"], "name": "x", "items": items}
				}
				bad := []struct {
					what string
					body any
				}{
					{"no name", map[string]any{"libraryId": e.IDs["library"]}},
					{"empty name", createBody(e, "")},
					{"tags only", createBody(e, "<b></b>")},
					{"numeric name", map[string]any{"libraryId": e.IDs["library"], "name": 5}},
					{"no library", map[string]any{"name": "x"}},
					{"numeric description", map[string]any{"libraryId": e.IDs["library"], "name": "x", "description": 5}},
					{"item without id", createBody(e, "x", map[string]any{})},
					{"numeric item id", createBody(e, "x", map[string]any{"libraryItemId": 5})},
					{"episode on book", createBody(e, "x", episode(fx.novel, fx.ep1))},
					{"podcast item without episode", podcastBody(episode(fx.podcast, fx.ep1), book(fx.podcast))},
					{"unknown item", createBody(e, "x", book("no-such-item"))},
					{"unknown episode", podcastBody(episode(fx.podcast, "no-such-episode"))},
					{"podcast item in book library", createBody(e, "x", episode(fx.podcast, fx.ep1))},
					{"book item in podcast library", podcastBody(book(fx.novel))},
				}
				unchanged(t, e, func() {
					for _, tc := range bad {
						if resp := call(t, e, token, http.MethodPost, "/api/playlists", tc.body); resp.Status != http.StatusBadRequest {
							t.Fatalf("%s: status %d, want 400; body %.200s", tc.what, resp.Status, resp.Body)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r089", Invariant: "INV-R089-ACCESS", Case: "error", State: "failure", Assertion: "create-rejects-anonymous-and-inaccessible-library",
			Sources: []string{"server/controllers/PlaylistController.js:40-43", "server/routers/ApiRouter.js:159"},
			Claim:   "Create answers 401 without a token and 403 in a library the user cannot access, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				r := revokedUser(t, e)
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, "", http.MethodPost, "/api/playlists", createBody(e, "x", book(fx.novel))), http.StatusUnauthorized)
					wantStatus(t, call(t, e, r.token, http.MethodPost, "/api/playlists", createBody(e, "x", book(fx.novel))), http.StatusForbidden)
				})
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-CONTRACT-001", Case: "normal", State: "initial", Assertion: "from-collection-copies-name-and-books",
			Sources: []string{"server/controllers/PlaylistController.js:511-567"},
			Claim:   "POST /api/playlists/collection/:id creates a playlist owned by the caller with the collection's name, library, description and books in collection order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-from-collection")
				col := call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", map[string]any{
					"libraryId": e.IDs["library"], "name": "Copy me", "description": "Source", "books": []string{fx.saga2, fx.saga1, fx.novel},
				})
				wantStatus(t, col, http.StatusOK)
				colID, _ := col.Object(t)["id"].(string)
				p := decode(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/"+colID, nil))
				if p.Name != "Copy me" || p.Description == nil || *p.Description != "Source" || p.UserID != userID(e, "pl-from-collection") || p.LibraryID != e.IDs["library"] {
					t.Fatalf("playlist from collection wrong: %+v", p)
				}
				sameStrings(t, "items", p.refs(), []string{fx.saga2, fx.saga1, fx.novel})
				wantRow(t, e, p)
				wantBookLinks(t, e, p.ID, fx.saga2, fx.saga1, fx.novel)
				if n := e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE collectionId = '"+colID+"'"); n != 3 {
					t.Fatalf("the collection must keep its books, has %d", n)
				}
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-CONTRACT-001", Case: "edge", State: "repeat", Assertion: "from-collection-repeat-and-null-description",
			Sources: []string{"server/controllers/PlaylistController.js:534-541"},
			Claim:   "Creating from the same collection twice makes two separate playlists; a collection without description gives a playlist with null description.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-from-collection")
				path := "/api/playlists/collection/" + e.IDs["collection"]
				first := decode(t, call(t, e, token, http.MethodPost, path, nil))
				second := decode(t, call(t, e, token, http.MethodPost, path, nil))
				if first.ID == second.ID || first.Name != "Favourites" || second.Name != "Favourites" || first.Description != nil {
					t.Fatalf("repeat wrong: %+v %+v", first, second)
				}
				sameStrings(t, "items", second.refs(), []string{fx.saga1, fx.novel})
				wantBookLinks(t, e, second.ID, fx.saga1, fx.novel)
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-CONTRACT-001", Case: "normal", State: "changed", Assertion: "from-collection-uses-current-books",
			Sources: []string{"server/controllers/PlaylistController.js:521-552"},
			Claim:   "A playlist made after the collection changed holds the collection's current books, and later collection changes do not touch the playlist.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				root := e.Token(t, "root")
				token := plainUser(t, e, "pl-from-collection")
				col := call(t, e, root, http.MethodPost, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": "Grows", "books": []string{fx.novel}})
				wantStatus(t, col, http.StatusOK)
				colID, _ := col.Object(t)["id"].(string)
				wantStatus(t, call(t, e, root, http.MethodPost, "/api/collections/"+colID+"/book", map[string]any{"id": fx.ebook}), http.StatusOK)
				p := decode(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/"+colID, nil))
				sameStrings(t, "items", p.refs(), []string{fx.novel, fx.ebook})
				wantStatus(t, call(t, e, root, http.MethodPost, "/api/collections/"+colID+"/book", map[string]any{"id": fx.saga1}), http.StatusOK)
				sameStrings(t, "playlist after collection change", fetchAs(t, e, token, p.ID).refs(), []string{fx.novel, fx.ebook})
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-REJECT", Case: "error", State: "failure", Assertion: "from-collection-rejections",
			Sources: []string{"server/controllers/PlaylistController.js:512-530", "server/routers/ApiRouter.js:168"},
			Claim:   "Creating from a collection answers 401 without a token, 404 for an unknown collection or one in a library the user cannot access, and 400 for an empty collection, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				token := plainUser(t, e, "pl-from-collection")
				// A collection cannot be created empty; empty it by removing its only book.
				emptyID := makeCollection(t, e, "Empty", fixtureOf(e).novel)
				wantStatus(t, call(t, e, root, http.MethodDelete, "/api/collections/"+emptyID+"/book/"+fixtureOf(e).novel, nil), http.StatusOK)
				r := revokedUser(t, e)
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, "", http.MethodPost, "/api/playlists/collection/"+e.IDs["collection"], nil), http.StatusUnauthorized)
					wantStatus(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/no-such-collection", nil), http.StatusNotFound)
					wantStatus(t, call(t, e, token, http.MethodPost, "/api/playlists/collection/"+emptyID, nil), http.StatusBadRequest)
					wantStatus(t, call(t, e, r.token, http.MethodPost, "/api/playlists/collection/"+e.IDs["collection"], nil), http.StatusNotFound)
				})
			},
		},
	}
}
