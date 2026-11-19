package admin_files

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	uploadPath = "/api/upload"

	uploadRoute = "server/routers/ApiRouter.js:346"
	uploadSrc   = "server/controllers/MiscController.js:37-104"
	sanitizeSrc = "server/utils/fileUtils.js:360-420"
	uploadUISrc = "client/pages/upload/index.vue:313-345"
)

// upload sends the multipart form the upload page posts: library, folder and
// title, plus author and series for books, and the files under numeric fields.
func upload(t *testing.T, e *harness.Env, token string, fields map[string]string, files ...harness.File) harness.Response {
	t.Helper()
	body, contentType := harness.Multipart(fields, files...)
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: uploadPath, Token: token, Body: body,
		Header: map[string]string{"Content-Type": contentType}})
}

// uploadFile is one form file the upload page sends.
func uploadFile(field, name, content string) harness.File {
	return harness.File{Field: field, Name: name, Content: []byte(content)}
}

// wantFile asserts the exact content of a written file.
func wantFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read uploaded file %s: %v", path, err)
	}
	if string(data) != content {
		t.Fatalf("uploaded file %s = %q, want %q", path, data, content)
	}
}

// wantDirFiles asserts the exact file names inside a directory.
func wantDirFiles(t *testing.T, dir string, want ...string) {
	t.Helper()
	sameList(t, "files in "+dir, subtreeFiles(t, dir), want)
}

func uploadScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r203", "INV-ROUTE-R203-ROUTE", "error", "failure", "upload-rejects-callers-and-bodies",
			"POST /api/upload answers 401 without a token, 403 to the fixture listener (which cannot upload) before the body is looked at, 400 without a file or without string library, folder and title, 404 for an unknown library or a folder of another library, and 403 for an uploader without access to the library; no file or directory is written and no row changes.",
			[]string{uploadRoute, uploadSrc, uploadUISrc}, func(t *testing.T, e *harness.Env) {
				folderID, _ := libraryFolder(t, e, e.IDs["library"])
				podcastFolderID, _ := libraryFolder(t, e, e.IDs["podcastLibrary"])
				// An uploader that may only reach the podcast library.
				_, podcastOnly := createUser(t, e, "upload-podcast-only", map[string]any{"upload": true}, []string{e.IDs["podcastLibrary"]})
				_ = e.Token(t, "listener")
				root := e.Token(t, "root")
				verifyUnchanged := unchangedExcept(t, e, "logs", "tmp") // multipart staging is not product state
				defer verifyUnchanged()
				good := map[string]string{"library": e.IDs["library"], "folder": folderID, "title": "Rejected Upload"}
				wantStatus(t, "anonymous", upload(t, e, "", good, uploadFile("0", "rejected.m4b", "no")), http.StatusUnauthorized)
				wantStatus(t, "listener without a file", upload(t, e, e.Token(t, "listener"), good), http.StatusForbidden)
				wantStatus(t, "listener", upload(t, e, e.Token(t, "listener"), good, uploadFile("0", "rejected.m4b", "no")), http.StatusForbidden)
				wantStatus(t, "no file", upload(t, e, root, good), http.StatusBadRequest)
				for name, fields := range map[string]map[string]string{
					"no title":    {"library": e.IDs["library"], "folder": folderID},
					"no library":  {"folder": folderID, "title": "Rejected Upload"},
					"no folder":   {"library": e.IDs["library"], "title": "Rejected Upload"},
					"empty title": {"library": e.IDs["library"], "folder": folderID, "title": ""},
				} {
					wantStatus(t, name, upload(t, e, root, fields, uploadFile("0", "rejected.m4b", "no")), http.StatusBadRequest)
				}
				unknown := map[string]string{"library": "00000000-0000-0000-0000-000000000000", "folder": podcastFolderID, "title": "Rejected Upload"}
				wantStatus(t, "unknown library", upload(t, e, root, unknown, uploadFile("0", "rejected.m4b", "no")), http.StatusNotFound)
				foreignFolder := map[string]string{"library": e.IDs["library"], "folder": podcastFolderID, "title": "Rejected Upload"}
				wantStatus(t, "folder of another library", upload(t, e, root, foreignFolder, uploadFile("0", "rejected.m4b", "no")), http.StatusNotFound)
				wantStatus(t, "library without access", upload(t, e, podcastOnly, good, uploadFile("0", "rejected.m4b", "no")), http.StatusForbidden)
				wantStatus(t, "unknown library before access", upload(t, e, podcastOnly, unknown, uploadFile("0", "rejected.m4b", "no")), http.StatusNotFound)
			}),
		scenario("route-r203", "INV-ROUTE-R203-WRITE", "normal", "initial", "upload-book-writes-the-library-folder",
			"POST /api/upload as an admin with one file and author, series and title writes the file below <folder>/<author>/<series>/<title>/ with its exact bytes, answers 200, and creates no library item synchronously.",
			[]string{uploadRoute, uploadSrc, uploadUISrc}, func(t *testing.T, e *harness.Env) {
				folderID, folder := libraryFolder(t, e, e.IDs["library"])
				rowsBefore := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				fields := map[string]string{"library": e.IDs["library"], "folder": folderID, "title": "Upload Title",
					"author": "Uploader Author", "series": "Upload Series"}
				wantStatus(t, "upload book", upload(t, e, e.Token(t, "root"), fields, uploadFile("0", "track one.m4b", "first track bytes")), http.StatusOK)
				dir := filepath.Join(folder, "Uploader Author", "Upload Series", "Upload Title")
				wantFile(t, filepath.Join(dir, "track one.m4b"), "first track bytes")
				wantDirFiles(t, dir, "track one.m4b")
				wantInt(t, "library items after an upload", e.Count(t, "SELECT COUNT(*) FROM libraryItems"), rowsBefore)
			}),
		scenario("route-r203", "INV-ROUTE-R203-WRITE", "edge", "initial", "upload-podcast-uses-the-title-only-and-sanitizes",
			"An uploader with access to the podcast library uploads without author or series: the file lands below <folder>/<title>, with the colon of the title replaced and the directory part of the file name dropped by the multipart parser, and no author, series or nested file directory is created.",
			[]string{uploadRoute, uploadSrc, sanitizeSrc, "server/libs/busboy/types/multipart.js:299-325", uploadUISrc}, func(t *testing.T, e *harness.Env) {
				folderID, folder := libraryFolder(t, e, e.IDs["podcastLibrary"])
				_, token := createUser(t, e, "upload-podcaster", map[string]any{"upload": true}, []string{e.IDs["podcastLibrary"]})
				fields := map[string]string{"library": e.IDs["podcastLibrary"], "folder": folderID,
					"title": "Fixture: Upload Cast", "author": "Ignored Author", "series": "Ignored Series"}
				wantStatus(t, "upload podcast", upload(t, e, token, fields, uploadFile("0", "sub/episode one.mp3", "podcast bytes")), http.StatusOK)
				dir := filepath.Join(folder, "Fixture - Upload Cast")
				wantFile(t, filepath.Join(dir, "episode one.mp3"), "podcast bytes")
				wantDirFiles(t, dir, "episode one.mp3")
				if _, err := os.Stat(filepath.Join(folder, "Ignored Author")); !os.IsNotExist(err) {
					t.Fatalf("podcast upload created an author directory: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
					t.Fatalf("uploaded file name created a directory: %v", err)
				}
			}),
		scenario("route-r203", "INV-ROUTE-R203-REPEAT", "normal", "repeat", "upload-repeat-overwrites-one-file",
			"Uploading the same title and file name twice with empty author and series answers 200 both times, writes below <folder>/<title>/ without an author directory, and leaves one file with the second content.",
			[]string{uploadRoute, uploadSrc, uploadUISrc}, func(t *testing.T, e *harness.Env) {
				folderID, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				fields := map[string]string{"library": e.IDs["library"], "folder": folderID, "title": "Upload Repeat", "author": "", "series": ""}
				wantStatus(t, "first upload", upload(t, e, root, fields, uploadFile("0", "repeat.m4b", "first content")), http.StatusOK)
				wantStatus(t, "second upload", upload(t, e, root, fields, uploadFile("0", "repeat.m4b", "second content")), http.StatusOK)
				dir := filepath.Join(folder, "Upload Repeat")
				wantFile(t, filepath.Join(dir, "repeat.m4b"), "second content")
				wantDirFiles(t, dir, "repeat.m4b")
			}),
		scenario("route-r203", "INV-ROUTE-R203-WRITE", "normal", "changed", "upload-second-series-keeps-the-first-file",
			"After a title was uploaded without a series, uploading it again with a series writes below <folder>/<series>/<title>/ and leaves the first uploaded file and its directory untouched.",
			[]string{uploadRoute, uploadSrc, uploadUISrc}, func(t *testing.T, e *harness.Env) {
				folderID, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				fields := map[string]string{"library": e.IDs["library"], "folder": folderID, "title": "Upload Repeat",
					"author": "", "series": "Second Series"}
				wantStatus(t, "upload with a series", upload(t, e, root, fields, uploadFile("0", "second.m4b", "second series")), http.StatusOK)
				wantFile(t, filepath.Join(folder, "Second Series", "Upload Repeat", "second.m4b"), "second series")
				wantFile(t, filepath.Join(folder, "Upload Repeat", "repeat.m4b"), "second content")
				wantDirFiles(t, filepath.Join(folder, "Upload Repeat"), "repeat.m4b")
			}),
	}
}
