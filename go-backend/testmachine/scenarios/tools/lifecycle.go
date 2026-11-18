package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// taskItemMatcher matches task_started/task_finished payloads of any action
// that carries the item id in data.libraryItemId.
func taskItemMatcher(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		return containsArg(args, `"libraryItemId":"`+id+`"`)
	}
}

// embedItemMatcher matches embed-metadata task payloads for the item.
func embedItemMatcher(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		return containsArg(args, `"action":"embed-metadata"`) && containsArg(args, `"libraryItemId":"`+id+`"`)
	}
}

// taskProgressMatcher matches task_progress payloads for the item.
func taskProgressMatcher(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		return containsArg(args, `"libraryItemId":"`+id+`"`)
	}
}

// requireEmbedOptions pins the task data options of the submitted job.
func requireEmbedOptions(t *testing.T, task taskDocument, force, backup bool) {
	t.Helper()
	if task.Data.Options.ForceEmbedChapters != force || task.Data.Options.BackupFiles != backup {
		t.Fatalf("task data options = %+v, want forceEmbedChapters=%t backupFiles=%t", task.Data.Options, force, backup)
	}
}

// checkEmbedTaskLifecycle runs one idle single-item metadata embed and pins the
// task_started and task_finished payloads and their audiences: every
// authenticated socket receives both task documents, anonymous sockets none,
// and no socket receives a second lifecycle event for the job.
func checkEmbedTaskLifecycle(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga2"]
	item := itemAt(t, e, root, id)
	before := audioModTime(t, item)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	started := make(map[string]taskDocument, len(a.sockets))
	for name, socket := range a.sockets {
		if name == "anonymous" {
			socket.None(t, a.marks[name], "task_started", 300*time.Millisecond, embedItemMatcher(id))
			continue
		}
		started[name] = waitTaskStarted(t, socket, a.marks[name], "embed-metadata", id)
		requireCommonTask(t, started[name], "embed-metadata", id, item.Media.Metadata.Title)
	}
	for name, socket := range a.sockets {
		if name == "anonymous" {
			continue
		}
		finished := waitEmbedTaskTerminal(t, socket, a.marks[name], id, false)
		requireCommonTask(t, finished, "embed-metadata", id, item.Media.Metadata.Title)
		requireSuccessfulTask(t, finished)
		if started[name].ID != finished.ID {
			t.Fatalf("%s: task_started id %q and task_finished id %q differ", name, started[name].ID, finished.ID)
		}
	}
	// The success path rewrote the audio file in place.
	requireRewritten(t, before, item)
	a.barrier(t)
	for _, socket := range a.sockets {
		after := socket.Mark()
		socket.None(t, after, "task_started", 200*time.Millisecond, embedItemMatcher(id))
		socket.None(t, after, "task_finished", 200*time.Millisecond, embedItemMatcher(id))
	}
}

// checkEmbedTaskFailed runs an embed whose target directory is not writable and
// pins the failed task projection: a clean task_started, a task_finished with
// isFailed and the source error key on every authenticated socket, none on
// anonymous ones, and no media change or cache directory.
func checkEmbedTaskFailed(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga2"]
	item := itemAt(t, e, root, id)
	before := audioModTime(t, item)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	restore := makeUnwritable(t, item.libraryItemDir(t))
	defer restore()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	for name, socket := range a.sockets {
		if name == "anonymous" {
			continue
		}
		waitTaskStarted(t, socket, a.marks[name], "embed-metadata", id)
	}
	for name, socket := range a.sockets {
		if name == "anonymous" {
			continue
		}
		failed := waitEmbedTaskTerminal(t, socket, a.marks[name], id, true)
		requireFailedDocument(t, failed, "MessageTaskTargetDirectoryNotWritable", nil)
	}
	requireUnchangedAudio(t, before, item)
	requireNoItemCache(t, e, id)
	a.barrier(t)
	a.sockets["anonymous"].None(t, a.marks["anonymous"], "task_finished", 200*time.Millisecond, embedItemMatcher(id))
}

// checkEmbedTaskRepeat embeds one item twice in a row and pins that each run
// gets a fresh task document: a new id and a startedAt that is not before the
// previous task's finishedAt.
func checkEmbedTaskRepeat(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["disposable"]
	item := itemAt(t, e, root, id)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	firstStarted := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	first := waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, false)
	requireSuccessfulTask(t, first)
	a.reMark()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	secondStarted := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	second := waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, false)
	if secondStarted.ID == firstStarted.ID {
		t.Fatalf("repeated embed reused task id %q", firstStarted.ID)
	}
	if *secondStarted.StartedAt < *first.FinishedAt {
		t.Fatalf("second task startedAt %v precedes the first task's finishedAt %v", *secondStarted.StartedAt, *first.FinishedAt)
	}
	requireCommonTask(t, secondStarted, "embed-metadata", id, item.Media.Metadata.Title)
	requireSuccessfulTask(t, second)
	a.barrier(t)
}

// checkEmbedTaskForce embeds a three-file book with and without
// forceEmbedChapters and pins the task data difference: the forced job records
// the option and carries the book's chapters, the default job records no option
// and carries no chapters. Both jobs report to the same audiences.
func checkEmbedTaskForce(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga1"]
	item := itemAt(t, e, root, id)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	plain := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	requireCommonTask(t, plain, "embed-metadata", id, item.Media.Metadata.Title)
	requireEmbedOptions(t, plain, false, false)
	if plain.Data.Chapters != nil {
		t.Fatalf("default multi-file embed carries chapters %v, want null", plain.Data.Chapters)
	}
	waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, false)
	a.reMark()
	wantStatus(t, submitItem(t, e, root, id, forceChapters()), 200)
	forced := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	requireCommonTask(t, forced, "embed-metadata", id, item.Media.Metadata.Title)
	requireEmbedOptions(t, forced, true, false)
	if forced.Data.Chapters == nil || len(*forced.Data.Chapters) != 3 {
		t.Fatalf("forced embed chapters = %v, want the book's three chapters", forced.Data.Chapters)
	}
	for name, socket := range a.sockets {
		if name == "anonymous" {
			continue
		}
		waitEmbedTaskTerminal(t, socket, a.marks[name], id, false)
	}
	a.barrier(t)
}

// checkEncodeCancel grows the item's audio file so the merge runs for seconds,
// starts an encode, waits until the merge is observably in flight, cancels it
// and pins the defined cancel state: 200 from DELETE, one failed task_finished
// with the source cancel message on every authenticated socket, the item media
// and directory untouched, the cache's merged temp file and metadata file
// removed and the cache directory kept, and no pending task left.
func checkEncodeCancel(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["novel"]
	item := itemAt(t, e, root, id)
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	growAudio(t, e, audioPath)
	before := itemSize(t, audioPath)
	itemDir := filepath.Dir(audioPath)
	targetFilename := "Single File Novel.m4b"
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	// The merge-phase task_progress proves ffmpeg is running: it is emitted
	// only from the merge's progress handler, which runs mid-merge. Cancelling
	// after it lands inside the merge, where the kill is deterministic.
	a.sockets["root"].Wait(t, a.marks["root"], "task_progress", finishedTimeout, taskProgressMatcher(id))
	wantStatus(t, e.Delete(t, root, "/api/tools/item/"+id+"/encode-m4b"), 200)
	task := waitEncodeFinished(t, a.sockets["root"], a.marks["root"], id, true)
	if task.Error == nil || *task.Error != "Task canceled by user" {
		t.Fatalf("canceled task error = %v, want 'Task canceled by user'", task.Error)
	}
	if task.ErrorKey == nil || *task.ErrorKey != "MessageTaskCanceledByUser" {
		t.Fatalf("canceled task errorKey = %v, want MessageTaskCanceledByUser", task.ErrorKey)
	}
	if task.ErrorSubs != nil {
		t.Fatalf("canceled task errorSubs = %v, want null", task.ErrorSubs)
	}
	requireCommonTask(t, encodeTaskProjection(t, task), "encode-m4b", id, item.Media.Metadata.Title)
	if size := itemSize(t, audioPath); size != before {
		t.Fatalf("audio file size %d after cancel, want the untouched %d", size, before)
	}
	entries, err := os.ReadDir(itemDir)
	if err != nil {
		t.Fatalf("read item directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != item.Media.AudioFiles[0].Metadata.Filename {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("item directory after cancel = %v, want only %q", names, item.Media.AudioFiles[0].Metadata.Filename)
	}
	// The temp file is named like the target, which for this item equals the
	// source file name.
	requireEncodeCacheState(t, e, id, targetFilename, false, "", 0)
	for name, socket := range a.sockets {
		if name == "root" || name == "anonymous" {
			continue
		}
		waitEncodeFinished(t, socket, a.marks[name], id, true)
	}
	a.barrier(t)
	// The task left the pending list: a second cancel finds nothing.
	wantStatus(t, e.Delete(t, root, "/api/tools/item/"+id+"/encode-m4b"), 404)
}

// encodeTaskProjection adapts an encode task payload to the shared projection
// checks (the common fields are identical on the wire).
func encodeTaskProjection(t *testing.T, task encodeTask) taskDocument {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("re-encode task: %v", err)
	}
	var doc taskDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode re-encoded task: %v", err)
	}
	return doc
}
