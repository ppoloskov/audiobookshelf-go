package item

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
)

// removal is what deleting an item's rows left to do after the commit.
type removal struct {
	authors, series []string // parents to sweep
	closedFeeds     []feeds.Minified
	playlists       []store.PlaylistChange
}

// delete is LibraryItemController.delete (js:114-150). Order: 404, 403
// (library, then delete permission), 400 for a media type without a table.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.loadRef(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !user.CanAccessLibrary(ref.LibraryID) || !user.CanDelete() {
		return httpx.Forbidden()
	}
	if ref.MediaType != "book" && ref.MediaType != "podcast" {
		return httpx.BadRequest("")
	}
	var files func(context.Context, itemRef)
	if r.URL.Query().Get("hard") == "1" {
		files = h.removeFiles
	}
	rem, err := h.deleteItem(ctx, ref, files)
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	h.sweepAuthors(ctx, rem.authors)
	h.sweepSeries(ctx, rem.series)
	h.deps.Filters.ResetIssues(ctx, h.db.SQL(), ref.LibraryID) // Database.resetLibraryIssuesFilterData
	return httpx.Status(w, http.StatusOK)
}

// deleteItem removes the item's rows and then, in upstream's order, announces
// changed playlists and closed feeds, drops the cover cache and item metadata,
// runs files (nil keeps the files on disk) and announces item_removed. The
// caller sweeps the authors and series it returns.
func (h *Handler) deleteItem(ctx context.Context, ref itemRef, files func(context.Context, itemRef)) (removal, error) {
	slog.Info("deleting library item", "path", ref.Path, "id", ref.ID)
	var rem removal
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		var err error
		rem, err = h.deleteRows(ctx, ref)
		return err
	})
	if err != nil {
		return removal{}, err
	}
	playlistview.Announce(ctx, h.db.SQL(), h.emit, rem.playlists)
	for _, feed := range rem.closedFeeds {
		h.emit.ToAll("rss_feed_closed", feed)
	}
	purgeCoverCache(h.deps.CoverCacheDir, ref.ID)
	if h.deps.ItemMetadataDir != "" {
		if err := os.RemoveAll(filepath.Join(h.deps.ItemMetadataDir, ref.ID)); err != nil {
			slog.Warn("remove item metadata", "id", ref.ID, "error", err)
		}
	}
	if files != nil {
		files(ctx, ref)
	}
	h.emit.ToAll("item_removed", removed{ref.ID, ref.LibraryID})
	return rem, nil
}

// deleteRows removes the item and what hangs off it inside the caller's
// transaction (ApiRouter.handleDeleteLibraryItem, ApiRouter.js:373-407).
func (h *Handler) deleteRows(ctx context.Context, ref itemRef) (removal, error) {
	var rem removal
	var mediaIDs []string
	var err error
	switch ref.MediaType {
	case "book":
		mediaIDs = []string{ref.MediaID}
		if err = h.db.Select(ctx, &rem.authors, `SELECT authorId FROM bookAuthors WHERE bookId = ? AND authorId IS NOT NULL`, ref.MediaID); err != nil {
			return removal{}, err
		}
		if err = h.db.Select(ctx, &rem.series, `SELECT seriesId FROM bookSeries WHERE bookId = ? AND seriesId IS NOT NULL`, ref.MediaID); err != nil {
			return removal{}, err
		}
	case "podcast":
		if err = h.db.Select(ctx, &mediaIDs, `SELECT id FROM podcastEpisodes WHERE podcastId = ?`, ref.MediaID); err != nil {
			return removal{}, err
		}
	}
	if rem.closedFeeds, err = h.closeFeeds(ctx, ref.ID); err != nil {
		return removal{}, err
	}
	if _, err = h.db.Exec(ctx, `DELETE FROM mediaProgresses WHERE mediaItemId IN (?)`, mediaIDs); err != nil {
		return removal{}, err
	}
	if rem.playlists, err = h.removeFromPlaylists(ctx, mediaIDs); err != nil {
		return removal{}, err
	}
	// The media rows go with their shares; an empty id list matches nothing.
	statements := []string{
		`DELETE FROM mediaItemShares WHERE mediaItemId IN (?)`,
		`DELETE FROM bookAuthors WHERE bookId IN (?)`,
		`DELETE FROM bookSeries WHERE bookId IN (?)`,
	}
	for _, query := range statements {
		if _, err = h.db.Exec(ctx, query, mediaIDs); err != nil {
			return removal{}, err
		}
	}
	rows := [][2]string{{`DELETE FROM books WHERE id = ?`, ref.MediaID}}
	if ref.MediaType == "podcast" {
		rows = [][2]string{{`DELETE FROM podcastEpisodes WHERE podcastId = ?`, ref.MediaID}, {`DELETE FROM podcasts WHERE id = ?`, ref.MediaID}}
	}
	for _, row := range append(rows, [2]string{`DELETE FROM libraryItems WHERE id = ?`, ref.ID}) {
		if _, err = h.db.Exec(ctx, row[0], row[1]); err != nil {
			return removal{}, err
		}
	}
	return rem, nil
}

// removeFromPlaylists is Playlist.removeMediaItemsFromPlaylists
// (Playlist.js:172-208): it drops the entries for mediaIDs, destroys the
// playlists left empty and renumbers the others. It returns what changed in
// the form playlistview announces (a transitional type until playlists move).
func (h *Handler) removeFromPlaylists(ctx context.Context, mediaIDs []string) ([]store.PlaylistChange, error) {
	var playlistIDs []string
	if err := h.db.Select(ctx, &playlistIDs, `SELECT DISTINCT playlistId FROM playlistMediaItems WHERE mediaItemId IN (?) AND playlistId IS NOT NULL`, mediaIDs); err != nil {
		return nil, err
	}
	if len(playlistIDs) == 0 {
		return nil, nil
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId IN (?)`, mediaIDs); err != nil {
		return nil, err
	}
	var changes []store.PlaylistChange
	for _, id := range playlistIDs {
		var playlist db.Playlist
		var entryIDs []string
		if err := h.db.Get(ctx, &playlist, `SELECT * FROM playlists WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if err := h.db.Select(ctx, &entryIDs, "SELECT id FROM playlistMediaItems WHERE playlistId = ? ORDER BY `order`", id); err != nil {
			return nil, err
		}
		changes = append(changes, store.PlaylistChange{Playlist: legacyPlaylist(playlist), Removed: len(entryIDs) == 0})
		if len(entryIDs) == 0 {
			if _, err := h.db.Exec(ctx, `DELETE FROM playlists WHERE id = ?`, id); err != nil {
				return nil, err
			}
			continue
		}
		for order, entryID := range entryIDs {
			if _, err := h.db.Exec(ctx, "UPDATE playlistMediaItems SET `order` = ? WHERE id = ?", order+1, entryID); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}

// legacyPlaylist converts a playlist row to the sqlc row playlistview reads.
func legacyPlaylist(p db.Playlist) sqlc.Playlist {
	text := func(s *string) sql.NullString { return sql.NullString{String: db.Val(s), Valid: s != nil} }
	created, _ := p.CreatedAt.Value() // a Time always yields a string or nil
	updated, _ := p.UpdatedAt.Value()
	return sqlc.Playlist{ID: p.ID, Name: text(p.Name), Description: text(p.Description), Createdat: created, Updatedat: updated, Libraryid: text(p.LibraryID), Userid: text(p.UserID)}
}

// removeFiles deletes a hard-deleted item's path when it lies inside one of the
// library's folders; failures are logged (LibraryItemController.js:134-139).
func (h *Handler) removeFiles(ctx context.Context, ref itemRef) {
	if ref.Path == "" {
		return
	}
	var roots []string
	if err := h.db.Select(ctx, &roots, `SELECT path FROM libraryFolders WHERE libraryId = ? AND path IS NOT NULL`, ref.LibraryID); err != nil {
		slog.Error("folders of library", "library", ref.LibraryID, "error", err)
		return
	}
	if !within(ref.Path, roots) {
		return
	}
	slog.Info("deleting library item from file system", "path", ref.Path)
	if err := os.RemoveAll(ref.Path); err != nil {
		slog.Error("delete library item from file system", "path", ref.Path, "error", err)
	}
}

// within reports whether target, with symlinks resolved, lies inside one of roots.
func within(target string, roots []string) bool {
	resolved, err := resolve(target)
	if err != nil {
		return false
	}
	for _, root := range roots {
		base, err := resolve(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(base, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// purgeCoverCache removes the cached cover variants of itemID.
func purgeCoverCache(dir, itemID string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), itemID+"_") {
			_ = os.Remove(filepath.Join(dir, entry.Name())) // a stale cache file is harmless
		}
	}
}
