package author

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func imageDeleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r137", Invariant: "INV-ROUTE-R137-AUTH", Case: "error", State: "failure", Assertion: "image-delete-denied-changes-nothing",
			Sources: []string{"server/controllers/AuthorController.js:427-442", "server/Auth.js:322"},
			Claim:   "DELETE /api/authors/:id/image is 401 without a token, 404 for an unknown author, 403 for a user without delete permission (update permission is not enough) and 404 for a user with every permission but no access to the library; image, file and events stay unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				path := rowText(before, "imagePath")
				if !fileExists(path) {
					t.Fatalf("setup: image file %s is missing", path)
				}
				w := watch(t, e)
				wantStatus(t, "no token", e.Delete(t, "", authorURL(id, "/image")), http.StatusUnauthorized)
				wantStatus(t, "unknown", e.Delete(t, e.Token(t, "listener"), authorURL("00000000-0000-4000-8000-000000000000", "/image")), http.StatusNotFound)
				wantStatus(t, "listener", e.Delete(t, e.Token(t, "listener"), authorURL(id, "/image")), http.StatusForbidden)
				wantStatus(t, "restricted library with every permission", e.Delete(t, restrictedWriter(t, e).token, authorURL(id, "/image")), http.StatusNotFound)
				wantStatus(t, "update only", e.Delete(t, noUpload(t, e).token, authorURL(id, "/image")), http.StatusForbidden)
				assertRowEqual(t, before, mustRow(t, e, id))
				if !fileExists(path) {
					t.Fatal("the image file must stay")
				}
				w.none(t, "author_updated", idIs(id))
			},
		},
		{
			Requirement: "route-r137", Invariant: "INV-ROUTE-R137-CONTRACT", Case: "normal", State: "initial", Assertion: "image-delete-removes-file-and-cache",
			Sources: []string{"server/controllers/AuthorController.js:313-329", "server/managers/CoverManager.js:34-44", "server/managers/CacheManager.js:100-112"},
			Claim:   "DELETE /api/authors/:id/image answers {author} with imagePath null, deletes the image file and the author's resized cache, clears the column and emits author_updated with numBooks.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				root := e.Token(t, "root")
				path := rowText(mustRow(t, e, id), "imagePath")
				wantStatus(t, "warm cache", e.Get(t, root, authorURL(id, "/image?width=60&format=png")), http.StatusOK)
				if len(cacheFiles(t, e, id)) == 0 {
					t.Fatal("setup: cache must exist")
				}
				before := mustRow(t, e, id)
				w := watch(t, e)
				resp := e.Delete(t, root, authorURL(id, "/image"))
				wantStatus(t, "delete image", resp, http.StatusOK)
				got := decode[struct{ Author authorJSON }](t, resp.Body).Author
				if got.ID != id || got.ImagePath != nil || got.Name != "Terry Fixture" {
					t.Fatalf("response = %s", resp.Body)
				}
				row := mustRow(t, e, id)
				if row["imagePath"] != nil || dbTime(t, row["updatedAt"]) <= dbTime(t, before["updatedAt"]) {
					t.Fatalf("row = %v", row)
				}
				if fileExists(path) || len(cacheFiles(t, e, id)) != 0 {
					t.Fatalf("file %v and cache %v must be gone", fileExists(path), cacheFiles(t, e, id))
				}
				event := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 2)
				if event.ImagePath != nil {
					t.Fatalf("event must carry imagePath null: %+v", event)
				}
				wantStatus(t, "image gone", e.Get(t, root, authorURL(id, "/image")), http.StatusNotFound)
				wantStatus(t, "raw image gone", e.Get(t, root, authorURL(id, "/image?raw=1")), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r137", Invariant: "INV-ROUTE-R137-REMOVE", Case: "error", State: "repeat", Assertion: "image-delete-without-image-is-400",
			Sources: []string{"server/controllers/AuthorController.js:314-317"},
			Claim:   "DELETE /api/authors/:id/image for an author without image (also repeated) answers 400 and emits nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				for range 2 {
					wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), authorURL(id, "/image")), http.StatusBadRequest)
				}
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", idIs(id))
			},
		},
		{
			Requirement: "route-r137", Invariant: "INV-ROUTE-R137-REMOVE", Case: "edge", State: "changed", Assertion: "image-delete-file-already-missing",
			Sources: []string{"server/controllers/AuthorController.js:318-322", "server/managers/CoverManager.js:34-44"},
			Claim:   "When the image file is already gone from disk DELETE still succeeds and clears imagePath.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e) // Ada: the file was removed by an earlier scenario, imagePath is set
				if row := mustRow(t, e, id); row["imagePath"] == nil || fileExists(rowText(row, "imagePath")) {
					t.Fatalf("setup: want imagePath with a missing file, got %v", row)
				}
				resp := e.Delete(t, e.Token(t, "root"), authorURL(id, "/image"))
				wantStatus(t, "delete", resp, http.StatusOK)
				if mustRow(t, e, id)["imagePath"] != nil {
					t.Fatal("imagePath must be cleared")
				}
			},
		},
	}
}
