package admin_files

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	watcherPath = "/api/watcher/update"
	tasksPath   = "/api/tasks"

	watcherRoute = "server/routers/ApiRouter.js:360"
	watcherSrc   = "server/controllers/MiscController.js:579-614"
	watcherQueue = "server/Watcher.js:188-229,268-330"
	taskSrc      = "server/managers/TaskManager.js:17-37"
	watcherEvent = "client/layouts/default.vue:452-462"
)

// scanTask is one task payload of GET /api/tasks and of the task socket events.
type scanTask struct {
	ID          string         `json:"id"`
	Action      string         `json:"action"`
	Data        map[string]any `json:"data"`
	Title       string         `json:"title"`
	TitleKey    string         `json:"titleKey"`
	TitleSubs   []string       `json:"titleSubs"`
	ShowSuccess bool           `json:"showSuccess"`
	IsFailed    bool           `json:"isFailed"`
	IsFinished  bool           `json:"isFinished"`
	StartedAt   int64          `json:"startedAt"`
	FinishedAt  *int64         `json:"finishedAt"`
}

// watcherTask decodes one task argument of a task event.
func watcherTask(t *testing.T, args []json.RawMessage) (scanTask, bool) {
	t.Helper()
	if len(args) != 1 {
		return scanTask{}, false
	}
	var task scanTask
	if err := json.Unmarshal(args[0], &task); err != nil {
		return scanTask{}, false
	}
	return task, true
}

// waitWatcherTask waits for a task event for one library and returns it.
func waitWatcherTask(t *testing.T, socket *harness.Socket, from int, event, libraryID string, timeout time.Duration) scanTask {
	t.Helper()
	var found scanTask
	socket.Wait(t, from, event, timeout, func(args []json.RawMessage) bool {
		task, ok := watcherTask(t, args)
		if !ok || task.Data["libraryId"] != libraryID {
			return false
		}
		found = task
		return true
	})
	return found
}

// listTasks reads GET /api/tasks as an admin.
func listTasks(t *testing.T, e *harness.Env) []scanTask {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), tasksPath)
	wantStatus(t, "list tasks", resp, http.StatusOK)
	var payload struct {
		Tasks []scanTask `json:"tasks"`
	}
	resp.JSON(t, &payload)
	return payload.Tasks
}

// watcherTasksFor returns the scan tasks the watcher queued for one library.
func watcherTasksFor(t *testing.T, e *harness.Env, libraryID string) []scanTask {
	t.Helper()
	var out []scanTask
	for _, task := range listTasks(t, e) {
		if task.Action == "watcher-scan" && task.Data["libraryId"] == libraryID {
			out = append(out, task)
		}
	}
	return out
}

// waitWatcherIdle waits until the watcher has dispatched the queued file changes.
func waitWatcherIdle(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	harness.Eventually(t, 30*time.Second, "the watcher scan queue to drain", func() bool {
		return len(watcherTasksFor(t, e, libraryID)) == 0
	})
}

// postWatcher posts one filesystem-change notification and requires 200.
func postWatcher(t *testing.T, e *harness.Env, body map[string]any) {
	t.Helper()
	wantStatus(t, "POST "+watcherPath, e.Post(t, e.Token(t, "root"), watcherPath, body), http.StatusOK)
}

// libraryName reads the name the server reports for a library.
func libraryName(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID)
	wantStatus(t, "get library", resp, http.StatusOK)
	name, _ := resp.Object(t)["name"].(string)
	if name == "" {
		t.Fatalf("library %s has no name: %s", libraryID, resp.Body)
	}
	return name
}

func watcherScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r217", "INV-ROUTE-R217-ROUTE", "edge", "initial", "watcher-update-ignores-changes-it-cannot-watch",
			"POST /api/watcher/update answers 200 for a library id the watcher does not know, for a path outside every library folder and for an ignored dotfile, and queues no scan task.",
			[]string{watcherRoute, watcherSrc, watcherQueue}, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				waitWatcherIdle(t, e, e.IDs["library"])
				postWatcher(t, e, map[string]any{"libraryId": "00000000-0000-0000-0000-000000000000",
					"path": filepath.Join(folder, "ghost.m4b"), "type": "unlink"})
				postWatcher(t, e, map[string]any{"libraryId": e.IDs["library"],
					"path": filepath.Join(e.Install, "outside.m4b"), "type": "unlink"})
				postWatcher(t, e, map[string]any{"libraryId": e.IDs["library"],
					"path": filepath.Join(folder, ".hidden.m4b"), "type": "add"})
				// The task is created inside the request, so an immediate read sees it.
				if tasks := watcherTasksFor(t, e, e.IDs["library"]); len(tasks) != 0 {
					t.Fatalf("ignored file changes queued a scan: %+v", tasks)
				}
			}),
		scenario("route-r217", "INV-ROUTE-R217-NOTIFY", "normal", "initial", "watcher-update-notifies-every-socket-and-the-scanner",
			"POST /api/watcher/update with type unlink answers 200, emits task_started for a watcher-scan task of the library (library id and name, translation key, substitution, not finished) to an admin and to a non-admin socket, and about ten seconds later emits task_finished with the same id, no failure and zero added, updated and removed files, without changing library rows.",
			[]string{watcherRoute, watcherSrc, watcherQueue, taskSrc, watcherEvent}, func(t *testing.T, e *harness.Env) {
				libraryID := e.IDs["library"]
				_, folder := libraryFolder(t, e, libraryID)
				name := libraryName(t, e, libraryID)
				waitWatcherIdle(t, e, libraryID)
				rowsBefore := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				admin := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				adminMark, listenerMark := admin.Mark(), listener.Mark()
				postWatcher(t, e, map[string]any{"libraryId": libraryID, "path": filepath.Join(folder, "Watcher Probe", "ghost.m4b"), "type": "unlink"})
				started := waitWatcherTask(t, admin, adminMark, "task_started", libraryID, 5*time.Second)
				listenerStarted := waitWatcherTask(t, listener, listenerMark, "task_started", libraryID, 5*time.Second)
				for what, task := range map[string]scanTask{"admin": started, "listener": listenerStarted} {
					if task.Action != "watcher-scan" || task.Data["libraryId"] != libraryID || task.Data["libraryName"] != name {
						t.Fatalf("%s task_started: %+v", what, task)
					}
					if task.TitleKey != "MessageTaskScanningFileChanges" || len(task.TitleSubs) != 1 || task.TitleSubs[0] != name {
						t.Fatalf("%s task_started title: %+v", what, task)
					}
					if task.IsFinished || task.IsFailed || task.StartedAt <= 0 {
						t.Fatalf("%s task_started state: %+v", what, task)
					}
				}
				if started.ID != listenerStarted.ID {
					t.Fatalf("audiences saw different tasks: %s and %s", started.ID, listenerStarted.ID)
				}
				finished := waitWatcherTask(t, admin, adminMark, "task_finished", libraryID, 30*time.Second)
				if finished.ID != started.ID || !finished.IsFinished || finished.IsFailed || finished.FinishedAt == nil || *finished.FinishedAt < finished.StartedAt {
					t.Fatalf("task_finished: %+v", finished)
				}
				results, _ := finished.Data["scanResults"].(map[string]any)
				for _, key := range []string{"added", "updated", "removed"} {
					if results[key] != float64(0) {
						t.Fatalf("task_finished scanResults.%s = %v, want 0: %+v", key, results[key], finished)
					}
				}
				waitWatcherIdle(t, e, libraryID)
				wantInt(t, "library items after the watched change", e.Count(t, "SELECT COUNT(*) FROM libraryItems"), rowsBefore)
			}),
		scenario("route-r217", "INV-ROUTE-R217-REPEAT", "normal", "repeat", "watcher-update-repeat-queues-one-task",
			"Posting the same unlink notification twice answers 200 both times and lists exactly one watcher-scan task for the library.",
			[]string{watcherRoute, watcherSrc, watcherQueue, taskSrc}, func(t *testing.T, e *harness.Env) {
				libraryID := e.IDs["library"]
				_, folder := libraryFolder(t, e, libraryID)
				name := libraryName(t, e, libraryID)
				waitWatcherIdle(t, e, libraryID)
				body := map[string]any{"libraryId": libraryID, "path": filepath.Join(folder, "repeat.m4b"), "type": "unlink"}
				postWatcher(t, e, body)
				postWatcher(t, e, body)
				tasks := watcherTasksFor(t, e, libraryID)
				if len(tasks) != 1 {
					t.Fatalf("watcher-scan tasks = %d, want 1: %+v", len(tasks), tasks)
				}
				if tasks[0].Action != "watcher-scan" || tasks[0].Data["libraryId"] != libraryID || tasks[0].Data["libraryName"] != name ||
					tasks[0].IsFinished || tasks[0].IsFailed {
					t.Fatalf("queued scan task: %+v", tasks[0])
				}
			}),
		scenario("route-r217", "INV-ROUTE-R217-NOTIFY", "normal", "changed", "watcher-update-rename-notifies-again",
			"After the previous scan was dispatched, POST /api/watcher/update with type rename and an oldPath answers 200, queues a new watcher-scan task and that task finishes without changing library rows.",
			[]string{watcherRoute, watcherSrc, watcherQueue, taskSrc}, func(t *testing.T, e *harness.Env) {
				libraryID := e.IDs["library"]
				_, folder := libraryFolder(t, e, libraryID)
				waitWatcherIdle(t, e, libraryID)
				rowsBefore := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				postWatcher(t, e, map[string]any{"libraryId": libraryID, "type": "rename",
					"oldPath": filepath.Join(folder, "Before.m4b"), "path": filepath.Join(folder, "After.m4b")})
				tasks := watcherTasksFor(t, e, libraryID)
				if len(tasks) != 1 {
					t.Fatalf("rename queued %d scan tasks, want 1: %+v", len(tasks), tasks)
				}
				waitWatcherIdle(t, e, libraryID)
				wantInt(t, "library items after the rename", e.Count(t, "SELECT COUNT(*) FROM libraryItems"), rowsBefore)
			}),
		scenario("route-r217", "INV-ROUTE-R217-ROUTE", "error", "failure", "watcher-update-rejects-callers-and-bodies",
			"POST /api/watcher/update answers 401 without a token, 403 to a non-admin (also with an invalid body), and 400 when libraryId, path or type is missing, when type is unknown and when a rename has no oldPath; no task is queued and nothing changes.",
			[]string{watcherRoute, watcherSrc, watcherQueue}, func(t *testing.T, e *harness.Env) {
				libraryID := e.IDs["library"]
				_, folder := libraryFolder(t, e, libraryID)
				_ = e.Token(t, "listener")
				_ = e.Token(t, "root")
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				waitWatcherIdle(t, e, libraryID)
				anonymousAndListener(t, e, "POST", watcherPath, map[string]any{"libraryId": libraryID, "path": filepath.Join(folder, "x.m4b"), "type": "unlink"})
				root := e.Token(t, "root")
				path := filepath.Join(folder, "x.m4b")
				for name, body := range map[string]map[string]any{
					"empty":          {},
					"no libraryId":   {"path": path, "type": "unlink"},
					"no path":        {"libraryId": libraryID, "type": "unlink"},
					"no type":        {"libraryId": libraryID, "path": path},
					"unknown type":   {"libraryId": libraryID, "path": path, "type": "chmod"},
					"rename no path": {"libraryId": libraryID, "type": "rename"},
				} {
					wantStatus(t, name, e.Post(t, root, watcherPath, body), http.StatusBadRequest)
				}
				wantStatus(t, "listener with an invalid body", e.Post(t, e.Token(t, "listener"), watcherPath, map[string]any{}), http.StatusForbidden)
				if tasks := watcherTasksFor(t, e, libraryID); len(tasks) != 0 {
					t.Fatalf("rejected notifications queued a scan: %+v", tasks)
				}
			}),
	}
}
