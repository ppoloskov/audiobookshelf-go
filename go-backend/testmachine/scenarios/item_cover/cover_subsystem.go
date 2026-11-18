package item_cover

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// seedFile writes a file into a directory, creating the directory.
func seedFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// subsystemScenarios covers the CoverManager/CacheManager obligations of the
// cover subsystem that the four routes alone do not state.
func subsystemScenarios(s *setup) []harness.Scenario {
	const req = "sub-covers"
	return []harness.Scenario{
		scenario(req, "INV-COVER-UPLOAD-001", "normal", "initial", "cover-upload-cleanup-allowlist",
			"A multipart png upload stores cover.png, removes the older cover.jpeg of the same item, keeps a non-allowlisted cover.svg, purges every resized cache entry of the item, persists coverPath, moves updatedAt and emits item_updated.",
			srcs(uploadCoverSrc, coverManagerSrc+":47-125", cacheManagerSrc+":93-110"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				dir := coverDir(e, id)
				seedFile(t, dir, "cover.jpeg", jpegImage(t, 30, 30))
				svg := seedFile(t, dir, "cover.svg", []byte("<svg/>"))
				seedFile(t, coverCacheDir(e), id+"_33.jpeg", []byte("stale"))
				seedFile(t, coverCacheDir(e), "other-item_33.jpeg", []byte("unrelated"))
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				image := pngImage(t, 14, 14)
				wantCoverResponse(t, uploadFile(t, e, e.Token(t, "root"), id, "cover", "cover.png", image), filepath.Join(dir, "cover.png"))
				requireEqual(t, "cover directory", sortedCopy(dirNames(t, dir)), []string{"cover.png", "cover.svg", "metadata.json"})
				requireEqual(t, "cover.svg retained", string(readDisk(t, svg)), "<svg/>")
				requireEqual(t, "stored cover", string(readDisk(t, filepath.Join(dir, "cover.png"))), string(image))
				requireEqual(t, "item cache purged", cacheEntries(t, e, id), []string{})
				if !pathExists(filepath.Join(coverCacheDir(e), "other-item_33.jpeg")) {
					t.Fatal("the cache of another item was purged")
				}
				wantMediaCoverPath(t, e, id, filepath.Join(dir, "cover.png"))
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
			}),
		scenario(req, "INV-COVER-CACHE-001", "normal", "changed", "cover-cache-generates-hits-and-invalidates",
			"A resized request creates the cache file <id>_64.jpeg with a 64x64 image, a repeated request serves identical bytes, and uploading a different cover purges the entry so the next request renders the new cover.",
			srcs(cacheManagerSrc+":43-110", "server/utils/ffmpegHelpers.js:78-104"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				first := get(t, e, coverURLPath(id)+"?width=64&format=jpeg", nil)
				wantStatus(t, "first resize", first, http.StatusOK)
				wantImageSize(t, "first resize", first.Body, 64, 64)
				cachePath := filepath.Join(coverCacheDir(e), id+"_64.jpeg")
				requireEqual(t, "cache bytes", string(readDisk(t, cachePath)), string(first.Body))
				hit := get(t, e, coverURLPath(id)+"?width=64&format=jpeg", nil)
				wantStatus(t, "cache hit", hit, http.StatusOK)
				requireEqual(t, "cache hit bytes", string(hit.Body), string(first.Body))
				stub := imageStub(t, "image/jpeg", jpegImage(t, 50, 50))
				wantStatus(t, "replace cover", uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/new.jpg"), http.StatusOK)
				if pathExists(cachePath) {
					t.Fatalf("cache file %s survived the cover upload", cachePath)
				}
				second := get(t, e, coverURLPath(id)+"?width=64&format=jpeg", nil)
				wantStatus(t, "rebuilt cache", second, http.StatusOK)
				wantImageSize(t, "rebuilt cache", second.Body, 64, 64)
				if string(second.Body) == string(first.Body) {
					t.Fatal("the rebuilt cache still serves the previous cover")
				}
			}),
		scenario(req, "INV-COVER-RAW-001", "normal", "changed", "cover-raw-follows-the-current-cover",
			"Raw cover requests always serve the current stored file byte for byte: after a cover replacement, the raw route returns the new bytes with the new Content-Length and a different ETag.",
			srcs(getCoverSrc, "node_modules/send/index.js:862"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				old := get(t, e, coverURLPath(id)+"?raw=1", nil)
				wantStatus(t, "old raw", old, http.StatusOK)
				image := jpegImage(t, 33, 44)
				stub := imageStub(t, "image/jpeg", image)
				wantStatus(t, "replace", uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/new.jpg"), http.StatusOK)
				raw := get(t, e, coverURLPath(id)+"?raw=1", nil)
				wantStatus(t, "new raw", raw, http.StatusOK)
				requireEqual(t, "raw bytes", string(raw.Body), string(image))
				requireEqual(t, "raw Content-Length", raw.Header.Get("Content-Length"), itoa(len(image)))
				wantImageSize(t, "raw image", raw.Body, 33, 44)
				if raw.Header.Get("ETag") == old.Header.Get("ETag") {
					t.Fatalf("the ETag %q did not change with the cover", raw.Header.Get("ETag"))
				}
			}),
		scenario(req, "INV-COVER-DELETE-001", "normal", "changed", "cover-delete-purges-cache-and-reads-404",
			"Deleting the cover of an item with a stored cover clears coverPath, purges its resized cache, answers 200 and makes raw and resized reads answer 404 while the cover file stays on disk.",
			srcs(removeCoverSrc, cacheManagerSrc+":93-110"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				stored := mediaCoverPath(t, e, id).(string)
				wantStatus(t, "seed cache", get(t, e, coverURLPath(id)+"?width=48&format=jpeg", nil), http.StatusOK)
				requireEqual(t, "cache seeded", cacheEntries(t, e, id), []string{id + "_48.jpeg"})
				wantStatus(t, "delete", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				wantMediaCoverPath(t, e, id, nil)
				requireEqual(t, "cache purged", cacheEntries(t, e, id), []string{})
				wantStatus(t, "raw", get(t, e, coverURLPath(id)+"?raw=1", nil), http.StatusNotFound)
				wantStatus(t, "resized", get(t, e, coverURLPath(id)+"?width=48&format=jpeg", nil), http.StatusNotFound)
				if !pathExists(stored) {
					t.Fatalf("cover file %s was removed", stored)
				}
			}),
		scenario(req, "INV-COVER-URL-UPLOAD-001", "normal", "initial", "cover-url-ssrf-filter",
			"A cover url on a whitelisted loopback host stores the image, while a literal private IPv4 destination answers 400 and leaves the previous cover path, file and cache untouched.",
			srcs(uploadCoverSrc, coverManagerSrc+":300-345", fileUtilsSrc+":289-356", "server/Server.js:110-118"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				coverBefore := mediaCoverPath(t, e, id)
				rows := snapshot(t, e)
				wantStatus(t, "private address", uploadURL(t, e, e.Token(t, "root"), id, "http://10.0.0.7/cover.jpg"), http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				requireEqual(t, "coverPath unchanged", mediaCoverPath(t, e, id), coverBefore)
				o.none(t, "item_updated", idOf(id))
				image := pngImage(t, 21, 21)
				stub := imageStub(t, "image/png", image)
				wantCoverResponse(t, uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/cover.png"), filepath.Join(coverDir(e, id), "cover.png"))
				requireEqual(t, "downloaded bytes", string(readDisk(t, filepath.Join(coverDir(e, id), "cover.png"))), string(image))
				wantStatus(t, "previous cover removed", get(t, e, coverURLPath(id)+"?raw=1", nil), http.StatusOK)
			}),
		scenario(req, "INV-COVER-URL-UPLOAD-001", "edge", "changed", "cover-url-follows-store-with-item",
			"With the server setting storeCoverWithItem enabled, a cover url upload is downloaded into the item's own folder as cover.jpg and a multipart upload stores cover.png there too, instead of under metadata/items/<id>; the setting is restored afterwards.",
			srcs(coverManagerSrc+":17-22", coverManagerSrc+":300-320", "server/controllers/MiscController.js:139-175"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				t.Cleanup(func() { setStoreCoverWithItem(t, e, false) })
				setStoreCoverWithItem(t, e, true)
				image := jpegImage(t, 12, 14)
				stub := imageStub(t, "image/jpeg", image)
				want := filepath.Join(itemPath(t, e, id), "cover.jpg")
				wantCoverResponse(t, uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/item.jpg"), want)
				requireEqual(t, "coverPath", mediaCoverPath(t, e, id), want)
				requireEqual(t, "stored bytes", string(readDisk(t, want)), string(image))
				wantImageSize(t, "stored cover", readDisk(t, want), 12, 14)
				png := pngImage(t, 15, 15)
				stored := filepath.Join(itemPath(t, e, id), "cover.png")
				wantCoverResponse(t, uploadFile(t, e, e.Token(t, "root"), id, "cover", "upload.png", png), stored)
				requireEqual(t, "multipart stored bytes", string(readDisk(t, stored)), string(png))
				requireEqual(t, "old cover.jpg removed", pathExists(filepath.Join(itemPath(t, e, id), "cover.jpg")), false)
			}),
		scenario(req, "INV-COVER-PATCH-INVALID-IMAGE-001", "error", "failure", "cover-patch-invalid-image-text",
			"PATCH cover whose scanned library file is not an image answers 500 'Invalid image' and leaves the cover path, files and updatedAt unchanged.",
			srcs(updateCoverSrc, coverManagerSrc+":63-80", "client/components/modals/item/tabs/Cover.vue:428-432"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				note := seedFile(t, itemPath(t, e, id), "note.txt", []byte("not an image"))
				wantStatus(t, "rescan", e.Post(t, e.Token(t, "root"), "/api/items/"+id+"/scan", nil), http.StatusOK)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": note})
				wantStatus(t, "invalid image", resp, http.StatusInternalServerError)
				requireEqual(t, "error body", string(resp.Body), "Invalid image")
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-COVER-PROXY-REDIRECT-001", "error", "failure", "cover-url-307-not-followed-in-proxy-mode",
			"On a server started with EXP_PROXY_SUPPORT=1 a cover url answering 307 is not followed: the upload answers 400 and the previous cover path, file and cache stay unchanged.",
			srcs("server/Server.js:89-107", fileUtilsSrc+":289-356", uploadCoverSrc),
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: map[string]string{"EXP_PROXY_SUPPORT": "1"}})
				id := own.IDs["saga2"]
				image := jpegImage(t, 26, 26)
				target := imageStub(t, "image/jpeg", image)
				redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", target.URL+"/final.jpg")
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				t.Cleanup(redirect.Close)
				wantCoverResponse(t, uploadURL(t, own, own.Token(t, "root"), id, target.URL+"/first.jpg"), filepath.Join(coverDir(own, id), "cover.jpg"))
				o := observe(t, own, map[string]string{"root": own.Token(t, "root")})
				before := updatedAt(t, own, id)
				rows := snapshot(t, own)
				wantStatus(t, "307 redirect", uploadURL(t, own, own.Token(t, "root"), id, redirect.URL+"/cover"), http.StatusBadRequest)
				wantUnchanged(t, own, id, before)
				wantSnapshot(t, own, rows)
				requireEqual(t, "coverPath unchanged", mediaCoverPath(t, own, id), filepath.Join(coverDir(own, id), "cover.jpg"))
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-COVER-PATCH-SUCCESS-001", "edge", "repeat", "cover-patch-copies-and-repeats",
			"PATCH cover with a scanned image that is not the already-stored cover of the item copies it into metadata/items/<id>/cover.png, purges a stale resized cache, keeps an unrelated cover.svg and answers {success,cover}; repeating the same scanned path copies it again and emits item_updated again.",
			srcs(updateCoverSrc, coverManagerSrc+":132-207", cacheManagerSrc+":93-98"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				seedFile(t, itemPath(t, e, id), "art.png", pngImage(t, 25, 25))
				source := seedFile(t, itemPath(t, e, id), "second.png", pngImage(t, 25, 25))
				wantStatus(t, "rescan item", e.Post(t, e.Token(t, "root"), "/api/items/"+id+"/scan", nil), http.StatusOK)
				dir := coverDir(e, id)
				svg := seedFile(t, dir, "cover.svg", []byte("<svg/>"))
				seedFile(t, coverCacheDir(e), id+"_40.jpeg", []byte("stale"))
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				want := filepath.Join(dir, "cover.png")
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": source})
				wantStatus(t, "patch cover", resp, http.StatusOK)
				wantCoverResponse(t, resp, want)
				requireEqual(t, "copied bytes", string(readDisk(t, want)), string(readDisk(t, source)))
				wantImageSize(t, "copied cover", readDisk(t, want), 25, 25)
				requireEqual(t, "cover.svg retained", string(readDisk(t, svg)), "<svg/>")
				requireEqual(t, "cache purged", cacheEntries(t, e, id), []string{})
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
				o.reset()
				stable := updatedAt(t, e, id)
				wantStatus(t, "repeat patch", chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": source}), http.StatusOK)
				wantMoved(t, e, id, stable)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
			}),
		scenario(req, "INV-COVER-MULTIPART-URL-PRECEDENCE-001", "error", "initial", "cover-url-precedes-multipart-file",
			"When a multipart cover request carries both a url and a cover file, the url wins: an unsupported gif at that url answers 400 without falling back to the valid uploaded file, and the item is unchanged.",
			srcs(controllerSrc+":301-307", coverManagerSrc+":63-80"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				gif := imageStub(t, "image/gif", []byte("GIF89a not supported"))
				body, contentType := harness.Multipart(map[string]string{"url": gif.URL + "/anim.gif"},
					harness.File{Field: "cover", Name: "cover.png", Content: pngImage(t, 10, 10)})
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				coverBefore := mediaCoverPath(t, e, id)
				resp := e.Do(t, harness.Request{
					Method: http.MethodPost, Path: coverURLPath(id), Token: e.Token(t, "root"),
					Header: map[string]string{"Content-Type": contentType}, Body: body,
				})
				wantStatus(t, "url over file", resp, http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				requireEqual(t, "coverPath unchanged", mediaCoverPath(t, e, id), coverBefore)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-COVER-PATCH-BODY-LIMIT-001", "edge", "initial", "cover-patch-10mb-json-limit",
			"PATCH cover parses a 1.1 MB JSON body and answers the invalid-path 500, while a body above the 10 MB express.json limit answers 413; neither changes an item.",
			srcs("server/Server.js:352", updateCoverSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				rows := snapshot(t, e)
				wantStatus(t, "1.1 MB", chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": strings.Repeat("x", 1_100_000)}), http.StatusInternalServerError)
				wantStatus(t, "11 MB", chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": strings.Repeat("y", 11<<20)}), http.StatusRequestEntityTooLarge)
				wantSnapshot(t, e, rows)
			}),
	}
}
