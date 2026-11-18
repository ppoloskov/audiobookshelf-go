package scan

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	subReq        = "sub-scanner-watcher"
	subSemantics  = "INV-SCAN-SEMANTICS-001"
	subIdentity   = "INV-SCAN-IDENTITY-001"
	subAudio      = "INV-SCAN-AUDIO-001"
	subDiscovery  = "INV-SCAN-DISCOVERY-001"
	subPrecedence = "INV-SCAN-PRECEDENCE-001"
	subEvents     = "INV-SCAN-EVENTS-001"
	subSidecar    = "INV-SCAN-SIDECAR-001"
	subWatcher    = "INV-SCAN-WATCHER-001"
	subCron       = "INV-SCAN-CRON-001"
)

// libraryScanTask triggers a library scan, waits for its task_finished event and
// returns the task payload the scanner fills with the added/updated/missing counters.
func libraryScanTask(t *testing.T, e *harness.Env, socket *harness.Socket, libraryID string, force bool) taskPayload {
	t.Helper()
	mark := socket.Mark()
	path := "/api/libraries/" + libraryID + "/scan"
	if force {
		path += "?force=1"
	}
	wantStatus(t, "library scan", e.Post(t, e.Token(t, "root"), path, nil), http.StatusOK)
	return waitTaskFinished(t, socket, mark, "library-scan")
}

// itemsAddedFor matches items_added events whose payload holds the item id.
func itemsAddedFor(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var list []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(args[0], &list) != nil {
			return false
		}
		for _, entry := range list {
			if entry.ID == id {
				return true
			}
		}
		return false
	}
}

// libraryItemCount counts the persisted items of a library.
func libraryItemCount(t *testing.T, e *harness.Env, libraryID string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?", libraryID)
}

// missingCount counts the items of a library flagged missing.
func missingCount(t *testing.T, e *harness.Env, libraryID string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND isMissing = 1", libraryID)
}

// moveAway renames a path out of the library tree and returns an idempotent
// function that puts it back, so a deferred call and an explicit call are safe.
func moveAway(t *testing.T, e *harness.Env, path, name string) func() {
	t.Helper()
	target := filepath.Join(e.Install, name)
	if err := os.Rename(path, target); err != nil {
		t.Fatalf("move %s away: %v", path, err)
	}
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		if err := os.Rename(target, path); err != nil {
			t.Fatalf("restore %s: %v", path, err)
		}
	}
}

// scannerScenarios binds the scanner behaviour the watcher requirement names:
// discovery, audio ordering, identity, precedence, events, sidecar and counters.
func scannerScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: subReq, Invariant: subDiscovery, Case: "normal", State: "initial", Assertion: "discovery-groups-media-and-skips-ignored",
			Sources: []string{"server/utils/scandir.js:48-121", "server/utils/fileUtils.js:140-166,227-249", "server/scanner/LibraryScanner.js:304-367"},
			Claim:   "One scan of a library root adds a single-file item for a media file in the root, folds CD01/CD02 folders into one item, and ignores dot directories, @eaDir, temporary extensions and folders flagged by a .ignore file.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageRoot := filepath.Join(e.Install, "staging-discovery-root", "Discovery Root.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), stageRoot)
				if err := os.MkdirAll(filepath.Join(e.Media, "audiobooks"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(stageRoot, filepath.Join(e.Media, "audiobooks", "Discovery Root.mp3")); err != nil {
					t.Fatal(err)
				}
				stageFolder(t, e, "discovery", "audiobooks/Discovery Author/Disc Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "CD01", "01 - Alpha.mp3"))
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "CD02", "01 - Beta.mp3"))
				})
				stageFolder(t, e, "discovery-ignored", "audiobooks/Discovery Author/Ignored Files", func(dir string) {
					writeFile(t, filepath.Join(dir, "01 - Kept.mp3"), readFile(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3")))
					writeFile(t, filepath.Join(dir, "02 - Partial.mp3.part"), []byte("partial download"))
					writeFile(t, filepath.Join(dir, ".hidden", "secret.mp3"), []byte("hidden"))
					writeFile(t, filepath.Join(dir, "@eaDir", "thumb.mp3"), []byte("vendorthumbnail"))
				})
				stageFolder(t, e, "discovery-ignore-flag", "audiobooks/Discovery Author/Flagged Book", func(dir string) {
					writeFile(t, filepath.Join(dir, ".ignore"), nil)
					writeFile(t, filepath.Join(dir, "Flagged.mp3"), readFile(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3")))
				})
				before := libraryItems(t, e, library).Total
				libraryScan(t, e, library, false)
				if got := libraryItems(t, e, library).Total; got != before+3 {
					t.Fatalf("item total = %d, want %d (root file, CD book, Ignored Files)", got, before+3)
				}
				rootItem := itemDocument(t, e, itemIDByRelPath(t, e, "Discovery Root.mp3"))
				if !rootItem.IsFile || rootItem.Media.Metadata.Title != "Discovery Root" {
					t.Fatalf("root item = isFile %v title %q, want a file item titled Discovery Root", rootItem.IsFile, rootItem.Media.Metadata.Title)
				}
				cd := itemDocument(t, e, itemIDByRelPath(t, e, "Discovery Author/Disc Book"))
				if cd.IsFile || len(cd.Media.AudioFiles) != 2 {
					t.Fatalf("CD item = isFile %v audioFiles %d, want one folder item with two files", cd.IsFile, len(cd.Media.AudioFiles))
				}
				requireStrings(t, "CD file names", sortedLibraryFileNames(cd), []string{"01 - Alpha.mp3", "01 - Beta.mp3"})
				ignored := itemDocument(t, e, itemIDByRelPath(t, e, "Discovery Author/Ignored Files"))
				requireStrings(t, "kept file names", sortedLibraryFileNames(ignored), []string{"01 - Kept.mp3"})
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND relPath LIKE 'Discovery Author/Flagged Book%'", library) != 0 {
					t.Fatal("the folder flagged with .ignore became an item")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subDiscovery, Case: "edge", State: "changed", Assertion: "discovery-drops-podcast-root-files",
			Sources: []string{"server/utils/scandir.js:48-56", "server/scanner/LibraryScanner.js:304-340"},
			Claim:   "A media file in the root of a podcast library becomes no item, because root files are filtered out for podcast libraries.",
			Run: func(t *testing.T, e *harness.Env) {
				podcasts := e.IDs["podcastLibrary"]
				before := libraryItems(t, e, podcasts).Total
				source := filepath.Join(e.Install, "stray.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), source)
				if err := os.Rename(source, filepath.Join(e.Media, "podcasts", "stray-root-episode.mp3")); err != nil {
					t.Fatal(err)
				}
				libraryScan(t, e, podcasts, false)
				if got := libraryItems(t, e, podcasts).Total; got != before {
					t.Fatalf("podcast item total = %d, want the unchanged %d", got, before)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE relPath = 'stray-root-episode.mp3'") != 0 {
					t.Fatal("the podcast root file became an item")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subAudio, Case: "edge", State: "changed", Assertion: "audio-disc-order-and-chapters",
			Sources: []string{"server/scanner/AudioFileScanner.js:52-100", "server/scanner/AudioFileScanner.js:495-573", "server/objects/files/AudioFile.js"},
			Claim:   "The CD01/CD02 book's two files are ordered by disc number and index 1/2, carry the probed mp3 fields, and produce two chapters whose offsets are the cumulative durations in that order.",
			Run: func(t *testing.T, e *harness.Env) {
				doc := itemDocument(t, e, itemIDByRelPath(t, e, "Discovery Author/Disc Book"))
				if len(doc.Media.AudioFiles) != 2 {
					t.Fatalf("audioFiles = %d, want 2", len(doc.Media.AudioFiles))
				}
				for i, file := range doc.Media.AudioFiles {
					if file.Index != i+1 {
						t.Fatalf("audio file %d index = %d, want %d", i, file.Index, i+1)
					}
					if file.DiscNumFromFilename == nil || *file.DiscNumFromFilename != i+1 {
						t.Fatalf("audio file %d disc = %v, want %d", i, file.DiscNumFromFilename, i+1)
					}
					if file.Metadata.Ext != ".mp3" || file.Codec != "mp3" || file.Format == "" || file.Duration <= 0 || file.Ino == "" {
						t.Fatalf("audio file %d = %+v, want probed mp3 fields", i, file)
					}
				}
				if doc.Media.AudioFiles[0].Metadata.RelPath != "CD01/01 - Alpha.mp3" || doc.Media.AudioFiles[1].Metadata.RelPath != "CD02/01 - Beta.mp3" {
					t.Fatalf("audio order = %q, %q, want disc order", doc.Media.AudioFiles[0].Metadata.RelPath, doc.Media.AudioFiles[1].Metadata.RelPath)
				}
				if len(doc.Media.Chapters) != 2 {
					t.Fatalf("chapters = %+v, want 2", doc.Media.Chapters)
				}
				start := 0.0
				for i, file := range doc.Media.AudioFiles {
					chapter := doc.Media.Chapters[i]
					if chapter.ID != i || chapter.Start < start-1e-6 || chapter.End < start {
						t.Fatalf("chapter %d = %+v after %.6f", i, chapter, start)
					}
					start += file.Duration
					if chapter.End < start-1e-6 {
						t.Fatalf("chapter %d ends at %.6f, want at least %.6f", i, chapter.End, start)
					}
				}
			},
		},
		{
			Requirement: subReq, Invariant: subIdentity, Case: "edge", State: "changed", Assertion: "identity-survives-folder-rename",
			Sources: []string{"server/scanner/LibraryScanner.js:169-200", "server/scanner/LibraryItemScanData.js:182-281"},
			Claim:   "Renaming an item folder in place keeps the same item and book ids and the folder inode, updates path and relPath, and keeps the title stored in the item's metadata.json.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByRelPath(t, e, "Discovery Author/Disc Book")
				before := itemDocument(t, e, id)
				from := filepath.Join(e.Media, "audiobooks/Discovery Author/Disc Book")
				to := filepath.Join(e.Media, "audiobooks/Discovery Author/Disc Book Renamed")
				if err := os.Rename(from, to); err != nil {
					t.Fatalf("rename the item folder: %v", err)
				}
				libraryScan(t, e, library, false)
				after := itemDocument(t, e, id)
				if after.ID != id || after.Media.ID != before.Media.ID || after.Ino != before.Ino {
					t.Fatalf("identity changed: item %s, media %s -> %s, ino %s -> %s", id, before.Media.ID, after.Media.ID, before.Ino, after.Ino)
				}
				if after.RelPath != "Discovery Author/Disc Book Renamed" || after.Path != to {
					t.Fatalf("relPath/path = %q/%q, want the renamed folder", after.RelPath, after.Path)
				}
				if after.Media.Metadata.Title != before.Media.Metadata.Title {
					t.Fatalf("title = %q, want the stored %q", after.Media.Metadata.Title, before.Media.Metadata.Title)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND (relPath LIKE 'Discovery Author/Disc Book%')", library) != 1 {
					t.Fatal("the rename left more than one item for the folder")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subPrecedence, Case: "normal", State: "force", Assertion: "precedence-snapshot-applied-by-forced-scan",
			Sources: []string{"server/scanner/LibraryScanner.js:62-67", "server/scanner/LibraryScanner.js:94-104", sourceOrderSrc},
			Claim:   "A forced scan applies the library's persisted metadataPrecedence order and re-persists it unchanged in extraData, so absMetadata first lets the folder title win while the stored sidecar values remain for the other fields.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByRelPath(t, e, "Precedence Author/Precedence Folder")
				order := []string{"absMetadata", "folderStructure"}
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": order})
				before := libraryDocumentOf(t, e, library)
				scanThenAdvanced(t, e, library, true)
				requireStrings(t, "snapshot", lastScanPrecedence(t, e, library), order)
				doc := itemDocument(t, e, id)
				if doc.Media.Metadata.Title != "Precedence Folder" {
					t.Fatalf("title after the forced scan = %q, want the folder title", doc.Media.Metadata.Title)
				}
				requireStrings(t, "series", seriesNames(doc), []string{"Base Series #5"})
				scanThenAdvanced(t, e, library, true)
				requireStrings(t, "snapshot after the repeat", lastScanPrecedence(t, e, library), order)
				if got := itemDocument(t, e, id).Media.Metadata.Title; got != "Precedence Folder" {
					t.Fatalf("title after the repeat forced scan = %q, want the folder title", got)
				}
				if after := libraryDocumentOf(t, e, library); after.LastScan == nil || before.LastScan == nil || *after.LastScan <= *before.LastScan {
					t.Fatalf("lastScan = %v, want after %v", after.LastScan, before.LastScan)
				}
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": defaultPrecedence})
				scanThenAdvanced(t, e, library, false)
			},
		},
		{
			Requirement: subReq, Invariant: subSidecar, Case: "normal", State: "changed", Assertion: "sidecar-written-with-item-not-inside-it",
			Sources: []string{bookWriterSrc, "server/scanner/BookScanner.js:826-840"},
			Claim:   "A scan that adds a book writes metadata/items/<id>/metadata.json only (the item folder gets none), and the written file carries the item's values without becoming one of the item's library files.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				folder := stageFolder(t, e, "sidecar-book", "audiobooks/Sidecar Author/Sidecar Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Sidecar Book.mp3"))
				})
				if fileExists(filepath.Join(folder, "metadata.json")) {
					t.Fatal("the staged folder already holds a metadata.json")
				}
				libraryScan(t, e, library, false)
				id := itemIDByRelPath(t, e, "Sidecar Author/Sidecar Book")
				doc := itemDocument(t, e, id)
				if !fileExists(sidecarPath(e, id)) {
					t.Fatalf("no metadata.json at %s", sidecarPath(e, id))
				}
				if fileExists(filepath.Join(folder, "metadata.json")) {
					t.Fatal("with storeMetadataWithItem off the scan must not write metadata.json into the item folder")
				}
				if slices.Contains(fileNames(doc), "metadata.json") {
					t.Fatalf("libraryFiles = %v, want no metadata.json", fileNames(doc))
				}
				wantSidecarMatchesItem(t, e, id, doc)
				if doc.Media.Metadata.Title != "Sidecar Book" {
					t.Fatalf("title = %q, want the folder title", doc.Media.Metadata.Title)
				}
			},
		},
		{
			Requirement: subReq, Invariant: subEvents, Case: "normal", State: "changed", Assertion: "scan-events-filter-by-library-access",
			Sources: []string{"server/scanner/LibraryScanner.js:224-281", "server/SocketAuthority.js:97-131"},
			Claim:   "A scan that adds a book sends one items_added array holding the expanded item to the root and listener sockets and to no socket of a user restricted to another library or of an unauthenticated client.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				restricted := createLibraryUser(t, e, "scan-podcast-user")
				root := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				other := e.Socket(t, restricted)
				anonymous := e.Socket(t, "")
				marks := []int{root.Mark(), listener.Mark(), other.Mark(), anonymous.Mark()}
				stageFolder(t, e, "event-book", "audiobooks/Event Author/Event Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Event Book.mp3"))
				})
				libraryScan(t, e, library, false)
				id := itemIDByRelPath(t, e, "Event Author/Event Book")
				rootEvent := root.Wait(t, marks[0], "items_added", eventTimeout, itemsAddedFor(id))
				listenerEvent := listener.Wait(t, marks[1], "items_added", eventTimeout, itemsAddedFor(id))
				if !slices.Equal(rootEvent.Args[0], listenerEvent.Args[0]) {
					t.Fatalf("payloads differ: %s vs %s", rootEvent.Args[0], listenerEvent.Args[0])
				}
				var payload []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					Media struct {
						Metadata struct {
							Title string `json:"title"`
						} `json:"metadata"`
					} `json:"media"`
				}
				if err := json.Unmarshal(rootEvent.Args[0], &payload); err != nil {
					t.Fatalf("items_added payload: %v", err)
				}
				if len(payload) != 1 || payload[0].ID != id || payload[0].Media.Metadata.Title != "Event Book" {
					t.Fatalf("items_added payload = %+v, want only the new item", payload)
				}
				other.None(t, marks[2], "items_added", absentWindow, nil)
				anonymous.None(t, marks[3], "items_added", absentWindow, nil)
			},
		},
		{
			Requirement: subReq, Invariant: subSemantics, Case: "normal", State: "repeat", Assertion: "counters-count-missing-on-every-scan",
			Sources: []string{"server/scanner/LibraryScanner.js:169-200", "server/scanner/LibraryScanner.js:106-115"},
			Claim:   "A scan after an item folder disappears reports one missing, zero added and zero updated, flags the row missing, and a second scan reports the same missing count again instead of clearing it.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				saga := e.IDs["saga1"]
				folder := filepath.Join(e.Media, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning")
				restore := moveAway(t, e, folder, "hidden-saga1")
				defer restore()
				socket := e.Socket(t, e.Token(t, "root"))
				first := libraryScanTask(t, e, socket, library, false)
				if first.Data.ScanResults.Missing != 1 || first.Data.ScanResults.Added != 0 || first.Data.ScanResults.Updated != 0 {
					t.Fatalf("first scan counters = %+v, want 0 added, 0 updated, 1 missing", first.Data.ScanResults)
				}
				if first.Data.LibraryID != library || e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ? AND isMissing = 1", saga) != 1 {
					t.Fatalf("task library %q or the row's isMissing flag is wrong", first.Data.LibraryID)
				}
				second := libraryScanTask(t, e, socket, library, false)
				if second.Data.ScanResults.Missing != 1 || second.Data.ScanResults.Added != 0 || second.Data.ScanResults.Updated != 0 {
					t.Fatalf("second scan counters = %+v, want 0 added, 0 updated, 1 missing", second.Data.ScanResults)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ? AND isMissing = 1", saga) != 1 {
					t.Fatal("the second scan cleared the missing flag")
				}
				restore()
				third := libraryScanTask(t, e, socket, library, false)
				if third.Data.ScanResults.Missing != 0 || third.Data.ScanResults.Updated != 1 {
					t.Fatalf("restore scan counters = %+v, want 0 missing and 1 updated", third.Data.ScanResults)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ? AND isMissing = 0", saga) != 1 {
					t.Fatal("the restored item is still flagged missing")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subSemantics, Case: "edge", State: "changed", Assertion: "counters-exempt-podcast-folder-without-episodes",
			Sources: []string{"server/scanner/LibraryScanner.js:182-200"},
			Claim:   "Deleting every episode file of a podcast whose folder still exists leaves the podcast item not missing and reports zero missing, and the episodes stay stored.",
			Run: func(t *testing.T, e *harness.Env) {
				podcasts := e.IDs["podcastLibrary"]
				podcast := e.IDs["podcast"]
				folder := filepath.Join(e.Media, "podcasts/Fixture Cast")
				for _, name := range []string{"episode-1.mp3", "episode-2.mp3", "episode-3.mp3"} {
					restore := moveAway(t, e, filepath.Join(folder, name), "saved-"+name)
					defer restore()
				}
				socket := e.Socket(t, e.Token(t, "root"))
				task := libraryScanTask(t, e, socket, podcasts, false)
				if task.Data.ScanResults.Missing != 0 || task.Data.ScanResults.Added != 0 {
					t.Fatalf("scan counters = %+v, want 0 added and 0 missing", task.Data.ScanResults)
				}
				doc := itemDocument(t, e, podcast)
				if doc.IsMissing {
					t.Fatal("the podcast item was flagged missing although its folder exists")
				}
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId = ?", doc.Media.ID) != 3 {
					t.Fatal("the episodes were removed although the folder still exists")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subSemantics, Case: "error", State: "failure", Assertion: "scan-missing-library-folder-flags-all-items",
			Sources: []string{"server/scanner/LibraryScanner.js:304-311", "server/scanner/LibraryScanner.js:169-200"},
			Claim:   "A scan of a library whose folder path no longer exists adds nothing, reports every item missing, flags all rows missing and still records the completed scan; a scan after the folder returns clears the flags.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				total := libraryItemCount(t, e, library)
				restore := moveAway(t, e, filepath.Join(e.Media, "audiobooks"), "audiobooks-away")
				defer restore()
				socket := e.Socket(t, e.Token(t, "root"))
				task := libraryScanTask(t, e, socket, library, false)
				if task.Data.ScanResults.Added != 0 || task.Data.ScanResults.Missing != total {
					t.Fatalf("scan counters = %+v, want 0 added and %d missing", task.Data.ScanResults, total)
				}
				if got := missingCount(t, e, library); got != total {
					t.Fatalf("missing rows = %d, want all %d", got, total)
				}
				restore()
				task = libraryScanTask(t, e, socket, library, false)
				if task.Data.ScanResults.Missing != 0 || missingCount(t, e, library) != 0 {
					t.Fatalf("restore scan counters = %+v and %d missing rows, want none", task.Data.ScanResults, missingCount(t, e, library))
				}
			},
		},
		{
			Requirement: subReq, Invariant: subAudio, Case: "error", State: "failure", Assertion: "audio-probe-failure-skips-the-candidate",
			Sources: []string{"server/scanner/AudioFileScanner.js:160-180", "server/scanner/BookScanner.js:455-458"},
			Claim:   "A folder whose only audio file cannot be probed becomes no library item and the scan still completes, while the other items of the library keep their rows.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				before := itemTitles(t, e, library)
				total := libraryItemCount(t, e, library)
				stageFolder(t, e, "unprobeable", "audiobooks/Broken Author/Broken Book", func(dir string) {
					writeFile(t, filepath.Join(dir, "Broken.mp3"), []byte("not an audio stream\n"))
				})
				scanThenAdvanced(t, e, library, false)
				if got := libraryItemCount(t, e, library); got != total {
					t.Fatalf("item count = %d, want the unchanged %d", got, total)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE relPath LIKE 'Broken Author%'") != 0 {
					t.Fatal("the unprobeable folder became an item")
				}
				requireStrings(t, "item titles", itemTitles(t, e, library), before)
			},
		},
	}
}

// sortedLibraryFileNames lists the library file names of an item sorted.
func sortedLibraryFileNames(doc item) []string {
	names := make([]string, 0, len(doc.LibraryFiles))
	for _, file := range doc.LibraryFiles {
		names = append(names, file.Metadata.Filename)
	}
	slices.Sort(names)
	return names
}

// fileExists reports whether the path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// createLibraryUser creates a non-admin user whose accessible libraries are the
// podcast library only and returns that user's token.
func createLibraryUser(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	body := map[string]any{
		"username": name, "password": name + "-password", "type": "user", "isActive": true,
		"permissions": map[string]any{"download": true, "accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}},
	}
	wantStatus(t, "create library user", e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK)
	token, _ := e.Login(t, name, name+"-password")
	return token
}
