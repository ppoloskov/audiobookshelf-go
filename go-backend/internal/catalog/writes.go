package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
)

// itemScope is the library item row every item route starts from.
type itemScope struct {
	id, libraryID, mediaType, mediaID, path string
	isFile, missing                         bool
}

// itemScopeColumns are the libraryItems columns scanItemScope reads.
const itemScopeColumns = `li.id,li.libraryId,li.mediaType,li.mediaId,li.path,COALESCE(li.isFile,0),COALESCE(li.isMissing,0)`

type rowScanner interface{ Scan(...any) error }

func scanItemScope(row rowScanner) (itemScope, error) {
	var s itemScope
	var isFile, missing int
	err := row.Scan(&s.id, &s.libraryID, &s.mediaType, &s.mediaID, &s.path, &isFile, &missing)
	s.isFile, s.missing = isFile != 0, missing != 0
	return s, err
}

// mediaTable is the table of the item's media; other types are refused.
func (s itemScope) mediaTable() (string, error) {
	if table := mediaTableOf(s.mediaType); table != "" {
		return table, nil
	}
	return "", httputil.Status(http.StatusBadRequest, "")
}

func mediaTableOf(mediaType string) string {
	return map[string]string{"book": "books", "podcast": "podcasts"}[mediaType]
}

// loadItem reads the item row: 404 when there is none.
func (h *Handler) loadItem(ctx context.Context, id string) (itemScope, error) {
	scope, err := scanItemScope(h.db.QueryRowContext(ctx, `SELECT `+itemScopeColumns+` FROM libraryItems li WHERE li.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return scope, httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return scope, httputil.Internal("database read failed", err)
	}
	return scope, nil
}

// itemFor is the prologue of every item route: the item (404), then the
// user's access to its library and, for a write, the update permission (403).
func (h *Handler) itemFor(ctx context.Context, user store.User, id string, write bool) (itemScope, error) {
	scope, err := h.loadItem(ctx, id)
	if err != nil {
		return scope, err
	}
	perms := access.For(ctx, user)
	if !perms.Library(scope.libraryID) || write && !perms.Update() {
		return scope, httputil.Status(http.StatusForbidden, "")
	}
	return scope, nil
}

func (h *Handler) updateMedia(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	scope, err := h.itemFor(r.Context(), user, itemID, true)
	if err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&payload); err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid request body")
	}
	var metadata map[string]json.RawMessage
	if raw := payload["metadata"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return httputil.Status(http.StatusBadRequest, "Invalid metadata")
		}
	}
	if scope.mediaType == "podcast" {
		// LibraryItemController.updateMedia (LibraryItemController.js:225).
		if raw, ok := payload["autoDownloadSchedule"]; ok {
			var schedule string
			if json.Unmarshal(raw, &schedule) == nil && schedule != "" && !cronspec.Valid(schedule) {
				return httputil.Status(http.StatusBadRequest, "Invalid auto download schedule cron expression")
			}
		}
	}
	result, err := h.applyMedia(r.Context(), scope, payload, metadata)
	if errors.Is(err, errUnsupportedMedia) {
		return httputil.Status(http.StatusBadRequest, "")
	}
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.finishItemWrites(r.Context(), itemWrite{itemID: itemID, libraryID: scope.libraryID, changes: result.changes, updated: result.updated, reschedule: result.schedule}); err != nil {
		return httputil.Internal("metadata file write failed", err)
	}
	item, err := h.fullItem(r.Context(), itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"updated": result.updated, "libraryItem": item}, httputil.ForRequest(r))
	return nil
}

var errUnsupportedMedia = errors.New("unsupported media type")

// mediaUpdateResult is what applying a media payload to an item changed.
type mediaUpdateResult struct {
	updated bool
	changes itemwrite.Changes
	// schedule: a podcast's autoDownloadEpisodes or autoDownloadSchedule changed
	// (LibraryItemController.js:216-223).
	schedule bool
}

// applyMedia is the database part of LibraryItemController.updateMedia
// (LibraryItemController.js:208-291) and of each entry of batchUpdate
// (LibraryItemController.js:630-735): media.updateFromRequest, then for books
// updateSeriesFromRequest and updateAuthorsFromRequest, saved in one
// transaction. The caller runs the follow-ups (empty series and author
// cleanup, filter data, events, the metadata file).
func (h *Handler) applyMedia(ctx context.Context, scope itemScope, payload, metadata map[string]json.RawMessage) (mediaUpdateResult, error) {
	var result mediaUpdateResult
	table := map[string]string{"book": "books", "podcast": "podcasts"}[scope.mediaType]
	if table == "" {
		return result, errUnsupportedMedia
	}
	prefixes := h.sortingPrefixes(ctx)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current, err := rowColumns(ctx, tx, table, scope.mediaID)
	if err != nil {
		return result, err
	}
	columns := newColumnSet(current)
	if scope.mediaType == "book" {
		columns.bookFields(metadata, payload)
	} else {
		columns.podcastFields(metadata, payload)
	}
	// A new title carries its sort form (Book.js:394-396, Podcast.js:243-245);
	// the libraryItems copy follows through the database trigger.
	if title, ok := columns.set["title"]; ok {
		text, _ := title.(string)
		columns.set["titleIgnorePrefix"] = textsort.TitleIgnorePrefix(text, prefixes)
	}
	result.updated = len(columns.set) > 0
	_, enabled := columns.set["autoDownloadEpisodes"]
	_, cron := columns.set["autoDownloadSchedule"]
	result.schedule = enabled || cron
	if scope.mediaType == "book" {
		rules := itemwrite.Rules{Replace: true, AuthorUpdates: true}
		if names, ok := authorNames(metadata["authors"]); ok {
			changed, e := result.changes.LinkAuthors(ctx, tx, scope.libraryID, scope.mediaID, names, rules)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
		if series, ok := seriesEntries(metadata["series"]); ok {
			changed, e := result.changes.LinkSeries(ctx, tx, scope.libraryID, scope.mediaID, series, prefixes, rules)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
	}
	if result.updated {
		if err := columns.update(ctx, tx, table, scope.mediaID); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbtime.Now(), scope.id); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

// authorNames are the names of an authors payload; ok is false when it is not
// an array.
func authorNames(raw json.RawMessage) ([]string, bool) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return nil, false
	}
	var names []string
	for _, a := range incoming {
		if name, ok := a["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names, true
}

// seriesEntries are the entries of a series payload; ok is false when it is
// not an array or an entry has no name.
func seriesEntries(raw json.RawMessage) ([]itemwrite.Series, bool) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return nil, false
	}
	series := make([]itemwrite.Series, len(incoming))
	for i, s := range incoming {
		name, _ := s["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, false
		}
		sequence, _ := s["sequence"].(string)
		series[i] = itemwrite.Series{Name: name, Sequence: sequence}
	}
	return series, true
}

func (h *Handler) deleteLibraryFile(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	fileID := r.PathValue("ino")
	scope, err := h.itemFor(r.Context(), user, itemID, true)
	if err != nil {
		return err
	}
	var libraryFilesRaw, audioRaw, ebookRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryFiles,b.audioFiles,b.ebookFile FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&libraryFilesRaw, &audioRaw, &ebookRaw); err != nil {
		return httputil.Internal("database read failed", err)
	}
	files := store.DecodeJSONSlice[libitem.File](libraryFilesRaw)
	var removed libitem.File
	remaining := make([]libitem.File, 0, len(files))
	for _, f := range files {
		if f.Ino() == fileID {
			removed = f
		} else {
			remaining = append(remaining, f)
		}
	}
	if removed == nil {
		return httputil.Status(404, "")
	}
	target := removed.Path()
	if target == "" || !h.pathAllowed(r.Context(), target, scope.libraryID) {
		return httputil.Status(404, "")
	}
	_ = os.Remove(target)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	now := dbtime.Now()
	if scope.mediaType == "book" {
		audio := store.DecodeJSONSlice[libitem.File](audioRaw)
		nextAudio := make([]libitem.File, 0, len(audio))
		for _, f := range audio {
			if f.Ino() != fileID {
				nextAudio = append(nextAudio, f)
			}
		}
		ebookChanged := libitem.File(store.DecodeJSONObject(ebookRaw)).Ino() == fileID
		if len(nextAudio) != len(audio) || ebookChanged {
			var ebookValue any
			if !ebookChanged {
				ebookValue = string(ebookRaw)
			}
			if _, err := tx.ExecContext(r.Context(), `UPDATE books SET audioFiles=?,ebookFile=?,updatedAt=? WHERE id=?`, jsonValue(nextAudio), ebookValue, now, scope.mediaID); err != nil {
				return httputil.Internal("database write failed", err)
			}
		}
		missing := len(nextAudio) == 0 && (len(ebookRaw) == 0 || ebookChanged || string(ebookRaw) == "null")
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,isMissing=?,updatedAt=? WHERE id=?`, jsonValue(remaining), missing, now, itemID); err != nil {
			return httputil.Internal("database write failed", err)
		}
	} else if scope.mediaType == "podcast" {
		var episodeID string
		err := tx.QueryRowContext(r.Context(), `SELECT id FROM podcastEpisodes WHERE podcastId=? AND json_extract(audioFile,'$.ino')=?`, scope.mediaID, fileID).Scan(&episodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return httputil.Status(404, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if err := store.RemoveFromPlaylists(r.Context(), tx, []string{episodeID}); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId=? AND mediaItemType='podcastEpisode'`, episodeID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE id=?`, episodeID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, jsonValue(remaining), now, itemID); err != nil {
			return httputil.Internal("database write failed", err)
		}
	} else {
		return httputil.Status(400, "")
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.itemUpdated(r.Context(), itemID); err != nil {
		return httputil.Internal("database read failed", err)
	}
	w.WriteHeader(http.StatusOK)
	return nil
}
