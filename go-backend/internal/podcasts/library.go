package podcasts

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// libraryRoute serves the two podcast-only library routes
// GET /api/libraries/:id/recent-episodes (recent true) and
// GET /api/libraries/:id/episode-downloads.
func (h *Handler) libraryRoute(recent bool) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, libraryID := requestUser(r), r.PathValue("id")
		lib, err := httputil.OpenLibrary(r, h.db, access.For(r.Context(), user).Library, libraryID)
		if err != nil {
			return err
		}
		limit, page := lib.Limit, lib.Page
		if !recent {
			return httputil.JSON(w, r, h.libraryDownloadQueue(libraryID))
		}
		// LibraryController.getRecentEpisodes (LibraryController.js:1280).
		if lib.MediaType != "podcast" {
			return httputil.Status(http.StatusNotFound, "")
		}
		episodes, err := h.recentEpisodes(r.Context(), user, libraryID, limit, limit*page)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		return httputil.JSON(w, r, map[string]any{"episodes": episodes, "limit": limit, "page": page})
	}
}

// libraryDownloadQueue is PodcastManager.getDownloadQueueDetails(libraryId)
// (server/managers/PodcastManager.js:509): the current download only when it
// belongs to the library, and the queued downloads of that library.
func (h *Handler) libraryDownloadQueue(libraryID string) EpisodeDownloadQueue {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	out := EpisodeDownloadQueue{Queue: make([]wire.EpisodeDownload, 0)}
	for _, d := range h.queue {
		if d.item.LibraryID == libraryID {
			out.Queue = append(out.Queue, d.clientJSON())
		}
	}
	if h.current != nil && h.current.item.LibraryID == libraryID {
		current := h.current.clientJSON()
		out.CurrentDownload = &current
	}
	return out
}

// recentEpisode is an expanded episode with its bare podcast and library.
type recentEpisode struct {
	libitem.EpisodeExpanded
	Podcast   any    `json:"podcast"`
	LibraryID string `json:"libraryId"`
}

// recentEpisodes is libraryItemsPodcastFilters.getRecentEpisodes
// (server/utils/queries/libraryItemsPodcastFilters.js:520): the newest
// unfinished episodes of the library by publishedAt, each carrying its
// podcast without episodes and the libraryId. A zero limit yields no rows
// because Sequelize emits LIMIT offset,0.
func (h *Handler) recentEpisodes(ctx context.Context, user store.User, libraryID string, limit, offset int) ([]recentEpisode, error) {
	episodes := make([]recentEpisode, 0)
	permission, args := access.For(ctx, user).MediaClause("p")
	query := `SELECT pe.id, li.id FROM podcastEpisodes pe
		JOIN podcasts p ON p.id = pe.podcastId
		JOIN libraryItems li ON li.mediaId = p.id AND li.mediaType = 'podcast'
		LEFT JOIN mediaProgresses mp ON mp.mediaItemId = pe.id AND mp.userId = ?
		WHERE li.libraryId = ? AND (mp.isFinished IS NULL OR mp.isFinished = 0)` + permission + `
		ORDER BY pe.publishedAt DESC LIMIT ? OFFSET ?`
	queryArgs := append([]any{user.ID, libraryID}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := h.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	type ref struct{ episodeID, itemID string }
	refs := make([]ref, 0)
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.episodeID, &x.itemID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		refs = append(refs, x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(refs))
	for i, x := range refs {
		itemIDs[i] = x.itemID
	}
	loaded, err := libitem.Load(ctx, h.db, itemIDs, libitem.Options{Episodes: true})
	if err != nil {
		return nil, fmt.Errorf("podcasts: load items: %w", err)
	}
	items := make(map[string]libitem.Item, len(loaded))
	for _, it := range loaded {
		items[it.ID] = it
	}
	for _, x := range refs {
		item, ok := items[x.itemID]
		if !ok || item.Podcast == nil {
			continue
		}
		episode, ok := episodeOf(item, x.episodeID)
		if !ok {
			continue
		}
		bare := *item.Podcast
		bare.Episodes = nil
		item.Podcast = &bare
		episodes = append(episodes, recentEpisode{episode.Expanded(item.ID), item.Full().Media, item.LibraryID})
	}
	return episodes, nil
}
