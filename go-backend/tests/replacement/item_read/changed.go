package item_read

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	downloadsSource = "server/controllers/LibraryItemController.js:163-197"
	zipSource       = "server/utils/zipHelpers.js:6-55"
	tokenSource     = "server/auth/TokenManager.js:277-305"
)

// rootFile is the single-file library item that scenarios create in the library root.
type rootFile struct {
	mu     sync.Mutex
	itemID string
	file   fileInfo
}

// create copies the novel's m4b into the library root, scans, and returns the item.
func (r *rootFile) create(t *testing.T, e *harness.Env) (string, fileInfo) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.itemID != "" {
		return r.itemID, r.file
	}
	source := fileNamed(t, e, e.IDs["novel"], "Single File Novel.m4b")
	target := filepath.Join(filepath.Dir(filepath.Dir(itemPath(t, e, e.IDs["disposable"]))), "Solo Book.m4b")
	if err := os.WriteFile(target, readDisk(t, source.Path), 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
	wantStatus(t, "scan", e.Post(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"/scan", nil), http.StatusOK)
	harness.Eventually(t, 30*time.Second, "the scan to add the root file item", func() bool {
		return e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE relPath = 'Solo Book.m4b' AND isFile = 1") == 1
	})
	row := firstRow(t, e, "SELECT id FROM libraryItems WHERE relPath = 'Solo Book.m4b'")
	r.itemID, _ = row["id"].(string)
	r.file = filesOfItem(t, e, r.itemID)[0]
	return r.itemID, r.file
}

// newAccount creates a user with the given permissions and returns its id and token.
func newAccount(t *testing.T, e *harness.Env, username string, perms map[string]bool) (id, token string) {
	t.Helper()
	body := map[string]any{"username": username, "password": accountPassword, "type": "user", "isActive": true, "permissions": perms}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, "create "+username, resp, http.StatusOK)
	id, _ = resp.Object(t)["user"].(map[string]any)["id"].(string)
	token, _ = e.Login(t, username, accountPassword)
	return id, token
}

// changedScenarios holds the lifecycle scenarios of the downloads (sub-downloads) and the
// scenarios that create or remove library files. They run last.
func changedScenarios(s *setup) []harness.Scenario {
	root := &rootFile{}
	sub := func(invariant, assertion, caseName, state, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "sub-downloads", Invariant: invariant, Case: caseName, State: state, Assertion: assertion,
			Sources: []string{downloadsSource, zipSource, middlewareSource}, Claim: claim, Run: run,
		}
	}
	return []harness.Scenario{
		harness.Scenario{
			Requirement: "route-r063", Invariant: "INV-ROUTE-R063-CONTRACT", Case: "edge", State: "stateless", Assertion: "metadata-object-after-edit-with-subtitle",
			Sources: []string{metadataObjectSource, ffmetadataSource},
			Claim:   "After the book is edited to have a subtitle, publisher and description, the object gains TIT3, a subtitled album, copyright, publisher, comment and description.",
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				body := map[string]any{"metadata": map[string]any{"subtitle": "A Subtitle", "publisher": "Ir Press", "description": "Edited description"}}
				wantStatus(t, "edit", e.Patch(t, e.Token(t, "root"), "/api/items/"+id+"/media", body), http.StatusOK)
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id+"/metadata-object")
				wantStatus(t, "metadata object", resp, http.StatusOK)
				got := resp.Object(t)
				requireSame(t, "edited metadata object", ffMetadata(t, e, id), got)
				for key, want := range map[string]string{
					"TIT3": "A Subtitle", "album": "Single File Novel: A Subtitle", "copyright": "Ir Press", "publisher": "Ir Press",
					"comment": "Edited description", "description": "Edited description",
				} {
					if got[key] != want {
						t.Fatalf("%s = %v, want %q", key, got[key], want)
					}
				}
			},
		},
		sub("INV-DL-ZIP-001", "sub-zip-first-download", "normal", "initial",
			"The first download of a multi-file item is a zip named after the title with the item's files as entries and unchanged bytes.",
			func(t *testing.T, e *harness.Env) {
				wantItemZip(t, e, get(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs["saga1"]+"/download", nil), e.IDs["saga1"])
			}),
		sub("INV-DL-ZIP-001", "sub-zip-repeat", "normal", "repeat",
			"Downloading the same item again gives a zip with the same entries and bytes, and no stored row changes.",
			func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e)
				for range 2 {
					wantItemZip(t, e, get(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs["saga1"]+"/download", nil), e.IDs["saga1"])
				}
				if snapshotItems(t, e) != before {
					t.Fatal("downloading changed stored rows")
				}
			}),
		sub("INV-DL-ZIP-001", "sub-zip-follows-folder-changes", "edge", "changed",
			"The zip is built from the folder at request time: a file added to the item folder is an entry of the next download and gone from the one after it is removed.",
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				extra := filepath.Join(itemPath(t, e, id), "added-notes.txt")
				if err := os.WriteFile(extra, []byte("added after the scan"), 0o644); err != nil {
					t.Fatalf("write %s: %v", extra, err)
				}
				defer removeIfExists(t, extra)
				resp := get(t, e, e.Token(t, "listener"), "/api/items/"+id+"/download", nil)
				wantItemZip(t, e, resp, id)
				if got := zipEntries(t, resp.Body)["added-notes.txt"]; string(got) != "added after the scan" {
					t.Fatalf("zip entry added-notes.txt = %q", got)
				}
				removeIfExists(t, extra)
				if _, present := zipEntries(t, get(t, e, e.Token(t, "listener"), "/api/items/"+id+"/download", nil).Body)["added-notes.txt"]; present {
					t.Fatal("removed file is still in the zip")
				}
			}),
		sub("INV-DL-GATE-001", "sub-gate-refuses-without-permission", "error", "initial",
			"A user without the download permission gets 403 from the item download and the file download.",
			func(t *testing.T, e *harness.Env) {
				token := s.token(t, e, "nodownload")
				file := fileNamed(t, e, e.IDs["saga1"], "01 - Part 1.mp3")
				wantStatus(t, "item download", get(t, e, token, "/api/items/"+e.IDs["saga1"]+"/download", nil), http.StatusForbidden)
				wantStatus(t, "file download", get(t, e, token, downloadRoute(e.IDs["saga1"], file), nil), http.StatusForbidden)
			}),
		sub("INV-DL-GATE-001", "sub-gate-failure-changes-nothing", "error", "failure",
			"A refused download changes no stored row and no file in the item folder.",
			func(t *testing.T, e *harness.Env) {
				dir := itemPath(t, e, e.IDs["saga1"])
				rows, files := snapshotItems(t, e), directoryFiles(t, dir)
				resp := get(t, e, s.token(t, e, "nodownload"), "/api/items/"+e.IDs["saga1"]+"/download", nil)
				wantStatus(t, "refused download", resp, http.StatusForbidden)
				after := directoryFiles(t, dir)
				if snapshotItems(t, e) != rows || len(after) != len(files) {
					t.Fatal("a refused download changed rows or files")
				}
				for name, content := range files {
					if string(after[name]) != string(content) {
						t.Fatalf("file %s changed", name)
					}
				}
			}),
		sub("INV-DL-GATE-001", "sub-gate-follows-permission-changes", "error", "changed",
			"Revoking a user's download permission takes effect on the next request with the same token, and granting it back restores the download.",
			func(t *testing.T, e *harness.Env) {
				id, token := newAccount(t, e, "ir-revoked", map[string]bool{"download": true})
				path := "/api/items/" + e.IDs["saga2"] + "/download"
				wantItemZip(t, e, get(t, e, token, path, nil), e.IDs["saga2"])
				setDownload := func(allowed bool) {
					resp := e.Patch(t, e.Token(t, "root"), "/api/users/"+id, map[string]any{"permissions": map[string]bool{"download": allowed}})
					wantStatus(t, "patch user", resp, http.StatusOK)
				}
				setDownload(false)
				wantStatus(t, "after revoke", get(t, e, token, path, nil), http.StatusForbidden)
				setDownload(true)
				wantItemZip(t, e, get(t, e, token, path, nil), e.IDs["saga2"])
			}),
		sub("INV-DL-GATE-001", "sub-gate-api-key-user", "edge", "initial",
			"An API key resolves to its user: a key of a user without download permission is refused with 403, a key of a user with it receives the zip.",
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				key := func(username string, perms map[string]bool) string {
					id, _ := newAccount(t, e, username, perms)
					resp := e.Post(t, root, "/api/api-keys", map[string]any{"name": username, "userId": id, "isActive": true})
					wantStatus(t, "create api key", resp, http.StatusOK)
					value, _ := resp.Object(t)["apiKey"].(map[string]any)["apiKey"].(string)
					if value == "" {
						t.Fatal("api key response has no key")
					}
					return value
				}
				path := "/api/items/" + e.IDs["saga2"] + "/download"
				wantStatus(t, "key without download", get(t, e, key("ir-key-denied", map[string]bool{"download": false}), path, nil), http.StatusForbidden)
				wantItemZip(t, e, get(t, e, key("ir-key-allowed", map[string]bool{"download": true}), path, nil), e.IDs["saga2"])
			}),
		sub("INV-DL-FILE-ORDER-001", "sub-file-lookup-before-gate", "error", "initial",
			"For a user without download permission, a file id that is not in the item is 404 and a real file id is 403.",
			func(t *testing.T, e *harness.Env) {
				token := s.token(t, e, "nodownload")
				file := fileNamed(t, e, e.IDs["saga2"], "The Middle.flac")
				wantStatus(t, "unknown file", get(t, e, token, "/api/items/"+e.IDs["saga2"]+"/file/0/download", nil), http.StatusNotFound)
				wantStatus(t, "real file", get(t, e, token, downloadRoute(e.IDs["saga2"], file), nil), http.StatusForbidden)
			}),
		sub("INV-DL-FILE-ORDER-001", "sub-file-lookup-repeat", "error", "repeat",
			"Repeating the refused and the missing-file requests gives the same statuses every time.",
			func(t *testing.T, e *harness.Env) {
				token := s.token(t, e, "nodownload")
				file := fileNamed(t, e, e.IDs["saga2"], "The Middle.flac")
				for range 2 {
					wantStatus(t, "unknown file", get(t, e, token, "/api/items/"+e.IDs["saga2"]+"/file/0/download", nil), http.StatusNotFound)
					wantStatus(t, "real file", get(t, e, token, downloadRoute(e.IDs["saga2"], file), nil), http.StatusForbidden)
				}
			}),
		harness.Scenario{
			Requirement: "route-r052", Invariant: "INV-R052-SINGLE-001", Case: "normal", State: "stateless", Assertion: "download-single-file-item-without-zip",
			Sources: []string{"server/controllers/LibraryItemController.js:179-186", "server/utils/fileUtils.js:424-429"},
			Claim:   "An item that is one file in the library root is downloaded as that file, not zipped: attachment named after its relative path, the audio media type and the exact bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				id, file := root.create(t, e)
				resp := get(t, e, e.Token(t, "listener"), "/api/items/"+id+"/download", nil)
				wantFileResponse(t, resp, file, "audio/mp4")
				if kind, name := attachment(t, resp); kind != "attachment" || name != "Solo Book.m4b" {
					t.Fatalf("Content-Disposition %q filename %q", kind, name)
				}
				wantStatus(t, "no download permission", get(t, e, s.token(t, e, "nodownload"), "/api/items/"+id+"/download", nil), http.StatusForbidden)
			},
		},
		sub("INV-DL-ZIP-001", "sub-single-file-item-is-not-zipped", "edge", "changed",
			"After a scan adds a single file to the library root, its item downloads as the plain file with its own media type, and its stream and file-download routes serve the same bytes.",
			func(t *testing.T, e *harness.Env) {
				id, file := root.create(t, e)
				token := e.Token(t, "listener")
				wantFileResponse(t, get(t, e, token, "/api/items/"+id+"/download", nil), file, "audio/mp4")
				wantFileResponse(t, get(t, e, token, fileRoute(id, file), nil), file, "audio/mp4")
				wantFileResponse(t, get(t, e, token, downloadRoute(id, file), nil), file, "audio/mp4")
			}),
		harness.Scenario{
			Requirement: "route-r066", Invariant: "INV-R066-SAMPLED", Case: "error", State: "stateless", Assertion: "file-missing-on-disk",
			Sources: []string{getFileSource},
			Claim:   "A library file that is recorded but gone from disk answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				wantMissing(t, e, "saga2", "desc.txt", func(item string, file fileInfo) string { return fileRoute(item, file) })
			},
		},
		harness.Scenario{
			Requirement: "route-r068", Invariant: "INV-R068-HEADERS-001", Case: "error", State: "stateless", Assertion: "file-download-missing-on-disk",
			Sources: []string{downloadFileSource, "server/controllers/LibraryItemController.js:152-160"},
			Claim:   "A library file that is recorded but gone from disk answers 404 on the download route.",
			Run: func(t *testing.T, e *harness.Env) {
				wantMissing(t, e, "saga2", "reader.txt", downloadRoute)
			},
		},
		harness.Scenario{
			Requirement: "route-r069", Invariant: "INV-ROUTE-R069-CONTRACT", Case: "error", State: "stateless", Assertion: "ebook-missing-on-disk",
			Sources: []string{ebookSource, "server/controllers/LibraryItemController.js:152-160"},
			Claim:   "An ebook that is recorded but gone from disk answers 404, with and without file id.",
			Run: func(t *testing.T, e *harness.Env) {
				wantMissing(t, e, "ebook", "Ebook Only.epub", func(item string, file fileInfo) string { return "/api/items/" + item + "/ebook" })
				wantStatus(t, "ebook by id", e.Get(t, e.Token(t, "root"), "/api/items/"+e.IDs["ebook"]+"/ebook/"+fileNamed(t, e, e.IDs["ebook"], "Ebook Only.epub").Ino), http.StatusNotFound)
			},
		},
		sub("INV-DL-FILE-DISK-ERROR-001", "sub-disk-file-missing", "error", "failure",
			"A recorded file whose disk file is gone answers 404 on the stream and download routes, without internal path details, and the stored rows stay unchanged.",
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				file := filesOfItem(t, e, id)[0]
				removeIfExists(t, file.Path)
				rows := snapshotItems(t, e)
				for _, path := range []string{fileRoute(id, file), downloadRoute(id, file)} {
					resp := get(t, e, e.Token(t, "root"), path, nil)
					wantStatus(t, path, resp, http.StatusNotFound)
					if bytesContain(resp.Body, filepath.Dir(file.Path)) {
						t.Fatalf("404 body exposes the server path: %s", resp.Body)
					}
				}
				if snapshotItems(t, e) != rows {
					t.Fatal("failed downloads changed stored rows")
				}
			}),
	}
}

// wantMissing deletes the stored file from disk and checks that the route answers 404 while the row remains.
func wantMissing(t *testing.T, e *harness.Env, itemKey, name string, route func(item string, file fileInfo) string) {
	t.Helper()
	id := e.IDs[itemKey]
	file := fileNamed(t, e, id, name)
	removeIfExists(t, file.Path)
	wantStatus(t, name+" missing on disk", e.Get(t, e.Token(t, "root"), route(id, file)), http.StatusNotFound)
	fileNamed(t, e, id, name) // the library file row is still recorded
}
