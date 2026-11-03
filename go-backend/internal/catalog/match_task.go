package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"audiobookshelf-compat/internal/wire"
)

// background runs fn after the HTTP response is written, the way the source
// lets a request handler's promise continue (batchQuickMatch, matchAll).
func (h *Handler) background(fn func(ctx context.Context)) {
	go fn(context.Background())
}

// matchTask is the 'library-match-all' task of Scanner.matchLibraryItems
// (Scanner.js:489-497, 518-548) and its start time.
type matchTask struct {
	scanID string
	task   wire.Task
}

func (h *Handler) startTask(library libraryRecord) *matchTask {
	t := &matchTask{scanID: uuid.NewString()}
	t.task = wire.Task{
		ID: uuid.NewString(), Action: "library-match-all", Data: wire.TaskData{LibraryID: library.id},
		Title: fmt.Sprintf("Matching books in %q", library.name), TitleKey: "MessageTaskMatchingBooksInLibrary", TitleSubs: []string{library.name},
		ShowSuccess: true, StartedAt: time.Now().UnixMilli(),
	}
	if h.emit != nil {
		h.emit("*", "task_started", t.task)
	}
	return t
}

// finishTask ends the task: failed with 'No items found' for an empty library,
// otherwise finished with the scan results and the description cleared.
func (h *Handler) finishTask(t *matchTask, numItems, updated int) {
	finished := time.Now().UnixMilli()
	t.task.IsFinished, t.task.FinishedAt = true, &finished
	elapsed := finished - t.task.StartedAt
	if numItems == 0 {
		text, key := "No items found", "MessageNoItemsFound"
		t.task.IsFailed, t.task.Error, t.task.ErrorKey = true, &text, &key
	} else {
		t.task.Data.ScanResults = &wire.TaskScanResults{Added: 0, Updated: updated, Missing: 0, Elapsed: elapsed, Text: matchResultsText(updated, elapsed)}
	}
	if h.emit != nil {
		h.emit("*", "task_finished", t.task)
	}
}

// matchResultsText is LibraryScan.scanResultsString (LibraryScan.js:61-68).
func matchResultsText(updated int, elapsedMs int64) string {
	changes := "No changes needed"
	if updated > 0 {
		changes = fmt.Sprintf("%d updated", updated)
	}
	seconds := float64(elapsedMs) / 1000
	var pretty string
	switch {
	case seconds > 0 && seconds < 1:
		pretty = fmt.Sprintf("%d ms", int(seconds*1000))
	case seconds < 60:
		pretty = fmt.Sprintf("%d sec", int(seconds))
	default:
		pretty = strings.TrimSpace(fmt.Sprintf("%d min %d sec", int(seconds)/60, int(seconds)%60))
	}
	return fmt.Sprintf("%s (%s)", changes, pretty)
}
