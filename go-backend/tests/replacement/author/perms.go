package author

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// permAuthor creates (once) an author on the ebook item and returns its id.
func permAuthor(t *testing.T, e *harness.Env) string {
	t.Helper()
	if rows := e.Rows(t, "SELECT id FROM authors WHERE name = 'Perm Author'"); len(rows) == 1 {
		return rows[0]["id"].(string)
	}
	wantStatus(t, "create Perm Author", setItemAuthors(t, e, e.Token(t, "root"), e.IDs["ebook"], "Ada Example", "Perm Author"), http.StatusOK)
	return authorID(t, e, "Perm Author")
}

// permissionScenarios prove that non-admin users holding exactly the right permission succeed.
func permissionScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-AUTH", Case: "normal", State: "changed", Assertion: "update-by-update-only-user",
			Sources: []string{"server/controllers/AuthorController.js:435-438", "client/components/modals/authors/EditModal.vue"},
			Claim:   "A non-admin user holding only the update permission can PATCH an author (200, row changed, author_updated sent), while a user holding only delete permission gets 403 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				id := permAuthor(t, e)
				updater := permissionUser(t, e, "author-updater", "update")
				deleter := permissionUser(t, e, "author-deleter", "delete")
				before := mustRow(t, e, id)
				wantStatus(t, "delete-only user", patch(t, e, deleter.token, id, map[string]any{"description": "refused"}), http.StatusForbidden)
				assertRowEqual(t, before, mustRow(t, e, id))
				w := watch(t, e)
				resp := patch(t, e, updater.token, id, map[string]any{"description": "Written by an update-only user."})
				wantStatus(t, "update-only user", resp, http.StatusOK)
				if got := decode[updateResponse](t, resp.Body); got.Updated == nil || !*got.Updated || rowText(mustRow(t, e, id), "description") != "Written by an update-only user." {
					t.Fatalf("response %s", resp.Body)
				}
				authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 1)
			},
		},
		{
			Requirement: "route-r134", Invariant: "INV-ROUTE-R134-AUTH", Case: "edge", State: "repeat", Assertion: "match-by-update-only-user",
			Sources: []string{"server/controllers/AuthorController.js:435-438"},
			Claim:   "POST /api/authors/:id/match is not refused for a non-admin user holding only update permission (it reaches the lookup: 404 no match), while a user holding only delete permission gets 403.",
			Run: func(t *testing.T, e *harness.Env) {
				id := permAuthor(t, e)
				body := map[string]any{"q": "Zzqx Vwrk Fixture Nobody 7391824"}
				wantStatus(t, "update-only user", e.Post(t, permissionUser(t, e, "author-updater", "update").token, authorURL(id, "/match"), body), http.StatusNotFound)
				wantStatus(t, "delete-only user", e.Post(t, permissionUser(t, e, "author-deleter", "delete").token, authorURL(id, "/match"), body), http.StatusForbidden)
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-AUTH", Case: "normal", State: "changed", Assertion: "image-upload-by-update-and-upload-user",
			Sources: []string{"server/controllers/AuthorController.js:267-271,435-438"},
			Claim:   "A non-admin user holding update and upload permission can upload an author image (200, file and row set), while a user holding only upload or only delete permission gets 403.",
			Run: func(t *testing.T, e *harness.Env) {
				id := permAuthor(t, e)
				stub := imageStub(t, map[string]stubResponse{"/perm.jpg": {http.StatusOK, "image/jpeg", imageBytes(t, "jpeg", 40, 30)}})
				url := stub.URL + "/perm.jpg"
				wantStatus(t, "upload-only user", uploadImage(t, e, permissionUser(t, e, "author-uploader", "upload").token, id, url), http.StatusForbidden)
				wantStatus(t, "delete-only user", uploadImage(t, e, permissionUser(t, e, "author-deleter", "delete").token, id, url), http.StatusForbidden)
				if mustRow(t, e, id)["imagePath"] != nil {
					t.Fatal("refused uploads must not set an image")
				}
				resp := uploadImage(t, e, permissionUser(t, e, "author-updater-uploader", "update", "upload").token, id, url)
				wantStatus(t, "update+upload user", resp, http.StatusOK)
				if rowText(mustRow(t, e, id), "imagePath") != authorImagePath(e, id, "jpg") || !fileExists(authorImagePath(e, id, "jpg")) {
					t.Fatal("the image must be stored")
				}
			},
		},
		{
			Requirement: "route-r137", Invariant: "INV-ROUTE-R137-AUTH", Case: "normal", State: "changed", Assertion: "image-delete-by-delete-only-user",
			Sources: []string{"server/controllers/AuthorController.js:432-434"},
			Claim:   "A non-admin user holding only the delete permission can remove an author image (200, file and imagePath gone, author_updated sent); a user holding update and upload but not delete gets 403 first.",
			Run: func(t *testing.T, e *harness.Env) {
				id := permAuthor(t, e)
				path := authorImagePath(e, id, "jpg")
				if !fileExists(path) {
					t.Fatal("setup: the image file must exist")
				}
				wantStatus(t, "update+upload user", e.Delete(t, permissionUser(t, e, "author-updater-uploader", "update", "upload").token, authorURL(id, "/image")), http.StatusForbidden)
				if !fileExists(path) {
					t.Fatal("a refused delete must keep the file")
				}
				w := watch(t, e)
				wantStatus(t, "delete-only user", e.Delete(t, permissionUser(t, e, "author-deleter", "delete").token, authorURL(id, "/image")), http.StatusOK)
				if fileExists(path) || mustRow(t, e, id)["imagePath"] != nil {
					t.Fatal("file and imagePath must be gone")
				}
				w.waitBoth(t, "author_updated", idIs(id))
			},
		},
		{
			Requirement: "route-r133", Invariant: "INV-ROUTE-R133-AUTH", Case: "normal", State: "changed", Assertion: "delete-by-delete-only-user",
			Sources: []string{"server/controllers/AuthorController.js:432-434", "client/components/modals/authors/EditModal.vue"},
			Claim:   "A non-admin user holding only the delete permission can delete an author (200, row gone, author_removed sent), while an update-only user gets 403.",
			Run: func(t *testing.T, e *harness.Env) {
				id := permAuthor(t, e)
				wantStatus(t, "update-only user", e.Delete(t, permissionUser(t, e, "author-updater", "update").token, authorURL(id, "")), http.StatusForbidden)
				if authorRow(t, e, id) == nil {
					t.Fatal("a refused delete keeps the author")
				}
				w := watch(t, e)
				wantStatus(t, "delete-only user", e.Delete(t, permissionUser(t, e, "author-deleter", "delete").token, authorURL(id, "")), http.StatusOK)
				w.waitBoth(t, "author_removed", idIs(id))
				if authorRow(t, e, id) != nil {
					t.Fatal("the author must be gone")
				}
			},
		},
	}
}
