package item

import (
	"cmp"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
)

// Removal is what deleting items' rows left to do once the transaction that
// deleted them committed: Announce it, then Sweep the parents.
type Removal struct {
	refs            []itemRef
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

// deleteItem removes the item and then, in upstream's order, announces changed
// playlists and closed feeds, drops the cover cache and item metadata, runs
// files (nil keeps the files on disk) and announces item_removed. The caller
// sweeps the authors and series it returns.
func (h *Handler) deleteItem(ctx context.Context, ref itemRef, files func(context.Context, itemRef)) (Removal, error) {
	slog.Info("deleting library item", "path", ref.Path, "id", ref.ID)
	var rem Removal
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		var err error
		rem, err = h.deleteRows(ctx, []itemRef{ref})
		return err
	})
	if err != nil {
		return Removal{}, err
	}
	h.announceRemoval(ctx, rem, files)
	return rem, nil
}

// DeleteRows removes the items and what hangs off them (feeds, progress,
// playlist entries, shares, author and series links, media rows) inside the
// caller's transaction: ctx must carry it, so the deletion commits or rolls
// back with the caller's other writes. Unknown ids are skipped. Nothing is
// announced; call Announce and Sweep after the commit.
func (h *Handler) DeleteRows(ctx context.Context, itemIDs []string) (Removal, error) {
	var refs []itemRef
	err := forChunks(itemIDs, func(chunk []string) error {
		var found []itemRef
		err := h.db.Select(ctx, &found, `SELECT `+itemRefColumns+` FROM libraryItems li WHERE li.id IN (?)`, chunk)
		refs = append(refs, found...)
		return err
	})
	if err != nil {
		return Removal{}, err
	}
	pos := make(map[string]int, len(itemIDs))
	for i, id := range itemIDs {
		pos[id] = i
	}
	slices.SortFunc(refs, func(a, b itemRef) int { return cmp.Compare(pos[a.ID], pos[b.ID]) })
	return h.deleteRows(ctx, refs)
}

// Add merges the removal of another deletion into r, for one Sweep of several.
func (r *Removal) Add(o Removal) {
	r.refs = append(r.refs, o.refs...)
	r.authors, r.series = append(r.authors, o.authors...), append(r.series, o.series...)
	r.closedFeeds, r.playlists = append(r.closedFeeds, o.closedFeeds...), append(r.playlists, o.playlists...)
}

// Announce does what follows the commit of DeleteRows: the changed playlists,
// then per item its closed feeds, the cover cache and item metadata, and
// item_removed.
func (h *Handler) Announce(ctx context.Context, rem Removal) { h.announceRemoval(ctx, rem, nil) }

// Sweep removes the authors and then the series the deleted books left empty.
func (h *Handler) Sweep(ctx context.Context, rem Removal) {
	h.sweepAuthors(ctx, rem.authors)
	h.sweepSeries(ctx, rem.series)
}

func (h *Handler) announceRemoval(ctx context.Context, rem Removal, files func(context.Context, itemRef)) {
	playlistview.Announce(ctx, h.db.SQL(), h.emit, rem.playlists)
	for _, ref := range rem.refs {
		for _, feed := range rem.closedFeeds {
			if db.Val(feed.EntityID) == ref.ID {
				h.emit.ToAll("rss_feed_closed", feed)
			}
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
	}
}

// forChunks calls fn with ids in groups, as SQLite limits the variables of a statement.
func forChunks(ids []string, fn func(chunk []string) error) error {
	for chunk := range slices.Chunk(ids, 500) {
		if err := fn(chunk); err != nil {
			return err
		}
	}
	return nil
}

// deleteRows removes the items and what hangs off them inside the caller's
// transaction (ApiRouter.handleDeleteLibraryItem, ApiRouter.js:373-407).
func (h *Handler) deleteRows(ctx context.Context, refs []itemRef) (Removal, error) {
	rem := Removal{refs: refs}
	var itemIDs, bookIDs, podcastIDs []string
	for _, ref := range refs {
		itemIDs = append(itemIDs, ref.ID)
		switch ref.MediaType {
		case "book":
			bookIDs = append(bookIDs, ref.MediaID)
		case "podcast":
			podcastIDs = append(podcastIDs, ref.MediaID)
		}
	}
	mediaIDs := slices.Clone(bookIDs)
	err := forChunks(bookIDs, func(chunk []string) error {
		var authors, series []string
		if err := h.db.Select(ctx, &authors, `SELECT authorId FROM bookAuthors WHERE bookId IN (?) AND authorId IS NOT NULL`, chunk); err != nil {
			return err
		}
		if err := h.db.Select(ctx, &series, `SELECT seriesId FROM bookSeries WHERE bookId IN (?) AND seriesId IS NOT NULL`, chunk); err != nil {
			return err
		}
		rem.authors, rem.series = append(rem.authors, authors...), append(rem.series, series...)
		return nil
	})
	if err != nil {
		return Removal{}, err
	}
	err = forChunks(podcastIDs, func(chunk []string) error {
		var episodes []string
		err := h.db.Select(ctx, &episodes, `SELECT id FROM podcastEpisodes WHERE podcastId IN (?)`, chunk)
		mediaIDs = append(mediaIDs, episodes...)
		return err
	})
	if err != nil {
		return Removal{}, err
	}
	for _, id := range itemIDs {
		closed, err := h.closeFeeds(ctx, id)
		if err != nil {
			return Removal{}, err
		}
		rem.closedFeeds = append(rem.closedFeeds, closed...)
	}
	if err = forChunks(mediaIDs, func(chunk []string) error {
		_, err := h.db.Exec(ctx, `DELETE FROM mediaProgresses WHERE mediaItemId IN (?)`, chunk)
		return err
	}); err != nil {
		return Removal{}, err
	}
	if rem.playlists, err = h.removeFromPlaylists(ctx, mediaIDs); err != nil {
		return Removal{}, err
	}
	// The media rows go with their shares; an empty id list matches nothing.
	for _, statement := range []struct {
		query string
		ids   []string
	}{
		{`DELETE FROM mediaItemShares WHERE mediaItemId IN (?)`, mediaIDs},
		{`DELETE FROM bookAuthors WHERE bookId IN (?)`, bookIDs},
		{`DELETE FROM bookSeries WHERE bookId IN (?)`, bookIDs},
		{`DELETE FROM books WHERE id IN (?)`, bookIDs},
		{`DELETE FROM podcastEpisodes WHERE podcastId IN (?)`, podcastIDs},
		{`DELETE FROM podcasts WHERE id IN (?)`, podcastIDs},
		{`DELETE FROM libraryItems WHERE id IN (?)`, itemIDs},
	} {
		if err = forChunks(statement.ids, func(chunk []string) error {
			_, err := h.db.Exec(ctx, statement.query, chunk)
			return err
		}); err != nil {
			return Removal{}, err
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
	err := forChunks(mediaIDs, func(chunk []string) error {
		var found []string
		if err := h.db.Select(ctx, &found, `SELECT DISTINCT playlistId FROM playlistMediaItems WHERE mediaItemId IN (?) AND playlistId IS NOT NULL`, chunk); err != nil {
			return err
		}
		for _, id := range found {
			if !slices.Contains(playlistIDs, id) {
				playlistIDs = append(playlistIDs, id)
			}
		}
		if len(found) == 0 {
			return nil
		}
		_, err := h.db.Exec(ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId IN (?)`, chunk)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(playlistIDs) == 0 {
		return nil, nil
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
