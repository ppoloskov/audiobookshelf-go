package itemwrite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
)

// DeleteTarget is the minimal library-item row deletion needs.
type DeleteTarget struct {
	ItemID, LibraryID, MediaType, MediaID, Path string
}

// DeleteChanges carries the post-commit changes of DeleteRows.
type DeleteChanges struct {
	Targets         []DeleteTarget
	Authors         []string
	Series          []string
	PlaylistChanges []store.PlaylistChange
	ClosedFeeds     []feeds.Minified
}

// DeleteRows removes targets and their references on the caller's transaction only.
func DeleteRows(ctx context.Context, tx *sql.Tx, targets []DeleteTarget) (DeleteChanges, error) {
	changes := DeleteChanges{Targets: append([]DeleteTarget(nil), targets...)}
	var itemIDs, bookIDs, podcastIDs []string
	for _, target := range targets {
		itemIDs = append(itemIDs, target.ItemID)
		switch target.MediaType {
		case "book":
			bookIDs = append(bookIDs, target.MediaID)
		case "podcast":
			podcastIDs = append(podcastIDs, target.MediaID)
		}
	}
	list := func(column, table, by string, ids []string) ([]string, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+column+` FROM `+table+` WHERE `+by+` IN (`+store.Placeholders(len(ids))+`)`, store.StringArgs(ids)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	var err error
	changes.Authors, err = list("authorId", "bookAuthors", "bookId", bookIDs)
	if err != nil {
		return DeleteChanges{}, err
	}
	changes.Series, err = list("seriesId", "bookSeries", "bookId", bookIDs)
	if err != nil {
		return DeleteChanges{}, err
	}
	episodeIDs, err := list("id", "podcastEpisodes", "podcastId", podcastIDs)
	if err != nil {
		return DeleteChanges{}, err
	}

	for _, itemID := range itemIDs {
		closed, err := feeds.CloseEntity(ctx, tx, itemID)
		if err != nil {
			return DeleteChanges{}, err
		}
		changes.ClosedFeeds = append(changes.ClosedFeeds, closed...)
	}
	exec := func(table, by string, ids []string) {
		if err == nil && len(ids) > 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+by+` IN (`+store.Placeholders(len(ids))+`)`, store.StringArgs(ids)...)
		}
	}
	q := sqlc.New(tx)
	mediaIDs := append(append([]string{}, bookIDs...), episodeIDs...)
	exec("mediaProgresses", "mediaItemId", mediaIDs)
	if err == nil && len(mediaIDs) > 0 {
		changes.PlaylistChanges, err = store.RemoveFromPlaylists(ctx, tx, mediaIDs)
	}
	exec("bookAuthors", "bookId", bookIDs)
	exec("bookSeries", "bookId", bookIDs)
	if err == nil && len(bookIDs) > 0 {
		err = q.DeleteBookSharesByMediaIDs(ctx, store.Texts(bookIDs))
	}
	exec("books", "id", bookIDs)
	if err == nil && len(episodeIDs) > 0 {
		err = q.DeletePodcastEpisodeSharesByMediaIDs(ctx, store.Texts(episodeIDs))
	}
	exec("podcastEpisodes", "id", episodeIDs)
	exec("podcasts", "id", podcastIDs)
	exec("libraryItems", "id", itemIDs)
	if err != nil {
		return DeleteChanges{}, err
	}
	return changes, nil
}

// Deleter owns standalone item deletion and its post-commit effects.
type Deleter struct {
	Env
	CoverCacheDir   string
	ItemMetadataDir string
}

// DeleteSoft deletes one target and runs post-commit effects except the sweep.
func (d Deleter) DeleteSoft(ctx context.Context, target DeleteTarget) (DeleteChanges, error) {
	return d.delete(ctx, target, "Soft", nil)
}

// DeleteHard deletes one target, its files and post-commit effects except the sweep.
func (d Deleter) DeleteHard(ctx context.Context, target DeleteTarget) (DeleteChanges, error) {
	return d.delete(ctx, target, "Hard", d.RemoveItemFiles)
}

func (d Deleter) delete(ctx context.Context, target DeleteTarget, kind string, after func(context.Context, DeleteTarget)) (DeleteChanges, error) {
	logging.Infof("[LibraryItemController] (%s) deleting Library Item at %q with id %q", kind, target.Path, target.ItemID) // LibraryItemController.js:587
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeleteChanges{}, err
	}
	defer tx.Rollback()
	changes, err := DeleteRows(ctx, tx, []DeleteTarget{target})
	if err != nil {
		return DeleteChanges{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeleteChanges{}, err
	}
	playlistview.Announce(ctx, d.DB, d.Emit, changes.PlaylistChanges)
	for _, feed := range changes.ClosedFeeds {
		d.Emit.ToAll("rss_feed_closed", feed)
	}
	d.RemoveItemArtifacts(ctx, target)
	if after != nil {
		after(ctx, target)
	}
	d.AnnounceItemRemoved(target)
	return changes, nil
}

// DeleteFolderSoft deletes folder items, sweeping after each item.
func (d Deleter) DeleteFolderSoft(ctx context.Context, libraryID, folderID string) error {
	if d.DB == nil {
		return errors.New("itemwrite: deleter not configured")
	}
	var targets []DeleteTarget
	err := store.QueryIn(ctx, d.DB, `SELECT li.id,li.libraryId,li.mediaType,li.mediaId,COALESCE(li.path,'') FROM libraryItems li WHERE li.libraryFolderId IN `, ``, []string{folderID}, func(rows *sql.Rows) error {
		var target DeleteTarget
		if err := rows.Scan(&target.ItemID, &target.LibraryID, &target.MediaType, &target.MediaID, &target.Path); err != nil {
			return err
		}
		targets = append(targets, target)
		return nil
	})
	if err != nil {
		return err
	}
	for _, target := range targets {
		changes, err := d.DeleteSoft(ctx, target)
		if err != nil {
			return err
		}
		d.Sweep(ctx, changes.Authors, changes.Series)
		d.Filters.ResetIssues(ctx, d.DB, target.LibraryID)
	}
	return nil
}

// RemoveItemArtifacts purges cover-cache variants and the metadata directory.
func (d Deleter) RemoveItemArtifacts(_ context.Context, target DeleteTarget) {
	mediafiles.PurgeCoverCache(d.CoverCacheDir, target.ItemID)
	if d.ItemMetadataDir != "" {
		_ = os.RemoveAll(filepath.Join(d.ItemMetadataDir, target.ItemID))
	}
}

// RemoveItemFiles removes a hard-deleted path inside the library root; errors are logged.
func (d Deleter) RemoveItemFiles(ctx context.Context, target DeleteTarget) {
	if target.Path == "" || !mediafiles.PathAllowed(ctx, d.DB, target.Path, target.LibraryID) {
		return
	}
	logging.Infof("[LibraryItemController] Deleting library item from file system at %q", target.Path) // LibraryItemController.js:604
	if err := os.RemoveAll(target.Path); err != nil {
		logging.Errorf("[LibraryItemController] Failed to delete library item from file system at %q: %v", target.Path, err)
	}
}

// AnnounceItemRemoved emits the post-commit item_removed event.
func (d Deleter) AnnounceItemRemoved(target DeleteTarget) {
	d.Emit.ToAll("item_removed", map[string]any{"id": target.ItemID, "libraryId": target.LibraryID})
}
