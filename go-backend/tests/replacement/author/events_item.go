package author

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// setItemAuthors sets the authors of the library item's book through PATCH /api/items/:id/media.
func setItemAuthors(t *testing.T, e *harness.Env, token, itemID string, names ...string) harness.Response {
	t.Helper()
	authors := make([]map[string]string, 0, len(names))
	for _, name := range names {
		authors = append(authors, map[string]string{"name": name})
	}
	return e.Patch(t, token, "/api/items/"+itemID+"/media", map[string]any{"metadata": map[string]any{"authors": authors}})
}

// nameIs matches events whose first argument has the name.
func nameIs(name string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool { return first(args)["name"] == name }
}

// removedPayload is the author_removed payload of the item and scan paths.
type removedPayload struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
}

func itemEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "normal", State: "changed", Assertion: "author-added-by-item-update",
			Sources: []string{"server/models/Book.js:466-472", "server/controllers/LibraryItemController.js:251-267"},
			Claim:   "Adding a new author name to a book through PATCH /api/items/:id/media creates the author and sends author_added with the author JSON (no numBooks) to every client, and none for the author already on the book.",
			Run: func(t *testing.T, e *harness.Env) {
				item := e.IDs["ebook"]
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), item, "Ada Example", "Brand New Coauthor"), http.StatusOK)
				got := authorEvent(t, w.waitBoth(t, "author_added", nameIs("Brand New Coauthor")), -1)
				row := mustRow(t, e, got.ID)
				if got.LibraryID != e.IDs["library"] || got.AddedAt != dbTime(t, row["createdAt"]) || bookCount(t, e, got.ID) != 1 || rowText(row, "lastFirst") != "Coauthor, Brand New" {
					t.Fatalf("event %+v does not match row %v", got, row)
				}
				w.none(t, "author_added", nameIs("Ada Example"))
			},
		},
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "edge", State: "repeat", Assertion: "author-added-not-repeated",
			Sources: []string{"server/models/Book.js:441-476", "server/controllers/LibraryItemController.js:231-267"},
			Claim:   "Repeating the same item update, which changes nothing, sends neither author_added nor author_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				item := e.IDs["ebook"]
				w := watch(t, e)
				wantStatus(t, "repeat", setItemAuthors(t, e, e.Token(t, "root"), item, "Ada Example", "Brand New Coauthor"), http.StatusOK)
				w.none(t, "author_added", nil)
				w.none(t, "author_updated", nil)
				if got := bookAuthorNames(t, e, item); len(got) != 2 {
					t.Fatalf("authors = %v", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "error", State: "failure", Assertion: "author-added-denied-update-sends-nothing",
			Sources: []string{"server/controllers/LibraryItemController.js:186-200"},
			Claim:   "An item update refused with 403 (user without update permission) creates no author and sends no author event.",
			Run: func(t *testing.T, e *harness.Env) {
				item := e.IDs["ebook"]
				w := watch(t, e)
				wantStatus(t, "listener", setItemAuthors(t, e, e.Token(t, "listener"), item, "Ada Example", "Brand New Coauthor", "Refused Author"), http.StatusForbidden)
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Refused Author'") != 0 {
					t.Fatal("no author may be created")
				}
				w.none(t, "author_added", nil)
				w.none(t, "author_updated", nil)
				w.none(t, "author_removed", nil)
			},
		},
		{
			Requirement: "socket-outgoing-author-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-UPDATED", Case: "normal", State: "changed", Assertion: "author-updated-existing-author-added-to-book",
			Sources: []string{"server/models/Book.js:473-475"},
			Claim:   "Adding an existing author to a book through an item update sends author_updated with the author JSON and the new numBooks to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example", "Brand New Coauthor", "Scan Author"), http.StatusOK)
				got := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 3)
				if got.Name != "Scan Author" || bookCount(t, e, id) != 3 {
					t.Fatalf("event %+v, rows %d", got, bookCount(t, e, id))
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "edge", State: "changed", Assertion: "author-added-not-sent-for-existing-author",
			Sources: []string{"server/models/Book.js:466-475"},
			Claim:   "Adding an author that already exists to a book sends author_updated for it and no author_added.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan New")
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example", "Brand New Coauthor", "Scan Author", "Scan New"), http.StatusOK)
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 2)
				w.none(t, "author_added", idIs(id))
			},
		},
		{
			Requirement: "socket-outgoing-authors-num-books-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHORS-NUM-BOOKS-UPDATED", Case: "error", State: "failure", Assertion: "num-books-not-sent-by-item-update",
			Sources: []string{"server/scanner/BookScanner.js:905-930", "server/models/Book.js:441-476"},
			Claim:   "Changing the authors of a book through the API (author_updated is sent) does not send authors_num_books_updated; only the scanner does.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan New")
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example", "Brand New Coauthor", "Scan Author"), http.StatusOK)
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 1)
				w.none(t, "authors_num_books_updated", nil)
			},
		},
		{
			Requirement: "socket-outgoing-author-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-UPDATED", Case: "edge", State: "changed", Assertion: "author-updated-author-removed-from-one-book",
			Sources: []string{"server/models/Book.js:455-463", "server/controllers/LibraryItemController.js:254-258"},
			Claim:   "Removing an author from one of several books sends author_updated with the remaining numBooks and no author_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example", "Brand New Coauthor"), http.StatusOK)
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 2)
				w.none(t, "author_removed", idIs(id))
				if authorRow(t, e, id) == nil {
					t.Fatal("the author keeps its other books")
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "normal", State: "initial", Assertion: "author-removed-last-book-removed-by-item-update",
			Sources: []string{"server/routers/ApiRouter.js:472-523", "server/controllers/LibraryItemController.js:254-258"},
			Claim:   "Removing an author without asin, description and image from its only book through an item update deletes the author and sends author_removed {id, libraryId} to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Brand New Coauthor")
				w := watch(t, e)
				wantStatus(t, "item update", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example"), http.StatusOK)
				got := decode[removedPayload](t, w.waitBoth(t, "author_removed", idIs(id)))
				if got.LibraryID != e.IDs["library"] || authorRow(t, e, id) != nil || bookCount(t, e, id) != 0 {
					t.Fatalf("payload %+v, row %v", got, authorRow(t, e, id))
				}
				wantStatus(t, "gone", e.Get(t, e.Token(t, "root"), authorURL(id, "")), http.StatusNotFound)
			},
		},
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "edge", State: "changed", Assertion: "author-removed-not-sent-for-author-with-description",
			Sources: []string{"server/routers/ApiRouter.js:478-496"},
			Claim:   "An author with a description that loses its last book is kept: no author_removed is sent and the row stays.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				wantStatus(t, "add", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Example", "Keeper Author"), http.StatusOK)
				id := authorID(t, e, "Keeper Author")
				wantStatus(t, "describe", patch(t, e, root, id, map[string]any{"description": "Kept without books."}), http.StatusOK)
				w := watch(t, e)
				wantStatus(t, "remove", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Example"), http.StatusOK)
				w.none(t, "author_removed", idIs(id))
				if row := authorRow(t, e, id); row == nil || bookCount(t, e, id) != 0 {
					t.Fatalf("the described author must stay without books: %v", row)
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "normal", State: "changed", Assertion: "author-removed-by-item-delete",
			Sources: []string{"server/controllers/LibraryItemController.js:114-150", "server/routers/ApiRouter.js:472-523"},
			Claim:   "Deleting the library item that is the only book of an author deletes the author and sends author_removed {id, libraryId}.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan New")
				items := e.Rows(t, "SELECT li.id AS id FROM libraryItems li JOIN bookAuthors ba ON ba.bookId = li.mediaId WHERE ba.authorId = ?", id)
				if len(items) != 1 {
					t.Fatalf("Scan New must have exactly one book, has %d", len(items))
				}
				w := watch(t, e)
				wantStatus(t, "delete item", e.Delete(t, e.Token(t, "root"), "/api/items/"+items[0]["id"].(string)), http.StatusOK)
				got := decode[removedPayload](t, w.waitBoth(t, "author_removed", idIs(id)))
				if got.LibraryID != e.IDs["library"] || authorRow(t, e, id) != nil {
					t.Fatalf("payload %+v, row %v", got, authorRow(t, e, id))
				}
			},
		},
	}
}

func authorRouteEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-author-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-UPDATED", Case: "normal", State: "initial", Assertion: "author-updated-by-patch",
			Sources: []string{"server/controllers/AuthorController.js:189-218"},
			Claim:   "A PATCH that changes an author sends author_updated with the whole author JSON, numBooks and the new updatedAt to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				before := mustRow(t, e, id)
				w := watch(t, e)
				wantStatus(t, "patch", patch(t, e, e.Token(t, "root"), id, map[string]any{"description": "Scanned in.", "asin": "B000APZOQA"}), http.StatusOK)
				got := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 2)
				row := mustRow(t, e, id)
				if got.Name != "Scan Author" || got.Description == nil || *got.Description != "Scanned in." || got.ASIN == nil || *got.ASIN != "B000APZOQA" || got.LibraryID != e.IDs["library"] ||
					got.AddedAt != dbTime(t, row["createdAt"]) || got.UpdatedAt != dbTime(t, row["updatedAt"]) || got.UpdatedAt <= dbTime(t, before["updatedAt"]) {
					t.Fatalf("event %+v, row %v", got, row)
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-UPDATED", Case: "edge", State: "repeat", Assertion: "author-updated-not-sent-for-noop-patch",
			Sources: []string{"server/controllers/AuthorController.js:182-189"},
			Claim:   "Repeating a PATCH that changes nothing sends no author_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				w := watch(t, e)
				wantStatus(t, "patch", patch(t, e, e.Token(t, "root"), id, map[string]any{"description": "Scanned in.", "asin": "B000APZOQA"}), http.StatusOK)
				w.none(t, "author_updated", nil)
			},
		},
		{
			Requirement: "socket-outgoing-author-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-UPDATED", Case: "error", State: "failure", Assertion: "author-updated-not-sent-for-denied-patch",
			Sources: []string{"server/controllers/AuthorController.js:435-438", "server/controllers/AuthorController.js:104-107"},
			Claim:   "A PATCH refused with 403 or 400 sends no author_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				w := watch(t, e)
				wantStatus(t, "listener", patch(t, e, e.Token(t, "listener"), id, map[string]any{"description": "Refused."}), http.StatusForbidden)
				wantStatus(t, "invalid", patch(t, e, e.Token(t, "root"), id, map[string]any{"name": 5}), http.StatusBadRequest)
				w.none(t, "author_updated", nil)
			},
		},
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "normal", State: "changed", Assertion: "author-removed-by-author-delete-full-payload",
			Sources: []string{"server/controllers/AuthorController.js:252"},
			Claim:   "DELETE /api/authors/:id sends author_removed with the full author JSON (id, name, libraryId, timestamps), unlike the sweep paths that send only id and libraryId.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				wantStatus(t, "setup", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Example", "Doomed Author"), http.StatusOK)
				id := authorID(t, e, "Doomed Author")
				full := decode[authorJSON](t, e.Get(t, root, authorURL(id, "")).Body)
				w := watch(t, e)
				wantStatus(t, "delete", e.Delete(t, root, authorURL(id, "")), http.StatusOK)
				if got := authorEvent(t, w.waitBoth(t, "author_removed", idIs(id)), -1); !reflect.DeepEqual(got, full) || got.Name != "Doomed Author" || got.AddedAt == 0 {
					t.Fatalf("event %+v, want %+v", got, full)
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "error", State: "failure", Assertion: "author-removed-not-sent-for-denied-delete",
			Sources: []string{"server/controllers/AuthorController.js:432-434"},
			Claim:   "A DELETE refused with 403 or 404 sends no author_removed and keeps the author.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				w := watch(t, e)
				wantStatus(t, "listener", e.Delete(t, e.Token(t, "listener"), authorURL(id, "")), http.StatusForbidden)
				wantStatus(t, "unknown", e.Delete(t, e.Token(t, "root"), authorURL("00000000-0000-4000-8000-000000000000", "")), http.StatusNotFound)
				w.none(t, "author_removed", nil)
				if authorRow(t, e, id) == nil {
					t.Fatal("the author must stay")
				}
			},
		},
	}
}
