package playlists

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// setItemMedia patches /api/items/:id/media as root and returns a restore
// function (also registered as cleanup) that sends the given body.
func setItemMedia(t *testing.T, e *harness.Env, itemID string, body, restore map[string]any) {
	t.Helper()
	root := e.Token(t, "root")
	wantStatus(t, call(t, e, root, http.MethodPatch, "/api/items/"+itemID+"/media", body), http.StatusOK)
	t.Cleanup(func() { call(t, e, root, http.MethodPatch, "/api/items/"+itemID+"/media", restore) }) // best effort restore of the shared fixture
}

// limitedUser is a user whose permissions are the all-open set plus overrides.
func limitedUser(t *testing.T, e *harness.Env, name string, override map[string]any) string {
	permissions := map[string]any{"download": true, "update": false, "delete": false, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true}
	for key, value := range override {
		permissions[key] = value
	}
	return extraUser(t, e, name, permissions)
}

// fromCollection posts /api/playlists/collection/:id.
func fromCollection(t *testing.T, e *harness.Env, token, collectionID string) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/playlists/collection/"+collectionID, nil)
}

// accessOrder is one scenario of a route that breaks two rules at once: the
// caller cannot access the playlist's library and does not own the playlist.
func accessOrder(requirement, invariant, state, assertion, source, method, suffix string, body func(fixture) any) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: "edge", State: state, Assertion: assertion,
		Sources: []string{source, "server/controllers/PlaylistController.js:575-593"},
		Claim:   "A caller without access to the playlist's library asking for another user's playlist gets 403 (owner check first), not 404, and nothing changes.",
		Run: func(t *testing.T, e *harness.Env) {
			r := revokedUser(t, e)
			unchanged(t, e, func() {
				path := "/api/playlists/" + e.IDs["playlist"] + suffix
				wantStatus(t, call(t, e, r.token, method, path, body(fixtureOf(e))), http.StatusForbidden)
			})
		},
	}
}

func restrictedScenarios() []harness.Scenario {
	none := func(fixture) any { return nil }
	items := func(fx fixture) any { return map[string]any{"items": []any{book(fx.novel)}} }
	return []harness.Scenario{
		{
			Requirement: "route-r098", Invariant: "INV-R098-CONTRACT-001", Case: "edge", State: "changed", Assertion: "from-collection-hides-restricted-books",
			Sources: []string{"server/controllers/PlaylistController.js:521-530", "server/models/Collection.js:222-244"},
			Claim:   "For users restricted by allowed tags, denied tags or explicit content, a playlist from a collection holds only the books they may see, in collection order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				setItemMedia(t, e, fx.saga2, map[string]any{"tags": []string{"pl-open"}}, map[string]any{"tags": []string{}})
				setItemMedia(t, e, fx.novel, map[string]any{"tags": []string{"pl-secret"}}, map[string]any{"tags": []string{}})
				setItemMedia(t, e, fx.saga1, map[string]any{"metadata": map[string]any{"explicit": true}}, map[string]any{"metadata": map[string]any{"explicit": false}})
				colID := makeCollection(t, e, "Restricted mix", fx.saga1, fx.saga2, fx.novel)
				for _, tc := range []struct {
					user     string
					override map[string]any
					want     []string
				}{
					{"pl-tag-allow", map[string]any{"accessAllTags": false, "itemTagsSelected": []string{"pl-open"}, "selectedTagsNotAccessible": false}, []string{fx.saga1, fx.saga2}},
					{"pl-tag-deny", map[string]any{"accessAllTags": false, "itemTagsSelected": []string{"pl-secret"}, "selectedTagsNotAccessible": true}, []string{fx.saga1, fx.saga2}},
					{"pl-no-explicit", map[string]any{"accessExplicitContent": false}, []string{fx.saga2, fx.novel}},
				} {
					token := limitedUser(t, e, tc.user, tc.override)
					p := decode(t, fromCollection(t, e, token, colID))
					sameStrings(t, tc.user+" playlist items", p.refs(), tc.want)
					wantBookLinks(t, e, p.ID, tc.want...)
				}
				sameStrings(t, "unrestricted user", decode(t, fromCollection(t, e, plainUser(t, e, "pl-from-collection"), colID)).refs(), []string{fx.saga1, fx.saga2, fx.novel})
			},
		},
		{
			Requirement: "route-r098", Invariant: "INV-R098-REJECT", Case: "error", State: "failure", Assertion: "from-collection-404-when-every-book-hidden",
			Sources: []string{"server/controllers/PlaylistController.js:521-525", "server/models/Collection.js:236-239"},
			Claim:   "When the caller's tag or explicit-content restrictions hide every book of a collection, creating a playlist from it answers 404 and creates nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				setItemMedia(t, e, fx.novel, map[string]any{"tags": []string{"pl-secret"}}, map[string]any{"tags": []string{}})
				setItemMedia(t, e, fx.saga1, map[string]any{"metadata": map[string]any{"explicit": true}}, map[string]any{"metadata": map[string]any{"explicit": false}})
				tagged := makeCollection(t, e, "All tagged", fx.novel)
				explicit := makeCollection(t, e, "All explicit", fx.saga1)
				allow := limitedUser(t, e, "pl-tag-allow", map[string]any{"accessAllTags": false, "itemTagsSelected": []string{"pl-open"}, "selectedTagsNotAccessible": false})
				deny := limitedUser(t, e, "pl-tag-deny", map[string]any{"accessAllTags": false, "itemTagsSelected": []string{"pl-secret"}, "selectedTagsNotAccessible": true})
				noExplicit := limitedUser(t, e, "pl-no-explicit", map[string]any{"accessExplicitContent": false})
				unchanged(t, e, func() {
					wantStatus(t, fromCollection(t, e, allow, tagged), http.StatusNotFound)
					wantStatus(t, fromCollection(t, e, deny, tagged), http.StatusNotFound)
					wantStatus(t, fromCollection(t, e, noExplicit, explicit), http.StatusNotFound)
				})
				if p := decode(t, fromCollection(t, e, e.Token(t, "root"), tagged)); len(p.Items) != 1 {
					t.Fatalf("an unrestricted user must still get the book: %+v", p)
				}
			},
		},
		accessOrder("route-r091", "INV-R091-ACCESS", "stateless", "get-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:161", http.MethodGet, "", none),
		accessOrder("route-r092", "INV-R092-ACCESS", "failure", "patch-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:162", http.MethodPatch, "", func(fixture) any { return map[string]any{"name": "x"} }),
		accessOrder("route-r093", "INV-R093-ACCESS", "failure", "delete-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:163", http.MethodDelete, "", none),
		accessOrder("route-r094", "INV-R094-ACCESS", "failure", "add-item-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:164", http.MethodPost, "/item", func(fx fixture) any { return book(fx.saga1) }),
		accessOrder("route-r095", "INV-R095-ACCESS", "failure", "remove-item-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:165", http.MethodDelete, "/item/"+"no-item", none),
		accessOrder("route-r096", "INV-R096-ACCESS", "failure", "batch-add-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:166", http.MethodPost, "/batch/add", items),
		accessOrder("route-r097", "INV-R097-VALIDATE", "failure", "batch-remove-foreign-in-inaccessible-library-is-403", "server/routers/ApiRouter.js:167", http.MethodPost, "/batch/remove", items),
		{
			Requirement: "route-r028", Invariant: "INV-ROUTE-R028-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-restricted-caller-unknown-library-is-403",
			Sources: []string{"server/controllers/LibraryController.js:1475-1484"},
			Claim:   "A library-restricted user asking for the playlists of an unknown library gets 403 (access check before the lookup), while an unrestricted user gets 404.",
			Run: func(t *testing.T, e *harness.Env) {
				r := revokedUser(t, e)
				wantStatus(t, e.Get(t, r.token, "/api/libraries/no-such-library/playlists"), http.StatusForbidden)
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/libraries/no-such-library/playlists"), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r097", Invariant: "INV-R097-SAMPLED", Case: "edge", State: "changed", Assertion: "batch-remove-then-add-puts-new-item-last",
			Sources: []string{"server/controllers/PlaylistController.js:413-416", "server/controllers/PlaylistController.js:484-488"},
			Claim:   "After a batch remove from the middle, a batch add of a new book lists it last in the response and in later reads (only relative order is asserted).",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-modal")
				p := createAs(t, e, token, "Modal batch", book(fx.saga1), book(fx.novel), book(fx.saga2))
				decode(t, batchRemove(t, e, token, p.ID, bookNull(fx.novel)))
				added := decode(t, batchAdd(t, e, token, p.ID, bookNull(fx.ebook)))
				sameStrings(t, "response", added.refs(), []string{fx.saga1, fx.saga2, fx.ebook})
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.saga2, fx.ebook})
			},
		},
		{
			Requirement: "route-r095", Invariant: "INV-R095-CONTRACT-001", Case: "edge", State: "changed", Assertion: "remove-then-add-puts-new-item-last",
			Sources: []string{"server/controllers/PlaylistController.js:364-371", "server/controllers/PlaylistController.js:312-318"},
			Claim:   "After removing an item from the middle, adding a new book (single or batch) lists it last in the response and in later reads.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-modal")
				p := createAs(t, e, token, "Modal single", book(fx.saga1), book(fx.novel), book(fx.saga2))
				decode(t, removeItem(t, e, token, p.ID, fx.novel, ""))
				added := decode(t, addItem(t, e, token, p.ID, book(fx.ebook)))
				sameStrings(t, "response", added.refs(), []string{fx.saga1, fx.saga2, fx.ebook})
				batch := decode(t, batchAdd(t, e, token, p.ID, bookNull(fx.novel)))
				sameStrings(t, "batch response", batch.refs(), []string{fx.saga1, fx.saga2, fx.ebook, fx.novel})
				sameStrings(t, "read", fetchAs(t, e, token, p.ID).refs(), []string{fx.saga1, fx.saga2, fx.ebook, fx.novel})
			},
		},
	}
}
