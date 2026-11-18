package protocol

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// cookieName is the public share session cookie.
const cookieName = "share_session_id"

// openShareSession opens a public share and returns its share_session_id.
func openShareSession(t *testing.T, e *harness.Env, slug string) string {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/public/share/" + slug})
	wantStatus(t, resp, http.StatusOK, "open share "+slug)
	for _, cookie := range resp.Cookies {
		if cookie.Name == cookieName && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatalf("open share %s set no %s cookie: %v", slug, cookieName, resp.Cookies)
	return ""
}

// shareCookieHeader builds the Cookie header of a share session.
func shareCookieHeader(value string) map[string]string {
	return map[string]string{"Cookie": cookieName + "=" + value}
}

// mediaIDForItem returns the books.id or podcasts.id of a library item.
func mediaIDForItem(t *testing.T, e *harness.Env, libraryItemID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT mediaId FROM libraryItems WHERE id = ?", libraryItemID)
	if len(rows) != 1 {
		t.Fatalf("library item %s: %d rows", libraryItemID, len(rows))
	}
	mediaID, _ := rows[0]["mediaId"].(string)
	if mediaID == "" {
		t.Fatalf("library item %s has no media id", libraryItemID)
	}
	return mediaID
}

// rangeGet reads one file route with a Range header.
func rangeGet(t *testing.T, e *harness.Env, path, token, spec string) harness.Response {
	t.Helper()
	return doFile(t, e, http.MethodGet, path, token, map[string]string{"Range": spec})
}

// fileRoute is one route that serves a fixture file to a client.
type fileRoute struct {
	name  string
	path  string
	token string
	file  []byte
	mime  string
}

// fileRanges covers the byte-range contract shared by the sendFile-backed
// routes.
func fileRangeScenarios() []harness.Scenario {
	const req = "protocol-ranges-cache"
	const rangeInv = "INV-PROTOCOL-RANGES-CACHE-RANGE-001"
	enclosure := func(t *testing.T, e *harness.Env) fileRoute {
		return fileRoute{"feed enclosure", feedEnclosurePath(), "", readFile(t, filepath.Join(e.Media, novelMediaPath)), "audio/mp4"}
	}
	return []harness.Scenario{
		{
			Requirement: req, Invariant: rangeInv, Case: "edge", State: "repeat", Assertion: "single-suffix-and-open-ranges-return-exact-slices",
			Sources: []string{
				"server/Server.js:360-372",
				"server/managers/RssFeedManager.js:197-225",
				"server/controllers/LibraryItemController.js:1121-1160",
			},
			Claim: "A single closed range answers 206 with Content-Range bytes a-b/size, Content-Length b-a+1 and exactly the file's a..b bytes; an open range answers from a to the end; a suffix range answers the last N bytes; a range whose end passes the file is clamped to the last byte; the same layer serves a range of the ebook with its own Content-Range.",
			Run: func(t *testing.T, e *harness.Env) {
				route := enclosure(t, e)
				size := len(route.file)
				cases := []struct {
					name       string
					spec       string
					start, end int
				}{
					{"closed", "bytes=10-19", 10, 19},
					{"open", "bytes=10-", 10, size - 1},
					{"suffix", "bytes=-16", size - 16, size - 1},
					{"clamped", "bytes=" + strconv.Itoa(size-10) + "-999999", size - 10, size - 1},
				}
				for _, test := range cases {
					resp := rangeGet(t, e, route.path, route.token, test.spec)
					wantStatus(t, resp, http.StatusPartialContent, test.name+" range")
					wantHeader(t, resp, "Content-Range", "bytes "+strconv.Itoa(test.start)+"-"+strconv.Itoa(test.end)+"/"+strconv.Itoa(size), test.name+" range")
					wantHeader(t, resp, "Content-Length", strconv.Itoa(test.end-test.start+1), test.name+" range")
					wantHeader(t, resp, "Content-Type", route.mime, test.name+" range")
					wantBody(t, resp, route.file[test.start:test.end+1], test.name+" range")
				}
				ebook := readFile(t, filepath.Join(e.Media, ebookMediaPath))
				resp := rangeGet(t, e, "/api/items/"+ebookItemID+"/ebook", e.Token(t, "root"), "bytes=0-9")
				wantStatus(t, resp, http.StatusPartialContent, "ebook range")
				wantHeader(t, resp, "Content-Range", "bytes 0-9/"+strconv.Itoa(len(ebook)), "ebook range")
				wantBody(t, resp, ebook[:10], "ebook range")
			},
		},
		{
			Requirement: req, Invariant: rangeInv, Case: "edge", State: "initial", Assertion: "multipart-and-unsatisfiable-ranges-follow-the-server",
			Sources: []string{"server/Server.js:360-372", "server/managers/RssFeedManager.js:220-225"},
			Claim:   "A multi-range header is not served as multipart by this server: it answers the complete file as 200 with the file length and bytes; a malformed range and a range starting past the end each answer 416 with Content-Range bytes */size and a short error body that does not contain the file bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				route := enclosure(t, e)
				multi := rangeGet(t, e, route.path, route.token, "bytes=0-4,10-14")
				wantStatus(t, multi, http.StatusOK, "multi-range")
				wantHeader(t, multi, "Content-Length", strconv.Itoa(len(route.file)), "multi-range")
				if got := multi.Header.Get("Content-Type"); got == "multipart/byteranges" {
					t.Fatalf("multi-range unexpectedly served as multipart: %q", got)
				}
				wantBody(t, multi, route.file, "multi-range")
				for _, spec := range []string{"bytes=abc", "bytes=" + strconv.Itoa(len(route.file)) + "-"} {
					resp := rangeGet(t, e, route.path, route.token, spec)
					wantStatus(t, resp, http.StatusRequestedRangeNotSatisfiable, spec)
					wantHeader(t, resp, "Content-Range", "bytes */"+strconv.Itoa(len(route.file)), spec)
					noBytes(t, resp, route.file, spec)
				}
			},
		},
	}
}

// fileByteScenarios prove exact bytes and MIME on the public file routes.
func fileByteScenarios() []harness.Scenario {
	const req = "protocol-ranges-cache"
	const bytesInv = "INV-PROTOCOL-RANGES-CACHE"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: bytesInv, Case: "normal", State: "initial", Assertion: "public-file-routes-serve-exact-bytes-and-client-mime",
			Sources: []string{
				"server/controllers/ShareController.js:180-224",
				"server/managers/RssFeedManager.js:197-259",
				"server/controllers/LibraryItemController.js:401-431,989-1004,1121-1160",
				"server/utils/fileUtils.js:424-429",
			},
			Claim: "The share track, feed enclosure, ebook, inline library file, feed cover and raw item cover each serve the exact fixture file bytes with the MIME the route sets (audio/mp4, audio/mp4, application/epub+zip, application/epub+zip, image/jpg, image/jpeg); the static-file routes advertise Accept-Ranges: bytes and their Content-Length equals the file size, while the streamed cover carries neither.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
				apple := doFile(t, e, http.MethodGet, "/api/items/"+novelItemID+"/file/"+novelIno+"/download", token, map[string]string{"User-Agent": iphone})
				wantStatus(t, apple, http.StatusOK, "Apple mobile audio download")
				wantHeader(t, apple, "Content-Type", "audio/m4b", "Apple mobile audio download")
				desktop := doFile(t, e, http.MethodGet, "/api/items/"+novelItemID+"/file/"+novelIno+"/download", token, map[string]string{"User-Agent": "curl/8.0"})
				wantStatus(t, desktop, http.StatusOK, "desktop audio download")
				wantHeader(t, desktop, "Content-Type", "audio/mp4", "desktop audio download")

				cookie := openShareSession(t, e, novelShareSlug)
				novel := readFile(t, filepath.Join(e.Media, novelMediaPath))
				ebook := readFile(t, filepath.Join(e.Media, ebookMediaPath))
				cover := readFile(t, filepath.Join(e.Install, novelCoverPath))
				cases := []struct {
					name, path string
					header     map[string]string
					token      string
					file       []byte
					mime       string
					static     bool
				}{
					{"share track", "/public/share/" + novelShareSlug + "/track/1", nil, "", novel, "audio/mp4", true},
					{"feed enclosure", feedEnclosurePath(), nil, "", novel, "audio/mp4", true},
					{"ebook", "/api/items/" + ebookItemID + "/ebook", nil, token, ebook, "application/epub+zip", true},
					{"library file", "/api/items/" + ebookItemID + "/file/" + ebookIno, nil, token, ebook, "application/epub+zip", true},
					{"audio library file", "/api/items/" + novelItemID + "/file/" + novelIno, nil, token, novel, "audio/mp4", true},
					{"feed cover", "/feed/" + novelFeedSlug + "/cover.jpg", nil, "", cover, "image/jpg", false},
					{"raw item cover", novelCoverURL() + "?raw=1", nil, token, cover, "image/jpeg", true},
				}
				for _, test := range cases {
					header := test.header
					if test.name == "share track" {
						header = shareCookieHeader(cookie)
					}
					resp := doFile(t, e, http.MethodGet, test.path, test.token, header)
					wantStatus(t, resp, http.StatusOK, test.name)
					wantHeader(t, resp, "Content-Type", test.mime, test.name)
					wantBody(t, resp, test.file, test.name)
					if test.static {
						wantHeader(t, resp, "Accept-Ranges", "bytes", test.name)
						wantHeader(t, resp, "Content-Length", strconv.Itoa(len(test.file)), test.name)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-PROTOCOL-RANGES-CACHE-DISPOSITION-001", Case: "normal", State: "initial", Assertion: "download-routes-name-the-file-and-zip-entries",
			Sources: []string{
				"server/controllers/LibraryItemController.js:169-198,989-1004,1073-1119",
				"server/controllers/ShareController.js:226-273",
			},
			Claim: "GET /api/items/:id/file/:fileid/download of the ebook answers 200 with Content-Type application/epub+zip, Content-Disposition attachment; filename=\"Ebook Only.epub\" and the exact ebook bytes; the inline /file/:fileid route serves the same bytes without Content-Disposition; an m4b file download is audio/mp4 for curl but audio/m4b for an Apple mobile WebKit user agent; GET /api/items/:id/download of a folder item and the public share download answer 200 with an application/zip attachment named after the item title and a ZIP body, not the raw audio bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				ebook := readFile(t, filepath.Join(e.Media, ebookMediaPath))
				resp := e.Get(t, token, "/api/items/"+ebookItemID+"/file/"+ebookIno+"/download")
				wantStatus(t, resp, http.StatusOK, "ebook download")
				wantHeader(t, resp, "Content-Type", "application/epub+zip", "ebook download")
				wantHeader(t, resp, "Content-Disposition", `attachment; filename="Ebook Only.epub"`, "ebook download")
				wantBody(t, resp, ebook, "ebook download")

				novel := readFile(t, filepath.Join(e.Media, novelMediaPath))
				zip := e.Get(t, token, "/api/items/"+novelItemID+"/download")
				wantStatus(t, zip, http.StatusOK, "item download")
				wantHeader(t, zip, "Content-Type", "application/zip", "item download")
				wantHeader(t, zip, "Content-Disposition", `attachment; filename="Single File Novel.zip"`, "item download")
				if len(zip.Body) < 4 || string(zip.Body[:4]) != "PK\x03\x04" {
					t.Fatalf("item download is not a ZIP archive: %x", zip.Body[:min(4, len(zip.Body))])
				}
				if len(zip.Body) == len(novel) {
					t.Fatal("item download served the raw audio file instead of an archive")
				}
				inline := e.Get(t, token, "/api/items/"+ebookItemID+"/file/"+ebookIno)
				wantStatus(t, inline, http.StatusOK, "library file")
				if got := inline.Header.Get("Content-Disposition"); got != "" {
					t.Fatalf("inline library file is an attachment: %q", got)
				}
				wantBody(t, inline, ebook, "library file")

				iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
				apple := doFile(t, e, http.MethodGet, "/api/items/"+novelItemID+"/file/"+novelIno+"/download", token, map[string]string{"User-Agent": iphone})
				wantStatus(t, apple, http.StatusOK, "Apple mobile audio download")
				wantHeader(t, apple, "Content-Type", "audio/m4b", "Apple mobile audio download")
				desktop := doFile(t, e, http.MethodGet, "/api/items/"+novelItemID+"/file/"+novelIno+"/download", token, map[string]string{"User-Agent": "curl/8.0"})
				wantStatus(t, desktop, http.StatusOK, "desktop audio download")
				wantHeader(t, desktop, "Content-Type", "audio/mp4", "desktop audio download")

				cookie := openShareSession(t, e, novelShareSlug)
				shared := doFile(t, e, http.MethodGet, "/public/share/"+novelShareSlug+"/download", "", shareCookieHeader(cookie))
				wantStatus(t, shared, http.StatusOK, "share download")
				wantHeader(t, shared, "Content-Type", "application/zip", "share download")
				wantHeader(t, shared, "Content-Disposition", `attachment; filename="Single File Novel.zip"`, "share download")
				if len(shared.Body) < 4 || string(shared.Body[:4]) != "PK\x03\x04" {
					t.Fatalf("share download is not a ZIP archive: %x", shared.Body[:min(4, len(shared.Body))])
				}
			},
		},
	}
}

// fileHeadScenarios covers HEAD on the file-bearing routes.
func fileHeadScenarios() []harness.Scenario {
	const req = "protocol-ranges-cache"
	const headInv = "INV-PROTOCOL-RANGES-CACHE-HEAD-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: headInv, Case: "edge", State: "initial", Assertion: "head-returns-get-headers-without-a-body",
			Sources: []string{"server/managers/RssFeedManager.js:197-225", "server/controllers/LibraryItemController.js:1121-1160"},
			Claim:   "HEAD of the feed enclosure and of the ebook returns the GET status, Content-Type, Content-Length, Accept-Ranges, ETag and Last-Modified with an empty body; HEAD of a download keeps Content-Disposition and an empty body; HEAD of an unknown feed cover returns 404 and no bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				novel := readFile(t, filepath.Join(e.Media, novelMediaPath))
				ebook := readFile(t, filepath.Join(e.Media, ebookMediaPath))
				cases := []struct {
					name, path, token, mime string
					size                    int
				}{
					{"feed enclosure", feedEnclosurePath(), "", "audio/mp4", len(novel)},
					{"ebook", "/api/items/" + ebookItemID + "/ebook", token, "application/epub+zip", len(ebook)},
				}
				for _, test := range cases {
					get := doFile(t, e, http.MethodGet, test.path, test.token, nil)
					wantStatus(t, get, http.StatusOK, test.name+" GET")
					head := doFile(t, e, http.MethodHead, test.path, test.token, nil)
					wantStatus(t, head, http.StatusOK, test.name+" HEAD")
					if len(head.Body) != 0 {
						t.Fatalf("%s HEAD carried %d body bytes", test.name, len(head.Body))
					}
					for _, name := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "ETag", "Last-Modified"} {
						wantHeader(t, head, name, get.Header.Get(name), test.name+" HEAD")
					}
					wantHeader(t, head, "Content-Type", test.mime, test.name+" HEAD")
					wantHeader(t, head, "Content-Length", strconv.Itoa(test.size), test.name+" HEAD")
				}
				download := doFile(t, e, http.MethodHead, "/api/items/"+ebookItemID+"/file/"+ebookIno+"/download", token, nil)
				wantStatus(t, download, http.StatusOK, "download HEAD")
				wantHeader(t, download, "Content-Disposition", `attachment; filename="Ebook Only.epub"`, "download HEAD")
				if len(download.Body) != 0 {
					t.Fatalf("download HEAD carried %d body bytes", len(download.Body))
				}
				missing := doFile(t, e, http.MethodHead, "/feed/no-such-feed/cover.jpg", "", nil)
				wantStatus(t, missing, http.StatusNotFound, "unknown feed cover HEAD")
				if len(missing.Body) != 0 {
					t.Fatalf("unknown feed cover HEAD carried %d body bytes", len(missing.Body))
				}
			},
		},
	}
}

// fileConditionalScenarios covers ETag/If-None-Match and If-Modified-Since.
func fileConditionalScenarios() []harness.Scenario {
	const req = "protocol-ranges-cache"
	const condInv = "INV-PROTOCOL-RANGES-CACHE-CONDITIONAL-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: condInv, Case: "normal", State: "repeat", Assertion: "conditional-reads-answer-304-with-the-same-validator",
			Sources: []string{"server/Server.js:360-372", "server/managers/RssFeedManager.js:220-225"},
			Claim:   "A file route's 200 carries an ETag and Last-Modified; If-None-Match with that ETag and If-Modified-Since with that date each answer 304 with no body and the same ETag; a different ETag answers 200 with the unchanged bytes; a streamed feed cover that has no ETag answers 200 to If-None-Match.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := readFile(t, filepath.Join(e.Media, novelMediaPath))
				first := doFile(t, e, http.MethodGet, feedEnclosurePath(), "", nil)
				wantStatus(t, first, http.StatusOK, "first enclosure read")
				etag := first.Header.Get("ETag")
				lastModified := first.Header.Get("Last-Modified")
				if etag == "" || lastModified == "" {
					t.Fatalf("enclosure response lacks validators: %v", first.Header)
				}
				for name, header := range map[string]map[string]string{
					"If-None-Match":     {"If-None-Match": etag},
					"If-Modified-Since": {"If-Modified-Since": lastModified},
				} {
					resp := doFile(t, e, http.MethodGet, feedEnclosurePath(), "", header)
					wantStatus(t, resp, http.StatusNotModified, name)
					if len(resp.Body) != 0 {
						t.Fatalf("%s 304 carried %d body bytes", name, len(resp.Body))
					}
					wantHeader(t, resp, "ETag", etag, name+" 304")
				}
				stale := doFile(t, e, http.MethodGet, feedEnclosurePath(), "", map[string]string{"If-None-Match": `"stale-validator"`})
				wantStatus(t, stale, http.StatusOK, "stale validator")
				wantBody(t, stale, novel, "stale validator")

				// The same protocol layer guards the cookie-bound share track.
				cookie := openShareSession(t, e, novelShareSlug)
				track := doFile(t, e, http.MethodGet, "/public/share/"+novelShareSlug+"/track/1", "", shareCookieHeader(cookie))
				wantStatus(t, track, http.StatusOK, "share track")
				if track.Header.Get("ETag") == "" {
					t.Fatalf("share track has no ETag: %v", track.Header)
				}
				notModified := doFile(t, e, http.MethodGet, "/public/share/"+novelShareSlug+"/track/1", "", map[string]string{"Cookie": cookieName + "=" + cookie, "If-None-Match": track.Header.Get("ETag")})
				wantStatus(t, notModified, http.StatusNotModified, "share track conditional")
				if len(notModified.Body) != 0 {
					t.Fatalf("share track 304 carried %d body bytes", len(notModified.Body))
				}

				streamed := doFile(t, e, http.MethodGet, "/feed/"+novelFeedSlug+"/cover.jpg", "", map[string]string{"If-None-Match": etag})
				wantStatus(t, streamed, http.StatusOK, "streamed cover conditional")
				if streamed.Header.Get("ETag") != "" {
					t.Fatalf("streamed cover unexpectedly has an ETag: %q", streamed.Header.Get("ETag"))
				}
				wantBody(t, streamed, readFile(t, filepath.Join(e.Install, novelCoverPath)), "streamed cover conditional")
			},
		},
		{
			Requirement: req, Invariant: condInv, Case: "normal", State: "changed", Assertion: "changed-file-revalidates-the-old-validator",
			Sources: []string{"server/Server.js:360-372", "server/controllers/LibraryItemController.js:401-431"},
			Claim:   "After the item cover file on disk changes, a conditional read with the previous ETag answers 200 with the new byte length and bytes and a different ETag; a conditional read with the new ETag then answers 304; restoring the original file serves the original bytes again.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				coverPath := filepath.Join(e.Install, novelCoverPath)
				original := readFile(t, coverPath)
				first := doFile(t, e, http.MethodGet, novelCoverURL()+"?raw=1", token, nil)
				wantStatus(t, first, http.StatusOK, "cover before change")
				wantBody(t, first, original, "cover before change")
				oldETag := first.Header.Get("ETag")
				if oldETag == "" {
					t.Fatal("cover response has no ETag")
				}

				changed := append(append([]byte{}, original...), 0xFF, 0xD9)
				if err := os.WriteFile(coverPath, changed, 0o644); err != nil {
					t.Fatalf("change cover: %v", err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(coverPath, original, 0o644); err != nil {
						t.Errorf("restore cover: %v", err)
					}
				})
				after := doFile(t, e, http.MethodGet, novelCoverURL()+"?raw=1", token, map[string]string{"If-None-Match": oldETag})
				wantStatus(t, after, http.StatusOK, "old validator after change")
				if got := after.Header.Get("ETag"); got == "" || got == oldETag {
					t.Fatalf("ETag after change = %q, want a new validator", got)
				}
				wantHeader(t, after, "Content-Length", strconv.Itoa(len(changed)), "after change")
				wantBody(t, after, changed, "after change")
				notModified := doFile(t, e, http.MethodGet, novelCoverURL()+"?raw=1", token, map[string]string{"If-None-Match": after.Header.Get("ETag")})
				wantStatus(t, notModified, http.StatusNotModified, "new validator after change")
				if len(notModified.Body) != 0 {
					t.Fatalf("304 after change carried %d body bytes", len(notModified.Body))
				}
			},
		},
	}
}

// fileDeniedScenarios prove a revoked share and a removed file serve nothing.
func fileDeniedScenarios() []harness.Scenario {
	const req = "protocol-ranges-cache"
	const denyInv = "INV-PROTOCOL-RANGES-CACHE-DENIED-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: denyInv, Case: "error", State: "failure", Assertion: "revoked-share-and-removed-file-serve-no-bytes",
			Sources: []string{
				"server/controllers/ShareController.js:180-215",
				"server/managers/ShareManager.js:149-165",
				"server/controllers/LibraryItemController.js:403-431",
			},
			Claim: "After an admin deletes a share, its slug's track and cover answer 404 with no media bytes and the share row is gone; after the item cover file is removed, both the raw cover route and a cached-cover request for an uncached width answer 404 with no image bytes; an ebook request with an unknown file id answers 404 and an item without an ebook answers 404, both without file bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				novel := readFile(t, filepath.Join(e.Media, novelMediaPath))
				slug := "protocol-revoked-share"
				created := e.Post(t, token, "/api/share/mediaitem", map[string]any{
					"slug": slug, "mediaItemType": "book", "mediaItemId": mediaIDForItem(t, e, e.IDs["disposable"]),
					"expiresAt": 0, "isDownloadable": true,
				})
				wantStatus(t, created, http.StatusCreated, "create share")
				var share struct {
					ID string `json:"id"`
				}
				created.JSON(t, &share)
				cookie := openShareSession(t, e, slug)
				open := doFile(t, e, http.MethodGet, "/public/share/"+slug+"/track/1", "", shareCookieHeader(cookie))
				wantStatus(t, open, http.StatusOK, "track while share is open")
				wantStatus(t, e.Delete(t, token, "/api/share/mediaitem/"+share.ID), http.StatusNoContent, "delete share")
				for _, path := range []string{"/public/share/" + slug + "/track/1", "/public/share/" + slug + "/cover"} {
					resp := doFile(t, e, http.MethodGet, path, "", shareCookieHeader(cookie))
					wantStatus(t, resp, http.StatusNotFound, "revoked "+path)
					noBytes(t, resp, novel, "revoked "+path)
				}

				coverPath := filepath.Join(e.Install, novelCoverPath)
				original := readFile(t, coverPath)
				if err := os.Rename(coverPath, coverPath+".removed"); err != nil {
					t.Fatalf("remove cover: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Rename(coverPath+".removed", coverPath); err != nil {
						t.Errorf("restore cover: %v", err)
					}
				})
				removed := e.Get(t, token, novelCoverURL()+"?raw=1")
				wantStatus(t, removed, http.StatusNotFound, "removed cover")
				noBytes(t, removed, original, "removed cover")
				cached := e.Get(t, token, novelCoverURL()+"?width=77")
				wantStatus(t, cached, http.StatusNotFound, "removed cover cache miss")

				badEbook := e.Get(t, token, "/api/items/"+ebookItemID+"/ebook/not-an-ebook-inode")
				wantStatus(t, badEbook, http.StatusNotFound, "invalid ebook file id")
				noEbook := e.Get(t, token, "/api/items/"+novelItemID+"/ebook")
				wantStatus(t, noEbook, http.StatusNotFound, "item without an ebook")
				noBytes(t, noEbook, original, "item without an ebook")
			},
		},
	}
}
