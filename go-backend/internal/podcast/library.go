package podcast

import (
	"database/sql"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/wire"
)

// openLibrary is LibraryController.middleware: 403 when the library is closed
// to the user, 404 when it does not exist, 400 for a negative limit or page.
func (h *Handler) openLibrary(r *http.Request) (library db.Library, limit, page int, err error) {
	id := r.PathValue("id")
	if !auth.User(r.Context()).CanAccessLibrary(id) {
		return library, 0, 0, httpx.Forbidden()
	}
	if err := h.db.Get(r.Context(), &library, `SELECT * FROM libraries WHERE id=?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return library, 0, 0, httpx.Error(http.StatusNotFound, "Library not found")
		}
		return library, 0, 0, httpx.Internal("database read failed", err)
	}
	limit, page = httpx.QueryInt(r, "limit", 0), httpx.QueryInt(r, "page", 0)
	if limit < 0 || page < 0 {
		return library, 0, 0, httpx.BadRequest("Invalid request. limit and page must be positive integers")
	}
	return library, limit, page, nil
}

// libraryDownloads answers GET /api/libraries/{id}/episode-downloads: the
// running download when it belongs to the library, and the queued ones of it
// (PodcastManager.getDownloadQueueDetails).
func (h *Handler) libraryDownloads(w http.ResponseWriter, r *http.Request) error {
	library, _, _, err := h.openLibrary(r)
	if err != nil {
		return err
	}
	queued, current := h.downloadsOf(func(it *item.Item) bool { return db.Val(it.LibraryID) == library.ID })
	return httpx.JSON(w, r, struct {
		CurrentDownload *wire.EpisodeDownload  `json:"currentDownload,omitempty"`
		Queue           []wire.EpisodeDownload `json:"queue"`
	}{current, queued})
}

// recentEpisode is an expanded episode with its podcast (without episodes) and
// library.
type recentEpisode struct {
	item.EpisodeExpanded
	Podcast   item.PodcastFull `json:"podcast"`
	LibraryID string           `json:"libraryId"`
}

// recentEpisodes answers GET /api/libraries/{id}/recent-episodes: the newest
// unfinished episodes of the library the user may see, by publishedAt
// (libraryItemsPodcastFilters.getRecentEpisodes). A zero limit yields none.
func (h *Handler) recentEpisodes(w http.ResponseWriter, r *http.Request) error {
	library, limit, page, err := h.openLibrary(r)
	if err != nil {
		return err
	}
	if db.Val(library.MediaType) != "podcast" {
		return httpx.NotFound()
	}
	user := auth.User(r.Context())
	clause, args := user.MediaClause("p")
	var refs []struct {
		EpisodeID string `db:"episodeId"`
		ItemID    string `db:"itemId"`
	}
	err = h.db.Select(r.Context(), &refs, `SELECT pe.id AS episodeId, li.id AS itemId FROM podcastEpisodes pe
		JOIN podcasts p ON p.id = pe.podcastId
		JOIN libraryItems li ON li.mediaId = p.id AND li.mediaType = 'podcast'
		LEFT JOIN mediaProgresses mp ON mp.mediaItemId = pe.id AND mp.userId = ?
		WHERE li.libraryId = ? AND (mp.isFinished IS NULL OR mp.isFinished = 0)`+clause+`
		ORDER BY pe.publishedAt DESC LIMIT ? OFFSET ?`, append([]any{user.ID, library.ID}, append(args, limit, limit*page)...)...)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	itemIDs := make([]string, len(refs))
	for i, ref := range refs {
		itemIDs[i] = ref.ItemID
	}
	loaded, err := item.Load(r.Context(), h.db, itemIDs, item.Options{Episodes: true})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	byID := make(map[string]item.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	episodes := []recentEpisode{}
	for _, ref := range refs {
		it, ok := byID[ref.ItemID]
		if !ok || it.Podcast == nil {
			continue
		}
		ep, ok := episodeOf(&it, ref.EpisodeID)
		if !ok {
			continue
		}
		bare := *it.Podcast
		bare.Episodes = nil
		episodes = append(episodes, recentEpisode{ep.Expanded(it.ID), bare.Full(it.ID), db.Val(it.LibraryID)})
	}
	return httpx.JSON(w, r, struct {
		Episodes []recentEpisode `json:"episodes"`
		Limit    int             `json:"limit"`
		Page     int             `json:"page"`
	}{episodes, limit, page})
}
