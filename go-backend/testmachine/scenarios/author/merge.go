package author

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// itemIDsOf returns the library item ids of the author's books, sorted.
func itemIDsOf(t *testing.T, e *harness.Env, authorID string) []string {
	t.Helper()
	rows := e.Rows(t, "SELECT li.id AS id FROM libraryItems li JOIN bookAuthors ba ON ba.bookId = li.mediaId WHERE ba.authorId = ? ORDER BY li.id", authorID)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row["id"].(string))
	}
	return ids
}

// itemsUpdatedIDs returns the sorted ids of the items_updated payload.
func itemsUpdatedIDs(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	items := decode[[]struct {
		ID string `json:"id"`
	}](t, raw)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	slices.Sort(ids)
	return ids
}

func renameScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-RENAME", Case: "normal", State: "changed", Assertion: "update-rename-author",
			Sources: []string{"server/controllers/AuthorController.js:111-114,182-218", "server/models/Author.js:25-28"},
			Claim:   "Renaming an author stores the name and lastFirst, rewrites the author name in the metadata.json of each of its books while their other authors stay, and sends author_updated with numBooks and items_updated with those books to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Ada Example")
				items := itemIDsOf(t, e, id)
				if len(items) < 2 {
					t.Fatalf("setup: Ada has %d books", len(items))
				}
				before := mustRow(t, e, id)
				w := watch(t, e)
				resp := patch(t, e, e.Token(t, "root"), id, map[string]any{"name": "Ada Renamed"})
				wantStatus(t, "rename", resp, http.StatusOK)
				got := decode[updateResponse](t, resp.Body)
				row := mustRow(t, e, id)
				if got.Updated == nil || !*got.Updated || got.Merged != nil || got.Author.Name != "Ada Renamed" || rowText(row, "name") != "Ada Renamed" || rowText(row, "lastFirst") != "Renamed, Ada" ||
					dbTime(t, row["updatedAt"]) <= dbTime(t, before["updatedAt"]) || got.Author.UpdatedAt != dbTime(t, row["updatedAt"]) {
					t.Fatalf("response %s, row %v", resp.Body, row)
				}
				// Every book's metadata.json lists the book's authors with the new name; a stale or
				// other name means the handler did not rewrite the file from the current book.
				for _, item := range items {
					names := metadataFileAuthors(t, e, item)
					if want := bookAuthorNames(t, e, item); !slices.Equal(sortedIDs(names...), want) || !slices.Contains(names, "Ada Renamed") {
						t.Fatalf("metadata.json of %s lists %v, want the book's authors %v", item, names, want)
					}
				}
				if event := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), len(items)); event.Name != "Ada Renamed" {
					t.Fatalf("event = %+v", event)
				}
				if ids := itemsUpdatedIDs(t, w.waitBoth(t, "items_updated", nil)); !slices.Equal(ids, items) {
					t.Fatalf("items_updated ids %v, want %v", ids, items)
				}
				if read := decode[authorJSON](t, e.Get(t, e.Token(t, "listener"), authorURL(id, "")).Body); read.Name != "Ada Renamed" {
					t.Fatalf("a later read must show the new name: %+v", read)
				}
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-MERGE", Case: "normal", State: "changed", Assertion: "update-rename-merges-into-existing-author",
			Sources: []string{"server/controllers/AuthorController.js:116-175", "server/models/Author.js:70-95"},
			Claim:   "Renaming an author to the exact name of another author in the library moves its books to that author, deletes the old author, answers {author: existing, merged:true}, and sends author_removed (full old author), author_updated (existing author with numBooks) and items_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				zed, ada := authorID(t, e, "Zed Disposable"), authorID(t, e, "Ada Renamed")
				zedBefore := decode[authorJSON](t, e.Get(t, e.Token(t, "root"), authorURL(zed, "")).Body)
				moved := itemIDsOf(t, e, zed)
				adaBefore := bookCount(t, e, ada)
				w := watch(t, e)
				resp := patch(t, e, e.Token(t, "root"), zed, map[string]any{"name": "Ada Renamed"})
				wantStatus(t, "merge", resp, http.StatusOK)
				got := decode[updateResponse](t, resp.Body)
				if got.Merged == nil || !*got.Merged || got.Updated != nil || got.Author.ID != ada || got.Author.Name != "Ada Renamed" {
					t.Fatalf("response = %s", resp.Body)
				}
				if authorRow(t, e, zed) != nil || bookCount(t, e, zed) != 0 || len(moved) != 1 || bookCount(t, e, ada) != adaBefore+1 || !slices.Equal(bookAuthorNames(t, e, moved[0]), []string{"Ada Renamed"}) {
					t.Fatalf("zed row %v, books %d, ada %d -> %d", authorRow(t, e, zed), bookCount(t, e, zed), adaBefore, bookCount(t, e, ada))
				}
				if names := metadataFileAuthors(t, e, moved[0]); !slices.Equal(names, []string{"Ada Renamed"}) {
					t.Fatalf("metadata.json of the moved book lists %v", names)
				}
				removed := authorEvent(t, w.waitBoth(t, "author_removed", idIs(zed)), -1)
				if !reflect.DeepEqual(removed, zedBefore) {
					t.Fatalf("author_removed must carry the full old author: %+v vs %+v", removed, zedBefore)
				}
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(ada)), adaBefore+1)
				if ids := itemsUpdatedIDs(t, w.waitBoth(t, "items_updated", nil)); !slices.Equal(ids, moved) {
					t.Fatalf("items_updated ids %v, want %v", ids, moved)
				}
				wantStatus(t, "old author gone", e.Get(t, e.Token(t, "root"), authorURL(zed, "")), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-MERGE", Case: "edge", State: "repeat", Assertion: "update-merge-shared-book-and-bookless-author",
			Sources: []string{"server/controllers/AuthorController.js:131-175"},
			Claim:   "Merging an author whose book already has the target author leaves one link (counts do not grow); merging an author without books sends author_removed and author_updated but no items_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				ada := authorID(t, e, "Ada Renamed")
				wantStatus(t, "setup shared book", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Renamed", "Dup Author"), http.StatusOK)
				dup := authorID(t, e, "Dup Author")
				adaBefore := bookCount(t, e, ada)
				w := watch(t, e)
				resp := patch(t, e, root, dup, map[string]any{"name": "Ada Renamed"})
				wantStatus(t, "merge shared", resp, http.StatusOK)
				if got := decode[updateResponse](t, resp.Body); got.Merged == nil || !*got.Merged || authorRow(t, e, dup) != nil || bookCount(t, e, ada) != adaBefore ||
					!slices.Equal(bookAuthorNames(t, e, e.IDs["ebook"]), []string{"Ada Renamed"}) {
					t.Fatalf("shared merge: %s, ada %d -> %d, authors %v", resp.Body, adaBefore, bookCount(t, e, ada), bookAuthorNames(t, e, e.IDs["ebook"]))
				}
				authorEvent(t, w.waitBoth(t, "author_removed", idIs(dup)), -1)
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(ada)), adaBefore)

				keeper, terry := authorID(t, e, "Keeper Author"), authorID(t, e, "Terry Fixture")
				terryBooks := bookCount(t, e, terry)
				w = watch(t, e)
				wantStatus(t, "merge bookless", patch(t, e, root, keeper, map[string]any{"name": "Terry Fixture"}), http.StatusOK)
				authorEvent(t, w.waitBoth(t, "author_removed", idIs(keeper)), -1)
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(terry)), terryBooks)
				w.none(t, "items_updated", nil)
				if authorRow(t, e, keeper) != nil || bookCount(t, e, terry) != terryBooks {
					t.Fatal("keeper must be gone and Terry's books unchanged")
				}
			},
		},
	}
}
