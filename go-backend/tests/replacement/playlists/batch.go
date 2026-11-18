package playlists

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func batchAdd(t *testing.T, e *harness.Env, token, id string, items ...map[string]any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/playlists/"+id+"/batch/add", map[string]any{"items": items})
}

func batchRemove(t *testing.T, e *harness.Env, token, id string, items ...map[string]any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/playlists/"+id+"/batch/remove", map[string]any{"items": items})
}

func batchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r096", Invariant: "INV-R096-SAMPLED", Case: "normal", State: "initial", Assertion: "batch-add-appends-in-request-order",
			Sources: []string{"server/controllers/PlaylistController.js:394-454"},
			Claim:   "POST /batch/add appends the new books in request order after the existing ones, persisting orders n+1.., and answers the expanded playlist.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch grow", book(fx.saga1))
				got := decode(t, batchAdd(t, e, token, p.ID, bookNull(fx.ebook), bookNull(fx.novel))) // episodeId: null as the web client sends it
				sameStrings(t, "response", got.refs(), []string{fx.saga1, fx.ebook, fx.novel})
				wantBookLinks(t, e, p.ID, fx.saga1, fx.ebook, fx.novel)
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.ebook, fx.novel})
			},
		},
		{
			Requirement: "route-r096", Invariant: "INV-R096-SAMPLED", Case: "edge", State: "repeat", Assertion: "batch-add-skips-existing-items",
			Sources: []string{"server/controllers/PlaylistController.js:414-421", "server/controllers/PlaylistController.js:447-453"},
			Claim:   "Items already in the playlist are skipped without error: a request of only existing items answers 200 and changes nothing, a mixed request adds just the new ones.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch skip", book(fx.saga1), book(fx.novel))
				unchanged(t, e, func() {
					got := decode(t, batchAdd(t, e, token, p.ID, book(fx.novel), book(fx.saga1)))
					sameStrings(t, "response", got.refs(), []string{fx.saga1, fx.novel})
				})
				got := decode(t, batchAdd(t, e, token, p.ID, book(fx.saga1), book(fx.ebook)))
				sameStrings(t, "mixed response", got.refs(), []string{fx.saga1, fx.novel, fx.ebook})
				wantBookLinks(t, e, p.ID, fx.saga1, fx.novel, fx.ebook)
			},
		},
		{
			Requirement: "route-r096", Invariant: "INV-R096-SAMPLED", Case: "normal", State: "changed", Assertion: "batch-add-episodes",
			Sources: []string{"server/controllers/PlaylistController.js:417-443"},
			Claim:   "POST /batch/add with episodeId entries appends episodes of a podcast playlist with their expanded episodes and podcastEpisode links.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createPodcastAs(t, e, token, "Batch episodes", episode(fx.podcast, fx.ep2))
				got := decode(t, batchAdd(t, e, token, p.ID, episode(fx.podcast, fx.ep3), episode(fx.podcast, fx.ep1)))
				sameStrings(t, "response", got.refs(), []string{ref(fx.podcast, fx.ep2), ref(fx.podcast, fx.ep3), ref(fx.podcast, fx.ep1)})
				if got.Items[1].Episode == nil || got.Items[1].Episode.Title != "Third Time" {
					t.Fatalf("episode must be expanded: %+v", got.Items[1])
				}
				wantEpisodeLinks(t, e, p.ID, fx.ep2, fx.ep3, fx.ep1)
			},
		},
		{
			Requirement: "route-r096", Invariant: "INV-R096-VALIDATE", Case: "error", State: "failure", Assertion: "batch-add-rejects-invalid-bodies",
			Sources: []string{"server/controllers/PlaylistController.js:395-405"},
			Claim:   "POST /batch/add answers 400 for missing, empty or non-array items, an entry without a string libraryItemId, a non-string episodeId and unknown library items, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch strict", book(fx.novel))
				unchanged(t, e, func() {
					for _, tc := range []struct {
						what string
						body any
					}{
						{"no items", map[string]any{}},
						{"empty items", map[string]any{"items": []any{}}},
						{"items not array", map[string]any{"items": "x"}},
						{"entry without id", map[string]any{"items": []any{map[string]any{}}}},
						{"numeric id", map[string]any{"items": []any{map[string]any{"libraryItemId": 5}}}},
						{"numeric episode id", map[string]any{"items": []any{map[string]any{"libraryItemId": fx.saga1, "episodeId": 5}}}},
						{"unknown item", map[string]any{"items": []any{book(fx.saga1), book("no-such-item")}}},
					} {
						if resp := call(t, e, token, http.MethodPost, "/api/playlists/"+p.ID+"/batch/add", tc.body); resp.Status != http.StatusBadRequest {
							t.Fatalf("%s: status %d, want 400; body %.200s", tc.what, resp.Status, resp.Body)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r096", Invariant: "INV-R096-ACCESS", Case: "error", State: "failure", Assertion: "batch-add-rejects-foreign-unknown-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "POST /batch/add answers 401 without a token, 404 for an unknown playlist, 403 for another user or an administrator, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				unchanged(t, e, func() {
					wantStatus(t, batchAdd(t, e, "", e.IDs["playlist"], book(fx.saga1)), http.StatusUnauthorized)
					wantStatus(t, batchAdd(t, e, e.Token(t, "listener"), "no-such-playlist", book(fx.saga1)), http.StatusNotFound)
					wantStatus(t, batchAdd(t, e, e.Token(t, "finisher"), e.IDs["playlist"], book(fx.saga1)), http.StatusForbidden)
					wantStatus(t, batchAdd(t, e, e.Token(t, "root"), e.IDs["playlist"], book(fx.saga1)), http.StatusForbidden)
				})
			},
		},
		{
			Requirement: "route-r097", Invariant: "INV-R097-SAMPLED", Case: "normal", State: "initial", Assertion: "batch-remove-keeps-remaining-order",
			Sources: []string{"server/controllers/PlaylistController.js:463-502"},
			Claim:   "POST /batch/remove deletes the listed books; the response and later reads list the remaining ones in their original order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch shrink", book(fx.saga1), book(fx.novel), book(fx.ebook), book(fx.saga2))
				got := decode(t, batchRemove(t, e, token, p.ID, bookNull(fx.novel), bookNull(fx.saga2))) // episodeId: null as the web client sends it
				sameStrings(t, "response", got.refs(), []string{fx.saga1, fx.ebook})
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.ebook})
				if n := len(links(t, e, p.ID)); n != 2 {
					t.Fatalf("persisted links = %d, want 2", n)
				}
			},
		},
		{
			Requirement: "route-r097", Invariant: "INV-R097-SAMPLED", Case: "edge", State: "repeat", Assertion: "batch-remove-ignores-missing-items",
			Sources: []string{"server/controllers/PlaylistController.js:479-482", "server/controllers/PlaylistController.js:491-501"},
			Claim:   "Entries that are not in the playlist are ignored: removing only absent items answers 200 with the playlist unchanged, and removing an episode needs its episodeId.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createPodcastAs(t, e, token, "Batch ignore", episode(fx.podcast, fx.ep1), episode(fx.podcast, fx.ep2))
				unchanged(t, e, func() {
					got := decode(t, batchRemove(t, e, token, p.ID, book(fx.novel), book(fx.podcast), episode(fx.podcast, fx.ep3)))
					sameStrings(t, "response", got.refs(), []string{ref(fx.podcast, fx.ep1), ref(fx.podcast, fx.ep2)})
				})
				got := decode(t, batchRemove(t, e, token, p.ID, episode(fx.podcast, fx.ep1), episode(fx.podcast, fx.ep1)))
				sameStrings(t, "after removing ep1", got.refs(), []string{ref(fx.podcast, fx.ep2)})
			},
		},
		{
			Requirement: "route-r097", Invariant: "INV-R097-EMPTY", Case: "edge", State: "changed", Assertion: "batch-remove-everything-deletes-playlist",
			Sources: []string{"server/controllers/PlaylistController.js:491-500"},
			Claim:   "Removing every item answers 200 with an item-less playlist and deletes the playlist and its links.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch empty", book(fx.saga1), book(fx.novel))
				got := decode(t, batchRemove(t, e, token, p.ID, book(fx.saga1), book(fx.novel)))
				if got.ID != p.ID || len(got.Items) != 0 {
					t.Fatalf("response must be the emptied playlist: %+v", got)
				}
				noPlaylistRow(t, e, p.ID)
				wantStatus(t, e.Get(t, token, "/api/playlists/"+p.ID), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r097", Invariant: "INV-R097-VALIDATE", Case: "error", State: "failure", Assertion: "batch-remove-rejects-invalid-bodies-and-foreign-callers",
			Sources: []string{"server/controllers/PlaylistController.js:464-466", "server/controllers/PlaylistController.js:575-593"},
			Claim:   "POST /batch/remove answers 400 for missing, empty or malformed items, 401 without a token, 404 for an unknown playlist and 403 for another user or an administrator, and removes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-batch")
				p := createAs(t, e, token, "Batch remove strict", book(fx.novel))
				unchanged(t, e, func() {
					for _, body := range []any{
						map[string]any{},
						map[string]any{"items": []any{}},
						map[string]any{"items": "x"},
						map[string]any{"items": []any{map[string]any{}}},
						map[string]any{"items": []any{map[string]any{"libraryItemId": fx.novel, "episodeId": 5}}},
					} {
						wantStatus(t, call(t, e, token, http.MethodPost, "/api/playlists/"+p.ID+"/batch/remove", body), http.StatusBadRequest)
					}
					wantStatus(t, batchRemove(t, e, "", e.IDs["playlist"], book(fx.novel)), http.StatusUnauthorized)
					wantStatus(t, batchRemove(t, e, e.Token(t, "listener"), "no-such-playlist", book(fx.novel)), http.StatusNotFound)
					wantStatus(t, batchRemove(t, e, e.Token(t, "finisher"), e.IDs["playlist"], book(fx.novel)), http.StatusForbidden)
					wantStatus(t, batchRemove(t, e, e.Token(t, "root"), e.IDs["playlist"], book(fx.novel)), http.StatusForbidden)
				})
			},
		},
	}
}
