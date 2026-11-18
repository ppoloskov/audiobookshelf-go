package item_cover

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// rawCoverScenarios covers GET /api/items/:id/cover?raw=1: the unchanged
// original cover bytes, public and conditionally cacheable.
func rawCoverScenarios() []harness.Scenario {
	const req, inv = "route-r054", "INV-R054-RAW-001"
	return []harness.Scenario{
		scenario(req, inv, "normal", "stateless", "cover-raw-serves-original-bytes",
			"The public raw cover of a book answers 200 with the image/jpeg stored file byte for byte, its size as Content-Length, Last-Modified and an ETag, and with ts the header Cache-Control: private, max-age=86400; repeating with If-None-Match answers 304 without a body.",
			srcs(getCoverSrc, "node_modules/send/index.js:862", "client/store/globals.js:96-108"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				file := readDisk(t, mediaCoverPath(t, e, id).(string))
				resp := get(t, e, coverURLPath(id)+"?raw=1&ts=1700000000000", nil)
				wantStatus(t, "raw cover", resp, http.StatusOK)
				requireEqual(t, "Content-Type", resp.Header.Get("Content-Type"), "image/jpeg")
				requireEqual(t, "Content-Length", resp.Header.Get("Content-Length"), itoa(len(file)))
				if string(resp.Body) != string(file) {
					t.Fatalf("raw cover body is %d bytes, want the stored %d bytes", len(resp.Body), len(file))
				}
				if resp.Header.Get("Last-Modified") == "" || resp.Header.Get("ETag") == "" {
					t.Fatalf("raw cover lacks Last-Modified/ETag: %v", resp.Header)
				}
				requireEqual(t, "ts Cache-Control", resp.Header.Get("Cache-Control"), "private, max-age=86400")
				again := get(t, e, coverURLPath(id)+"?raw=1", map[string]string{"If-None-Match": resp.Header.Get("ETag")})
				wantStatus(t, "conditional raw cover", again, http.StatusNotModified)
				if len(again.Body) != 0 {
					t.Fatalf("304 carries a body of %d bytes", len(again.Body))
				}
			}),
		scenario(req, inv, "error", "stateless", "cover-raw-without-file-is-404",
			"Raw cover requests answer 404 for an item with no stored cover, for an unknown item id, and for an item whose stored cover file no longer exists.",
			srcs(getCoverSrc, "server/models/LibraryItem.js:586-605"),
			func(t *testing.T, e *harness.Env) {
				wantStatus(t, "no coverPath", get(t, e, coverURLPath(e.IDs["saga2"])+"?raw=1", nil), http.StatusNotFound)
				wantStatus(t, "unknown item", get(t, e, coverURLPath("00000000-0000-4000-8000-000000000000")+"?raw=1", nil), http.StatusNotFound)
				// Give a cover-less item a real cover, then remove the file from disk.
				id := e.IDs["ebook"]
				resp := uploadFile(t, e, e.Token(t, "root"), id, "cover", "cover.png", pngImage(t, 12, 12))
				wantStatus(t, "seed cover", resp, http.StatusOK)
				stored := mediaCoverPath(t, e, id).(string)
				if err := removeFile(stored); err != nil {
					t.Fatalf("remove stored cover: %v", err)
				}
				wantStatus(t, "cover file gone", get(t, e, coverURLPath(id)+"?raw=1", nil), http.StatusNotFound)
				wantStatus(t, "resize of gone cover", get(t, e, coverURLPath(id)+"?width=60&format=jpeg", nil), http.StatusNotFound)
			}),
	}
}

// resizedCoverScenarios covers the non-raw cover route backed by CacheManager.
func resizedCoverScenarios() []harness.Scenario {
	const req, inv = "route-r054", "INV-R054-RESIZED-001"
	return []harness.Scenario{
		scenario(req, inv, "normal", "stateless", "cover-resized-is-jpeg-of-requested-width",
			"A cover request with width and format answers 200 image/jpeg of exactly the requested width and the proportional height of the stored cover, and a repeated request serves the same resized bytes.",
			srcs(getCoverSrc, "server/managers/CacheManager.js:43-91", "server/utils/ffmpegHelpers.js:78-100"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				resp := get(t, e, coverURLPath(id)+"?width=200&format=jpeg", nil)
				wantStatus(t, "resized cover", resp, http.StatusOK)
				requireEqual(t, "Content-Type", resp.Header.Get("Content-Type"), "image/jpeg")
				wantImageSize(t, "resized cover", resp.Body, 200, 200)
				first := append([]byte(nil), resp.Body...)
				again := get(t, e, coverURLPath(id)+"?width=200&format=jpeg", nil)
				wantStatus(t, "cached resized cover", again, http.StatusOK)
				if string(again.Body) != string(first) {
					t.Fatal("the repeated resized request served different bytes")
				}
			}),
		scenario(req, inv, "error", "stateless", "cover-resized-rejects-invalid-format",
			"An unsupported resize format answers 400 and does not create a cache file, while a cover request for a cover-less item answers 404.",
			srcs(getCoverSrc, "server/managers/CacheManager.js:55-59"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				before := cacheEntries(t, e, id)
				wantStatus(t, "gif format", get(t, e, coverURLPath(id)+"?width=100&format=gif", nil), http.StatusBadRequest)
				wantStatus(t, "unknown item", get(t, e, coverURLPath("00000000-0000-4000-8000-000000000000")+"?width=100&format=jpeg", nil), http.StatusNotFound)
				requireEqual(t, "cache entries after rejections", sortedCopy(cacheEntries(t, e, id)), sortedCopy(before))
			}),
		scenario(req, inv, "edge", "stateless", "cover-resized-clamps-width-and-defaults-to-webp",
			"A width above 4096 is clamped to 4096, a zero width falls back to the default 400, and a client whose Accept lists image/webp receives a WebP file by default.",
			srcs(getCoverSrc, "server/utils/index.js:62-65", "server/utils/index.js:144-147", "server/managers/CacheManager.js:43-49"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				wantStatus(t, "clamped width", get(t, e, coverURLPath(id)+"?width=5000&height=7&format=png", nil), http.StatusOK)
				png := get(t, e, coverURLPath(id)+"?width=5000&height=7&format=png", nil)
				wantImageSize(t, "clamped 5000x7", png.Body, 4096, 7)
				zero := get(t, e, coverURLPath(id)+"?width=0&format=jpeg", nil)
				wantStatus(t, "zero width", zero, http.StatusOK)
				wantImageSize(t, "zero width falls back to 400", zero.Body, 400, 400)
				webp := get(t, e, coverURLPath(id)+"?width=64", map[string]string{"Accept": "image/webp"})
				wantStatus(t, "webp default", webp, http.StatusOK)
				requireEqual(t, "WebP Content-Type", webp.Header.Get("Content-Type"), "image/webp")
				wantWebP(t, "default cover", webp.Body)
			}),
	}
}
