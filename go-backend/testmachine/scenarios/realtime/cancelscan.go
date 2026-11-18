package realtime

// The Socket.IO cancel_scan event: only admins may send it, and it cancels a
// library scan that is currently running by flagging the library at the
// scanner. The observable contract is the scan task and the libraries row:
// a cancelled scan finishes its task with "Task canceled by user" and does not
// move libraries.lastScan, while a rejected or misdirected cancel leaves the
// scan completing normally.
//
// The scenarios stage many untagged folders into the fixture library so a
// scan has enough work to still be running when the cancel arrives, and they
// disable the library watcher first so no watcher scan races the API scan.

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// cancelScanHandler dispatches cancel_scan behind the admin gate.
	cancelScanHandler = "server/SocketAuthority.js:89-94,191-194"
	// cancelScanDispatch is the Server dispatch to the scanner flag.
	cancelScanDispatch = "server/Server.js:163-165"
	// cancelScanFlag is the scanner flag the dispatch sets.
	cancelScanFlag = "server/scanner/LibraryScanner.js:41-44"
	// scanLifecycle is the scan run: the task, the cancel check and the
	// cancelled completion that skips the libraries.lastScan save.
	scanLifecycle = "server/scanner/LibraryScanner.js:88-135,286-293"
	// cancelScanClient is the web client emitting cancel_scan for a running
	// library-scan task (client/components/cards/ItemTaskRunningCard.vue:128).
	cancelScanClient = "client/components/cards/ItemTaskRunningCard.vue:124-132"
)

const (
	scanWait = 120 * time.Second // a library scan triggered through the API
	taskWait = 15 * time.Second  // a socket event the server sends during a scan
)

// taskPayload is the task object of task_started/task_finished events.
type taskPayload struct {
	ID          string  `json:"id"`
	Action      string  `json:"action"`
	IsFinished  bool    `json:"isFinished"`
	IsFailed    bool    `json:"isFailed"`
	Description *string `json:"description"`
	Data        struct {
		LibraryID string `json:"libraryId"`
	} `json:"data"`
}

// waitTaskFinished waits for the next task_finished event of the action.
func waitTaskFinished(t *testing.T, socket *harness.Socket, mark int, action string) taskPayload {
	t.Helper()
	event := socket.Wait(t, mark, "task_finished", scanWait, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload taskPayload
		return json.Unmarshal(args[0], &payload) == nil && payload.Action == action
	})
	return decodeArg[taskPayload](t, event)
}

// waitTaskStarted waits for the next task_started event of the action.
func waitTaskStarted(t *testing.T, socket *harness.Socket, mark int, action string) taskPayload {
	t.Helper()
	event := socket.Wait(t, mark, "task_started", taskWait, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload taskPayload
		return json.Unmarshal(args[0], &payload) == nil && payload.Action == action
	})
	return decodeArg[taskPayload](t, event)
}

// libraryScanStamp returns the libraries.lastScan text of a library.
func libraryScanStamp(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT lastScan FROM libraries WHERE id = ?", libraryID)
	if len(rows) != 1 {
		t.Fatalf("library %s: %d rows", libraryID, len(rows))
	}
	stamp, _ := rows[0]["lastScan"].(string)
	return stamp
}

// setLibrarySettings patches a library's settings through the API.
func setLibrarySettings(t *testing.T, e *harness.Env, libraryID string, settings map[string]any) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+libraryID, map[string]any{"settings": settings})
	requireStatus(t, resp, http.StatusOK, "patch library settings")
}

// disableWatcher stops the watcher of a library so a scenario's file changes
// are only processed by the scans it triggers itself.
func disableWatcher(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	setLibrarySettings(t, e, libraryID, map[string]any{"disableWatcher": true})
}

// enableWatcher starts the watcher of a library again.
func enableWatcher(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	setLibrarySettings(t, e, libraryID, map[string]any{"disableWatcher": false})
}

// stripCopy writes a copy of the fixture audio file without any metadata tags,
// so folder names alone name the book (the same way a ripped library looks).
func stripCopy(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(dst), err)
	}
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := exec.Command(ffmpeg, "-v", "error", "-y", "-i", src, "-map", "0:a", "-map_metadata", "-1", "-write_id3v2", "0", "-c", "copy", dst)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("strip tags into %s: %v: %s", dst, err, output)
	}
}

// stageBookFolder moves a built folder into the audiobooks library folder.
func stageBookFolder(t *testing.T, e *harness.Env, name, author, book string, audio []byte) {
	t.Helper()
	staging := filepath.Join(e.Install, "staging", name)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, book+".mp3"), audio, 0o644); err != nil {
		t.Fatalf("stage %s: %v", name, err)
	}
	target := filepath.Join(e.Media, "audiobooks", author, book)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, target); err != nil {
		t.Fatalf("stage %s: %v", name, err)
	}
}

// stageCancelBooks stages count book folders whose title starts with the
// prefix, backed by copies of one untagged fixture audio file.
func stageCancelBooks(t *testing.T, e *harness.Env, batch, count int, prefix string) {
	t.Helper()
	source := filepath.Join(e.Install, "rt-cancel-source", prefix+".mp3")
	stripCopy(t, filepath.Join(e.Media, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), source)
	audio, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read stripped audio: %v", err)
	}
	for i := 0; i < count; i++ {
		title := prefix + strconv.Itoa(batch) + "-" + strconv.Itoa(i)
		stageBookFolder(t, e, "rt-"+prefix+strconv.Itoa(batch)+"-"+strconv.Itoa(i), "RT Cancel Author", title, audio)
	}
}

// stagedTitles counts the added items whose title starts with the prefix.
func stagedTitles(t *testing.T, e *harness.Env, libraryID, prefix string) int {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID+"/items?limit=1000&minified=1")
	requireStatus(t, resp, http.StatusOK, "list library items")
	var payload struct {
		Results []struct {
			Media struct {
				Metadata struct {
					Title string `json:"title"`
				} `json:"metadata"`
			} `json:"media"`
		} `json:"results"`
	}
	resp.JSON(t, &payload)
	count := 0
	for _, result := range payload.Results {
		if len(result.Media.Metadata.Title) >= len(prefix) && result.Media.Metadata.Title[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}

// cancelScanScenarios covers socket-incoming-cancel-scan. It runs last in the
// packet because it stages folders into the fixture library and leaves them
// behind.
func cancelScanScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-cancel-scan", Invariant: "INV-SOCKET-INCOMING-CANCEL-SCAN", Case: "normal", State: "initial", Assertion: "cancel-scan-admin-stops-running-scan",
			Sources: []string{cancelScanHandler, cancelScanDispatch, cancelScanFlag, scanLifecycle, cancelScanClient},
			Claim:   "An admin's cancel_scan for a running library scan stops it before completion: the task finishes as 'Task canceled by user', libraries.lastScan is not moved and only some of the staged items are added.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				const staged = 220
				disableWatcher(t, e, library)
				stageCancelBooks(t, e, 1, staged, "RT Cancel Book")
				before := libraryScanStamp(t, e, library)

				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				mark := admin.Mark()
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK, "start library scan")
				started := waitTaskStarted(t, admin, mark, "library-scan")
				if started.Data.LibraryID != library {
					t.Fatalf("scan task started for %s: %+v", library, started)
				}
				admin.Emit(t, "cancel_scan", library)

				task := waitTaskFinished(t, admin, mark, "library-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("cancelled task = %+v, want finished and not failed", task)
				}
				if task.Description == nil || *task.Description != "Task canceled by user" {
					t.Fatalf("task description = %v, want \"Task canceled by user\"", task.Description)
				}
				if got := libraryScanStamp(t, e, library); got != before {
					t.Fatalf("lastScan moved from %q to %q although the scan was cancelled", before, got)
				}
				if added := stagedTitles(t, e, library, "RT Cancel Book"); added >= staged {
					t.Fatalf("all %d staged items were added before the cancel took effect", staged)
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-scan", Invariant: "INV-SOCKET-INCOMING-CANCEL-SCAN", Case: "error", State: "failure", Assertion: "cancel-scan-gated-to-admin",
			Sources: []string{cancelScanHandler, cancelScanDispatch, cancelScanFlag, scanLifecycle},
			Claim:   "cancel_scan from an unauthenticated and from a non-admin socket does nothing: the running scan still completes normally and moves libraries.lastScan.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				disableWatcher(t, e, library)
				// New books give the scan enough work to still be running.
				stageCancelBooks(t, e, 2, 60, "RT Gate Book")
				before := libraryScanStamp(t, e, library)

				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				anon := connectAnon(t, e)
				listener, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := admin.Mark()
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK, "start library scan")
				waitTaskStarted(t, admin, mark, "library-scan")

				anon.Emit(t, "cancel_scan", library)
				listener.Emit(t, "cancel_scan", library)
				task := waitTaskFinished(t, admin, mark, "library-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("task = %+v, want finished and not failed", task)
				}
				if task.Description != nil {
					t.Fatalf("a scan a non-admin tried to cancel must complete normally, description %v", *task.Description)
				}
				if got := libraryScanStamp(t, e, library); got == before {
					t.Fatalf("a completed scan must move lastScan; still %q", got)
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-scan", Invariant: "INV-SOCKET-INCOMING-CANCEL-SCAN", Case: "edge", State: "changed", Assertion: "cancel-scan-unknown-library-is-noop",
			Sources: []string{cancelScanHandler, cancelScanDispatch, cancelScanFlag, scanLifecycle},
			Claim:   "An admin's cancel_scan for a library that is not scanning (an unknown id) sets no cancel flag: a following scan completes normally and moves libraries.lastScan.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				disableWatcher(t, e, library)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				mark := admin.Mark()
				admin.Emit(t, "cancel_scan", "no-such-library")
				admin.Emit(t, "cancel_scan", "00000000-0000-0000-0000-000000000000")
				flush(t, admin)

				before := libraryScanStamp(t, e, library)
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK, "start library scan")
				task := waitTaskFinished(t, admin, mark, "library-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("task = %+v, want finished and not failed", task)
				}
				if task.Description != nil {
					t.Fatalf("a scan after idle cancel attempts must complete normally, description %v", *task.Description)
				}
				if got := libraryScanStamp(t, e, library); got == before {
					t.Fatalf("a completed scan must move lastScan; still %q", got)
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-scan", Invariant: "INV-SOCKET-INCOMING-CANCEL-SCAN", Case: "normal", State: "repeat", Assertion: "cancel-scan-double-cancel-single-cancelled-task",
			Sources: []string{cancelScanHandler, cancelScanDispatch, cancelScanFlag, scanLifecycle},
			Claim:   "Two admin cancel_scan events during one running scan cancel it once: exactly one task finishes as 'Task canceled by user' and libraries.lastScan is not moved.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				disableWatcher(t, e, library)
				defer enableWatcher(t, e, library)
				stageCancelBooks(t, e, 3, 120, "RT Repeat Book")
				before := libraryScanStamp(t, e, library)

				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				mark := admin.Mark()
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries/"+library+"/scan", nil), http.StatusOK, "start library scan")
				waitTaskStarted(t, admin, mark, "library-scan")

				admin.Emit(t, "cancel_scan", library)
				admin.Emit(t, "cancel_scan", library)
				task := waitTaskFinished(t, admin, mark, "library-scan")
				if !task.IsFinished || task.IsFailed {
					t.Fatalf("cancelled task = %+v, want finished and not failed", task)
				}
				if task.Description == nil || *task.Description != "Task canceled by user" {
					t.Fatalf("task description = %v, want \"Task canceled by user\"", task.Description)
				}
				if got := countEvents(admin, mark, "task_finished"); got != 1 {
					t.Fatalf("want exactly one cancelled scan task, got %d", got)
				}
				if got := libraryScanStamp(t, e, library); got != before {
					t.Fatalf("lastScan moved from %q to %q although the scan was cancelled", before, got)
				}
			},
		},
	}
}
