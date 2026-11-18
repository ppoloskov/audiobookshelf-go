package author

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// uploadImage posts {url} to the author's image route.
func uploadImage(t *testing.T, e *harness.Env, token, id, url string) harness.Response {
	t.Helper()
	return e.Post(t, token, authorURL(id, "/image"), map[string]any{"url": url})
}

// userCache remembers users created by scenarios, keyed by install and name, so that a user is
// logged in once (repeated logins run into the login rate limiter).
var userCache sync.Map

// ensureUser returns the user, creating and logging it in on first use.
func ensureUser(t *testing.T, e *harness.Env, name string, permissions map[string]any) user {
	t.Helper()
	key := e.Install + "/" + name
	if cached, ok := userCache.Load(key); ok {
		return cached.(user)
	}
	created := createUser(t, e, name, permissions)
	userCache.Store(key, created)
	return created
}

// noUpload is a user who may update but not upload.
func noUpload(t *testing.T, e *harness.Env) user {
	return ensureUser(t, e, "author-no-upload", map[string]any{"update": true, "upload": false, "delete": false})
}

// restrictedWriter is a user who holds update, upload and delete permission but cannot access the
// audiobook library, so only the library rule can reject it.
func restrictedWriter(t *testing.T, e *harness.Env) user {
	return ensureUser(t, e, "author-podcast-writer", map[string]any{
		"update": true, "upload": true, "delete": true,
		"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]},
	})
}

// permissionUser is a user holding exactly the listed permissions (update, upload, delete).
func permissionUser(t *testing.T, e *harness.Env, name string, granted ...string) user {
	permissions := map[string]any{"update": false, "upload": false, "delete": false}
	for _, permission := range granted {
		permissions[permission] = true
	}
	return ensureUser(t, e, name, permissions)
}

// imageUnchanged asserts the author still has no image, no image file and no author_updated event.
func imageUnchanged(t *testing.T, e *harness.Env, w *watchers, id string) {
	t.Helper()
	if row := mustRow(t, e, id); row["imagePath"] != nil {
		t.Fatalf("imagePath must stay NULL, got %v", row["imagePath"])
	}
	for _, ext := range []string{"jpg", "png"} {
		if fileExists(authorImagePath(e, id, ext)) {
			t.Fatalf("no image file may exist, found .%s", ext)
		}
	}
	w.none(t, "author_updated", idIs(id))
}

func imageUploadScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-AUTH", Case: "error", State: "failure", Assertion: "image-upload-denied-changes-nothing",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/AuthorController.js:267-271", "server/controllers/AuthorController.js:427-442"},
			Claim:   "POST /api/authors/:id/image is 401 without a token, 404 for an unknown author (also for users who may not update), 403 for a user without update permission and 403 for a user with update but without upload permission, and 404 for a user with every permission but no access to the library; nothing is downloaded or changed.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				stub := imageStub(t, map[string]stubResponse{"/a.jpg": {http.StatusOK, "image/jpeg", imageBytes(t, "jpeg", 30, 20)}})
				w := watch(t, e)
				url := stub.URL + "/a.jpg"
				wantStatus(t, "no token", uploadImage(t, e, "", id, url), http.StatusUnauthorized)
				wantStatus(t, "listener unknown author", uploadImage(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", url), http.StatusNotFound)
				wantStatus(t, "listener", uploadImage(t, e, e.Token(t, "listener"), id, url), http.StatusForbidden)
				wantStatus(t, "update without upload", uploadImage(t, e, noUpload(t, e).token, id, url), http.StatusForbidden)
				wantStatus(t, "restricted library with every permission", uploadImage(t, e, restrictedWriter(t, e).token, id, url), http.StatusNotFound)
				wantStatus(t, "update without upload and bad body", e.Post(t, noUpload(t, e).token, authorURL(id, "/image"), map[string]any{}), http.StatusForbidden)
				imageUnchanged(t, e, w, id)
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-INPUT", Case: "error", State: "initial", Assertion: "image-upload-invalid-url-rejected",
			Sources: []string{"server/controllers/AuthorController.js:272-288", "server/finders/AuthorFinder.js:43-65", "server/utils/fileUtils.js:298-357"},
			Claim:   "POST /api/authors/:id/image without a url, or with a url that is empty, not a string or does not start with http: or https:, is rejected with 400 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				w := watch(t, e)
				for _, body := range []any{
					map[string]any{}, map[string]any{"url": ""}, map[string]any{"url": 5}, map[string]any{"url": []string{"http://x"}},
					map[string]any{"url": "ftp://127.0.0.1/a.png"}, map[string]any{"url": "/etc/hostname"}, map[string]any{"url": "file:///etc/hostname"},
				} {
					wantStatus(t, "url body", e.Post(t, e.Token(t, "root"), authorURL(id, "/image"), body), http.StatusBadRequest)
				}
				imageUnchanged(t, e, w, id)
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-INPUT", Case: "error", State: "failure", Assertion: "image-upload-download-failure-rejected",
			Sources: []string{"server/controllers/AuthorController.js:272-288", "server/finders/AuthorFinder.js:43-65", "server/utils/fileUtils.js:298-357"},
			Claim:   "When the download fails (HTTP error, non-image or SVG content type, unreachable host) POST /api/authors/:id/image answers 400 and leaves the author without image and without file.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				stub := imageStub(t, map[string]stubResponse{
					"/page.png": {http.StatusOK, "text/html", []byte("<html></html>")},
					"/art.png":  {http.StatusOK, "image/svg+xml", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
					"/none.png": {http.StatusOK, "", imageBytes(t, "png", 10, 10)},
					"/gone.png": {http.StatusNotFound, "image/png", nil},
					"/boom.png": {http.StatusInternalServerError, "image/png", nil},
				})
				w := watch(t, e)
				for _, path := range []string{"/page.png", "/art.png", "/none.png", "/gone.png", "/boom.png", "/missing.png"} {
					wantStatus(t, "download "+path, uploadImage(t, e, e.Token(t, "root"), id, stub.URL+path), http.StatusBadRequest)
				}
				closed := imageStub(t, nil)
				url := closed.URL + "/a.png"
				closed.Close()
				wantStatus(t, "unreachable host", uploadImage(t, e, e.Token(t, "root"), id, url), http.StatusBadRequest)
				imageUnchanged(t, e, w, id)
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-CONTRACT", Case: "normal", State: "initial", Assertion: "image-upload-jpg-stored",
			Sources: []string{"server/controllers/AuthorController.js:282-304", "server/finders/AuthorFinder.js:43-65", "server/models/Author.js:177-198", "server/managers/CacheManager.js:97-112"},
			Claim:   "POST /api/authors/:id/image downloads the image to metadata/authors/<id>.jpg, stores that path in imagePath, answers {author} with it and emits author_updated with numBooks to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				original := imageBytes(t, "jpeg", 300, 200)
				stub := imageStub(t, map[string]stubResponse{"/portrait.jpg": {http.StatusOK, "image/jpeg", original}})
				before := mustRow(t, e, id)
				w := watch(t, e)
				resp := uploadImage(t, e, e.Token(t, "root"), id, stub.URL+"/portrait.jpg")
				wantStatus(t, "upload", resp, http.StatusOK)
				path := authorImagePath(e, id, "jpg")
				got := decode[struct{ Author authorJSON }](t, resp.Body).Author
				if got.ID != id || got.ImagePath == nil || *got.ImagePath != path || got.UpdatedAt <= dbTime(t, before["updatedAt"]) {
					t.Fatalf("response author = %s, want imagePath %s and a newer updatedAt", resp.Body, path)
				}
				if !bytes.Equal(readFile(t, path), original) {
					t.Fatal("stored file must be the downloaded bytes")
				}
				if row := mustRow(t, e, id); rowText(row, "imagePath") != path || dbTime(t, row["updatedAt"]) != got.UpdatedAt {
					t.Fatalf("row = %v", row)
				}
				event := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), 2)
				if event.ImagePath == nil || *event.ImagePath != path || event.Name != "Terry Fixture" || event.UpdatedAt != got.UpdatedAt {
					t.Fatalf("event = %+v", event)
				}
				read := decode[authorJSON](t, e.Get(t, e.Token(t, "listener"), authorURL(id, "")).Body)
				if read.ImagePath == nil || *read.ImagePath != path {
					t.Fatalf("a later read must show the image: %+v", read)
				}
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-STORE", Case: "edge", State: "changed", Assertion: "image-upload-extension-rule",
			Sources: []string{"server/controllers/AuthorController.js:282-304", "server/finders/AuthorFinder.js:43-65", "server/models/Author.js:177-198", "server/managers/CacheManager.js:97-112"},
			Claim:   "The stored file extension follows the url: a url ending in .png is stored as <id>.png (imagePath changes), any other url ends as <id>.jpg whatever the content type.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				pngBytes := imageBytes(t, "png", 120, 80)
				stub := imageStub(t, map[string]stubResponse{
					"/face.png": {http.StatusOK, "image/png", pngBytes},
					"/photo":    {http.StatusOK, "image/png", pngBytes},
				})
				w := watch(t, e)
				wantStatus(t, "png url", uploadImage(t, e, e.Token(t, "root"), id, stub.URL+"/face.png"), http.StatusOK)
				pngPath := authorImagePath(e, id, "png")
				if rowText(mustRow(t, e, id), "imagePath") != pngPath || !bytes.Equal(readFile(t, pngPath), pngBytes) {
					t.Fatal("a .png url must be stored as <id>.png")
				}
				w.waitBoth(t, "author_updated", func(args []json.RawMessage) bool {
					return first(args)["id"] == id && first(args)["imagePath"] == pngPath
				})
				wantStatus(t, "url without extension", uploadImage(t, e, e.Token(t, "root"), id, stub.URL+"/photo"), http.StatusOK)
				jpgPath := authorImagePath(e, id, "jpg")
				if rowText(mustRow(t, e, id), "imagePath") != jpgPath || !bytes.Equal(readFile(t, jpgPath), pngBytes) {
					t.Fatal("a url without .png must be stored as <id>.jpg, with the downloaded bytes as they are")
				}
			},
		},
		{
			Requirement: "route-r136", Invariant: "INV-ROUTE-R136-STORE", Case: "normal", State: "repeat", Assertion: "image-upload-repeat-busts-cache",
			Sources: []string{"server/controllers/AuthorController.js:282-304", "server/finders/AuthorFinder.js:43-65", "server/models/Author.js:177-198", "server/managers/CacheManager.js:97-112"},
			Claim:   "Uploading again to the same path keeps imagePath, purges the resized cache of the author and raises updatedAt even though no column value changed.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				one := imageBytes(t, "jpeg", 100, 100)
				two := imageBytes(t, "jpeg", 200, 100)
				stub := imageStub(t, map[string]stubResponse{"/one.jpg": {http.StatusOK, "image/jpeg", one}, "/two.jpg": {http.StatusOK, "image/jpeg", two}})
				root := e.Token(t, "root")
				wantStatus(t, "first upload", uploadImage(t, e, root, id, stub.URL+"/one.jpg"), http.StatusOK)
				wantStatus(t, "warm cache", e.Get(t, root, authorURL(id, "/image?width=50&format=png")), http.StatusOK)
				if len(cacheFiles(t, e, id)) == 0 {
					t.Fatal("the resized image must be cached")
				}
				before := mustRow(t, e, id)
				resp := uploadImage(t, e, root, id, stub.URL+"/two.jpg")
				wantStatus(t, "second upload", resp, http.StatusOK)
				after := mustRow(t, e, id)
				if rowText(after, "imagePath") != rowText(before, "imagePath") || dbTime(t, after["updatedAt"]) <= dbTime(t, before["updatedAt"]) {
					t.Fatalf("imagePath must stay and updatedAt must rise: %v -> %v", before, after)
				}
				if leftover := cacheFiles(t, e, id); len(leftover) != 0 {
					t.Fatalf("the author cache must be purged, found %v", leftover)
				}
				if !bytes.Equal(readFile(t, authorImagePath(e, id, "jpg")), two) {
					t.Fatal("the file must be replaced by the new download")
				}
			},
		},
	}
}
