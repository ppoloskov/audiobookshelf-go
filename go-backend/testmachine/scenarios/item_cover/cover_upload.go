package item_cover

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// imageStub serves fixed bytes with a fixed content type.
func imageStub(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body) // a failed write fails the scenario through the download
	}))
	t.Cleanup(stub.Close)
	return stub
}

// uploadScenarios covers POST /api/items/:id/cover.
func uploadScenarios(s *setup) []harness.Scenario {
	const req = "route-r055"
	audience := func(t *testing.T, e *harness.Env) *observer {
		t.Helper()
		return observe(t, e, map[string]string{
			"root": e.Token(t, "root"), "listener": e.Token(t, "listener"),
			"podcastonly": s.token(t, e, "podcastonly"), "anonymous": "",
		})
	}
	return []harness.Scenario{
		scenario(req, "INV-R055-AUTH-001", "error", "initial", "upload-cover-auth-order",
			"POST cover answers 401 without a token, 404 for an unknown item even for a caller without permissions, 403 for a user without access to the item's library or without the update permission, and 403 for a user with update but without upload; every rejection changes nothing and emits no item_updated.",
			srcs(uploadCoverSrc, middlewareSrc, "server/Auth.js:322", "server/models/User.js:646-650", "client/components/modals/item/tabs/Cover.vue:150"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				a := audience(t, e)
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				dirBefore := dirNames(t, coverDir(e, id))
				url := imageStub(t, "image/png", pngImage(t, 10, 10)).URL + "/cover.png"
				wantStatus(t, "anonymous", uploadURL(t, e, "", id, url), http.StatusUnauthorized)
				unknown := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, "unknown item as root", uploadURL(t, e, e.Token(t, "root"), unknown, url), http.StatusNotFound)
				wantStatus(t, "unknown item before permission", uploadURL(t, e, s.token(t, e, "noupdate"), unknown, url), http.StatusNotFound)
				wantStatus(t, "no library access", uploadURL(t, e, s.token(t, e, "podcastonly"), id, url), http.StatusForbidden)
				wantStatus(t, "no update permission", uploadURL(t, e, s.token(t, e, "noupdate"), id, url), http.StatusForbidden)
				wantStatus(t, "no upload permission", uploadURL(t, e, s.token(t, e, "noupload"), id, url), http.StatusForbidden)
				wantStatus(t, "no file or url", e.Post(t, e.Token(t, "root"), coverURLPath(id), map[string]any{}), http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.none(t, "item_updated", idOf(id))
				if pathExists(coverDir(e, id)) {
					requireEqual(t, "cover directory after rejections", sortedCopy(dirNames(t, coverDir(e, id))), sortedCopy(dirBefore))
				}
			}),
		scenario(req, "INV-R055-URL-001", "normal", "initial", "upload-cover-from-url",
			"POST cover with a loopback image url stores the downloaded bytes as metadata/items/<id>/cover.<ext>, sets the media coverPath, answers {success:true,cover}, moves updatedAt and emits item_updated to users with access to the item only.",
			srcs(uploadCoverSrc, coverManagerSrc+":300-345", fileUtilsSrc+":353-356", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				image := jpegImage(t, 40, 30)
				stub := imageStub(t, "image/jpeg", image)
				a := audience(t, e)
				before := updatedAt(t, e, id)
				wantCoverResponse(t, uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/art.jpg"), filepath.Join(coverDir(e, id), "cover.jpg"))
				wantMediaCoverPath(t, e, id, filepath.Join(coverDir(e, id), "cover.jpg"))
				requireEqual(t, "stored cover bytes", string(readDisk(t, filepath.Join(coverDir(e, id), "cover.jpg"))), string(image))
				wantMoved(t, e, id, before)
				payload := a.itemUpdated(t, e, id, []string{"root", "listener"}, []string{"podcastonly", "anonymous"})
				requireEqual(t, "event coverPath", harness.Dig(payload, "media", "coverPath"), filepath.Join(coverDir(e, id), "cover.jpg"))
				// The cover survives the resize route as a real image.
				resized := get(t, e, coverURLPath(id)+"?width=20&format=jpeg", nil)
				wantStatus(t, "resized uploaded cover", resized, http.StatusOK)
				wantImageSize(t, "resized uploaded cover", resized.Body, 20, 15)
			}),
		scenario(req, "INV-R055-FILE-001", "normal", "changed", "upload-cover-file-replaces-older-cover",
			"POST cover with a multipart png replaces metadata/items/<id>/cover.jpg with cover.png, keeps an unrelated cover.svg, answers the new path, moves updatedAt, purges the resized cache and emits item_updated.",
			srcs(uploadCoverSrc, coverManagerSrc+":84-125", cacheManagerSrc+":93-110"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				dir := coverDir(e, id)
				svc := filepath.Join(dir, "cover.svg")
				if err := os.WriteFile(svc, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 0o600); err != nil {
					t.Fatalf("seed cover.svg: %v", err)
				}
				if !pathExists(filepath.Join(dir, "cover.jpg")) {
					t.Fatalf("fixture cover.jpg is gone before the upload")
				}
				// A resized cache entry must not survive the upload.
				wantStatus(t, "seed cache", get(t, e, coverURLPath(id)+"?width=120&format=jpeg", nil), http.StatusOK)
				if len(cacheEntries(t, e, id)) == 0 {
					t.Fatal("no cache entry before the upload")
				}
				a := audience(t, e)
				before := updatedAt(t, e, id)
				image := pngImage(t, 24, 18)
				wantCoverResponse(t, uploadFile(t, e, e.Token(t, "root"), id, "cover", "art.png", image), filepath.Join(dir, "cover.png"))
				wantMediaCoverPath(t, e, id, filepath.Join(dir, "cover.png"))
				requireEqual(t, "stored cover bytes", string(readDisk(t, filepath.Join(dir, "cover.png"))), string(image))
				requireEqual(t, "cover directory", sortedCopy(dirNames(t, dir)), []string{"cover.png", "cover.svg", "metadata.json"})
				requireEqual(t, "unrelated cover.svg", string(readDisk(t, svc)), "<svg xmlns='http://www.w3.org/2000/svg'/>")
				requireEqual(t, "cache purged by upload", cacheEntries(t, e, id), []string{})
				wantMoved(t, e, id, before)
				a.itemUpdated(t, e, id, []string{"root", "listener"}, []string{"podcastonly", "anonymous"})
			}),
		scenario(req, "INV-R055-FILE-001", "normal", "repeat", "upload-cover-repeat-keeps-working",
			"Uploading the same png again answers the same {success,cover}, keeps a single cover.png, moves updatedAt again and emits item_updated again.",
			srcs(uploadCoverSrc, coverManagerSrc+":84-125"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				a := audience(t, e)
				before := updatedAt(t, e, id)
				image := pngImage(t, 16, 16)
				wantCoverResponse(t, uploadFile(t, e, e.Token(t, "root"), id, "cover", "cover.png", image), filepath.Join(coverDir(e, id), "cover.png"))
				requireEqual(t, "stored cover bytes", string(readDisk(t, filepath.Join(coverDir(e, id), "cover.png"))), string(image))
				wantMoved(t, e, id, before)
				a.itemUpdated(t, e, id, []string{"root"}, []string{"anonymous"})
			}),
		scenario(req, "INV-R055-INVALID-001", "error", "failure", "upload-cover-rejects-non-image-and-unsupported-type",
			"POST cover answers 400 and changes no row, file or event when the url serves text/html, when it serves a non-image body labelled image/jpeg, and when the multipart file has an unsupported .gif extension; the download temp file is removed too.",
			srcs(uploadCoverSrc, coverManagerSrc+":63-80", fileDownloadSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				a := audience(t, e)
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				var dirBefore []string
				if pathExists(coverDir(e, id)) {
					dirBefore = dirNames(t, coverDir(e, id))
				}
				html := imageStub(t, "text/html", []byte("<html>not an image</html>"))
				wantStatus(t, "html url", uploadURL(t, e, e.Token(t, "root"), id, html.URL+"/page"), http.StatusBadRequest)
				garbage := imageStub(t, "image/jpeg", []byte("GIF89a not really a jpeg"))
				wantStatus(t, "mislabeled image", uploadURL(t, e, e.Token(t, "root"), id, garbage.URL+"/cover.jpg"), http.StatusBadRequest)
				wantStatus(t, "gif upload", uploadFile(t, e, e.Token(t, "root"), id, "cover", "cover.gif", []byte("GIF89a")), http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.none(t, "item_updated", idOf(id))
				if pathExists(coverDir(e, id)) {
					requireEqual(t, "cover directory after rejections", sortedCopy(dirNames(t, coverDir(e, id))), sortedCopy(dirBefore))
				}
			}),
		scenario(req, "INV-R055-EDGE-001", "edge", "initial", "upload-cover-requires-the-cover-field",
			"A multipart body without a url and without the cover field answers 400 and writes no file.",
			srcs(uploadCoverSrc, "server/Server.js:340-348"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				rows := snapshot(t, e)
				var dirBefore []string
				if pathExists(coverDir(e, id)) {
					dirBefore = dirNames(t, coverDir(e, id))
				}
				resp := uploadFile(t, e, e.Token(t, "root"), id, "image", "cover.png", pngImage(t, 8, 8))
				wantStatus(t, "wrong field name", resp, http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				if pathExists(coverDir(e, id)) {
					requireEqual(t, "cover directory", sortedCopy(dirNames(t, coverDir(e, id))), sortedCopy(dirBefore))
				}
			}),
	}
}
