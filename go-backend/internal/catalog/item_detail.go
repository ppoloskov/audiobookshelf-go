package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// numEpisodesIncomplete is podcasts.numEpisodes minus the user's finished
// progress rows tagged with the podcast
// (libraryItemsPodcastFilters.getFilteredLibraryItems,
// server/utils/queries/libraryItemsPodcastFilters.js:221-229). Callers set the
// key only when it is non-zero, as LibraryItem.getByFilterAndSort does
// (server/models/LibraryItem.js:313).
func (h *Handler) numEpisodesIncomplete(ctx context.Context, userID, podcastID string) (int64, error) {
	var numEpisodes, finished int64
	if err := h.db.QueryRowContext(ctx, `SELECT COALESCE(numEpisodes,0) FROM podcasts WHERE id=?`, podcastID).Scan(&numEpisodes); err != nil {
		return 0, err
	}
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mediaProgresses WHERE userId=? AND podcastId=? AND isFinished=1`, userID, podcastID).Scan(&finished); err != nil {
		return 0, err
	}
	return numEpisodes - finished, nil
}

func (h *Handler) item(w http.ResponseWriter, r *http.Request) error {
	user, itemID := store.RequestUser(r), r.PathValue("id")
	expanded := r.URL.Query().Get("expanded") == "1"
	it, err := items.LoadOne(r.Context(), h.db, itemID, items.Options{Episodes: true})
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !access.For(r.Context(), user).Item(it.Access()) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if !expanded {
		return httputil.JSON(w, r, it.Full())
	}
	item := it.Expanded(h.sortingPrefixes(r.Context()))
	if err := h.expandedItemIncludes(r, user, item.Head, item.MediaID(), &item.Extras); err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, item)
}

// expandedItemIncludes applies the include= entities of upstream
// LibraryItemController.findOne (server/controllers/LibraryItemController.js:71-96).
// Upstream splits include on "," without trimming or lowercasing.
func (h *Handler) expandedItemIncludes(r *http.Request, user store.User, head items.Head, mediaID string, extras *items.Extras) error {
	includes := strings.Split(r.URL.Query().Get("include"), ",")
	has := func(v string) bool { return slices.Contains(includes, v) }
	itemID := head.ID
	if has("progress") {
		progress, err := progressdto.ReadOne(r.Context(), h.db, user.ID, itemID, r.URL.Query().Get("episode"))
		if err != nil {
			return err
		}
		if progress == nil {
			extras.UserMediaProgress = items.JSONNull
		} else {
			extras.UserMediaProgress = progress
		}
	}
	if has("rssfeed") {
		feed, ok, err := h.feedMinified(r.Context(), "", itemID)
		if err != nil {
			return err
		}
		if ok {
			extras.RSSFeed = feed
		} else {
			extras.RSSFeed = items.JSONNull
		}
	}
	if head.MediaType == "podcast" && has("downloads") && h.deps.Downloads != nil {
		queued, current := h.deps.Downloads.EpisodeDownloads(itemID)
		extras.EpisodeDownloadsQueued = queued
		if current != nil {
			extras.EpisodesDownloading = []wire.EpisodeDownload{*current}
		}
	}
	// LibraryItemController.findOne: `item.mediaItemShare = ShareManager.
	// findByMediaItemId(item.media.id)`, null when no open share exists.
	if has("share") && access.AdminUser(user) && head.MediaType == "book" {
		share, err := h.openMediaItemShare(r.Context(), mediaID)
		if err != nil {
			return err
		}
		if share == nil {
			extras.MediaItemShare = items.JSONNull
		} else {
			extras.MediaItemShare = share
		}
	}
	return nil
}

// fullItem is the upstream legacy full item. Callers enforce permissions.
func (h *Handler) fullItem(ctx context.Context, itemID string) (items.Full, error) {
	it, err := items.LoadOne(ctx, h.db, itemID, items.Options{Episodes: true})
	return it.Full(), err
}

// expandedItem is the expanded item. Callers enforce permissions.
func (h *Handler) expandedItem(ctx context.Context, itemID string) (items.Expanded, error) {
	out, err := h.expandedItems(ctx, []string{itemID})
	if err == nil && len(out) == 0 {
		err = sql.ErrNoRows
	}
	if err != nil {
		return items.Expanded{}, err
	}
	return out[0], nil
}
