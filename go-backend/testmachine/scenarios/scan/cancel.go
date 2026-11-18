package scan

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// cancelScenarios is the last scenario group of the packet: it stages a large
// library so a scan can be cancelled while it is still adding items, and the
// staged folders stay behind.
func cancelScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: precedenceReq, Invariant: precedenceCancel, Case: "error", State: "failure", Assertion: "scan-cancelled-mid-run",
			Sources: []string{"server/scanner/LibraryScanner.js:88-135", "server/scanner/LibraryScanner.js:230-275", "server/SocketAuthority.js:191-194", "server/Server.js:163-165"},
			Claim:   "A scan cancelled through the cancel_scan socket event stops before completing: its task finishes with the message \"Task canceled by user\", the library's lastScan is not moved and fewer than all staged items are added.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				const staged = 200
				source := filepath.Join(e.Install, "cancel-source.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), source)
				for i := 0; i < staged; i++ {
					stageFolder(t, e, "cancel-"+strconv.Itoa(i), "audiobooks/Cancel Author/Cancel Book "+strconv.Itoa(i), func(dir string) {
						copyFile(t, source, filepath.Join(dir, "Cancel Book.mp3"))
					})
				}
				before := libraryScanStamp(t, e, library)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				wantStatus(t, "cancel scan", e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK)
				socket.Emit(t, "cancel_scan", library)
				task := waitTaskFinished(t, socket, mark, "library-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("cancelled task = %+v, want finished and not failed", task)
				}
				if task.Description == nil || *task.Description != "Task canceled by user" {
					t.Fatalf("task description = %v, want \"Task canceled by user\"", ptrText(task.Description))
				}
				if got := libraryScanStamp(t, e, library); got != before {
					t.Fatalf("lastScan moved from %q to %q although the scan was cancelled", before, got)
				}
				added := 0
				for _, title := range itemTitles(t, e, library) {
					if len(title) > len("Cancel Book") && title[:len("Cancel Book")] == "Cancel Book" {
						added++
					}
				}
				if added >= staged {
					t.Fatalf("all %d staged items were added before the cancel took effect", staged)
				}
			},
		},
	}
}
