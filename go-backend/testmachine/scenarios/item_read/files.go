package item_read

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	getFileSource      = "server/controllers/LibraryItemController.js:989-1006"
	downloadFileSource = "server/controllers/LibraryItemController.js:1073-1110"
	appleSafari        = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	desktopChrome      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// servedFile is a fixture library file with the media type the server must send for it.
type servedFile struct {
	item, name, mime string
}

// servedFiles covers audio (the MIME table of utils/constants.js), image, text and ebook files.
var servedFiles = []servedFile{
	{"saga1", "01 - Part 1.mp3", "audio/mpeg"},
	{"saga1", "cover.jpg", "image/jpeg"},
	{"novel", "Single File Novel.m4b", "audio/mp4"},
	{"saga2", "The Middle.flac", "audio/flac"},
	{"saga2", "desc.txt", "text/plain"},
	{"ebook", "Ebook Only.epub", "application/epub+zip"},
	{"podcast", "episode-1.mp3", "audio/mpeg"},
}

func fileRoute(itemID string, file fileInfo) string {
	return "/api/items/" + itemID + "/file/" + file.Ino
}

func downloadRoute(itemID string, file fileInfo) string { return fileRoute(itemID, file) + "/download" }

// byteRange is one Range request with its expected outcome.
type byteRange struct {
	header     string
	start, end int64 // inclusive
	status     int
}

// rangeCases derives ranges for a file of the given size.
func rangeCases(size int64) []byteRange {
	return []byteRange{
		{header: "bytes=0-99", start: 0, end: 99, status: http.StatusPartialContent},
		{header: "bytes=100-199", start: 100, end: 199, status: http.StatusPartialContent},
		{header: fmt.Sprintf("bytes=%d-", size-50), start: size - 50, end: size - 1, status: http.StatusPartialContent},
		{header: "bytes=-64", start: size - 64, end: size - 1, status: http.StatusPartialContent},
	}
}

// checkRanges runs every satisfiable range case against path and checks status, headers and bytes.
func checkRanges(t *testing.T, e *harness.Env, token, path string, file fileInfo) {
	t.Helper()
	whole := readDisk(t, file.Path)
	for _, c := range rangeCases(int64(len(whole))) {
		resp := get(t, e, token, path, map[string]string{"Range": c.header})
		wantStatus(t, "Range "+c.header, resp, c.status)
		if want := fmt.Sprintf("bytes %d-%d/%d", c.start, c.end, len(whole)); resp.Header.Get("Content-Range") != want {
			t.Fatalf("Range %s: Content-Range %q, want %q", c.header, resp.Header.Get("Content-Range"), want)
		}
		if !bytes.Equal(resp.Body, whole[c.start:c.end+1]) {
			t.Fatalf("Range %s: body is not bytes %d-%d of the file", c.header, c.start, c.end)
		}
	}
}

// wantUnsatisfiable checks the 416 answer of the stream route for a start beyond the end.
func wantUnsatisfiable(t *testing.T, e *harness.Env, token, path string, size int64) {
	t.Helper()
	resp := get(t, e, token, path, map[string]string{"Range": fmt.Sprintf("bytes=%d-", size+10)})
	wantStatus(t, "unsatisfiable range", resp, http.StatusRequestedRangeNotSatisfiable)
	if got, want := resp.Header.Get("Content-Range"), fmt.Sprintf("bytes */%d", size); got != want {
		t.Fatalf("Content-Range %q, want %q", got, want)
	}
}

// fileScenarios covers GET /api/items/:id/file/:fileid.
func fileScenarios(s *setup) []harness.Scenario {
	build := func(assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r066", Invariant: "INV-R066-SAMPLED", Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{getFileSource, middlewareSource, "server/utils/fileUtils.js:424-429", "server/utils/constants.js:36-58"}, Claim: claim, Run: run,
		}
	}
	route := func(t *testing.T, e *harness.Env, itemID string) string {
		return fileRoute(itemID, anyFile(t, e, itemID, false))
	}
	scenarios := []harness.Scenario{
		build("file-bytes-and-media-types", "normal",
			"A library file is streamed byte for byte with the media type of its extension: audio from the server's table (mp3, m4b as audio/mp4, flac), then image, text and ebook types.",
			func(t *testing.T, e *harness.Env) {
				for _, c := range servedFiles {
					file := fileNamed(t, e, e.IDs[c.item], c.name)
					resp := get(t, e, e.Token(t, "listener"), fileRoute(e.IDs[c.item], file), nil)
					wantFileResponse(t, resp, file, c.mime)
					if resp.Header.Get("Accept-Ranges") != "bytes" {
						t.Fatalf("%s: Accept-Ranges %q, want bytes", c.name, resp.Header.Get("Accept-Ranges"))
					}
				}
			}),
		build("file-byte-ranges", "edge",
			"Range requests are honoured: first bytes, a middle slice, an open end, a suffix answer 206 with the exact Content-Range and bytes; on the stream route a start beyond the end answers 416 with bytes */size.",
			func(t *testing.T, e *harness.Env) {
				for _, c := range []struct{ item, name string }{{"saga1", "02 - Part 2.mp3"}, {"novel", "Single File Novel.m4b"}} {
					file := fileNamed(t, e, e.IDs[c.item], c.name)
					checkRanges(t, e, e.Token(t, "listener"), fileRoute(e.IDs[c.item], file), file)
					wantUnsatisfiable(t, e, e.Token(t, "listener"), fileRoute(e.IDs[c.item], file), file.Size)
				}
			}),
		build("file-conditional-requests", "edge",
			"A repeated request with the ETag in If-None-Match, or Last-Modified in If-Modified-Since, answers 304 without a body; an If-Range with another validator gives the whole file.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["saga1"], "03 - Part 3.mp3")
				path, token := fileRoute(e.IDs["saga1"], file), e.Token(t, "listener")
				first := get(t, e, token, path, nil)
				etag, modified := first.Header.Get("ETag"), first.Header.Get("Last-Modified")
				if etag == "" || modified == "" {
					t.Fatalf("response has ETag %q and Last-Modified %q, want both", etag, modified)
				}
				for header, value := range map[string]string{"If-None-Match": etag, "If-Modified-Since": modified} {
					resp := get(t, e, token, path, map[string]string{header: value})
					wantStatus(t, header, resp, http.StatusNotModified)
					if len(resp.Body) != 0 {
						t.Fatalf("%s: 304 carries a body of %d bytes", header, len(resp.Body))
					}
				}
				resp := get(t, e, token, path, map[string]string{"Range": "bytes=0-9", "If-Range": `"other"`})
				wantFileResponse(t, resp, file, "audio/mpeg")
			}),
		build("file-streams-without-download-permission", "edge",
			"Streaming a library file needs no download permission, and the access token may be sent as the token query parameter instead of the header.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["saga1"], "01 - Part 1.mp3")
				path := fileRoute(e.IDs["saga1"], file)
				wantFileResponse(t, get(t, e, s.token(t, e, "nodownload"), path, nil), file, "audio/mpeg")
				resp := e.Do(t, harness.Request{Path: path, Query: url.Values{"token": {e.Token(t, "listener")}}})
				wantFileResponse(t, resp, file, "audio/mpeg")
			}),
		build("file-unknown-file-id", "error",
			"A file id that is not a library file of the item (unknown, or of another item) answers 404, and an inaccessible item answers 403 before the file lookup.",
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				other := fileNamed(t, e, e.IDs["saga2"], "The Middle.flac")
				for _, ino := range []string{"0", "not-an-inode", other.Ino} {
					wantStatus(t, "ino "+ino, e.Get(t, root, "/api/items/"+e.IDs["saga1"]+"/file/"+ino), http.StatusNotFound)
				}
				wantStatus(t, "inaccessible item, unknown file", e.Get(t, s.token(t, e, "tagallow"), "/api/items/"+e.IDs["saga1"]+"/file/0"), http.StatusForbidden)
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r066", invariant: "INV-R066-ACCESS-001", label: "file", path: route, sources: []string{getFileSource},
	})...)
}

// fileDownloadScenarios covers GET /api/items/:id/file/:fileid/download.
func fileDownloadScenarios(s *setup) []harness.Scenario {
	build := func(invariant, assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r068", Invariant: invariant, Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{downloadFileSource, middlewareSource, "server/utils/fileUtils.js:424-429"}, Claim: claim, Run: run,
		}
	}
	route := func(t *testing.T, e *harness.Env, itemID string) string {
		return downloadRoute(itemID, anyFile(t, e, itemID, false))
	}
	const headers = "INV-R068-HEADERS-001"
	scenarios := []harness.Scenario{
		build(headers, "file-download-attachment-and-bytes", "normal",
			"The file download sends the file byte for byte as an attachment named after the file, with the media type of its extension.",
			func(t *testing.T, e *harness.Env) {
				for _, c := range servedFiles {
					file := fileNamed(t, e, e.IDs[c.item], c.name)
					resp := get(t, e, e.Token(t, "listener"), downloadRoute(e.IDs[c.item], file), nil)
					wantFileResponse(t, resp, file, c.mime)
					if kind, name := attachment(t, resp); kind != "attachment" || name != c.name {
						t.Fatalf("%s: Content-Disposition %q filename %q, want attachment %q", c.name, kind, name, c.name)
					}
				}
			}),
		build(headers, "file-download-apple-mobile-m4b", "edge",
			"For an iPhone WebKit user agent the m4b download is sent as audio/m4b, other audio types and desktop agents keep audio/mp4 and audio/mpeg.",
			func(t *testing.T, e *harness.Env) {
				m4b := fileNamed(t, e, e.IDs["novel"], "Single File Novel.m4b")
				mp3 := fileNamed(t, e, e.IDs["saga1"], "01 - Part 1.mp3")
				token := e.Token(t, "listener")
				for _, c := range []struct {
					agent, mime string
					item        string
					file        fileInfo
				}{
					{appleSafari, "audio/m4b", "novel", m4b}, {desktopChrome, "audio/mp4", "novel", m4b},
					{appleSafari, "audio/mpeg", "saga1", mp3}, {"", "audio/mp4", "novel", m4b},
				} {
					header := map[string]string{}
					if c.agent != "" {
						header["User-Agent"] = c.agent
					}
					wantFileResponse(t, get(t, e, token, downloadRoute(e.IDs[c.item], c.file), header), c.file, c.mime)
				}
			}),
		build(headers, "file-download-byte-ranges", "edge",
			"The file download honours satisfiable Range requests with 206, the exact Content-Range and bytes (the status of an unsatisfiable range is an upstream accident and not asserted).",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["saga1"], "02 - Part 2.mp3")
				checkRanges(t, e, e.Token(t, "listener"), downloadRoute(e.IDs["saga1"], file), file)
			}),
		build("INV-R068-FILE-LOOKUP-ORDER-001", "file-download-lookup-before-permission", "error",
			"For a user without download permission an unknown file id (or item) is 404 while a real file is 403, and no file bytes are sent.",
			func(t *testing.T, e *harness.Env) {
				token := s.token(t, e, "nodownload")
				file := fileNamed(t, e, e.IDs["saga1"], "01 - Part 1.mp3")
				resp := get(t, e, token, downloadRoute(e.IDs["saga1"], file), nil)
				wantStatus(t, "real file", resp, http.StatusForbidden)
				if bytes.Contains(resp.Body, readDisk(t, file.Path)[:64]) {
					t.Fatal("403 response carries file content")
				}
				wantStatus(t, "unknown file", e.Get(t, token, "/api/items/"+e.IDs["saga1"]+"/file/0/download"), http.StatusNotFound)
				wantStatus(t, "unknown item", e.Get(t, token, "/api/items/"+missingID+"/file/"+file.Ino+"/download"), http.StatusNotFound)
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r068", invariant: "INV-R068-ACCESS-001", label: "file-download", path: route, sources: []string{downloadFileSource},
	})...)
}
