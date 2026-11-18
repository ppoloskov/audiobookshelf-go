package author

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func deleteScenarios() []harness.Scenario {
	const missingID = "00000000-0000-4000-8000-000000000000"
	return []harness.Scenario{
		{
			Requirement: "route-r133", Invariant: "INV-ROUTE-R133-AUTH", Case: "error", State: "failure", Assertion: "delete-denied-changes-nothing",
			Sources: []string{"server/controllers/AuthorController.js:427-442", "server/Auth.js:322"},
			Claim:   "DELETE /api/authors/:id is 401 without a token, 404 for an unknown author or an inaccessible library, and 403 for users without delete permission (update permission is not enough); the author, its book links and events stay unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				before, books := mustRow(t, e, id), bookCount(t, e, id)
				restricted := ensureUser(t, e, "author-podcast-only", map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
				w := watch(t, e)
				wantStatus(t, "no token", e.Delete(t, "", authorURL(id, "")), http.StatusUnauthorized)
				wantStatus(t, "unknown", e.Delete(t, e.Token(t, "listener"), authorURL(missingID, "")), http.StatusNotFound)
				wantStatus(t, "restricted library", e.Delete(t, restricted.token, authorURL(id, "")), http.StatusNotFound)
				wantStatus(t, "listener", e.Delete(t, e.Token(t, "listener"), authorURL(id, "")), http.StatusForbidden)
				wantStatus(t, "update only", e.Delete(t, noUpload(t, e).token, authorURL(id, "")), http.StatusForbidden)
				assertRowEqual(t, before, mustRow(t, e, id))
				if bookCount(t, e, id) != books {
					t.Fatal("book links must stay")
				}
				w.none(t, "author_removed", nil)
			},
		},
		{
			Requirement: "route-r133", Invariant: "INV-ROUTE-R133-CONTRACT", Case: "normal", State: "initial", Assertion: "delete-author-unlinks-books",
			Sources: []string{"server/controllers/AuthorController.js:233-258"},
			Claim:   "DELETE /api/authors/:id answers 200, deletes the author and its book links but keeps the books, rewrites each book's metadata.json to its remaining authors, and sends author_removed with the full author JSON to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				full := decode[authorJSON](t, e.Get(t, e.Token(t, "root"), authorURL(id, "")).Body)
				items := itemIDsOf(t, e, id)
				itemCount := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				w := watch(t, e)
				wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), authorURL(id, "")), http.StatusOK)
				if authorRow(t, e, id) != nil || bookCount(t, e, id) != 0 || e.Count(t, "SELECT COUNT(*) FROM libraryItems") != itemCount || len(items) != 2 {
					t.Fatalf("author row %v, links %d, items %d/%d", authorRow(t, e, id), bookCount(t, e, id), e.Count(t, "SELECT COUNT(*) FROM libraryItems"), itemCount)
				}
				// Each book's metadata.json keeps exactly its remaining authors.
				for _, item := range items {
					if names := metadataFileAuthors(t, e, item); !slices.Equal(sortedIDs(names...), bookAuthorNames(t, e, item)) || slices.Contains(names, "Scan Author") {
						t.Fatalf("metadata.json of %s lists %v, want the book's other authors", item, names)
					}
				}
				if got := authorEvent(t, w.waitBoth(t, "author_removed", idIs(id)), -1); !reflect.DeepEqual(got, full) {
					t.Fatalf("event %+v, want the author as it was: %+v", got, full)
				}
				wantStatus(t, "read after delete", e.Get(t, e.Token(t, "root"), authorURL(id, "")), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r133", Invariant: "INV-ROUTE-R133-DELETE", Case: "edge", State: "changed", Assertion: "delete-author-with-image-purges-cache",
			Sources: []string{"server/controllers/AuthorController.js:233-258", "server/managers/CacheManager.js:100-112"},
			Claim:   "Deleting an author who has an image purges its resized cache and leaves the stored image file on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				root := e.Token(t, "root")
				stub := imageStub(t, map[string]stubResponse{"/terry.png": {http.StatusOK, "image/png", imageBytes(t, "png", 60, 40)}})
				wantStatus(t, "upload", uploadImage(t, e, root, id, stub.URL+"/terry.png"), http.StatusOK)
				path := rowText(mustRow(t, e, id), "imagePath")
				wantStatus(t, "warm cache", e.Get(t, root, authorURL(id, "/image?width=33&format=png")), http.StatusOK)
				if len(cacheFiles(t, e, id)) == 0 {
					t.Fatal("setup: cache must exist")
				}
				w := watch(t, e)
				wantStatus(t, "delete", e.Delete(t, root, authorURL(id, "")), http.StatusOK)
				if authorRow(t, e, id) != nil || len(cacheFiles(t, e, id)) != 0 || !fileExists(path) {
					t.Fatalf("row %v, cache %v, image file kept %v", authorRow(t, e, id), cacheFiles(t, e, id), fileExists(path))
				}
				got := authorEvent(t, w.waitBoth(t, "author_removed", idIs(id)), -1)
				if got.ImagePath == nil || *got.ImagePath != path {
					t.Fatalf("the event carries the old imagePath: %+v", got)
				}
				wantStatus(t, "image gone", e.Get(t, root, authorURL(id, "/image")), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r133", Invariant: "INV-ROUTE-R133-DELETE", Case: "error", State: "repeat", Assertion: "delete-author-twice",
			Sources: []string{"server/controllers/AuthorController.js:428-429"},
			Claim:   "Deleting an author that was already deleted answers 404 and sends no event.",
			Run: func(t *testing.T, e *harness.Env) {
				const terry = "75e57a0b-a39d-4073-8501-e3b745ebd79d" // fixture id of the author deleted by the previous scenario
				if authorRow(t, e, terry) != nil {
					t.Fatal("setup: Terry Fixture must already be deleted")
				}
				w := watch(t, e)
				wantStatus(t, "repeat", e.Delete(t, e.Token(t, "root"), authorURL(terry, "")), http.StatusNotFound)
				wantStatus(t, "repeat as listener", e.Delete(t, e.Token(t, "listener"), authorURL(terry, "")), http.StatusNotFound)
				w.none(t, "author_removed", nil)
			},
		},
	}
}
