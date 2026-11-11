package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
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
	if table := library.MediaTableOf(s.mediaType); table != "" {
		return table, nil
	}
	return "", httputil.Status(http.StatusBadRequest, "")
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
	files := store.DecodeJSONSlice[mediafiles.File](libraryFilesRaw)
	var removed mediafiles.File
	remaining := make([]mediafiles.File, 0, len(files))
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
	var playlistChanges []store.PlaylistChange
	target := removed.Path()
	if target == "" || !mediafiles.PathAllowed(r.Context(), h.db, target, scope.libraryID) {
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
		audio := store.DecodeJSONSlice[mediafiles.File](audioRaw)
		nextAudio := make([]mediafiles.File, 0, len(audio))
		for _, f := range audio {
			if f.Ino() != fileID {
				nextAudio = append(nextAudio, f)
			}
		}
		ebookChanged := mediafiles.File(store.DecodeJSONObject(ebookRaw)).Ino() == fileID
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
		playlistChanges, err = store.DeleteEpisodes(r.Context(), tx, []string{episodeID})
		if err != nil {
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
	playlistview.Announce(r.Context(), h.db, h.emit, playlistChanges)
	if err := h.itemUpdated(r.Context(), itemID); err != nil {
		return httputil.Internal("database read failed", err)
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func jsonValue(value any) string { raw, _ := json.Marshal(value); return string(raw) }
