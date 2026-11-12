package library

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
)

// matchTaskData is the data of the library-match-all task; ScanResults is set
// once it finished.
type matchTaskData struct {
	LibraryID   string      `json:"libraryId"`
	ScanResults *scanResult `json:"scanResults,omitempty"`
}

// scanResult is LibraryScan.scanResults (LibraryScan.js:71-79).
type scanResult struct {
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Missing int    `json:"missing"`
	Elapsed int64  `json:"elapsed"`
	Text    string `json:"text"`
}

// matchAll is GET /api/libraries/{id}/matchall (LibraryController.matchAll,
// js:1245-1254): an admin starts the match task and gets the answer at once.
func (h *Handler) matchAll(w http.ResponseWriter, r *http.Request, scope Scope) error {
	if !isAdmin(scope.User) {
		slog.Error("non-admin user attempted to match library items", "user", db.Val(scope.User.Username))
		return httpx.Forbidden()
	}
	go h.matchLibraryItems(context.Background(), scope) // outlives the request
	return httpx.Status(w, http.StatusOK)
}

// matchLibraryItems is Scanner.matchLibraryItems (Scanner.js:472-548): it quick
// matches every book of a library, skipping those the library settings exempt,
// as a task the clients display. Podcast libraries are not supported.
func (h *Handler) matchLibraryItems(ctx context.Context, scope Scope) {
	name, settings := db.Val(scope.Name), scope.Settings.V
	if db.Val(scope.MediaType) == "podcast" {
		slog.Error("match all is not supported for podcasts yet")
		return
	}
	task := events.NewTask(ids.Must(), "library-match-all", matchTaskData{LibraryID: scope.ID}).
		Titled(fmt.Sprintf("Matching books in %q", name), "MessageTaskMatchingBooksInLibrary", []string{name})
	task.ShowSuccess = true
	h.emit.TaskStarted(task)
	targets, err := selectAll[struct {
		ID    string  `db:"id"`
		Title *string `db:"title"`
		ASIN  *string `db:"asin"`
		ISBN  *string `db:"isbn"`
	}](ctx, h.db, `SELECT li.id AS id, b.title AS title, b.asin AS asin, b.isbn AS isbn FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId = ? ORDER BY li.createdAt`, scope.ID)
	if err != nil {
		slog.Error("match all: list books", "library", scope.ID, "error", err)
		return
	}
	updated := 0
	for i, t := range targets {
		switch {
		case db.Val(t.ASIN) != "" && db.Val(settings.SkipMatchingMediaWithAsin):
			slog.Debug("match all: skipping a book that already has an ASIN", "title", db.Val(t.Title), "position", i+1, "of", len(targets))
			continue
		case db.Val(t.ISBN) != "" && db.Val(settings.SkipMatchingMediaWithIsbn):
			slog.Debug("match all: skipping a book that already has an ISBN", "title", db.Val(t.Title), "position", i+1, "of", len(targets))
			continue
		case h.deps.QuickMatch == nil:
			continue
		}
		result, err := h.deps.QuickMatch(ctx, t.ID, item.MatchOptions{Provider: db.Val(scope.Provider)})
		switch {
		case err != nil:
			slog.Error("match all: quick match", "item", t.ID, "error", err)
		case result.Warning != nil:
			slog.Warn("match all: match warning", "warning", *result.Warning, "title", db.Val(t.Title))
		case db.Val(result.Updated):
			updated++
		}
	}
	task = task.Finished()
	if len(targets) == 0 {
		task = task.Failed("No items found", "MessageNoItemsFound")
	} else {
		elapsed := *task.FinishedAt - task.StartedAt
		task.Data = matchTaskData{LibraryID: scope.ID, ScanResults: &scanResult{Updated: updated, Elapsed: elapsed, Text: resultsText(updated, elapsed)}}
	}
	h.emit.TaskFinished(task)
}

// resultsText is LibraryScan.scanResultsString (LibraryScan.js:61-68).
func resultsText(updated int, elapsedMs int64) string {
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
