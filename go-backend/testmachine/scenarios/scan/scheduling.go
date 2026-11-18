package scan

import (
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// schedulingScenarios binds the two automatic producers of scans: the file
// watcher and the library scan cron. They run late in the packet because they
// wait for real time to pass.
func schedulingScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: subReq, Invariant: subWatcher, Case: "normal", State: "changed", Assertion: "watcher-scans-a-new-book",
			Sources: []string{"server/Watcher.js:56-100,200-330", "server/scanner/LibraryScanner.js:374-464", "server/scanner/LibraryItemScanner.js:207-223"},
			Claim:   "With the library watcher enabled, a new book folder produces a watcher-scan task that adds the item without a library scan, so libraries.lastScan stays unchanged while the item appears.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				enableWatcher(t, e, library)
				defer disableWatcher(t, e, library)
				before := libraryScanStamp(t, e, library)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				stageFolder(t, e, "watcher-book", "audiobooks/Watcher Author/Watcher Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Watcher Book.mp3"))
				})
				task := waitTaskFinished(t, socket, mark, "watcher-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("watcher task = %+v, want a finished, successful task", task)
				}
				if task.Data.LibraryID != library {
					t.Fatalf("watcher task library = %q, want %q", task.Data.LibraryID, library)
				}
				doc := itemDocument(t, e, itemIDByRelPath(t, e, "Watcher Author/Watcher Book"))
				if doc.Media.Metadata.Title != "Watcher Book" || doc.IsMissing {
					t.Fatalf("watched item = %+v, want the new book", doc)
				}
				requireStrings(t, "authors", authorNames(doc), []string{"Watcher Author"})
				if got := libraryScanStamp(t, e, library); got != before {
					t.Fatalf("libraries.lastScan moved from %q to %q for a watcher scan", before, got)
				}
			},
		},
		{
			Requirement: subReq, Invariant: subCron, Case: "normal", State: "changed", Assertion: "cron-runs-a-library-scan",
			Sources: []string{"server/managers/CronManager.js:52-84", "server/controllers/LibraryController.js:471-478", "server/scanner/LibraryScanner.js:51-135"},
			Claim:   "Setting autoScanCronExpression to a per-minute schedule makes the server run a library scan by itself: a library-scan task finishes, lastScan moves forward and the newly staged book is imported without any scan request.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				stageFolder(t, e, "cron-book", "audiobooks/Cron Author/Cron Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Cron Book.mp3"))
				})
				before := libraryDocumentOf(t, e, library)
				setLibrarySettings(t, e, library, map[string]any{"autoScanCronExpression": "* * * * *"})
				defer setLibrarySettings(t, e, library, map[string]any{"autoScanCronExpression": nil})
				task := waitTaskFinished(t, socket, mark, "library-scan")
				if !task.IsFinished || task.IsFailed || task.Data.LibraryID != library {
					t.Fatalf("cron task = %+v, want a finished library scan of %s", task, library)
				}
				after := libraryDocumentOf(t, e, library)
				if before.LastScan == nil || after.LastScan == nil || *after.LastScan <= *before.LastScan {
					t.Fatalf("lastScan = %v, want strictly after %v", after.LastScan, before.LastScan)
				}
				doc := itemDocument(t, e, itemIDByRelPath(t, e, "Cron Author/Cron Book"))
				if doc.Media.Metadata.Title != "Cron Book" {
					t.Fatalf("cron item title = %q, want Cron Book", doc.Media.Metadata.Title)
				}
			},
		},
	}
}
