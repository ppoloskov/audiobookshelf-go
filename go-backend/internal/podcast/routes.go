package podcast

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/wire"
)

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[struct {
		RSSFeed string `json:"rssFeed"`
	}](w, r)
	if err != nil {
		return err
	}
	if httpURL(req.RSSFeed) == "" {
		return httpx.BadRequest(`Invalid request body. "rssFeed" must be a valid URL`)
	}
	parsed, err := h.fetchFeed(r.Context(), req.RSSFeed)
	if err != nil {
		return httpx.Error(http.StatusNotFound, "Podcast RSS feed request failed or invalid response data")
	}
	return httpx.JSON(w, r, struct {
		Podcast *feed `json:"podcast"`
	}{parsed})
}

func (h *Handler) parseOPML(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.DecodeLimit[struct {
		OPMLText string `json:"opmlText"`
	}](w, r, 4<<20)
	if err != nil {
		return err
	}
	if req.OPMLText == "" {
		return httpx.BadRequest("")
	}
	return httpx.JSON(w, r, struct {
		Feeds []opmlFeed `json:"feeds"`
	}{parseOPML(req.OPMLText)})
}

func (h *Handler) itemDownloads(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	queued, _ := h.EpisodeDownloads(it.ID)
	return httpx.JSON(w, r, struct {
		Downloads []wire.EpisodeDownload `json:"downloads"`
	}{queued})
}

func (h *Handler) clearQueue(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	h.clear(it.ID)
	return httpx.Status(w, http.StatusOK)
}

// downloadEpisodes queues feed episodes the web client posts back. Admin only.
func (h *Handler) downloadEpisodes(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	if !auth.User(r.Context()).IsAdmin() {
		return httpx.Forbidden()
	}
	episodes, err := httpx.DecodeLimit[[]feedEpisode](w, r, 8<<20)
	if err != nil {
		return err
	}
	if len(episodes) == 0 {
		return httpx.BadRequest("")
	}
	if err := h.enqueue(it, episodes, false); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.Status(w, http.StatusOK)
}

// searchEpisode lists the feed episodes similar to the title query, best first.
func (h *Handler) searchEpisode(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	feedURL := db.Val(it.Podcast.FeedURL)
	if feedURL == "" {
		return httpx.BadRequest("Podcast does not have an RSS feed URL")
	}
	title := r.URL.Query().Get("title")
	if title == "" {
		return httpx.Error(http.StatusInternalServerError, "") // upstream: a bare 500 for a missing title
	}
	type match struct {
		Episode feedEpisode `json:"episode"`
		score   float64
	}
	var matches []match // null when the feed cannot be read
	if parsed, err := h.fetchFeed(r.Context(), feedURL); err == nil {
		matches = []match{}
		needle := strings.ToLower(strings.TrimSpace(title))
		for _, ep := range parsed.Episodes {
			byTitle, bySubtitle := similarity(needle, strings.ToLower(ep.Title)), similarity(needle, strings.ToLower(ep.Subtitle))
			if byTitle > .6 || bySubtitle > .6 {
				matches = append(matches, match{ep, byTitle*.7 + bySubtitle*.3})
			}
		}
		slices.SortStableFunc(matches, func(a, b match) int {
			switch {
			case a.score > b.score:
				return -1
			case a.score < b.score:
				return 1
			}
			return 0
		})
	}
	return httpx.JSON(w, r, struct {
		Episodes []match `json:"episodes"`
	}{matches})
}

func (h *Handler) matchEpisodes(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	updated, err := h.QuickMatchEpisodes(r.Context(), it.ID, r.URL.Query().Get("override") == "1")
	if err != nil {
		return httpx.Internal("Podcast episode matching failed", err)
	}
	if updated > 0 {
		h.announce(r.Context(), it.ID)
	}
	return httpx.JSON(w, r, struct {
		NumEpisodesUpdated int `json:"numEpisodesUpdated"`
	}{updated})
}

// checkNew queues the feed episodes published since the last check, at most
// ?limit (default 3). Admin only.
func (h *Handler) checkNew(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	if !auth.User(r.Context()).IsAdmin() {
		return httpx.Forbidden()
	}
	feedURL := db.Val(it.Podcast.FeedURL)
	if feedURL == "" {
		return httpx.BadRequest("Podcast has no rss feed url")
	}
	type result struct {
		Episodes []feedEpisode `json:"episodes"`
	}
	parsed, err := h.fetchFeed(r.Context(), feedURL)
	if err != nil {
		return httpx.JSON(w, r, result{[]feedEpisode{}})
	}
	limit := 3
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = n
	}
	found := filterNew(parsed.Episodes, it, lastCheck(it), limit)
	if err := h.markChecked(r.Context(), it); err != nil {
		return httpx.Internal("database write failed", err)
	}
	if len(found) > 0 {
		if err := h.enqueue(it, found, false); err != nil {
			slog.Error("podcast: queue new episodes", "item", it.ID, "error", err) // the check itself succeeded
		}
	}
	return httpx.JSON(w, r, result{found})
}
