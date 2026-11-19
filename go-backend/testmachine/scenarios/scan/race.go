package scan

// The library scanner loads every item row once, after discovery, and processes
// that snapshot (server/scanner/LibraryScanner.js:161-171). The snapshot is
// written back: checkLibraryItemData compares the loaded libraryFiles array
// with the files it discovered and saves the array it holds
// (server/scanner/LibraryItemScanData.js:182-276); BookScanner re-saves the
// same instance when the item's isMissing flag or an ebook supplementary flag
// changes (server/scanner/BookScanner.js:426-429). A write that lands on the
// same row from another request while the scan runs is therefore overwritten by
// the snapshot, even when the scanner decided to save because of data it read
// before that write. This file pins that outcome: it is deliberate upstream
// behaviour, not a desirable one.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	subStaleRow = "INV-SCAN-STALE-ROW-001"
	// scannerLoadSrc is the single findAll the scan processes.
	scannerLoadSrc = "server/scanner/LibraryScanner.js:161-171"
	// scanDataSaveSrc compares and saves the loaded libraryFiles array.
	scanDataSaveSrc = "server/scanner/LibraryItemScanData.js:182-276"
	// bookResaveSrc is the libraryItem save of the loaded instance.
	bookResaveSrc = "server/scanner/BookScanner.js:390-430"
	// deleteFileSrc is the live route that removes a library file mid-scan.
	deleteFileSrc = "server/controllers/LibraryItemController.js:1012-1060"
)

// raceHelperFiles is how many audio files the slow helper book holds: the scan
// probes every modified file one after another, which keeps it busy between the
// helper item and the target item without any sleep.
const raceHelperFiles = 160

// createRaceLibrary creates a book library this scenario owns, with the watcher
// off so only its own scans process the files it stages.
func createRaceLibrary(t *testing.T, e *harness.Env) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries", map[string]any{
		"name":      "Scan Race Library",
		"mediaType": "book",
		"folders":   []map[string]any{{"fullPath": filepath.Join(e.Media, "scan-race-library")}},
		"settings":  map[string]any{"disableWatcher": true},
	})
	wantStatus(t, "create race library", resp, http.StatusOK)
	doc := decode[libraryDocument](t, resp.Body)
	if doc.ID == "" {
		t.Fatalf("created library has no id: %.300s", resp.Body)
	}
	return doc.ID
}

// stageRaceHelper stages a book with many audio files.
func stageRaceHelper(t *testing.T, e *harness.Env) {
	t.Helper()
	source := filepath.Join(e.Install, "race-helper.mp3")
	stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), source)
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	stageFolder(t, e, "race-helper", "scan-race-library/Race Author/A Helper", func(dir string) {
		for i := 0; i < raceHelperFiles; i++ {
			writeFile(t, filepath.Join(dir, fmt.Sprintf("%02d - Helper.mp3", i+1)), body)
		}
	})
}

// itemLastScan returns the libraryItems.lastScan value of one item, or nil.
func itemLastScan(t *testing.T, e *harness.Env, id string) any {
	t.Helper()
	rows := e.Rows(t, "SELECT lastScan FROM libraryItems WHERE id = ?", id)
	if len(rows) != 1 {
		t.Fatalf("item %s: %d rows", id, len(rows))
	}
	return rows[0]["lastScan"]
}

// libraryFileByFilename returns the library file of an item document with that name.
func libraryFileByFilename(t *testing.T, doc item, name string) libraryFile {
	t.Helper()
	for _, file := range doc.LibraryFiles {
		if file.Metadata.Filename == name {
			return file
		}
	}
	t.Fatalf("item %s has no library file %q: %v", doc.ID, name, fileNames(doc))
	return libraryFile{}
}

// raceScenarios pins the stale-snapshot re-save of a running library scan.
func raceScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: subReq, Invariant: subStaleRow, Case: "edge", State: "changed", Assertion: "scan-overwrites-live-item-write",
			Sources: []string{scannerLoadSrc, scanDataSaveSrc, bookResaveSrc, deleteFileSrc},
			Claim:   "A scan that finds one audio file of a two-file book modified loads the item rows before it rescans and writes back the libraryFiles array it loaded; deleting the book's other audio file through the DELETE file route while the scan is still probing an earlier, larger book leaves the media row with the remaining file only, but the scan then stores the deleted file as a library file again although it no longer exists.",
			Run: func(t *testing.T, e *harness.Env) {
				library := createRaceLibrary(t, e)
				stageRaceHelper(t, e)
				libraryScan(t, e, library, false)
				helper := itemIDByRelPath(t, e, "Race Author/A Helper")

				stageFolder(t, e, "race-target", "scan-race-library/Race Author/Z Target", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "01 - Z Target.mp3"))
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "02 - Z Target.mp3"))
				})
				libraryScan(t, e, library, false)
				target := itemIDByRelPath(t, e, "Race Author/Z Target")
				if before := itemDocument(t, e, target); len(before.LibraryFiles) != 2 || len(before.Media.AudioFiles) != 2 {
					t.Fatalf("imported target = %d library files, %d audio files, want 2/2", len(before.LibraryFiles), len(before.Media.AudioFiles))
				}

				// Rewrite every helper audio file and the target's second audio
				// file: the scan then probes all helper files before it reaches the
				// target, which it rescans because "02 - Z Target.mp3" changed.
				changed := filepath.Join(e.Install, "race-changed.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/01 - Part 1.mp3"), changed)
				changedBody, err := os.ReadFile(changed)
				if err != nil {
					t.Fatalf("read %s: %v", changed, err)
				}
				helperDir := filepath.Join(e.Media, "scan-race-library/Race Author/A Helper")
				for i := 0; i < raceHelperFiles; i++ {
					writeFile(t, filepath.Join(helperDir, fmt.Sprintf("%02d - Helper.mp3", i+1)), changedBody)
				}
				targetDir := filepath.Join(e.Media, "scan-race-library/Race Author/Z Target")
				writeFile(t, filepath.Join(targetDir, "02 - Z Target.mp3"), changedBody)

				helperStamp := itemLastScan(t, e, helper)
				libraryStamp := libraryScanStamp(t, e, library)
				wantStatus(t, "race scan", e.Post(t, e.Token(t, "root"), libraryScanPath(library, false), nil), http.StatusOK)
				harness.Eventually(t, scanTimeout, "the race scan to start rescanning the helper item", func() bool {
					return itemLastScan(t, e, helper) != helperStamp
				})

				// The mid-scan live write: remove the target's first audio file.
				deleted := libraryFileByFilename(t, itemDocument(t, e, target), "01 - Z Target.mp3")
				wantStatus(t, "delete the target audio file", e.Delete(t, e.Token(t, "root"), "/api/items/"+target+"/file/"+deleted.Ino), http.StatusOK)
				live := itemDocument(t, e, target)
				requireStrings(t, "libraryFiles after the live delete", sortedLibraryFileNames(live), []string{"02 - Z Target.mp3"})
				if len(live.Media.AudioFiles) != 1 || live.Media.AudioFiles[0].Metadata.Filename != "02 - Z Target.mp3" {
					t.Fatalf("media.audioFiles after the live delete = %+v, want only 02 - Z Target.mp3", live.Media.AudioFiles)
				}

				harness.Eventually(t, scanTimeout, "the race scan to finish", func() bool {
					return libraryScanStamp(t, e, library) != libraryStamp
				})
				after := itemDocument(t, e, target)
				// The scan saved the libraryFiles array it loaded before the
				// delete, so the deleted file is stored as a library file again
				// although it no longer exists on disk ...
				requireStrings(t, "libraryFiles after the race scan", sortedLibraryFileNames(after), []string{"01 - Z Target.mp3", "02 - Z Target.mp3"})
				if e.Count(t, `SELECT COUNT(*) FROM libraryItems li, json_each(li.libraryFiles) lf
					WHERE li.id = ? AND json_extract(lf.value, '$.metadata.filename') = '01 - Z Target.mp3'`, target) != 1 {
					t.Fatal("the deleted library file is not stored on the item row")
				}
				if fileExists(filepath.Join(targetDir, "01 - Z Target.mp3")) {
					t.Fatal("the deleted audio file still exists on disk")
				}
				// ... while the media row keeps the live delete's audio file list.
				if len(after.Media.AudioFiles) != 1 || after.Media.AudioFiles[0].Metadata.Filename != "02 - Z Target.mp3" {
					t.Fatalf("media.audioFiles after the race scan = %+v, want only 02 - Z Target.mp3", after.Media.AudioFiles)
				}
				if e.Count(t, `SELECT COUNT(*) FROM books b JOIN libraryItems li ON li.mediaId = b.id
					WHERE li.id = ? AND json_array_length(b.audioFiles) = 1`, target) != 1 {
					t.Fatal("the book row does not keep exactly the remaining audio file")
				}
			},
		},
	}
}
