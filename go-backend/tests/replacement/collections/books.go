package collections

import (
	"net/http"
	"slices"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func addBook(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/collections/"+id+"/book", body)
}

func removeBook(t *testing.T, e *harness.Env, token, id, itemID string) harness.Response {
	t.Helper()
	return e.Delete(t, token, "/api/collections/"+id+"/book/"+itemID)
}

func batch(t *testing.T, e *harness.Env, token, id, action string, books any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPost, "/api/collections/"+id+"/batch/"+action, map[string]any{"books": books})
}

// wantSameSet asserts got holds the same strings as want in any order.
func wantSameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	sameStrings(t, what, g, w)
}

func bookScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r085", Invariant: "INV-R085-CONTRACT-001", Case: "normal", State: "initial", Assertion: "add-book-appends-link",
			Sources: []string{"server/controllers/CollectionController.js:234-260"},
			Claim:   "POST /api/collections/:id/book with a library item id answers 200 with the expanded collection, the book last, and persists a link numbered after the existing ones.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Add one", ids.novel)
				got := decode(t, addBook(t, e, e.Token(t, "root"), c.ID, map[string]any{"id": ids.saga2}))
				sameStrings(t, "response books", got.itemIDs(), []string{ids.novel, ids.saga2})
				if got.LastUpdate < c.LastUpdate || got.ID != c.ID {
					t.Fatalf("unexpected response %+v", got)
				}
				wantLinks(t, e, c.ID, ids.novel, ids.saga2)
				sameStrings(t, "GET books", fetch(t, e, c.ID).itemIDs(), []string{ids.novel, ids.saga2})
			},
		},
		{
			Requirement: "route-r085", Invariant: "INV-R085-CONTRACT-001", Case: "error", State: "repeat", Assertion: "add-book-duplicate-rejected",
			Sources: []string{"server/controllers/CollectionController.js:245-249"},
			Claim:   "Adding a book that is already in the collection answers 400 and leaves the links unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Add twice", ids.novel)
				root := e.Token(t, "root")
				decode(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.saga1}))
				unchanged(t, e, func() {
					wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.saga1}), http.StatusBadRequest)
					wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.novel}), http.StatusBadRequest)
				})
			},
		},
		{
			Requirement: "route-r085", Invariant: "INV-R085-CONTRACT-001", Case: "edge", State: "changed", Assertion: "add-book-after-remove-goes-last",
			Sources: []string{"server/controllers/CollectionController.js:251-256", "server/controllers/CollectionController.js:271-308"},
			Claim:   "A book removed and added again becomes the last link, and the order stays 1..n.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Re-add", ids.saga1, ids.novel, ids.saga2)
				wantStatus(t, removeBook(t, e, root, c.ID, ids.saga1), http.StatusOK)
				got := decode(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.saga1}))
				sameStrings(t, "books", got.itemIDs(), []string{ids.novel, ids.saga2, ids.saga1})
				wantLinks(t, e, c.ID, ids.novel, ids.saga2, ids.saga1)
			},
		},
		{
			Requirement: "route-r085", Invariant: "INV-R085-CONTRACT-001", Case: "error", State: "failure", Assertion: "add-book-rejected-unchanged",
			Sources: []string{"server/controllers/CollectionController.js:235-243", "server/controllers/CollectionController.js:433-453"},
			Claim:   "Adding answers 404 for an unknown or missing item id, 400 for an item of another library, 401 without a token, 403 without update permission and 404 for an inaccessible collection, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Add rejected", ids.novel)
				root := e.Token(t, "root")
				unchanged(t, e, func() {
					wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": "no-such-item"}), http.StatusNotFound)
					wantStatus(t, addBook(t, e, root, c.ID, map[string]any{}), http.StatusNotFound)
					wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.podcast}), http.StatusBadRequest)
					wantStatus(t, addBook(t, e, "", c.ID, map[string]any{"id": ids.saga1}), http.StatusUnauthorized)
					wantStatus(t, addBook(t, e, e.Token(t, "listener"), c.ID, map[string]any{"id": ids.saga1}), http.StatusForbidden)
					wantStatus(t, addBook(t, e, podcastOnly(t, e), c.ID, map[string]any{"id": ids.saga1}), http.StatusNotFound)
					wantStatus(t, addBook(t, e, root, "no-such-collection", map[string]any{"id": ids.saga1}), http.StatusNotFound)
					// the collection lookup runs before the permission check
					wantStatus(t, addBook(t, e, e.Token(t, "listener"), "no-such-collection", map[string]any{"id": ids.saga1}), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r086", Invariant: "INV-R086-CONTRACT-001", Case: "normal", State: "initial", Assertion: "remove-book-renumbers",
			Sources: []string{"server/routers/ApiRouter.js:152", "server/controllers/CollectionController.js:433", "server/controllers/CollectionController.js:271"},
			Claim:   "DELETE /api/collections/:id/book/:itemId answers 200 with the collection without that book, renumbers the remaining links from 1 and keeps the library item.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Remove one", ids.saga1, ids.novel, ids.saga2)
				items := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				got := decode(t, removeBook(t, e, e.Token(t, "root"), c.ID, ids.saga1))
				sameStrings(t, "response books", got.itemIDs(), []string{ids.novel, ids.saga2})
				wantLinks(t, e, c.ID, ids.novel, ids.saga2)
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems") != items {
					t.Fatal("library item was removed")
				}
			},
		},
		{
			Requirement: "route-r086", Invariant: "INV-R086-CONTRACT-001", Case: "edge", State: "repeat", Assertion: "remove-book-not-member-is-noop",
			Sources: []string{"server/routers/ApiRouter.js:152", "server/controllers/CollectionController.js:433", "server/controllers/CollectionController.js:271"},
			Claim:   "Removing a book that is not in the collection answers 200 with the unchanged collection and changes no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Remove twice", ids.novel, ids.saga2)
				decode(t, removeBook(t, e, root, c.ID, ids.saga2))
				unchanged(t, e, func() {
					got := decode(t, removeBook(t, e, root, c.ID, ids.saga2))
					sameStrings(t, "books", got.itemIDs(), []string{ids.novel})
					got = decode(t, removeBook(t, e, root, c.ID, ids.ebook))
					sameStrings(t, "books", got.itemIDs(), []string{ids.novel})
				})
			},
		},
		{
			Requirement: "route-r086", Invariant: "INV-R086-ACCESS-001", Case: "edge", State: "changed", Assertion: "remove-book-by-user-with-delete-permission",
			Sources: []string{"server/controllers/CollectionController.js:271-308", "server/controllers/CollectionController.js:433-453", "client/components/tables/collection/BookTableRow.vue:45", "client/components/tables/collection/BookTableRow.vue:202"},
			Claim:   "A user with delete permission (the only users whose web client offers the remove button) but no update permission can remove a book from a collection.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Deleter removes", ids.novel, ids.saga2)
				deleter := extraUser(t, e, "collection-deleter", map[string]any{
					"download": true, "update": false, "delete": true, "accessAllLibraries": true,
					"accessAllTags": true, "accessExplicitContent": true,
				})
				got := decode(t, removeBook(t, e, deleter, c.ID, ids.novel))
				sameStrings(t, "books", got.itemIDs(), []string{ids.saga2})
				wantLinks(t, e, c.ID, ids.saga2)
			},
		},
		{
			Requirement: "route-r086", Invariant: "INV-R086-ACCESS-001", Case: "error", State: "failure", Assertion: "remove-book-rejected-unchanged",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:271-277", "server/controllers/CollectionController.js:433-453"},
			Claim:   "Removing answers 404 for an unknown item, 401 without a token, and 404 for an unknown or inaccessible collection, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Remove rejected", ids.novel)
				root := e.Token(t, "root")
				unchanged(t, e, func() {
					wantStatus(t, removeBook(t, e, root, c.ID, "no-such-item"), http.StatusNotFound)
					wantStatus(t, removeBook(t, e, "", c.ID, ids.novel), http.StatusUnauthorized)
					wantStatus(t, removeBook(t, e, podcastOnly(t, e), c.ID, ids.novel), http.StatusNotFound)
					wantStatus(t, removeBook(t, e, root, "no-such-collection", ids.novel), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r087", Invariant: "INV-R087-SAMPLED", Case: "normal", State: "initial", Assertion: "batch-add-appends-links",
			Sources: []string{"server/routers/ApiRouter.js:153"},
			Claim:   "POST /batch/add answers 200 with the collection after appending all given books after the existing links, numbered contiguously.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Batch add", ids.novel)
				got := decode(t, batch(t, e, e.Token(t, "root"), c.ID, "add", []string{ids.saga1, ids.saga2, ids.ebook}))
				if len(got.Books) != 4 || got.Books[0].ID != ids.novel {
					t.Fatalf("books = %v", got.itemIDs())
				}
				wantSameSet(t, "books", got.itemIDs(), []string{ids.novel, ids.saga1, ids.saga2, ids.ebook})
				persisted, orders := links(t, e, c.ID)
				sameStrings(t, "persisted order = response order", persisted, got.itemIDs())
				sameStrings(t, "orders", orders, []string{"1", "2", "3", "4"})
			},
		},
		{
			Requirement: "route-r087", Invariant: "INV-R087-SAMPLED", Case: "edge", State: "repeat", Assertion: "batch-add-skips-members-unknown-and-non-strings",
			Sources: []string{"server/routers/ApiRouter.js:153"},
			Claim:   "Batch add ignores non-string entries, unknown ids, podcast items and books already in the collection, adds only the rest, and a repeat of the same request changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Batch skip", ids.novel)
				mixed := []any{nil, 3, "", ids.novel, "no-such-item", ids.podcast, ids.saga2}
				got := decode(t, batch(t, e, root, c.ID, "add", mixed))
				sameStrings(t, "books", got.itemIDs(), []string{ids.novel, ids.saga2})
				wantLinks(t, e, c.ID, ids.novel, ids.saga2)
				unchanged(t, e, func() {
					again := decode(t, batch(t, e, root, c.ID, "add", mixed))
					sameStrings(t, "books", again.itemIDs(), []string{ids.novel, ids.saga2})
					if again.LastUpdate != got.LastUpdate {
						t.Fatalf("lastUpdate moved on a no-op: %d -> %d", got.LastUpdate, again.LastUpdate)
					}
				})
			},
		},
		{
			Requirement: "route-r087", Invariant: "INV-R087-SAMPLED", Case: "normal", State: "changed", Assertion: "batch-add-after-removal-goes-last",
			Sources: []string{"server/routers/ApiRouter.js:153"},
			Claim:   "Books removed and batch-added again are appended after the remaining links.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Batch re-add", ids.saga1, ids.novel)
				decode(t, batch(t, e, root, c.ID, "remove", []string{ids.saga1}))
				got := decode(t, batch(t, e, root, c.ID, "add", []string{ids.saga1}))
				sameStrings(t, "books", got.itemIDs(), []string{ids.novel, ids.saga1})
				wantLinks(t, e, c.ID, ids.novel, ids.saga1)
			},
		},
		{
			Requirement: "route-r087", Invariant: "INV-R087-ACCESS-001", Case: "error", State: "failure", Assertion: "batch-add-rejected-unchanged",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:318-336", "server/controllers/CollectionController.js:433-453"},
			Claim:   "Batch add answers 400 for a missing, empty, all-invalid or all-foreign books list, 401 without a token, 403 without update permission and 404 for an inaccessible or unknown collection, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Batch add rejected", ids.novel)
				root := e.Token(t, "root")
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, root, http.MethodPost, "/api/collections/"+c.ID+"/batch/add", map[string]any{}), http.StatusBadRequest)
					for _, books := range [][]any{{}, {nil, 4}, {"no-such-item"}, {ids.podcast}} {
						wantStatus(t, batch(t, e, root, c.ID, "add", books), http.StatusBadRequest)
					}
					wantStatus(t, batch(t, e, "", c.ID, "add", []string{ids.saga1}), http.StatusUnauthorized)
					wantStatus(t, batch(t, e, e.Token(t, "listener"), c.ID, "add", []string{ids.saga1}), http.StatusForbidden)
					wantStatus(t, batch(t, e, podcastOnly(t, e), c.ID, "add", []string{ids.saga1}), http.StatusNotFound)
					wantStatus(t, batch(t, e, root, "no-such-collection", "add", []string{ids.saga1}), http.StatusNotFound)
					// the collection lookup runs before the permission check
					wantStatus(t, batch(t, e, e.Token(t, "listener"), "no-such-collection", "add", []string{ids.saga1}), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r088", Invariant: "INV-R088-SAMPLED", Case: "normal", State: "initial", Assertion: "batch-remove-renumbers",
			Sources: []string{"server/routers/ApiRouter.js:154"},
			Claim:   "POST /batch/remove answers 200 with the remaining books in order, renumbers their links from 1 and keeps the library items.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Batch remove", ids.saga1, ids.novel, ids.saga2, ids.ebook)
				items := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				got := decode(t, batch(t, e, e.Token(t, "root"), c.ID, "remove", []string{ids.novel, ids.ebook}))
				sameStrings(t, "books", got.itemIDs(), []string{ids.saga1, ids.saga2})
				wantLinks(t, e, c.ID, ids.saga1, ids.saga2)
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems") != items {
					t.Fatal("library items were removed")
				}
			},
		},
		{
			Requirement: "route-r088", Invariant: "INV-R088-SAMPLED", Case: "edge", State: "repeat", Assertion: "batch-remove-repeat-and-nonmembers-noop",
			Sources: []string{"server/routers/ApiRouter.js:154"},
			Claim:   "Removing books that are not in the collection (repeat, unknown ids, other books, non-strings) answers 200 with the unchanged collection and changes no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Batch remove noop", ids.saga1, ids.novel)
				first := decode(t, batch(t, e, root, c.ID, "remove", []string{ids.saga1}))
				unchanged(t, e, func() {
					got := decode(t, batch(t, e, root, c.ID, "remove", []any{ids.saga1, nil, 2, "no-such-item", ids.ebook}))
					sameStrings(t, "books", got.itemIDs(), []string{ids.novel})
					if got.LastUpdate != first.LastUpdate {
						t.Fatalf("lastUpdate moved on a no-op")
					}
				})
			},
		},
		{
			Requirement: "route-r088", Invariant: "INV-R088-SAMPLED", Case: "normal", State: "changed", Assertion: "batch-remove-everything-leaves-empty-collection",
			Sources: []string{"server/routers/ApiRouter.js:154"},
			Claim:   "Removing every book leaves the collection with an empty books array and no links, and later reads still return it.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Batch empty", ids.saga1, ids.novel)
				got := decode(t, batch(t, e, root, c.ID, "remove", []string{ids.saga1, ids.novel, ids.ebook}))
				if got.Books == nil || len(got.Books) != 0 {
					t.Fatalf("books = %v", got.Books)
				}
				wantLinks(t, e, c.ID)
				if again := fetch(t, e, c.ID); again.Name != "Batch empty" || len(again.Books) != 0 {
					t.Fatalf("read after emptying: %+v", again)
				}
			},
		},
		{
			Requirement: "route-r088", Invariant: "INV-R088-ACCESS-001", Case: "error", State: "failure", Assertion: "batch-remove-rejected-unchanged",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:380-385", "server/controllers/CollectionController.js:433-453"},
			Claim:   "Batch remove answers 500 for a missing, empty or non-string books list, 401 without a token, 403 without update permission and 404 for an inaccessible or unknown collection, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Batch remove rejected", ids.novel)
				root := e.Token(t, "root")
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, root, http.MethodPost, "/api/collections/"+c.ID+"/batch/remove", map[string]any{}), http.StatusInternalServerError)
					for _, books := range [][]any{{}, {nil, 4}} {
						wantStatus(t, batch(t, e, root, c.ID, "remove", books), http.StatusInternalServerError)
					}
					wantStatus(t, batch(t, e, "", c.ID, "remove", []string{ids.novel}), http.StatusUnauthorized)
					wantStatus(t, batch(t, e, e.Token(t, "listener"), c.ID, "remove", []string{ids.novel}), http.StatusForbidden)
					wantStatus(t, batch(t, e, podcastOnly(t, e), c.ID, "remove", []string{ids.novel}), http.StatusNotFound)
					wantStatus(t, batch(t, e, root, "no-such-collection", "remove", []string{ids.novel}), http.StatusNotFound)
					// the collection lookup runs before the permission check
					wantStatus(t, batch(t, e, e.Token(t, "listener"), "no-such-collection", "remove", []string{ids.novel}), http.StatusNotFound)
				})
			},
		},
	}
}
