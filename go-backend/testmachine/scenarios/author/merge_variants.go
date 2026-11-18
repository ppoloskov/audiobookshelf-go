package author

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// makeSecondLibrary creates a book library with one book of the author and waits until it is scanned.
func makeSecondLibrary(t *testing.T, e *harness.Env, authorName string) string {
	t.Helper()
	folder := filepath.Join(e.Media, "second")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	stageBookIn(t, e, folder, authorName, "Cross Book")
	w := watch(t, e)
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries", map[string]any{"name": "Second Books", "mediaType": "book", "folders": []map[string]string{{"fullPath": folder}}})
	wantStatus(t, "create library", resp, http.StatusOK)
	library, _ := resp.Object(t)["id"].(string)
	if library == "" {
		t.Fatalf("library response has no id: %s", resp.Body)
	}
	wantStatus(t, "scan", e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK)
	w.waitBoth(t, "author_added", nameIs(authorName))
	return authorID(t, e, authorName)
}

func mergeVariantScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-MERGE", Case: "edge", State: "changed", Assertion: "update-case-different-name-is-plain-rename",
			Sources: []string{"server/controllers/AuthorController.js:116-128,177-218"},
			Claim:   "Renaming an author to a name that differs from another author's only in letter case does not merge: both authors stay, the author is renamed and author_updated is sent without author_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				ada := authorID(t, e, "Ada Renamed")
				wantStatus(t, "setup", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Renamed", "Casey Writer"), http.StatusOK)
				id := authorID(t, e, "Casey Writer")
				adaRow := mustRow(t, e, ada)
				w := watch(t, e)
				resp := patch(t, e, root, id, map[string]any{"name": "ada renamed"})
				wantStatus(t, "rename", resp, http.StatusOK)
				got := decode[updateResponse](t, resp.Body)
				if got.Updated == nil || !*got.Updated || got.Merged != nil || got.Author.ID != id || got.Author.Name != "ada renamed" {
					t.Fatalf("response %s", resp.Body)
				}
				if rowText(mustRow(t, e, id), "name") != "ada renamed" {
					t.Fatalf("row %v", mustRow(t, e, id))
				}
				assertRowEqual(t, adaRow, mustRow(t, e, ada))
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 1)
				w.none(t, "author_removed", nil)
				wantStatus(t, "cleanup", e.Delete(t, root, authorURL(id, "")), http.StatusOK)
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-MERGE", Case: "edge", State: "changed", Assertion: "update-same-name-in-other-library-is-plain-rename",
			Sources: []string{"server/controllers/AuthorController.js:116-128"},
			Claim:   "Renaming an author to the exact name of an author in another library does not merge: both authors stay with the same name in their own libraries and author_updated is sent without author_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				other := makeSecondLibrary(t, e, "Only In Library Two")
				otherRow := mustRow(t, e, other)
				wantStatus(t, "setup", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Renamed", "Casey Writer"), http.StatusOK)
				id := authorID(t, e, "Casey Writer")
				w := watch(t, e)
				resp := patch(t, e, root, id, map[string]any{"name": "Only In Library Two"})
				wantStatus(t, "rename", resp, http.StatusOK)
				got := decode[updateResponse](t, resp.Body)
				if got.Updated == nil || !*got.Updated || got.Merged != nil || got.Author.ID != id || got.Author.Name != "Only In Library Two" {
					t.Fatalf("response %s", resp.Body)
				}
				assertRowEqual(t, otherRow, mustRow(t, e, other))
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Only In Library Two'") != 2 || mustRow(t, e, id)["libraryId"] != e.IDs["library"] || mustRow(t, e, other)["libraryId"] == e.IDs["library"] {
					t.Fatal("two authors with that name must exist, one per library")
				}
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 1)
				w.none(t, "author_removed", nil)
				wantStatus(t, "cleanup", e.Delete(t, root, authorURL(id, "")), http.StatusOK)
			},
		},
	}
}
