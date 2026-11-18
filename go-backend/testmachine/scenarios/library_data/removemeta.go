package library_data

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	metaRoute = "server/routers/ApiRouter.js:95"
	metaSrc   = "server/controllers/LibraryController.js:1333-1373"
)

// treeDigest maps every file below dir (relative path) to its content hash.
func treeDigest(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// metaLibrary is a scratch library of four books: A has metadata.json, B metadata.abs, C neither, D both.
type metaLibrary struct {
	scratchLibrary
	folders map[string]string
}

func (s *suite) metaLibrary(t *testing.T, e *harness.Env) *metaLibrary {
	t.Helper()
	if s.meta != nil {
		return s.meta
	}
	lib := newScratchLibrary(t, e, "meta", nil, nil)
	m := &metaLibrary{scratchLibrary: lib, folders: map[string]string{}}
	source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me")
	for _, name := range []string{"A", "B", "C", "D"} {
		folder := filepath.Join(lib.Root, "Meta Author", "Book "+name)
		copyDir(t, source, folder)
		m.folders[name] = folder
	}
	for _, name := range []string{"A", "D"} {
		writeFile(t, filepath.Join(m.folders[name], "metadata.json"), `{"title":"Meta `+name+`"}`)
	}
	for _, name := range []string{"B", "D"} {
		writeFile(t, filepath.Join(m.folders[name], "metadata.abs"), ";ABS metadata\ntitle=Meta "+name+"\n")
	}
	lib.scan(t, e, false, 4)
	for _, file := range []string{"metadata.json", "metadata.abs"} {
		if n := e.Count(t, `SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND libraryFiles LIKE '%"filename":"`+file+`"%'`, lib.ID); n != 2 {
			t.Fatalf("scan must list %s of two items, got %d", file, n)
		}
	}
	s.meta = m
	return m
}

func (m *metaLibrary) has(name, file string) bool {
	return fileExists(filepath.Join(m.folders[name], file))
}

// files reports which of the four metadata files exist: "A.json", "B.abs", ...
func (m *metaLibrary) files() []string {
	var out []string
	for _, name := range []string{"A", "B", "C", "D"} {
		for _, file := range []string{"metadata.json", "metadata.abs"} {
			if m.has(name, file) {
				out = append(out, name+"/"+file)
			}
		}
	}
	return out
}

func libraryFilesColumns(t *testing.T, e *harness.Env, libraryID string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, libraryFiles, updatedAt FROM libraryItems WHERE libraryId = ? ORDER BY id", libraryID)
}

// removeMetadata posts remove-metadata and decodes the answer.
func removeMetadata(t *testing.T, e *harness.Env, libraryID, query string) map[string]any {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), libPath(libraryID, "/remove-metadata")+query, nil)
	requireStatus(t, resp, http.StatusOK, "remove-metadata"+query)
	var body map[string]any
	resp.JSON(t, &body)
	return body
}

func metaScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-AUTH-001", Case: "error", State: "failure", Assertion: "remove-metadata-denied-changes-nothing",
			Sources: []string{metaRoute, metaSrc, middleware},
			Claim:   "POST remove-metadata answers 401 without a token, 403 for a non-admin user (also with update and delete permissions) and without library access, 404 for an unknown library and 400 for an invalid limit before the admin check, and removes no file.",
			Run: func(t *testing.T, e *harness.Env) {
				m := s.metaLibrary(t, e)
				updater := s.createUser(t, e, "data-meta-updater", `"permissions":{"update":true,"delete":true,"upload":true}`)
				limited := s.podcastOnly(t, e)
				before, rows := m.files(), libraryFilesColumns(t, e, m.ID)
				if len(before) != 4 {
					t.Fatalf("setup files: %v", before)
				}
				path := libPath(m.ID, "/remove-metadata")
				requireStatus(t, e.Post(t, "", path, nil), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), path, nil), http.StatusForbidden, "listener")
				requireStatus(t, e.Post(t, updater.Token, path+"?ext=abs", nil), http.StatusForbidden, "updater")
				requireStatus(t, e.Post(t, limited.Token, path, nil), http.StatusForbidden, "limited")
				requireStatus(t, e.Post(t, e.Token(t, "root"), libPath(missingID, "/remove-metadata"), nil), http.StatusNotFound, "unknown")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), path+"?limit=-1", nil), http.StatusBadRequest, "paging before admin")
				requireEqual(t, "files", m.files(), before)
				requireEqual(t, "rows", libraryFilesColumns(t, e, m.ID), rows)
			},
		},
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-CONTRACT", Case: "normal", State: "initial", Assertion: "remove-metadata-json-files-only-in-library",
			Sources: []string{metaRoute, metaSrc, "server/utils/fileUtils.js"},
			Claim:   "POST remove-metadata without ext deletes the metadata.json file of every item of the library and answers {found, removed} counting them, keeps metadata.abs files, other items' files, other libraries and the metadata/items tree, and leaves the stored library file lists unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				m := s.metaLibrary(t, e)
				other := newScratchLibrary(t, e, "meta-other", nil, nil)
				folder := filepath.Join(other.Root, "Other Author", "Other Book")
				copyDir(t, filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me"), folder)
				writeFile(t, filepath.Join(folder, "metadata.json"), `{"title":"Other"}`)
				other.scan(t, e, false, 1)
				// the scan writes the item's metadata file shortly after the item row appears
				otherID := itemRows(t, e, other.ID)[0].ID
				harness.Eventually(t, 10*time.Second, "metadata file of the other item", func() bool {
					return fileExists(filepath.Join(e.Metadata, "items", otherID, "metadata.json"))
				})
				tree := treeDigest(t, filepath.Join(e.Metadata, "items"))
				rows := libraryFilesColumns(t, e, m.ID)
				body := removeMetadata(t, e, m.ID, "")
				requireEqual(t, "answer", body, map[string]any{"found": float64(2), "removed": float64(2)})
				requireEqual(t, "files", m.files(), []string{"B/metadata.abs", "D/metadata.abs"})
				for _, name := range []string{"A", "B", "C", "D"} {
					if !fileExists(filepath.Join(m.folders[name], "Hard Delete Me.mp3")) {
						t.Fatalf("book %s lost its audio file", name)
					}
				}
				if !fileExists(filepath.Join(folder, "metadata.json")) {
					t.Fatal("another library's metadata.json was removed")
				}
				requireEqual(t, "metadata tree", treeDigest(t, filepath.Join(e.Metadata, "items")), tree)
				requireEqual(t, "stored file lists", libraryFilesColumns(t, e, m.ID), rows)
			},
		},
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-REPEAT-001", Case: "edge", State: "repeat", Assertion: "remove-metadata-repeat-counts-gone-files-as-removed",
			Sources: []string{metaRoute, "server/controllers/LibraryController.js:1357-1372"},
			Claim:   "Repeating the removal answers the same {found: 2, removed: 2}: the stored file lists still name the files and deleting a file that is already gone counts as removed; nothing changes.",
			Run: func(t *testing.T, e *harness.Env) {
				m := s.metaLibrary(t, e)
				before := m.files()
				requireEqual(t, "repeat", removeMetadata(t, e, m.ID, ""), map[string]any{"found": float64(2), "removed": float64(2)})
				requireEqual(t, "files", m.files(), before)
			},
		},
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-EXT-001", Case: "edge", State: "changed", Assertion: "remove-metadata-ext-variants",
			Sources: []string{metaRoute, "server/controllers/LibraryController.js:1339-1340"},
			Claim:   "ext=abs deletes the metadata.abs files and keeps everything else; any other ext value (also ABS and txt) selects metadata.json, whose files are already gone but still listed.",
			Run: func(t *testing.T, e *harness.Env) {
				m := s.metaLibrary(t, e)
				for _, query := range []string{"?ext=ABS", "?ext=txt", "?ext=", "?ext=json"} {
					requireEqual(t, query, removeMetadata(t, e, m.ID, query), map[string]any{"found": float64(2), "removed": float64(2)})
					requireEqual(t, query+" files", m.files(), []string{"B/metadata.abs", "D/metadata.abs"})
				}
				requireEqual(t, "ext=abs", removeMetadata(t, e, m.ID, "?ext=abs"), map[string]any{"found": float64(2), "removed": float64(2)})
				requireEqual(t, "files", m.files(), []string(nil))
				requireEqual(t, "ext=abs again", removeMetadata(t, e, m.ID, "?ext=abs"), map[string]any{"found": float64(2), "removed": float64(2)})
				for _, name := range []string{"A", "B", "C", "D"} {
					if !fileExists(filepath.Join(m.folders[name], "Hard Delete Me.mp3")) {
						t.Fatalf("book %s lost its audio file", name)
					}
				}
			},
		},
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-EMPTY-001", Case: "edge", State: "initial", Assertion: "remove-metadata-without-files-answers-found-zero",
			Sources: []string{metaRoute, "server/controllers/LibraryController.js:1352-1356"},
			Claim:   "In a library whose items have no metadata file the answer is exactly {found: 0} without a removed field, for book and podcast libraries, with the default and the abs variant.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, id := range []string{e.IDs[bookLib], e.IDs[podcastLib]} {
					for _, query := range []string{"", "?ext=abs"} {
						requireEqual(t, id+query, removeMetadata(t, e, id, query), map[string]any{"found": float64(0)})
					}
				}
				empty := newScratchLibrary(t, e, "meta-empty", nil, nil)
				requireEqual(t, "empty library", removeMetadata(t, e, empty.ID, ""), map[string]any{"found": float64(0)})
			},
		},
		{
			Requirement: "route-r042", Invariant: "INV-ROUTE-R042-STORE-001", Case: "normal", State: "changed", Assertion: "remove-metadata-file-written-by-store-with-item-setting",
			Sources: []string{metaRoute, metaSrc, "server/models/LibraryItem.js:605-700", "server/controllers/MiscController.js"},
			Claim:   "With storeMetadataWithItem enabled an item update writes metadata.json into the item folder; after a scan lists it, remove-metadata finds and deletes exactly that file.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				requireStatus(t, e.Patch(t, root, "/api/settings", map[string]any{"storeMetadataWithItem": true}), http.StatusOK, "enable setting")
				t.Cleanup(func() { _ = e.Patch(t, root, "/api/settings", map[string]any{"storeMetadataWithItem": false}) }) // best effort: the server may be gone
				lib := newScratchLibrary(t, e, "meta-store", nil, nil)
				folder := filepath.Join(lib.Root, "Store Author", "Store Book")
				copyDir(t, filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me"), folder)
				lib.scan(t, e, false, 1)
				item := itemRows(t, e, lib.ID)[0]
				patchMedia(t, e, item.ID, `{"metadata":{"subtitle":"stored with the item"}}`)
				harness.Eventually(t, 10*time.Second, "metadata.json in the item folder", func() bool { return fileExists(filepath.Join(folder, "metadata.json")) })
				var stored struct {
					Subtitle string `json:"subtitle"`
				}
				data, err := os.ReadFile(filepath.Join(folder, "metadata.json"))
				if err != nil || json.Unmarshal(data, &stored) != nil || stored.Subtitle != "stored with the item" {
					t.Fatalf("metadata.json content: %s (%v)", data, err)
				}
				scanLibrary(t, e, lib.ID)
				harness.Eventually(t, 20*time.Second, "scan to list metadata.json", func() bool {
					return e.Count(t, `SELECT COUNT(*) FROM libraryItems WHERE id = ? AND libraryFiles LIKE '%"filename":"metadata.json"%'`, item.ID) == 1
				})
				requireEqual(t, "answer", removeMetadata(t, e, lib.ID, ""), map[string]any{"found": float64(1), "removed": float64(1)})
				if fileExists(filepath.Join(folder, "metadata.json")) || !fileExists(filepath.Join(folder, "Hard Delete Me.mp3")) {
					t.Fatal("exactly metadata.json must be removed")
				}
				entries, err := os.ReadDir(folder)
				if err != nil || len(entries) != 1 {
					t.Fatalf("item folder must keep only the audio file: %v %v", entries, err)
				}
			},
		},
	}
}
