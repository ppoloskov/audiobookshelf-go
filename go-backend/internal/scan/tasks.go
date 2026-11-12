package scan

import (
	"context"
	"fmt"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
)

// scanTaskData is the data of the library-scan and watcher-scan tasks.
type scanTaskData struct {
	LibraryID        string       `json:"libraryId"`
	LibraryName      string       `json:"libraryName"`
	LibraryMediaType string       `json:"libraryMediaType,omitempty"`
	ScanResults      *scanResults `json:"scanResults,omitempty"`
}

// scanResults is read by the web client's task card. A library scan reports
// "missing", a watcher scan "removed".
type scanResults struct {
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Missing *int   `json:"missing,omitempty"`
	Removed *int   `json:"removed,omitempty"`
	Text    string `json:"text"`
	Elapsed int64  `json:"elapsed"`
}

func (s *Scanner) startScanTask(ctx context.Context, libraryID string) events.Task {
	data := scanTaskData{LibraryID: libraryID, LibraryName: libraryID}
	var row db.Library
	if s.db.Get(ctx, &row, `SELECT * FROM libraries WHERE id = ?`, libraryID) == nil {
		data.LibraryMediaType = db.Val(row.MediaType)
		if name := db.Val(row.Name); name != "" {
			data.LibraryName = name
		}
	}
	task := events.NewTask(ids.Must(), "library-scan", data).
		Titled(fmt.Sprintf("Scanning %q library", data.LibraryName), "MessageTaskScanningLibrary", []string{data.LibraryName})
	task.ShowSuccess = true
	s.emit.TaskStarted(task)
	return task
}

func (s *Scanner) finishScanTask(task events.Task, res Result, err error) {
	task = task.Finished()
	elapsed := *task.FinishedAt - task.StartedAt
	task = withResults(task, scanResults{Added: res.Added, Updated: res.Updated, Missing: &res.Missing, Elapsed: elapsed,
		Text: fmt.Sprintf("%s (%s)", changesText(res.Added, res.Updated, res.Missing), elapsedText(elapsed))})
	if err != nil {
		task = task.Failed("Failed", "MessageTaskFailed")
	} else if res.Canceled {
		task = task.Described("Task canceled by user", "MessageTaskCanceledByUser")
	}
	s.emit.TaskFinished(task)
}

func withResults(t events.Task, results scanResults) events.Task {
	data := t.Data.(scanTaskData) // only scan tasks reach here
	data.ScanResults = &results
	t.Data = data
	return t
}

// changesText lists what a scan changed.
func changesText(added, updated, missing int) string {
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{added, "added"}, {updated, "updated"}, {missing, "missing"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.word))
		}
	}
	if len(parts) == 0 {
		return "No changes needed"
	}
	return strings.Join(parts, ", ")
}

// elapsedText is the duration in the units a scan reaches (utils/index.js elapsedPretty).
func elapsedText(ms int64) string {
	seconds := float64(ms) / 1000
	switch {
	case seconds > 0 && seconds < 1:
		return fmt.Sprintf("%d ms", int(seconds*1000))
	case seconds < 60:
		return fmt.Sprintf("%d sec", int(seconds))
	}
	minutes := int(seconds / 60)
	if minutes < 70 {
		return fmt.Sprintf("%d min", minutes)
	}
	return fmt.Sprintf("%d hr %d min", minutes/60, minutes%60)
}

// scanTally collects what the imports changed so the scan announces author
// book counts once and sweeps emptied authors and series at the end.
type scanTally struct{ authorsChanged, authorsRemoved, seriesRemoved []string }

func (t *scanTally) add(l item.ScanLinks) {
	t.authorsChanged = append(t.authorsChanged, l.AuthorsChanged...)
	t.authorsRemoved = append(t.authorsRemoved, l.AuthorsUnlinked...)
	t.seriesRemoved = append(t.seriesRemoved, l.SeriesUnlinked...)
}

// authorBookCount and authorCounts are the authors_num_books_updated payload.
type authorBookCount struct {
	ID       string `json:"id" db:"id"`
	NumBooks int    `json:"numBooks" db:"numBooks"`
}

type authorCounts struct {
	LibraryID string            `json:"libraryId"`
	Authors   []authorBookCount `json:"authors"`
}

// finalize announces the book counts of the authors a scan touched and, when
// sweep is set, removes the authors and series left without books.
func (s *Scanner) finalize(ctx context.Context, libraryID string, t scanTally) {
	if changed := uniqueIDs(t.authorsChanged); len(changed) > 0 {
		var counts []authorBookCount
		err := s.db.Select(ctx, &counts, `SELECT authorId AS id, COUNT(id) AS numBooks FROM bookAuthors WHERE authorId IN (?) GROUP BY authorId ORDER BY authorId`, changed)
		if err != nil {
			slogError("author book counts", err)
		} else if len(counts) > 0 {
			s.emit.ToAll("authors_num_books_updated", authorCounts{LibraryID: libraryID, Authors: counts})
		}
	}
	s.items.SweepOrphans(ctx, t.authorsRemoved, t.seriesRemoved)
}
