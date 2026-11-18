package tools

import (
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// checkSingleWaiter proves a two-item batch starts the first item at once,
// queues exactly one waiter behind it and runs the waiter after the first
// finishes; the embed also rewrites the media file and cleans its cache.
func checkSingleWaiter(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	first, second := e.IDs["saga1"], e.IDs["novel"]
	item := itemAt(t, e, root, first)
	before := audioModTime(t, item)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitBatch(t, e, root, []string{first, second}, nil), 200)
	requireQueuedOrder(t, e, root, []string{second})
	a.waitFinished(t, second)
	a.barrier(t)
	a.requireTrace(t, []string{first, second})
	requireRewritten(t, before, item)
	requireNoItemCache(t, e, first)
}

// checkDeepQueueRepeat submits four items twice: three waiters drain in
// submission order behind the running item, and the repeated round produces the
// same transcript with no stale or duplicated transition.
func checkDeepQueueRepeat(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	ids := []string{e.IDs["saga1"], e.IDs["novel"], e.IDs["saga2"], e.IDs["disposable"]}
	a := connectAudiences(t, e, suffix)
	defer a.close()
	for round := 1; round <= 2; round++ {
		a.reMark()
		from := a.markAll()
		wantStatus(t, submitBatch(t, e, root, ids, nil), 200)
		requireQueuedOrder(t, e, root, ids[1:])
		a.waitFinished(t, ids[len(ids)-1])
		a.barrier(t)
		a.requireRound(t, from, ids)
		requireQueuedEmpty(t, e, root)
	}
}

// checkDuplicateRejection re-admits the running item, a waiting item and a
// one-item batch while the queue is occupied: each request is refused with 400,
// the FIFO queue is unchanged and no queue or task event is fabricated.
func checkDuplicateRejection(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	running, waiting, last := e.IDs["saga1"], e.IDs["novel"], e.IDs["saga2"]
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitBatch(t, e, root, []string{running, waiting, last}, nil), 200)
	requireQueuedOrder(t, e, root, []string{waiting, last})
	wantStatus(t, submitItem(t, e, root, running, nil), 400)
	wantStatus(t, submitItem(t, e, root, waiting, nil), 400)
	wantStatus(t, submitBatch(t, e, root, []string{last}, nil), 400)
	requireQueuedOrder(t, e, root, []string{waiting, last})
	a.waitFinished(t, last)
	a.barrier(t)
	a.requireTrace(t, []string{running, waiting, last})
}

// checkForcedOptions proves forceEmbedChapters=1 only widens chapter embedding:
// a forced waiter is queued and dequeued like any other, and a forced admission
// on an idle queue starts at once without a transition.
func checkForcedOptions(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	first, second := e.IDs["saga1"], e.IDs["novel"]
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitBatch(t, e, root, []string{first, second}, forceChapters()), 200)
	doc := tasksAt(t, e, root, "?include=queue")
	if queued := queuedIDs(t, doc); len(queued) != 1 || queued[0] != second {
		t.Fatalf("forced queued ids = %v, want [%s]", queued, second)
	}
	if !(*doc.QueuedTaskData.EmbedMetadata)[0].Options.ForceEmbedChapters {
		t.Fatalf("queued task does not record forceEmbedChapters")
	}
	a.waitFinished(t, second)
	a.barrier(t)
	a.requireTrace(t, []string{first, second})
	a.reMark()
	from := a.markAll()
	wantStatus(t, submitItem(t, e, root, second, forceChapters()), 200)
	a.waitFinished(t, second)
	a.barrier(t)
	a.requireRound(t, from, []string{second})
	requireQueuedEmpty(t, e, root)
}

// checkFailureAdvance makes the first item's directory unwritable, submits it
// with two waiters and proves the failed job reports a failed task, changes no
// media, does not emit a queue transition of its own, still hands the slot to
// both waiters in order, and can be retried. It then repeats the single-item
// failure at the per-file access and FFmpeg tag stages, asserting the error the
// client renders and the cache directory upstream leaves after the tag failure.
func checkFailureAdvance(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	failing, next, last := e.IDs["saga2"], e.IDs["novel"], e.IDs["saga1"]
	item := itemAt(t, e, root, failing)
	before := audioModTime(t, item)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	restore := makeUnwritable(t, item.libraryItemDir(t))
	defer restore()
	wantStatus(t, submitBatch(t, e, root, []string{failing, next, last}, nil), 200)
	a.waitFinished(t, last)
	a.barrier(t)
	a.requireFailureTrace(t, failing, []string{next, last})
	requireUnchangedAudio(t, before, item)
	requireNoItemCache(t, e, failing)
	requireQueuedEmpty(t, e, root)

	restore()
	a.reMark()
	from := a.markAll()
	wantStatus(t, submitItem(t, e, root, failing, nil), 200)
	a.waitFinished(t, failing)
	a.barrier(t)
	a.requireRound(t, from, []string{failing})

	// A read-only audio file fails the per-file access check instead of the
	// directory check, with the same queue-advance and no-media-change outcome.
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	afterRetry := audioModTime(t, item)
	a.reMark()
	from = a.markAll()
	restoreFile := makeUnwritable(t, audioPath)
	defer restoreFile()
	wantStatus(t, submitItem(t, e, root, failing, nil), 200)
	failed := waitEmbedFailed(t, a.sockets["root"], from["root"], failing)
	a.barrier(t)
	a.requireFailedRound(t, from, failing)
	requireFailedTask(t, failed, "MessageTaskAudioFileNotWritable", []string{item.Media.AudioFiles[0].Metadata.Filename})
	requireUnchangedAudio(t, afterRetry, item)
	requireNoItemCache(t, e, failing)
	restoreFile()

	// A write-only audio file passes the earlier checks but fails when FFmpeg
	// reads it, reporting the per-file tagging error.
	afterFileFailure := audioModTime(t, item)
	a.reMark()
	from = a.markAll()
	restoreRead := makeUnreadable(t, audioPath)
	defer restoreRead()
	wantStatus(t, submitItem(t, e, root, failing, nil), 200)
	failed = waitEmbedFailed(t, a.sockets["root"], from["root"], failing)
	a.barrier(t)
	a.requireFailedRound(t, from, failing)
	requireFailedTask(t, failed, "MessageTaskFailedToEmbedMetadataInFile", []string{item.Media.AudioFiles[0].Metadata.Filename})
	requireUnchangedAudio(t, afterFileFailure, item)
	// The tag failure returns before the success cleanup, so upstream defines a
	// partial state: the item cache directory and its metadata file stay behind.
	cache := itemCachePath(e, failing)
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("tag failure should leave the item cache directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "ffmetadata.txt")); err != nil {
		t.Fatalf("tag failure should leave the written metadata file: %v", err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatalf("remove the failed job's cache directory: %v", err)
	}
	restoreRead()
}

// checkDrainedQueue proves the queue is empty once the batch finishes, so the
// next admission finds a free slot and starts immediately with no transition.
func checkDrainedQueue(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	first, second := e.IDs["novel"], e.IDs["saga2"]
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitBatch(t, e, root, []string{first, second}, nil), 200)
	a.waitFinished(t, second)
	a.barrier(t)
	a.requireTrace(t, []string{first, second})
	requireQueuedEmpty(t, e, root)
	a.reMark()
	from := a.markAll()
	wantStatus(t, submitItem(t, e, root, second, nil), 200)
	a.waitFinished(t, second)
	a.barrier(t)
	a.requireRound(t, from, []string{second})
	requireQueuedEmpty(t, e, root)
}
