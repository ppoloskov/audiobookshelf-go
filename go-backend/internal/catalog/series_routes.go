// Series routes mirroring server/controllers/SeriesController.js (pinned
// v2.37.2): the deprecated GET /api/series/:id the mobile apps still call and
// PATCH /api/series/:id.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// seriesRecord is a row of series.
type seriesRecord struct {
	id, libraryID        string
	name, description    sql.NullString
	createdAt, updatedAt any
}

func (h *Handler) loadSeries(ctx context.Context, id string) (seriesRecord, error) {
	var s seriesRecord
	err := h.db.QueryRowContext(ctx, `SELECT id,name,description,libraryId,createdAt,updatedAt FROM series WHERE id=?`, id).Scan(&s.id, &s.name, &s.description, &s.libraryID, &s.createdAt, &s.updatedAt)
	return s, err
}

// toOldJSON is Series.toOldJSON (server/models/Series.js:168-178); the
// prefix-free name is computed from the name, not read from the column.
func (h *Handler) seriesOldJSON(ctx context.Context, s seriesRecord) (wire.Series, error) {
	added, err := dbtime.OptionalMillis(s.createdAt)
	if err != nil || added == nil {
		return wire.Series{}, errors.New("series createdAt")
	}
	updated, err := dbtime.OptionalMillis(s.updatedAt)
	if err != nil || updated == nil {
		return wire.Series{}, errors.New("series updatedAt")
	}
	var prefixFree *string
	if s.name.Valid {
		value := h.titlePrefix(ctx, s.name.String)
		prefixFree = &value
	}
	return wire.Series{ID: s.id, Name: store.NullStringPtr(s.name), NameIgnorePrefix: prefixFree, Description: store.NullStringPtr(s.description), AddedAt: *added, UpdatedAt: *updated, LibraryID: s.libraryID}, nil
}

// seriesItem is a library item of a series with the book it holds.
type seriesItem struct{ itemID, bookID string }

// seriesItems is libraryItemsBookFilters.getLibraryItemsForSeries
// (libraryItemsBookFilters.js:1079-1082): the series' books the user may see,
// ordered by sequence as a number, books without one last.
func (h *Handler) seriesItems(ctx context.Context, user store.User, s seriesRecord) ([]seriesItem, error) {
	permissionSQL, permissionArgs := mediaPermission(user, "b")
	args := append([]any{s.libraryID, s.id}, permissionArgs...)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,b.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN bookSeries bs ON bs.bookId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND bs.seriesId=?`+permissionSQL+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, li.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]seriesItem, 0)
	for rows.Next() {
		var item seriesItem
		if err := rows.Scan(&item.itemID, &item.bookID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// seriesRequest is SeriesController.middleware (SeriesController.js:95-119): an
// unknown series, or one without a book the user may see, is a 404; then
// DELETE needs the delete permission and PATCH/POST the update permission.
func (h *Handler) seriesRequest(r *http.Request, user store.User) (seriesRecord, []seriesItem, error) {
	s, err := h.loadSeries(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil, httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return s, nil, httputil.Internal("database read failed", err)
	}
	items, err := h.seriesItems(r.Context(), user, s)
	if err != nil {
		return s, nil, httputil.Internal("database read failed", err)
	}
	if len(items) == 0 {
		logging.Warnf(`[SeriesController] User "%s" attempted to access series "%s" with no accessible books`, user.Username, s.id)
		return s, nil, httputil.Status(http.StatusNotFound, "")
	}
	switch {
	case r.Method == http.MethodDelete && !userAllows(user, "delete"):
		logging.Warnf(`[SeriesController] User "%s" attempted to delete without permission`, user.Username)
		return s, nil, httputil.Status(http.StatusForbidden, "")
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !userAllows(user, "update"):
		logging.Warnf(`[SeriesController] User "%s" attempted to update without permission`, user.Username)
		return s, nil, httputil.Status(http.StatusForbidden, "")
	}
	return s, items, nil
}

// seriesFindOne is SeriesController.findOne (SeriesController.js:35-62).
func (h *Handler) seriesFindOne(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	s, items, err := h.seriesRequest(r, user)
	if err != nil {
		return err
	}
	ctx := r.Context()
	oldJSON, err := h.seriesOldJSON(ctx, s)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	detail := wire.SeriesDetail{Series: oldJSON}
	var include []string
	for _, v := range strings.Split(r.URL.Query().Get("include"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			include = append(include, v)
		}
	}
	if sliceHas(include, "progress") {
		progress := wire.SeriesProgress{LibraryItemIDs: make([]string, 0, len(items)), LibraryItemIDsFinished: make([]string, 0)}
		for _, item := range items {
			progress.LibraryItemIDs = append(progress.LibraryItemIDs, item.itemID)
			var finished sql.NullBool
			// req.user.getMediaProgress(li.media.id)?.isFinished
			err := h.db.QueryRowContext(ctx, `SELECT isFinished FROM mediaProgresses WHERE userId=? AND mediaItemId=? LIMIT 1`, user.ID, item.bookID).Scan(&finished)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return httputil.Internal("database read failed", err)
			}
			if finished.Valid && finished.Bool {
				progress.LibraryItemIDsFinished = append(progress.LibraryItemIDsFinished, item.itemID)
			}
		}
		progress.IsFinished = len(progress.LibraryItemIDsFinished) == len(progress.LibraryItemIDs)
		detail.Progress = &progress
	}
	if sliceHas(include, "rssfeed") {
		// RssFeedManager.findFeedForEntityId matches on entityId alone.
		feed, found, err := h.feedMinified(ctx, "", s.id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		raw := json.RawMessage("null")
		if found {
			if raw, err = json.Marshal(feed); err != nil {
				return httputil.Internal("internal error", err)
			}
		}
		detail.RSSFeed = raw
	}
	return httputil.JSON(w, r, detail)
}

// seriesUpdate is SeriesController.update (SeriesController.js:70-87): only
// string name and description are read; a change is saved and announced with
// series_updated. The prefix column is not recomputed, as upstream.
func (h *Handler) seriesUpdate(w http.ResponseWriter, r *http.Request) error {
	s, _, err := h.seriesRequest(r, requestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
	current := map[string]*string{"name": store.NullStringPtr(s.name), "description": store.NullStringPtr(s.description)}
	changes := &authorColumnUpdate{}
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
		changes.set(key, current[key], &value)
	}
	if valid == 0 {
		return httputil.Status(http.StatusBadRequest, "No valid fields to update")
	}
	if len(changes.columns) > 0 {
		sets := make([]string, 0, len(changes.columns)+1)
		for _, c := range changes.columns {
			sets = append(sets, c+"=?")
		}
		sets = append(sets, "updatedAt=?")
		args := append(append([]any{}, changes.values...), dbNow(), s.id)
		if _, err := h.db.ExecContext(ctx, `UPDATE series SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	saved, err := h.loadSeries(ctx, s.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := h.seriesOldJSON(ctx, saved)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(changes.columns) > 0 && h.emit != nil {
		h.emit("*", "series_updated", oldJSON)
	}
	return httputil.JSON(w, r, oldJSON)
}
