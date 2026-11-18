package item_cover

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// libraryFolder returns the filesystem path of a library folder from the DB.
func libraryFolder(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	row := firstRow(t, e, "SELECT path FROM libraryFolders WHERE libraryId = ?", libraryID)
	path, _ := row["path"].(string)
	return path
}

// lastScan returns the library lastScan value the scan writes when it finishes.
func lastScan(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	raw, _ := firstRow(t, e, "SELECT lastScan FROM libraries WHERE id = ?", libraryID)["lastScan"].(string)
	return raw
}

// addBook writes a new book folder with a real audio file under root.
func addBook(t *testing.T, e *harness.Env, root, relPath string) string {
	t.Helper()
	dir := filepath.Join(root, relPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	source := filepath.Join(e.Media, "audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning", "01 - Part 1.mp3")
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(relPath)+".mp3"), readDisk(t, source), 0o600); err != nil {
		t.Fatalf("write audio in %s: %v", dir, err)
	}
	return dir
}

// scanLibrary starts a library scan and waits for it to finish, returning the
// events the observer saw meanwhile.
func scanLibrary(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	before := lastScan(t, e, libraryID)
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries/"+libraryID+"/scan", nil)
	wantStatus(t, "library scan", resp, http.StatusOK)
	harness.Eventually(t, 60*time.Second, "library scan to finish", func() bool {
		return lastScan(t, e, libraryID) != before
	})
}

// decodeItems decodes an items_added array into old-format item objects.
func decodeItems(t *testing.T, ev harness.Event) []map[string]any {
	t.Helper()
	if len(ev.Args) != 1 {
		t.Fatalf("items_added has %d arguments, want 1", len(ev.Args))
	}
	var items []map[string]any
	if err := json.Unmarshal(ev.Args[0], &items); err != nil {
		t.Fatalf("items_added payload: %v", err)
	}
	return items
}

// socketItemsAddedScenarios covers the items_added library scan event.
func socketItemsAddedScenarios(s *setup) []harness.Scenario {
	const req, inv = "socket-outgoing-items-added", "INV-SOCKET-OUTGOING-ITEMS-ADDED"
	library := func(t *testing.T, e *harness.Env) string { t.Helper(); return e.IDs["library"] }
	return []harness.Scenario{
		scenario(req, inv, "error", "initial", "items-added-not-emitted-to-non-admins",
			"A library scan requested by a non-admin answers 403 and emits no items_added to any socket.",
			srcs("server/controllers/LibraryController.js:1262-1267", libraryScanSrc),
			func(t *testing.T, e *harness.Env) {
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				wantStatus(t, "listener scan", e.Post(t, e.Token(t, "listener"), "/api/libraries/"+library(t, e)+"/scan", nil), http.StatusForbidden)
				o.none(t, "items_added", anyMatch)
			}),
		scenario(req, inv, "normal", "initial", "items-added-one-new-book",
			"A scan that finds one new book emits items_added with an array of one expanded item whose id, path, libraryId and title belong to the created row, to every user with access to the library and to no other socket.",
			srcs(libraryScanSrc, "server/SocketAuthority.js:112-135", "server/scanner/LibraryItemScanner.js:44-90"),
			func(t *testing.T, e *harness.Env) {
				s.quietWatchers(t, e)
				root := libraryFolder(t, e, library(t, e))
				o := observe(t, e, map[string]string{
					"root": e.Token(t, "root"), "listener": e.Token(t, "listener"),
					"podcastonly": s.token(t, e, "podcastonly"), "anonymous": "",
				})
				addBook(t, e, root, "Event Author/First New Book")
				scanLibrary(t, e, library(t, e))
				event := o.expectEvent(t, "root", "items_added", anyMatch)
				items := decodeItems(t, event)
				if len(items) != 1 {
					t.Fatalf("items_added carried %d items, want 1", len(items))
				}
				item := items[0]
				id, _ := item["id"].(string)
				row := itemRow(t, e, id)
				requireEqual(t, "payload id", id, row["id"])
				requireEqual(t, "payload libraryId", item["libraryId"], library(t, e))
				requireEqual(t, "payload path", item["path"], filepath.Join(root, "Event Author/First New Book"))
				requireEqual(t, "payload mediaType", item["mediaType"], "book")
				wantTitle, _ := mediaRow(t, e, id)["title"].(string)
				if wantTitle == "" {
					t.Fatalf("scanned book %s has no title", id)
				}
				requireEqual(t, "payload title", harness.Dig(item, "media", "metadata", "title"), wantTitle)
				requireEqual(t, "payload updatedAt", item["updatedAt"], float64(updatedAt(t, e, id).UnixMilli()))
				listener := decodeItems(t, o.expectEvent(t, "listener", "items_added", anyMatch))
				requireEqual(t, "same payload for the listener", listener, items)
				o.sockets["podcastonly"].None(t, o.marks["podcastonly"], "items_added", absentWindow, anyMatch)
				o.sockets["anonymous"].None(t, o.marks["anonymous"], "items_added", absentWindow, anyMatch)
			}),
		scenario(req, inv, "edge", "changed", "items-added-chunks-of-ten",
			"A scan that finds eleven new books emits two items_added events of ten and one item in scan order, and every item of both events exists in the database.",
			srcs(scannerSrc+":254-280", "server/SocketAuthority.js:112-135"),
			func(t *testing.T, e *harness.Env) {
				s.quietWatchers(t, e)
				root := libraryFolder(t, e, library(t, e))
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				for i := range 11 {
					addBook(t, e, root, filepath.Join("Batch Author", "Batch Book "+itoa(i)))
				}
				scanLibrary(t, e, library(t, e))
				first := decodeItems(t, o.expectEvent(t, "root", "items_added", anyMatch))
				second := decodeItems(t, o.expectEvent(t, "root", "items_added", func(args []json.RawMessage) bool {
					if len(args) != 1 {
						return false
					}
					var items []map[string]any
					return json.Unmarshal(args[0], &items) == nil && len(items) < 10
				}))
				requireEqual(t, "first event size", len(first), 10)
				requireEqual(t, "second event size", len(second), 1)
				for _, item := range append(first, second...) {
					id, _ := item["id"].(string)
					requireEqual(t, "payload row "+id, itemRow(t, e, id)["path"], item["path"])
				}
			}),
		scenario(req, inv, "edge", "repeat", "items-added-not-emitted-for-a-clean-rescan",
			"A scan that finds nothing new finishes without an items_added event.",
			srcs(scannerSrc+":254-280"),
			func(t *testing.T, e *harness.Env) {
				s.quietWatchers(t, e)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				scanLibrary(t, e, library(t, e))
				o.sockets["root"].None(t, o.marks["root"], "items_added", absentWindow, anyMatch)
			}),
		scenario(req, inv, "error", "failure", "items-added-not-emitted-for-a-scan-without-audio",
			"A scan that finds only a folder with no audio file finishes without an items_added event and creates no library item.",
			srcs(scannerSrc+":608-626", "server/scanner/LibraryScanner.js:592-605"),
			func(t *testing.T, e *harness.Env) {
				s.quietWatchers(t, e)
				root := libraryFolder(t, e, library(t, e))
				countBefore := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				if err := os.MkdirAll(filepath.Join(root, "Empty Author", "Empty Book"), 0o755); err != nil {
					t.Fatalf("create empty folder: %v", err)
				}
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				scanLibrary(t, e, library(t, e))
				o.sockets["root"].None(t, o.marks["root"], "items_added", absentWindow, anyMatch)
				requireEqual(t, "libraryItems count", e.Count(t, "SELECT COUNT(*) FROM libraryItems"), countBefore)
			}),
	}
}
