package library_data

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	downloadRoute = "server/routers/ApiRouter.js:97"
	downloadSrc   = "server/controllers/LibraryController.js:1422-1459"
	zipSrc        = "server/utils/zipHelpers.js:55-100"
)

var zipName = regexp.MustCompile(`^attachment; filename="LibraryItems-\d+\.zip"$`)

// zipFiles reads a zip answer into entry name -> content, skipping directory entries.
func zipFiles(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("answer is not a zip archive: %v", err)
	}
	out := map[string][]byte{}
	for _, file := range reader.File {
		if strings.HasSuffix(file.Name, "/") {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close() // the archive is in memory; a close error cannot lose data
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := out[file.Name]; dup {
			t.Fatalf("duplicate zip entry %q", file.Name)
		}
		out[file.Name] = data
	}
	return out
}

// diskFiles maps "<dir base>/<relative file>" (or the base name of a single file) to the content on disk.
func diskFiles(t *testing.T, itemPaths ...string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, item := range itemPaths {
		info, err := os.Stat(item)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(item)
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Base(item)] = data
			continue
		}
		err = filepath.WalkDir(item, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, err := filepath.Rel(item, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			out[filepath.Base(item)+"/"+filepath.ToSlash(rel)] = data
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func zipNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// download requests ids with the token in the Authorization header.
func download(t *testing.T, e *harness.Env, token, libraryID, ids string) harness.Response {
	t.Helper()
	return e.Get(t, token, libPath(libraryID, "/download")+"?ids="+ids)
}

func requireZip(t *testing.T, resp harness.Response, want map[string][]byte) {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, "download")
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content type %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !zipName.MatchString(cd) {
		t.Fatalf("content disposition %q", cd)
	}
	got := zipFiles(t, resp.Body)
	if !slices.Equal(zipNames(got), zipNames(want)) {
		t.Fatalf("zip entries %v, want %v", zipNames(got), zipNames(want))
	}
	for name, data := range want {
		if !bytes.Equal(got[name], data) {
			t.Fatalf("entry %q differs from the file on disk", name)
		}
	}
}

func itemPath(t *testing.T, e *harness.Env, key string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT path FROM libraryItems WHERE id = ?", e.IDs[key])
	if len(rows) != 1 {
		t.Fatalf("no item %s", key)
	}
	return rows[0]["path"].(string)
}

func downloadScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-CONTRACT", Case: "normal", State: "stateless", Assertion: "download-two-items-as-zip",
			Sources: []string{downloadRoute, downloadSrc, zipSrc},
			Claim:   "GET download?ids=a,b answers a zip attachment named LibraryItems-<time>.zip holding every file of both item folders under the folder's name with the bytes on disk, also when the token is passed as a query parameter like the web client does.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				want := diskFiles(t, itemPath(t, e, "saga1"), itemPath(t, e, "saga2"))
				requireEqual(t, "fixture files", zipNames(want), []string{
					"Book 1 - The Beginning/01 - Part 1.mp3", "Book 1 - The Beginning/02 - Part 2.mp3", "Book 1 - The Beginning/03 - Part 3.mp3", "Book 1 - The Beginning/cover.jpg",
					"Book 2 - The Middle/The Middle.flac", "Book 2 - The Middle/desc.txt", "Book 2 - The Middle/reader.txt",
				})
				ids := e.IDs["saga1"] + "," + e.IDs["saga2"]
				requireZip(t, download(t, e, e.Token(t, "listener"), e.IDs[bookLib], ids), want)
				viaQuery := e.Do(t, harness.Request{Path: libPath(e.IDs[bookLib], "/download"), Query: map[string][]string{"ids": {ids}, "token": {e.Token(t, "listener")}}})
				requireZip(t, viaQuery, want)
			},
		},
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-FILE-001", Case: "edge", State: "stateless", Assertion: "download-single-file-item-and-podcast-item",
			Sources: []string{downloadRoute, downloadSrc, zipSrc},
			Claim:   "An item that is a single file in the library folder is added under its file name, mixed with a folder item, and a podcast item downloads from a podcast library with all its episodes.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "download-file", nil, nil)
				loose := filepath.Join(lib.Root, "Loose Book.mp3")
				if err := copyFile(filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3"), loose); err != nil {
					t.Fatal(err)
				}
				folder := filepath.Join(lib.Root, "Folder Author", "Folder Book")
				copyDir(t, filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me"), folder)
				lib.scan(t, e, false, 2)
				var ids []string
				for _, item := range itemRows(t, e, lib.ID) {
					ids = append(ids, item.ID)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND isFile = 1", lib.ID) != 1 {
					t.Fatal("setup: one item must be a single file")
				}
				want := diskFiles(t, loose, folder)
				requireEqual(t, "entries", zipNames(want), []string{"Folder Book/Hard Delete Me.mp3", "Loose Book.mp3"})
				requireZip(t, download(t, e, e.Token(t, "root"), lib.ID, strings.Join(ids, ",")), want)
				podcast := diskFiles(t, itemPath(t, e, "podcast"))
				requireEqual(t, "podcast entries", zipNames(podcast), []string{"Fixture Cast/cover.jpg", "Fixture Cast/episode-1.mp3", "Fixture Cast/episode-2.mp3", "Fixture Cast/episode-3.mp3"})
				requireZip(t, download(t, e, e.Token(t, "listener"), e.IDs[podcastLib], e.IDs["podcast"]), podcast)
			},
		},
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-GONE-001", Case: "edge", State: "stateless", Assertion: "download-item-folder-gone-from-disk",
			Sources: []string{downloadRoute, "server/controllers/LibraryController.js:1460-1467", zipSrc},
			Claim:   "When an item's folder was removed from disk after the scan, download still answers 200 with a zip attachment that holds no file of it (the other item's files are there).",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "download-gone", nil, nil)
				source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me")
				kept, gone := filepath.Join(lib.Root, "Gone Author", "Kept Book"), filepath.Join(lib.Root, "Gone Author", "Gone Book")
				copyDir(t, source, kept)
				copyDir(t, source, gone)
				lib.scan(t, e, false, 2)
				var ids []string
				for _, item := range itemRows(t, e, lib.ID) {
					ids = append(ids, item.ID)
				}
				if err := os.RemoveAll(gone); err != nil {
					t.Fatal(err)
				}
				resp := download(t, e, e.Token(t, "root"), lib.ID, strings.Join(ids, ","))
				requireZip(t, resp, diskFiles(t, kept))
			},
		},
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-IDS-001", Case: "edge", State: "stateless", Assertion: "download-ignores-unknown-foreign-and-duplicate-ids",
			Sources: []string{downloadRoute, "server/controllers/LibraryController.js:1432-1459"},
			Claim:   "Ids that are unknown, belong to another library or repeat an id are ignored as long as one item of the library remains, which is zipped once; when none remains the answer is 404.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				want := diskFiles(t, itemPath(t, e, "saga1"))
				for _, ids := range []string{
					e.IDs["saga1"] + "," + missingID, missingID + "," + e.IDs["saga1"] + "," + e.IDs["podcast"], e.IDs["saga1"] + "," + e.IDs["saga1"],
				} {
					requireZip(t, download(t, e, root, e.IDs[bookLib], ids), want)
				}
				for _, ids := range []string{missingID, e.IDs["podcast"], "not-an-id", missingID + "," + e.IDs["podcast"]} {
					requireStatus(t, download(t, e, root, e.IDs[bookLib], ids), http.StatusNotFound, "no item of the library: "+ids)
				}
			},
		},
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-AUTH-001", Case: "error", State: "stateless", Assertion: "download-denied-and-invalid-requests",
			Sources: []string{downloadRoute, downloadSrc, middleware},
			Claim:   "GET download answers 401 without a token, 403 for a user without download permission (before ids are checked) and for one without library access, 404 for an unknown library, 400 for a missing or empty ids and for an invalid limit, and sends no archive.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				noDownload := s.noDownload(t, e)
				path := libPath(e.IDs[bookLib], "/download")
				ids := "?ids=" + e.IDs["saga1"]
				requireStatus(t, e.Get(t, "", path+ids), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, noDownload.Token, path+ids), http.StatusForbidden, "no download permission")
				requireStatus(t, e.Get(t, noDownload.Token, path), http.StatusForbidden, "permission before ids")
				requireStatus(t, e.Get(t, s.podcastOnly(t, e).Token, path+ids), http.StatusForbidden, "no library access")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/download")+ids), http.StatusNotFound, "unknown library")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path), http.StatusBadRequest, "missing ids")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path+"?ids="), http.StatusBadRequest, "empty ids")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path+"?ids=a&ids=b"), http.StatusBadRequest, "ids given twice are not a string")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path+ids+"&limit=-1"), http.StatusBadRequest, "paging before the rest")
			},
		},
		{
			Requirement: "route-r044", Invariant: "INV-ROUTE-R044-ACCESS-001", Case: "error", State: "stateless", Assertion: "download-explicit-and-tag-rules-reject-whole-request",
			Sources: []string{downloadRoute, "server/controllers/LibraryController.js:1439-1444", "server/models/User.js"},
			Claim:   "A user without explicit-content access, a tag user in allow mode and one in deny mode get 403 for the whole request as soon as one requested item is not accessible to them, and the archive when all are accessible.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				allow := s.createUser(t, e, "data-tag-allow", `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":false,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`)
				deny := s.createUser(t, e, "data-tag-deny", `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":true,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`)
				id := e.IDs[bookLib]
				pair := func(a, b string) string { return e.IDs[a] + "," + e.IDs[b] }
				cases := []struct {
					who   string
					token string
					ok    []string
					deny  []string
				}{
					{"no explicit content", e.Token(t, "finisher"), []string{e.IDs["saga1"], pair("saga1", "saga2")}, []string{e.IDs["ebook"], pair("saga1", "ebook"), pair("ebook", "saga1")}},
					{"tag allow mode", allow.Token, []string{e.IDs["novel"], pair("novel", "saga1")}, []string{e.IDs["saga2"], pair("novel", "saga2"), pair("saga2", "novel")}},
					{"tag deny mode", deny.Token, []string{e.IDs["saga2"], pair("saga2", "ebook")}, []string{e.IDs["novel"], pair("saga2", "novel"), pair("novel", "saga2")}},
				}
				for _, c := range cases {
					for _, ids := range c.ok {
						requireStatus(t, download(t, e, c.token, id, ids), http.StatusOK, c.who+" allowed "+ids)
					}
					for _, ids := range c.deny {
						requireStatus(t, download(t, e, c.token, id, ids), http.StatusForbidden, c.who+" denied "+ids)
					}
				}
				requireStatus(t, download(t, e, e.Token(t, "root"), id, pair("ebook", "novel")), http.StatusOK, "admin")
			},
		},
	}
}
