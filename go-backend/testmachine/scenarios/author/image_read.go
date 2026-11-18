package author

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// webpDims reads the canvas size from a lossy (VP8), lossless (VP8L) or extended (VP8X) WebP.
func webpDims(t *testing.T, body []byte) (width, height int) {
	t.Helper()
	if !isWebP(body) || len(body) < 30 {
		t.Fatalf("not a WebP file (%d bytes)", len(body))
	}
	switch string(body[12:16]) {
	case "VP8 ":
		return int(binary.LittleEndian.Uint16(body[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(body[28:30]) & 0x3fff)
	case "VP8L":
		bits := binary.LittleEndian.Uint32(body[21:25])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1
	case "VP8X":
		return int(body[24]) | int(body[25])<<8 | int(body[26])<<16 + 1, int(body[27]) | int(body[28])<<8 | int(body[29])<<16 + 1
	}
	t.Fatalf("unsupported WebP chunk %q", body[12:16])
	return 0, 0
}

// imageAuthor gives Ada an uploaded 300x200 JPEG once, and returns her id and the original bytes.
func imageAuthor(t *testing.T, e *harness.Env) (id string, original []byte) {
	t.Helper()
	id = authorID(t, e, "Ada Example")
	original = imageBytes(t, "jpeg", 300, 200)
	if row := mustRow(t, e, id); row["imagePath"] != nil {
		return id, readFile(t, authorImagePath(e, id, "jpg"))
	}
	stub := imageStub(t, map[string]stubResponse{"/ada.jpg": {http.StatusOK, "image/jpeg", original}})
	wantStatus(t, "setup upload", uploadImage(t, e, e.Token(t, "root"), id, stub.URL+"/ada.jpg"), http.StatusOK)
	return id, original
}

func imageReadScenarios() []harness.Scenario {
	const missingID = "00000000-0000-4000-8000-000000000000"
	return []harness.Scenario{
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-ERRORS", Case: "error", State: "failure", Assertion: "image-get-unavailable",
			Sources: []string{"server/controllers/AuthorController.js:391-419", "server/managers/CacheManager.js:155-186"},
			Claim:   "GET /api/authors/:id/image answers 404 (resized or raw) for an unknown author or an author without image; a resized request for a non-UUID id or a format other than webp, jpeg or png is 400.",
			Run: func(t *testing.T, e *harness.Env) {
				noImage := authorID(t, e, "Zed Disposable")
				root := e.Token(t, "root")
				for _, path := range []string{
					authorURL(noImage, "/image"), authorURL(noImage, "/image?raw=1"),
					authorURL(missingID, "/image"), authorURL(missingID, "/image?raw=1"), authorURL("no-such-author", "/image?raw=1"),
				} {
					wantStatus(t, path, e.Get(t, root, path), http.StatusNotFound)
				}
				for _, path := range []string{
					authorURL("no-such-author", "/image"), authorURL(noImage, "/image?format=gif"), authorURL(noImage, "/image?format=jpg"), authorURL(missingID, "/image?format=PNG"),
				} {
					wantStatus(t, path, e.Get(t, root, path), http.StatusBadRequest)
				}
				if files := cacheFiles(t, e, noImage); len(files) != 0 {
					t.Fatalf("failed requests must not create cache files: %v", files)
				}
			},
		},
	}
}

// sizeCase is one resized-image request and what it must produce.
type sizeCase struct {
	name, query, accept, contentType, cacheName, format string
	width, height                                       int
}

func imageSizeScenarios() []harness.Scenario {
	check := func(t *testing.T, e *harness.Env, id string, c sizeCase) {
		t.Helper()
		query, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		header := map[string]string{}
		if c.accept != "" {
			header["Accept"] = c.accept
		}
		resp := e.Do(t, harness.Request{Path: authorURL(id, "/image"), Token: e.Token(t, "root"), Query: query, Header: header})
		wantStatus(t, c.name, resp, http.StatusOK)
		if got := resp.Header.Get("Content-Type"); got != c.contentType {
			t.Fatalf("%s: Content-Type %q, want %q", c.name, got, c.contentType)
		}
		var width, height int
		if c.format == "webp" {
			width, height = webpDims(t, resp.Body)
		} else {
			width, height = imageDims(t, resp.Body, c.format)
		}
		if width != c.width || height < c.height-1 || height > c.height+1 {
			t.Fatalf("%s: image is %dx%d, want %dx%d (height +-1)", c.name, width, height, c.width, c.height)
		}
		cached := filepath.Join(imageCacheDir(e), id+"_"+c.cacheName)
		if !bytes.Equal(readFile(t, cached), resp.Body) {
			t.Fatalf("%s: the response must equal the cache file %s", c.name, cached)
		}
	}
	return []harness.Scenario{
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-CONTRACT", Case: "normal", State: "initial", Assertion: "image-get-default-jpeg-400",
			Sources: []string{"server/controllers/AuthorController.js:290-300", "server/controllers/AuthorController.js:413-418", "server/managers/CacheManager.js:97-112", "server/managers/CacheManager.js:155-189", "server/utils/index.js:62-65", "server/utils/index.js:144-147"},
			Claim:   "Without Accept, format, width or height GET returns a JPEG of width 400 (aspect kept), written to metadata/cache/images/<id>_400.jpeg.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				check(t, e, id, sizeCase{name: "default", query: "", contentType: "image/jpeg", cacheName: "400.jpeg", format: "jpeg", width: 400, height: 267})
				if files := cacheFiles(t, e, id); len(files) != 1 || files[0] != id+"_400.jpeg" {
					t.Fatalf("cache files = %v", files)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-RESIZE", Case: "edge", State: "initial", Assertion: "image-get-formats-and-sizes",
			Sources: []string{"server/controllers/AuthorController.js:290-300", "server/controllers/AuthorController.js:413-418", "server/managers/CacheManager.js:97-112", "server/managers/CacheManager.js:155-189", "server/utils/index.js:62-65", "server/utils/index.js:144-147"},
			Claim:   "format picks png, jpeg or webp (webp when Accept names image/webp or is */*), width and height resize (height-only keeps width 400), values above 4096 are clamped, and each result is cached as <id>_<width>[x<height>].<format>.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				for _, c := range []sizeCase{
					{name: "png width", query: "width=100&format=png", contentType: "image/png", cacheName: "100.png", format: "png", width: 100, height: 67},
					{name: "webp both", query: "width=64&height=32&format=webp", contentType: "image/webp", cacheName: "64x32.webp", format: "webp", width: 64, height: 32},
					{name: "height only", query: "height=50", contentType: "image/jpeg", cacheName: "400x50.jpeg", format: "jpeg", width: 400, height: 50},
					{name: "accept webp", query: "width=120", accept: "image/webp,image/*;q=0.8", contentType: "image/webp", cacheName: "120.webp", format: "webp", width: 120, height: 80},
					{name: "accept any", query: "width=130", accept: "*/*", contentType: "image/webp", cacheName: "130.webp", format: "webp", width: 130, height: 87},
					{name: "accept png only", query: "width=140", accept: "image/png", contentType: "image/jpeg", cacheName: "140.jpeg", format: "jpeg", width: 140, height: 93},
					{name: "explicit format wins", query: "width=150&format=png", accept: "image/webp", contentType: "image/png", cacheName: "150.png", format: "png", width: 150, height: 100},
					{name: "clamped", query: "width=99999&format=jpeg", contentType: "image/jpeg", cacheName: "4096.jpeg", format: "jpeg", width: 4096, height: 2731},
					{name: "invalid width is default", query: "width=-5&format=png", contentType: "image/png", cacheName: "400.png", format: "png", width: 400, height: 267},
				} {
					check(t, e, id, c)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-RESIZE", Case: "normal", State: "repeat", Assertion: "image-get-served-from-cache",
			Sources: []string{"server/controllers/AuthorController.js:290-300", "server/controllers/AuthorController.js:413-418", "server/managers/CacheManager.js:97-112", "server/managers/CacheManager.js:155-189", "server/utils/index.js:62-65", "server/utils/index.js:144-147"},
			Claim:   "A repeated request returns the same bytes from the existing cache file without rewriting it.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				path := authorURL(id, "/image?width=210&format=png")
				first := e.Get(t, e.Token(t, "listener"), path)
				wantStatus(t, "first", first, http.StatusOK)
				cached := filepath.Join(imageCacheDir(e), id+"_210.png")
				before, err := os.Stat(cached)
				if err != nil {
					t.Fatalf("cache file: %v", err)
				}
				second := e.Get(t, e.Token(t, "root"), path)
				wantStatus(t, "second", second, http.StatusOK)
				after, err := os.Stat(cached)
				if err != nil || !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(first.Body, second.Body) {
					t.Fatalf("cache must be reused unchanged: %v %v", err, after)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-ERRORS", Case: "error", State: "failure", Assertion: "image-get-invalid-format-creates-no-cache",
			Sources: []string{"server/controllers/AuthorController.js:391-419", "server/managers/CacheManager.js:155-186"},
			Claim:   "A resized request with an unsupported format is 400 for an author who has an image and creates no cache file.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				before := cacheFiles(t, e, id)
				for _, format := range []string{"gif", "jpg", "bmp"} {
					wantStatus(t, format, e.Get(t, e.Token(t, "root"), authorURL(id, "/image?width=77&format="+format)), http.StatusBadRequest)
				}
				if after := cacheFiles(t, e, id); len(after) != len(before) {
					t.Fatalf("cache files changed: %v -> %v", before, after)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-RAW", Case: "normal", State: "initial", Assertion: "image-get-raw-original",
			Sources: []string{"server/controllers/AuthorController.js:391-411"},
			Claim:   "raw=1 returns the stored file unchanged with its image content type and creates no cache file.",
			Run: func(t *testing.T, e *harness.Env) {
				id, original := imageAuthor(t, e)
				before := cacheFiles(t, e, id)
				resp := e.Get(t, e.Token(t, "listener"), authorURL(id, "/image?raw=1&width=50&format=png"))
				wantStatus(t, "raw", resp, http.StatusOK)
				if resp.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(resp.Body, original) {
					t.Fatalf("raw must be the stored JPEG: %q, %d bytes", resp.Header.Get("Content-Type"), len(resp.Body))
				}
				if after := cacheFiles(t, e, id); len(after) != len(before) {
					t.Fatalf("raw must not create cache: %v -> %v", before, after)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-AUTH", Case: "edge", State: "initial", Assertion: "image-get-is-public",
			Sources: []string{"server/Auth.js:20-37", "server/Auth.js:45-51", "server/Server.js:354"},
			Claim:   "GET /api/authors/:id/image needs no authentication: it is served without a token, with an invalid bearer or query token, with a ?token= of a user, and to a user who cannot access the author's library.",
			Run: func(t *testing.T, e *harness.Env) {
				id, original := imageAuthor(t, e)
				restricted := ensureUser(t, e, "author-podcast-only", map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
				for name, request := range map[string]harness.Request{
					"no token":      {Path: authorURL(id, "/image?raw=1")},
					"bad bearer":    {Path: authorURL(id, "/image?raw=1"), Token: "nope"},
					"bad query":     {Path: authorURL(id, "/image?raw=1"), Query: url.Values{"token": {"nope"}}},
					"user query":    {Path: authorURL(id, "/image?raw=1"), Query: url.Values{"token": {e.Token(t, "listener")}}},
					"other library": {Path: authorURL(id, "/image?raw=1"), Token: restricted.token},
				} {
					resp := e.Do(t, request)
					wantStatus(t, name, resp, http.StatusOK)
					if !bytes.Equal(resp.Body, original) {
						t.Fatalf("%s: raw body differs from the stored image", name)
					}
				}
				resized := e.Do(t, harness.Request{Path: authorURL(id, "/image"), Query: url.Values{"width": {"90"}}})
				wantStatus(t, "resized without token", resized, http.StatusOK)
				if width, _ := imageDims(t, resized.Body, "jpeg"); width != 90 {
					t.Fatalf("width %d, want 90", width)
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-RESIZE", Case: "normal", State: "changed", Assertion: "image-get-after-image-replaced",
			Sources: []string{"server/controllers/AuthorController.js:290-300", "server/controllers/AuthorController.js:413-418", "server/managers/CacheManager.js:97-112", "server/managers/CacheManager.js:155-189", "server/utils/index.js:62-65", "server/utils/index.js:144-147"},
			Claim:   "After a new image is uploaded the same resized request returns the new picture, not the cached old one.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				root := e.Token(t, "root")
				path := authorURL(id, "/image?width=80&format=png")
				old := e.Get(t, root, path)
				if _, height := imageDims(t, old.Body, "png"); height < 50 || height > 55 {
					t.Fatalf("old picture is 3:2, got height %d", height)
				}
				stub := imageStub(t, map[string]stubResponse{"/tall.png": {http.StatusOK, "image/png", imageBytes(t, "png", 100, 200)}})
				wantStatus(t, "replace", uploadImage(t, e, root, id, stub.URL+"/tall.png"), http.StatusOK)
				fresh := e.Get(t, root, path)
				wantStatus(t, "fresh", fresh, http.StatusOK)
				if width, height := imageDims(t, fresh.Body, "png"); width != 80 || height != 160 {
					t.Fatalf("new picture is 1:2, got %dx%d", width, height)
				}
				raw := e.Get(t, root, authorURL(id, "/image?raw=1"))
				if raw.Header.Get("Content-Type") != "image/png" {
					t.Fatalf("raw content type %q, want image/png", raw.Header.Get("Content-Type"))
				}
			},
		},
		{
			Requirement: "route-r135", Invariant: "INV-ROUTE-R135-ERRORS", Case: "error", State: "failure", Assertion: "image-get-file-missing-on-disk",
			Sources: []string{"server/controllers/AuthorController.js:391-419", "server/managers/CacheManager.js:155-186"},
			Claim:   "When the image file was removed from disk, raw and uncached resized requests answer 404.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := imageAuthor(t, e)
				path := rowText(mustRow(t, e, id), "imagePath")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				wantStatus(t, "raw", e.Get(t, e.Token(t, "root"), authorURL(id, "/image?raw=1")), http.StatusNotFound)
				wantStatus(t, "resized", e.Get(t, e.Token(t, "root"), authorURL(id, "/image?width=77")), http.StatusNotFound)
			},
		},
	}
}
