package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// checkTaskProjection submits a forced four-item batch, then reads the task
// list while it runs: the one running embed carries the full projection the
// task card reads, and include=queue adds the waiting task documents in FIFO
// order with the request's option and media paths.
func checkTaskProjection(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	socket := e.Socket(t, root) // connected before admission so the finish is observed
	defer socket.Close()
	mark := socket.Mark()
	ids := []string{e.IDs["saga1"], e.IDs["novel"], e.IDs["saga2"], e.IDs["disposable"]}
	wantStatus(t, submitBatch(t, e, root, ids, forceChapters()), 200)
	doc := tasksAt(t, e, root, "?include=queue")
	task := requireSingleEmbedTask(t, doc, ids)
	requireRunningTask(t, e, root, task)
	for _, data := range *doc.QueuedTaskData.EmbedMetadata {
		requireQueuedTask(t, e, root, data)
	}
	waitEmbedFinished(t, socket, mark, ids[len(ids)-1])
}

// requireRunningTask pins every field of a running embed projection that the
// bundled client renders or branches on.
func requireRunningTask(t *testing.T, e *harness.Env, token string, task taskDocument) {
	t.Helper()
	if task.ID == "" || task.Action != "embed-metadata" {
		t.Fatalf("running task id/action = %q/%q", task.ID, task.Action)
	}
	if task.Title != "Embedding Metadata" {
		t.Fatalf("task title = %q", task.Title)
	}
	if task.TitleKey == nil || *task.TitleKey != "MessageTaskEmbeddingMetadata" {
		t.Fatalf("task titleKey = %v", task.TitleKey)
	}
	if task.TitleSubs != nil {
		t.Fatalf("task titleSubs = %v, want null", task.TitleSubs)
	}
	item := itemAt(t, e, token, task.Data.LibraryItemID)
	title := item.Media.Metadata.Title
	if task.Description == nil || *task.Description != fmt.Sprintf("Embedding metadata in audiobook %q.", title) {
		t.Fatalf("task description = %v", task.Description)
	}
	if task.DescriptionKey == nil || *task.DescriptionKey != "MessageTaskEmbeddingMetadataDescription" {
		t.Fatalf("task descriptionKey = %v", task.DescriptionKey)
	}
	if !slices.Equal(task.DescriptionSubs, []string{title}) {
		t.Fatalf("task descriptionSubs = %v, want [%s]", task.DescriptionSubs, title)
	}
	if task.ShowSuccess || task.IsFailed || task.IsFinished {
		t.Fatalf("running task flags = showSuccess %t, isFailed %t, isFinished %t", task.ShowSuccess, task.IsFailed, task.IsFinished)
	}
	numericTime(t, "startedAt", task.StartedAt)
	if task.FinishedAt != nil {
		t.Fatalf("running task finishedAt = %v, want null", *task.FinishedAt)
	}
	if task.Error != nil || task.ErrorKey != nil || task.ErrorSubs != nil {
		t.Fatalf("running task error fields = %v/%v/%v, want null", task.Error, task.ErrorKey, task.ErrorSubs)
	}
	if !task.Data.Options.ForceEmbedChapters || task.Data.Options.BackupFiles {
		t.Fatalf("task options = %+v, want forceEmbedChapters true and backupFiles false", task.Data.Options)
	}
	if task.Data.Duration <= 0 || task.Data.MimeType == "" {
		t.Fatalf("task data duration/mimeType = %v/%q", task.Data.Duration, task.Data.MimeType)
	}
	if task.Data.ItemCachePath != itemCachePath(e, task.Data.LibraryItemID) {
		t.Fatalf("task data itemCachePath = %q, want %q", task.Data.ItemCachePath, itemCachePath(e, task.Data.LibraryItemID))
	}
	requireTaskAudioFiles(t, e, item, task.Data)
}

// requireQueuedTask pins the queued task document the client maps to a waiting
// item id and the paths the job will use.
func requireQueuedTask(t *testing.T, e *harness.Env, token string, data taskData) {
	t.Helper()
	if data.LibraryItemID == "" {
		t.Fatal("queued task data has no libraryItemId")
	}
	if !data.Options.ForceEmbedChapters || data.Options.BackupFiles {
		t.Fatalf("queued options = %+v, want forceEmbedChapters true and backupFiles false", data.Options)
	}
	if data.Duration <= 0 || data.MimeType == "" {
		t.Fatalf("queued data duration/mimeType = %v/%q", data.Duration, data.MimeType)
	}
	if data.ItemCachePath != itemCachePath(e, data.LibraryItemID) {
		t.Fatalf("queued itemCachePath = %q, want %q", data.ItemCachePath, itemCachePath(e, data.LibraryItemID))
	}
	requireTaskAudioFiles(t, e, itemAt(t, e, token, data.LibraryItemID), data)
}

// requireTaskAudioFiles pins the job's audio file list against the item.
func requireTaskAudioFiles(t *testing.T, e *harness.Env, item itemDocument, data taskData) {
	t.Helper()
	want := item.audioPaths()
	got := make([]string, len(data.AudioFiles))
	for i, file := range data.AudioFiles {
		if file.Filename == "" || file.Duration <= 0 || file.Index <= 0 {
			t.Fatalf("task audio file %d is incomplete: %+v", i, file)
		}
		if file.CachePath != itemCachePathJoin(e, data.LibraryItemID, file.Filename) {
			t.Fatalf("task audio cachePath = %q, want %q", file.CachePath, itemCachePathJoin(e, data.LibraryItemID, file.Filename))
		}
		got[i] = file.Path
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("task audio file paths = %v, want the item's %v", got, want)
	}
}

// checkTasksAuthorisation proves the route is available to every authenticated
// user the layout loads tasks for, refuses anonymous callers, and that reading
// it changes no task state. It admits a real four-item batch first so both the
// running task and the FIFO queue are non-empty for every comparison: the
// multi-file saga is submitted first to keep the single running slot occupied
// across the handful of reads (AudioMetadataManager.js:117-135), and the batch
// is drained before the scenario returns so the next one starts from idle.
func checkTasksAuthorisation(t *testing.T, e *harness.Env) {
	t.Helper()
	root := e.Token(t, "root")
	socket := e.Socket(t, root) // connected before admission so the finish is observed
	defer socket.Close()
	mark := socket.Mark()
	ids := []string{e.IDs["saga1"], e.IDs["novel"], e.IDs["saga2"], e.IDs["disposable"]}
	wantStatus(t, submitBatch(t, e, root, ids, forceChapters()), 200)

	before := tasksAt(t, e, root, "?include=queue")
	requireSingleEmbedTask(t, before, ids) // both the running task and the queue are real
	requireRunningTask(t, e, root, embedTasks(before)[0])

	for _, path := range []string{"/api/tasks", "/api/tasks?include=queue"} {
		wantStatus(t, e.Get(t, "", path), 401)
	}

	listener := tasksAt(t, e, e.Token(t, "listener"), "?include=queue")
	requireSameTaskProjection(t, listener, before)

	after := tasksAt(t, e, root, "?include=queue")
	requireSameTaskProjection(t, after, before)

	waitEmbedFinished(t, socket, mark, ids[len(ids)-1])
}

// requireSameTaskProjection compares two full decoded GET /api/tasks bodies.
// The handler is a read-only projection (MiscController.js:116-129), so every
// field the bundled layout reads must be identical across callers and reads.
func requireSameTaskProjection(t *testing.T, got, want tasksDocument) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	gotJSON, gotErr := json.Marshal(got)
	wantJSON, wantErr := json.Marshal(want)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("task projections differ (render failed): %v/%v", gotErr, wantErr)
	}
	t.Fatalf("task projection changed across GETs:\n got: %s\nwant: %s", gotJSON, wantJSON)
}

// checkTasksQueryShapes proves the optional include parameter: without it the
// response has only the tasks array; include=queue adds the queue as a real
// empty array; an unrelated include value is ignored. It runs first in the
// packet, before any embed rewrites library files, so the list is genuinely
// empty rather than holding a watcher-triggered scan task.
func checkTasksQueryShapes(t *testing.T, e *harness.Env) {
	t.Helper()
	root := e.Token(t, "root")
	doc := tasksAt(t, e, root, "")
	if doc.QueuedTaskData != nil {
		t.Fatalf("response without include=queue carries queuedTaskData: %s", rawTasks(t, doc))
	}
	if len(doc.Tasks) != 0 {
		t.Fatalf("idle tasks = %s, want []", rawTasks(t, doc))
	}
	doc = tasksAt(t, e, root, "?include=queue")
	if doc.QueuedTaskData == nil || doc.QueuedTaskData.EmbedMetadata == nil {
		t.Fatalf("include=queue did not return queuedTaskData.embedMetadata as an array: %s", rawTasks(t, doc))
	}
	if len(*doc.QueuedTaskData.EmbedMetadata) != 0 {
		t.Fatalf("idle queue = %d entries, want none", len(*doc.QueuedTaskData.EmbedMetadata))
	}
	doc = tasksAt(t, e, root, "?include=unknown")
	if doc.QueuedTaskData != nil {
		t.Fatalf("include=unknown carries queuedTaskData: %s", rawTasks(t, doc))
	}
	doc = tasksAt(t, e, root, "?include=queue,")
	if doc.QueuedTaskData == nil {
		t.Fatalf("include=queue, does not include the queue: %s", rawTasks(t, doc))
	}
}
