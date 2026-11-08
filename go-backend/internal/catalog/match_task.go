package catalog

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
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
	task   task
}

func (h *Handler) startTask(library libraryScope) *matchTask {
	t := &matchTask{scanID: uuid.NewString()}
	t.task = task{
		ID: uuid.NewString(), Action: "library-match-all", Data: taskData{LibraryID: library.id},
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
		t.task.Data.ScanResults = &taskScanResults{Added: 0, Updated: updated, Missing: 0, Elapsed: elapsed, Text: matchResultsText(updated, elapsed)}
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

// libraryMatchAll is LibraryController.matchAll (LibraryController.js:1245-1254)
// over Scanner.matchLibraryItems (Scanner.js:472-548) and
// matchLibraryItemsChunk (:435-469).
func (h *Handler) libraryMatchAll(w http.ResponseWriter, r *http.Request, library libraryScope) error {
	if !adminUser(library.user) {
		logging.Errorf(`[LibraryController] Non-root user "%s" attempted to match library items`, library.user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	h.background(func(ctx context.Context) { h.matchLibraryItems(ctx, library) })
	return httputil.OK(w, r)
}

func (h *Handler) matchLibraryItems(ctx context.Context, library libraryScope) {
	if library.mediaType == "podcast" {
		logging.Errorf("[Scanner] matchLibraryItems: Match all not supported for podcasts yet")
		return
	}
	settings := library.settings
	task := h.startTask(library)
	logging.Infof(`[Scanner] matchLibraryItems: Starting library match scan %s for %s`, task.scanID, library.name)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,b.title,b.asin,b.isbn FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? ORDER BY li.createdAt`, library.id)
	if err != nil {
		logging.Errorf("[Scanner] matchLibraryItems: %v", err)
		return
	}
	type target struct {
		id, title, asin, isbn string
	}
	var targets []target
	for rows.Next() {
		var t target
		var title, asin, isbn sql.NullString
		if rows.Scan(&t.id, &title, &asin, &isbn) == nil {
			t.title, t.asin, t.isbn = title.String, asin.String, isbn.String
			targets = append(targets, t)
		}
	}
	rows.Close()
	updated := 0
	for i, t := range targets {
		if t.asin != "" && settings.SkipMatchingMediaWithAsin {
			logging.Debugf(`[Scanner] matchLibraryItems: Skipping "%s" because it already has an ASIN (%d of %d)`, t.title, i+1, len(targets))
			continue
		}
		if t.isbn != "" && settings.SkipMatchingMediaWithIsbn {
			logging.Debugf(`[Scanner] matchLibraryItems: Skipping "%s" because it already has an ISBN (%d of %d)`, t.title, i+1, len(targets))
			continue
		}
		logging.Debugf(`[Scanner] matchLibraryItems: Quick matching "%s" (%d of %d)`, t.title, i+1, len(targets))
		result, err := h.quickMatchLibraryItem(ctx, t.id, quickMatchOptions{provider: library.provider})
		if err != nil {
			logging.Errorf("[Scanner] matchLibraryItems: %v", err)
			continue
		}
		if result.Warning != nil {
			logging.Warnf(`[Scanner] matchLibraryItems: Match warning %s for library item "%s"`, *result.Warning, t.title)
		} else if result.Updated != nil && *result.Updated {
			updated++
		}
	}
	h.finishTask(task, len(targets), updated)
}
