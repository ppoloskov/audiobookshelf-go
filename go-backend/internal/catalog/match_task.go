package catalog

import (
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// startMatchTask announces the 'library-match-all' task of
// Scanner.matchLibraryItems (Scanner.js:489-497).
func (h *Handler) startMatchTask(library libraryScope) events.Task {
	t := events.NewTask(ids.Must(), "library-match-all", taskData{LibraryID: library.id}).
		Titled(fmt.Sprintf("Matching books in %q", library.name), "MessageTaskMatchingBooksInLibrary", []string{library.name})
	t.ShowSuccess = true
	h.emit.TaskStarted(t)
	return t
}

// finishMatchTask ends the task: failed with 'No items found' for an empty
// library, otherwise finished with the scan results (Scanner.js:518-548).
func (h *Handler) finishMatchTask(t events.Task, libraryID string, numItems, updated int) {
	t = t.Finished()
	if numItems == 0 {
		t = t.Failed("No items found", "MessageNoItemsFound")
	} else {
		elapsed := *t.FinishedAt - t.StartedAt
		t.Data = taskData{LibraryID: libraryID, ScanResults: &taskScanResults{Added: 0, Updated: updated, Missing: 0, Elapsed: elapsed, Text: matchResultsText(updated, elapsed)}}
	}
	h.emit.TaskFinished(t)
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
	go h.matchLibraryItems(context.Background(), library)
	return httputil.OK(w, r)
}

func (h *Handler) matchLibraryItems(ctx context.Context, library libraryScope) {
	if library.mediaType == "podcast" {
		logging.Errorf("[Scanner] matchLibraryItems: Match all not supported for podcasts yet")
		return
	}
	settings := library.settings
	task := h.startMatchTask(library)
	logging.Infof(`[Scanner] matchLibraryItems: Starting library match scan %s for %s`, ids.Must(), library.name)
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
	h.finishMatchTask(task, library.id, len(targets), updated)
}
