package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// createUserAccess creates a user with an explicit all-libraries permission, so
// a restricted admin can exercise the per-item access checks that ordinary
// non-admins never reach (the tools middleware refuses them first).
func createUserAccess(t *testing.T, e *harness.Env, name, kind string, libraries []string, accessAll bool) string {
	t.Helper()
	const password = "tools-routes-password"
	request := struct {
		Username            string   `json:"username"`
		Password            string   `json:"password"`
		Type                string   `json:"type"`
		IsActive            bool     `json:"isActive"`
		LibrariesAccessible []string `json:"librariesAccessible"`
		Permissions         struct {
			AccessAllLibraries    bool `json:"accessAllLibraries"`
			AccessExplicitContent bool `json:"accessExplicitContent"`
		} `json:"permissions"`
	}{Username: name, Password: password, Type: kind, IsActive: true, LibrariesAccessible: libraries}
	request.Permissions.AccessAllLibraries = accessAll
	request.Permissions.AccessExplicitContent = true
	response := e.Post(t, e.Token(t, "root"), "/api/users", request)
	wantStatus(t, response, 200)
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	response.JSON(t, &body)
	if body.User.ID == "" {
		t.Fatal("created user response has no ID")
	}
	return body.User.ID
}

// loginLimitedAdmin creates and logs in an admin whose library access is
// restricted to the podcast library.
func loginLimitedAdmin(t *testing.T, e *harness.Env, suffix string) (id, token string) {
	t.Helper()
	id = createUserAccess(t, e, "limited-admin-"+suffix, "admin", []string{e.IDs["podcastLibrary"]}, false)
	token, _ = e.Login(t, "limited-admin-"+suffix, "tools-routes-password")
	return id, token
}

// requireNoToolsTasks proves no embed or encode task is running or waiting.
func requireNoToolsTasks(t *testing.T, e *harness.Env, token string) {
	t.Helper()
	if queued := queuedIDs(t, tasksAt(t, e, token, "?include=queue")); len(queued) != 0 {
		t.Fatalf("queued embeds left behind: %v", queued)
	}
	if tasks := embedTasks(tasksAt(t, e, token, "?include=queue")); len(tasks) != 0 {
		t.Fatalf("tools tasks left running: %s", rawTasks(t, tasksDocument{Tasks: tasks}))
	}
}

// checkEncodeRouteAuthorization pins the middleware and validation order of the
// tools routes: anonymous 401, the admin gate 403 before the item lookup,
// unknown ids 404, books without audio 400 with the message the client shows,
// podcast items not found, limited admins 403, and no rejected request admits a
// task.
func checkEncodeRouteAuthorization(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	listener := e.Token(t, "listener")
	_, limited := loginLimitedAdmin(t, e, suffix)
	encodePath := func(id string) string { return "/api/tools/item/" + id + "/encode-m4b" }
	embedPath := func(id string) string { return "/api/tools/item/" + id + "/embed-metadata" }
	// Anonymous and non-admin callers, including with unknown ids: the admin
	// gate runs before the item lookup.
	for _, path := range []func(string) string{encodePath, embedPath} {
		wantStatus(t, e.Post(t, "", path(e.IDs["novel"]), nil), 401)
		wantStatus(t, e.Post(t, listener, path(e.IDs["novel"]), nil), 403)
		wantStatus(t, e.Post(t, listener, path("unknown-id"), nil), 403)
	}
	wantStatus(t, e.Delete(t, "", encodePath(e.IDs["novel"])), 401)
	wantStatus(t, e.Delete(t, listener, encodePath(e.IDs["novel"])), 403)
	wantStatus(t, e.Delete(t, listener, encodePath("unknown-id")), 403)
	// Admin callers: unknown item, unfit books and podcast items.
	for _, path := range []func(string) string{encodePath, embedPath} {
		wantStatus(t, e.Post(t, root, path("unknown-id"), nil), 404)
	}
	wantStatus(t, e.Delete(t, root, encodePath("unknown-id")), 404)
	invalidEncode := e.Post(t, root, encodePath(e.IDs["ebook"]), nil)
	wantStatus(t, invalidEncode, 400)
	if string(invalidEncode.Body) != "Invalid audiobook: no audio tracks" {
		t.Fatalf("encode of a book without audio = %q, want the client-shown message", invalidEncode.Body)
	}
	wantStatus(t, e.Post(t, root, embedPath(e.IDs["ebook"]), nil), 400)
	podcastEncode := e.Post(t, root, encodePath(e.IDs["ep1"]), nil)
	wantStatus(t, podcastEncode, 404)
	if string(podcastEncode.Body) != "Not Found" {
		t.Fatalf("encode of a podcast item = %q, want Not Found", podcastEncode.Body)
	}
	wantStatus(t, e.Post(t, root, embedPath(e.IDs["ep1"]), nil), 404)
	wantStatus(t, e.Delete(t, root, encodePath(e.IDs["ep1"])), 404)
	// Limited admins pass the gate but fail the per-item access check.
	for _, path := range []string{encodePath(e.IDs["saga1"]), embedPath(e.IDs["saga1"])} {
		wantStatus(t, e.Post(t, limited, path, nil), 403)
	}
	// Only the encode route registers DELETE.
	wantStatus(t, e.Delete(t, limited, encodePath(e.IDs["saga1"])), 403)
	requireNoToolsTasks(t, e, root)
}

// checkEncodeSuccess runs a full encode of the single-file novel item, pins the
// sendStatus body and the task documents, and repeats the encode after the
// first finished: both tasks complete with fresh ids, the merged file replaces
// the source and the original stays in the item cache.
func checkEncodeSuccess(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["novel"]
	item := itemAt(t, e, root, id)
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	filename := item.Media.AudioFiles[0].Metadata.Filename
	firstID := ""
	for round := 1; round <= 2; round++ {
		before := itemSize(t, audioPath)
		beforeMod := modTime(t, audioPath)
		socket := e.Socket(t, root)
		mark := socket.Mark()
		response := e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil)
		wantStatus(t, response, 200)
		if string(response.Body) != "OK" {
			t.Fatalf("encode response body = %q, want sendStatus's OK", response.Body)
		}
		started := waitTaskStarted(t, socket, mark, "encode-m4b", id)
		requireCommonTask(t, started, "encode-m4b", id, item.Media.Metadata.Title)
		task := waitEncodeFinished(t, socket, mark, id, false)
		requireSuccessfulEncodeTask(t, task)
		if round == 2 && started.ID == firstID {
			t.Fatalf("repeated encode reused task id %q", firstID)
		}
		firstID = started.ID
		requireEncodeCacheState(t, e, id, filename, true, backupFilename(filename), before)
		if after := modTime(t, audioPath); !after.After(beforeMod) {
			t.Fatalf("audio file was not replaced: %v stayed at %v", audioPath, beforeMod)
		}
		socket.Close()
	}
	requireNoToolsTasks(t, e, root)
}

// checkEncodePending grows the novel audio and starts a long encode, then pins
// the pending-state behaviour: a repeated POST on the same item is refused with
// the message the client shows, unfit and unknown items are refused, a
// different item may encode concurrently, and both encodes finish and clean up.
func checkEncodePending(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["novel"]
	audioPath := itemAt(t, e, root, id).Media.AudioFiles[0].Metadata.Path
	growAudio(t, e, audioPath)
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	waitTaskStarted(t, socket, mark, "encode-m4b", id)
	// The same item is already processing; the body is shown in a toast.
	repeat := e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil)
	wantStatus(t, repeat, 400)
	if string(repeat.Body) != "Audiobook is already processing" {
		t.Fatalf("repeat encode body = %q, want 'Audiobook is already processing'", repeat.Body)
	}
	// Validation precedes the pending check for unfit books, and podcast items
	// are not found at all.
	invalid := e.Post(t, root, "/api/tools/item/"+e.IDs["ebook"]+"/encode-m4b", nil)
	wantStatus(t, invalid, 400)
	if string(invalid.Body) != "Invalid audiobook: no audio tracks" {
		t.Fatalf("invalid encode body = %q, want 'Invalid audiobook: no audio tracks'", invalid.Body)
	}
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+e.IDs["ep1"]+"/encode-m4b", nil), 404)
	// A different item encodes at the same time.
	disposableItem := itemAt(t, e, root, e.IDs["disposable"])
	disposableSize := itemSize(t, disposableItem.Media.AudioFiles[0].Metadata.Path)
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+e.IDs["disposable"]+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	otherStarted := waitTaskStarted(t, socket, mark, "encode-m4b", e.IDs["disposable"])
	other := waitEncodeFinished(t, socket, mark, e.IDs["disposable"], false)
	main := waitEncodeFinished(t, socket, mark, id, false)
	requireSuccessfulEncodeTask(t, other)
	requireSuccessfulEncodeTask(t, main)
	if otherStarted.ID == main.ID {
		t.Fatalf("concurrent encodes shared one task document")
	}
	requireNoToolsTasks(t, e, root)
	// The disposable item's merge finished: its original track moved to the
	// cache under its own name and the merged file replaced it.
	requireEncodeCacheState(t, e, e.IDs["disposable"], "Hard Delete Me.m4b", true, "Hard Delete Me.mp3", disposableSize)
}

// checkEmbedBackupOption embeds a book with backup=1 and pins the option the
// manager records: the task data carries backupFiles, the audio file is
// rewritten and the item cache keeps the backup copy and the metadata file.
func checkEmbedBackupOption(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	item := itemAt(t, e, root, id)
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	filename := item.Media.AudioFiles[0].Metadata.Filename
	beforeMod := modTime(t, audioPath)
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	response := e.Do(t, harness.Request{Method: "POST", Path: "/api/tools/item/" + id + "/embed-metadata", Token: root, Query: map[string][]string{"backup": {"1"}}})
	wantStatus(t, response, 200)
	started := waitTaskStarted(t, socket, mark, "embed-metadata", id)
	requireEmbedOptions(t, started, false, true)
	finished := waitEmbedTaskTerminal(t, socket, mark, id, false)
	requireCommonTask(t, finished, "embed-metadata", id, item.Media.Metadata.Title)
	requireSuccessfulTask(t, finished)
	if after := modTime(t, audioPath); !after.After(beforeMod) {
		t.Fatalf("audio file was not rewritten: %v stayed at %v", audioPath, beforeMod)
	}
	// The backup run keeps the item cache directory with the audio copy and
	// the metadata file (AudioMetadataManager.js:211-228, 256-264).
	cache := itemCachePath(e, id)
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("backup embed should keep the item cache directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, filename)); err != nil {
		t.Fatalf("backup embed should keep the audio copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "ffmetadata.txt")); err != nil {
		t.Fatalf("backup embed should keep the metadata file: %v", err)
	}
}

// checkEmbedCacheReuse embeds the same book again without backup while its
// cache directory still exists from the backup run: the reuse branch must not
// remove the pre-existing directory, only the metadata file it wrote.
func checkEmbedCacheReuse(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	item := itemAt(t, e, root, id)
	filename := item.Media.AudioFiles[0].Metadata.Filename
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	started := waitTaskStarted(t, socket, mark, "embed-metadata", id)
	requireEmbedOptions(t, started, false, false)
	waitEmbedTaskTerminal(t, socket, mark, id, false)
	cache := itemCachePath(e, id)
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("reused cache directory must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, filename)); err != nil {
		t.Fatalf("the earlier backup copy must survive the reuse run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "ffmetadata.txt")); err == nil {
		t.Fatalf("reuse run must remove its metadata file")
	}
	// The scenario leaves no cache behind for later scenarios.
	if err := os.RemoveAll(cache); err != nil {
		t.Fatalf("remove the reused cache directory: %v", err)
	}
}

// checkMissingItem flips the item's persisted isMissing flag (a state only the
// watcher's rescan normally produces) and pins the routes' answers: the encode
// route answers 404 with the client-shown message before any other check, the
// embed route 400 and the batch 400. The flag is restored afterwards.
func checkMissingItem(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	_ = suffix
	e.Stop(t)
	if n := e.Exec(t, "UPDATE libraryItems SET isMissing = 1 WHERE id = ?", id); n != 1 {
		t.Fatalf("isMissing update touched %d rows, want 1", n)
	}
	e.Restart(t)
	notFound := e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b", nil)
	wantStatus(t, notFound, 404)
	if string(notFound.Body) != "Audiobook not found" {
		t.Fatalf("encode of a missing item = %q, want 'Audiobook not found'", notFound.Body)
	}
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/embed-metadata", nil), 400)
	wantStatus(t, e.Post(t, root, "/api/tools/batch/embed-metadata", batchBody{[]string{id}}), 400)
	// Restore the flag so the shared install keeps its state.
	e.Stop(t)
	if n := e.Exec(t, "UPDATE libraryItems SET isMissing = 0 WHERE id = ?", id); n != 1 {
		t.Fatalf("isMissing restore touched %d rows, want 1", n)
	}
	e.Restart(t)
	wantStatus(t, e.Get(t, root, "/api/items/"+id), 200)
}

// checkBatchBusy starts a single embed and submits a batch containing the
// running item: the batch is refused with the message the client shows and the
// other batch member is not admitted, so it produces no task or track event at
// all.
func checkBatchBusy(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	running, rejected := e.IDs["novel"], e.IDs["saga2"]
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, submitItem(t, e, root, running, nil), 200)
	waitTaskStarted(t, socket, mark, "embed-metadata", running)
	response := e.Post(t, root, "/api/tools/batch/embed-metadata", batchBody{[]string{running, rejected}})
	wantStatus(t, response, 400)
	if string(response.Body) != "Library item is already in queue or processing" {
		t.Fatalf("busy batch body = %q, want 'Library item is already in queue or processing'", response.Body)
	}
	waitEmbedTaskTerminal(t, socket, mark, running, false)
	for _, event := range socket.Events()[mark:] {
		if taskEventNames[event.Name] && containsArg(event.Args, `"libraryItemId":"`+rejected+`"`) {
			t.Fatalf("rejected batch member produced a %s event: %s", event.Name, event.Args)
		}
	}
}

// taskEventNames are the socket events a tools job produces.
var taskEventNames = map[string]bool{
	"task_started": true, "task_finished": true,
	"track_started": true, "track_finished": true, "track_progress": true, "task_progress": true,
}

// checkEncodeDeleteAfterFinish starts and finishes an encode, then pins the
// idle-state DELETE: the finished task is no longer pending, so the cancel
// route answers 404 and other items are equally unaffected.
func checkEncodeDeleteAfterFinish(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["novel"]
	audioPath := itemAt(t, e, root, id).Media.AudioFiles[0].Metadata.Path
	growAudio(t, e, audioPath)
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	waitEncodeFinished(t, socket, mark, id, false)
	requireNoToolsTasks(t, e, root)
	wantStatus(t, e.Delete(t, root, "/api/tools/item/"+id+"/encode-m4b"), 404)
	// A different item without a pending task is also not found, while its own
	// item state is untouched.
	wantStatus(t, e.Delete(t, root, "/api/tools/item/"+e.IDs["saga2"]+"/encode-m4b"), 404)
	requireNoToolsTasks(t, e, root)
}

// checkBatchForce submits a one-item batch with forceEmbedChapters=1 and pins
// the recorded option: the admitted task carries forceEmbedChapters and
// finishes successfully.
func checkBatchForce(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	item := itemAt(t, e, root, id)
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, submitBatch(t, e, root, []string{id}, forceChapters()), 200)
	started := waitTaskStarted(t, socket, mark, "embed-metadata", id)
	requireEmbedOptions(t, started, true, false)
	if started.Data.Chapters == nil {
		t.Fatalf("forced embed chapters = null, want the single-file book's chapter list")
	}
	finished := waitEmbedTaskTerminal(t, socket, mark, id, false)
	requireSuccessfulTask(t, finished)
	requireCommonTask(t, finished, "embed-metadata", id, item.Media.Metadata.Title)
	requireNoToolsTasks(t, e, root)
}

// checkBatchValidation pins the batch route's authorisation and validation:
// anonymous 401, non-admin and restricted-admin 403, empty body 400 with the
// client-shown message, unknown and podcast ids 404, unfit books 400 before
// later entries are fetched or admitted, and nothing admitted by rejections.
func checkBatchValidation(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	listener := e.Token(t, "listener")
	_, limited := loginLimitedAdmin(t, e, suffix)
	batch := "/api/tools/batch/embed-metadata"
	wantStatus(t, e.Post(t, "", batch, batchBody{[]string{e.IDs["saga1"]}}), 401)
	wantStatus(t, e.Post(t, listener, batch, batchBody{[]string{e.IDs["saga1"]}}), 403)
	wantStatus(t, e.Post(t, limited, batch, batchBody{[]string{e.IDs["saga1"]}}), 403)
	empty := e.Post(t, root, batch, batchBody{[]string{}})
	wantStatus(t, empty, 400)
	if string(empty.Body) != "Invalid request payload" {
		t.Fatalf("empty batch body = %q, want 'Invalid request payload'", empty.Body)
	}
	wantStatus(t, e.Post(t, root, batch, batchBody{[]string{"unknown-id"}}), 404)
	wantStatus(t, e.Post(t, root, batch, batchBody{[]string{e.IDs["ep1"]}}), 404)
	// A silent admin socket watches for admissions while the rejections run.
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	unfit := e.Post(t, root, batch, batchBody{[]string{e.IDs["saga1"], e.IDs["ebook"]}})
	wantStatus(t, unfit, 400)
	if string(unfit.Body) != "Bad Request" {
		t.Fatalf("unfit batch body = %q, want sendStatus's Bad Request", unfit.Body)
	}
	// The first entry's failure wins over a later unknown id: the loop checks
	// each item in order.
	wantStatus(t, e.Post(t, root, batch, batchBody{[]string{e.IDs["ebook"], "unknown-id"}}), 400)
	barrierPing(t, socket)
	for _, event := range socket.Events()[mark:] {
		if event.Name == "task_started" || event.Name == "task_finished" {
			if containsArg(event.Args, `"action":"embed-metadata"`) {
				t.Fatalf("a rejected batch admitted a task: %s", event.Args)
			}
		}
	}
	requireNoToolsTasks(t, e, root)
}

// barrierPing sends a ping and waits for the pong, freezing the transcript.
func barrierPing(t *testing.T, socket *harness.Socket) {
	t.Helper()
	mark := socket.Mark()
	socket.Emit(t, "ping")
	socket.Wait(t, mark, "pong", 10*time.Second, nil)
}

// checkBatchFlow submits a two-item batch, waits for both embeds, and repeats
// the identical batch: both rounds admit and finish every member with fresh
// task documents and leave no task behind.
func checkBatchFlow(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	first, second := e.IDs["saga2"], e.IDs["novel"]
	socket := e.Socket(t, root)
	defer socket.Close()
	previous := make([]string, 0, 4)
	for round := 1; round <= 2; round++ {
		mark := socket.Mark()
		wantStatus(t, submitBatch(t, e, root, []string{first, second}, nil), 200)
		startedFirst := waitTaskStarted(t, socket, mark, "embed-metadata", first)
		waitEmbedTaskTerminal(t, socket, mark, first, false)
		waitTaskStarted(t, socket, mark, "embed-metadata", second)
		waitEmbedTaskTerminal(t, socket, mark, second, false)
		if slices.Contains(previous, startedFirst.ID) {
			t.Fatalf("repeated batch reused task id %q", startedFirst.ID)
		}
		previous = append(previous, startedFirst.ID)
	}
	requireNoToolsTasks(t, e, root)
}

// taskEventIndex returns the transcript index of the first event at or after
// from that the matcher accepts.
func taskEventIndex(socket *harness.Socket, from int, name string, match func([]json.RawMessage) bool) int {
	for i, event := range socket.Events() {
		if i >= from && event.Name == name && (match == nil || match(event.Args)) {
			return i
		}
	}
	return -1
}

// checkBatchDuplicateIds submits one item twice inside a single batch: both
// entries are admitted, the first runs while the second waits in the queue, and
// both finish in submission order with distinct task documents.
func checkBatchDuplicateIds(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	socket := e.Socket(t, root)
	defer socket.Close()
	mark := socket.Mark()
	wantStatus(t, submitBatch(t, e, root, []string{id, id}, nil), 200)
	firstStarted := waitTaskStarted(t, socket, mark, "embed-metadata", id)
	if queued := queuedIDs(t, tasksAt(t, e, root, "?include=queue")); len(queued) != 1 || queued[0] != id {
		t.Fatalf("queued ids while the duplicate waits = %v, want [%s]", queued, id)
	}
	waitEmbedTaskTerminal(t, socket, mark, id, false)
	// The queued duplicate starts only after the first task finished.
	finish := taskEventIndex(socket, mark, "task_finished", embedItemMatcher(id))
	secondStarted := waitTaskStarted(t, socket, finish+1, "embed-metadata", id)
	if secondStarted.ID == firstStarted.ID {
		t.Fatalf("both batch entries shared task id %q", firstStarted.ID)
	}
	waitEmbedTaskTerminal(t, socket, finish+1, id, false)
	if count := countEvents(socket, mark, "task_started", embedItemMatcher(id)); count != 2 {
		t.Fatalf("batch produced %d task_started events for the item, want 2", count)
	}
	if count := countEvents(socket, mark, "task_finished", embedItemMatcher(id)); count != 2 {
		t.Fatalf("batch produced %d task_finished events for the item, want 2", count)
	}
	requireNoToolsTasks(t, e, root)
}

// countEvents counts the recorded events matching the name and matcher.
func countEvents(socket *harness.Socket, from int, name string, match func([]json.RawMessage) bool) int {
	count := 0
	for i, event := range socket.Events() {
		if i >= from && event.Name == name && (match == nil || match(event.Args)) {
			count++
		}
	}
	return count
}
