// Series routes mirroring server/controllers/SeriesController.js (pinned
// v2.37.2): the deprecated GET /api/series/:id the mobile apps still call and
// PATCH /api/series/:id.
package people

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

type SeriesRecord struct {
	ID, LibraryID        string
	Name, Description    sql.NullString
	CreatedAt, UpdatedAt any
}

// LoadSeries reads one series row.
func LoadSeries(ctx context.Context, db *sql.DB, id string) (SeriesRecord, error) {
	var s SeriesRecord
	err := db.QueryRowContext(ctx, `SELECT id,name,description,libraryId,createdAt,updatedAt FROM series WHERE id=?`, id).Scan(&s.ID, &s.Name, &s.Description, &s.LibraryID, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

type SeriesItem struct{ ItemID, BookID string }

// SeriesItemsFor is libraryItemsBookFilters.getLibraryItemsForSeries
// (libraryItemsBookFilters.js:1079-1082): the series' books the user may see,
// ordered by sequence as a number, books without one last.
func SeriesItemsFor(ctx context.Context, db *sql.DB, user store.User, s SeriesRecord) ([]SeriesItem, error) {
	permissionSQL, permissionArgs := access.For(ctx, user).MediaClause("b")
	args := append([]any{s.LibraryID, s.ID}, permissionArgs...)
	rows, err := db.QueryContext(ctx, `SELECT li.id,b.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN bookSeries bs ON bs.bookId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND bs.seriesId=?`+permissionSQL+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, li.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SeriesItem, 0)
	for rows.Next() {
		var item SeriesItem
		if err := rows.Scan(&item.ItemID, &item.BookID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// seriesRequest is SeriesController.middleware (SeriesController.js:95-119): an
// unknown series, or one without a book the user may see, is a 404; then
// DELETE needs the delete permission and PATCH/POST the update permission.
func (h *Handler) seriesRequest(r *http.Request, user store.User) (SeriesRecord, []SeriesItem, error) {
	s, err := LoadSeries(r.Context(), h.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil, httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return s, nil, httputil.Internal("database read failed", err)
	}
	items, err := SeriesItemsFor(r.Context(), h.db, user, s)
	if err != nil {
		return s, nil, httputil.Internal("database read failed", err)
	}
	if len(items) == 0 {
		logging.Warnf(`[SeriesController] User "%s" attempted to access series "%s" with no accessible books`, user.Username, s.ID)
		return s, nil, httputil.Status(http.StatusNotFound, "")
	}
	switch {
	case r.Method == http.MethodDelete && !access.For(r.Context(), user).Delete():
		logging.Warnf(`[SeriesController] User "%s" attempted to delete without permission`, user.Username)
		return s, nil, httputil.Status(http.StatusForbidden, "")
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !access.For(r.Context(), user).Update():
		logging.Warnf(`[SeriesController] User "%s" attempted to update without permission`, user.Username)
		return s, nil, httputil.Status(http.StatusForbidden, "")
	}
	return s, items, nil
}

// seriesFindOne is SeriesController.findOne (SeriesController.js:35-62).
func (h *Handler) seriesFindOne(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	s, items, err := h.seriesRequest(r, user)
	if err != nil {
		return err
	}
	var include []string
	for _, v := range strings.Split(r.URL.Query().Get("include"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			include = append(include, v)
		}
	}
	detail, err := SeriesDetailFor(r.Context(), h.db, user, s, items, slices.Contains(include, "progress"), slices.Contains(include, "rssfeed"), h.sortingPrefixes(r.Context()))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, detail)
}

// SeriesDetailFor is the body of GET /api/series/:id (SeriesController.js:35-62)
// and GET /api/libraries/:id/series/:seriesId (LibraryController.js:778-805):
// the series, then progress and rssFeed when asked for.
func SeriesDetailFor(ctx context.Context, db *sql.DB, user store.User, s SeriesRecord, items []SeriesItem, withProgress, withFeed bool, prefixes []string) (SeriesDetail, error) {
	oldJSON, err := NewSeriesView(s, prefixes)
	if err != nil {
		return SeriesDetail{}, err
	}
	detail := SeriesDetail{SeriesView: oldJSON}
	if withProgress {
		progress := SeriesProgress{LibraryItemIDs: make([]string, 0, len(items)), LibraryItemIDsFinished: make([]string, 0)}
		for _, item := range items {
			progress.LibraryItemIDs = append(progress.LibraryItemIDs, item.ItemID)
			var finished sql.NullBool
			// req.user.getMediaProgress(li.media.id)?.isFinished
			err := db.QueryRowContext(ctx, `SELECT isFinished FROM mediaProgresses WHERE userId=? AND mediaItemId=? LIMIT 1`, user.ID, item.BookID).Scan(&finished)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return detail, err
			}
			if finished.Valid && finished.Bool {
				progress.LibraryItemIDsFinished = append(progress.LibraryItemIDsFinished, item.ItemID)
			}
		}
		progress.IsFinished = len(progress.LibraryItemIDsFinished) == len(progress.LibraryItemIDs)
		detail.Progress = &progress
	}
	if withFeed {
		// RssFeedManager.findFeedForEntityId matches on entityId alone.
		feed, found, err := feedMinified(ctx, db, "", s.ID)
		if err != nil {
			return detail, err
		}
		detail.RSSFeed = json.RawMessage("null")
		if found {
			if detail.RSSFeed, err = json.Marshal(feed); err != nil {
				return detail, err
			}
		}
	}
	return detail, nil
}

// seriesUpdate is SeriesController.update (SeriesController.js:70-87): only
// string name and description are read; a change is saved and announced with
// series_updated. The prefix column is not recomputed, as upstream.
func (h *Handler) seriesUpdate(w http.ResponseWriter, r *http.Request) error {
	s, _, err := h.seriesRequest(r, store.RequestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := httputil.DecodeObjectFields(r)
	current := map[string]*string{"name": store.NullStringPtr(s.Name), "description": store.NullStringPtr(s.Description)}
	changes := &AuthorColumnUpdate{}
	valid := 0
	for _, key := range []string{"name", "description"} {
		raw, present := body[key]
		if !present {
			continue
		}
		var value string
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			continue
		}
		valid++
		changes.Change(key, current[key], &value)
	}
	if valid == 0 {
		return httputil.Status(http.StatusBadRequest, "No valid fields to update")
	}
	if len(changes.Columns) > 0 {
		sets := make([]string, 0, len(changes.Columns)+1)
		for _, c := range changes.Columns {
			sets = append(sets, c+"=?")
		}
		sets = append(sets, "updatedAt=?")
		args := append(append([]any{}, changes.Values...), dbtime.Now(), s.ID)
		if _, err := h.db.ExecContext(ctx, `UPDATE series SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	saved, err := LoadSeries(ctx, h.db, s.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := NewSeriesView(saved, h.sortingPrefixes(ctx))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(changes.Columns) > 0 {
		h.emit.ToAll("series_updated", oldJSON)
	}
	return httputil.JSON(w, r, oldJSON)
}
