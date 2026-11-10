// Package podcasts implements the podcast RSS and episode service surfaces.
// It keeps the pinned SQLite schema and public response shapes at the boundary.
package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/podcasts/read"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textdist"
	"github.com/robfig/cron/v3"
)

var errPodcastExists = errors.New("podcast already exists")

type Handler struct {
	db              *sql.DB
	cfg             config.Config
	emit            events.Emitter
	client          *http.Client
	queueMu         sync.Mutex
	queue           []*downloadTask
	current         *downloadTask
	scheduler       *cron.Cron
	scheduleIDs     map[string]cron.EntryID
	failedCheckMap  map[string]int
	maxFailedChecks int
	notify          NotificationFunc
}

// NotificationFunc delivers a notification event (notifications.Handler.TriggerEvent).
type NotificationFunc func(context.Context, string, map[string]any) error

func New(db *sql.DB, cfg config.Config, emit events.Emitter, notify NotificationFunc) *Handler {
	h := &Handler{db: db, cfg: cfg, emit: emit, notify: notify, client: httputil.SafeClient(), scheduleIDs: map[string]cron.EntryID{}, failedCheckMap: map[string]int{}, maxFailedChecks: cfg.MaxFailedEpisodeChecks}
	h.startSchedules()
	return h
}

// Routes registers the podcast routes under /api/podcasts and the two
// podcast-only library routes. The server attaches the user to the request
// context before dispatch (store.ContextWithUser).
func (h *Handler) Routes(mux *http.ServeMux) {
	adminOnly := func(fn httputil.Handler) httputil.Handler { return access.RequireAdmin(http.StatusForbidden, fn) }
	mux.Handle("GET /api/libraries/{id}/recent-episodes", h.libraryRoute(true))
	mux.Handle("GET /api/libraries/{id}/episode-downloads", h.libraryRoute(false))
	mux.Handle("POST /api/podcasts/feed", adminOnly(h.feed))
	mux.Handle("POST /api/podcasts", adminOnly(h.createPodcast))
	mux.Handle("POST /api/podcasts/opml/parse", adminOnly(h.parseOPML))
	mux.Handle("POST /api/podcasts/opml/create", adminOnly(h.createFromOPML))
	mux.Handle("GET /api/podcasts/{id}/downloads", h.onItem(func(w http.ResponseWriter, r *http.Request, item items.Item) error {
		return httputil.JSON(w, r, EpisodeDownloads{Downloads: h.downloadsFor(item.ID)})
	}))
	mux.Handle("GET /api/podcasts/{id}/clear-queue", h.onItem(func(w http.ResponseWriter, r *http.Request, item items.Item) error {
		h.clearQueue(item.ID)
		return httputil.OK(w, r)
	}))
	mux.Handle("POST /api/podcasts/{id}/download-episodes", h.onItem(h.downloadEpisodes))
	mux.Handle("GET /api/podcasts/{id}/search-episode", h.onItem(h.searchEpisode))
	mux.Handle("POST /api/podcasts/{id}/match-episodes", h.onItem(h.matchEpisodes))
	mux.Handle("GET /api/podcasts/{id}/checknew", h.onItem(h.checkNew))
	mux.Handle("GET /api/podcasts/{id}/episode/{episodeId}", h.onItem(func(w http.ResponseWriter, r *http.Request, item items.Item) error {
		ep, ok := episodeOf(item, r.PathValue("episodeId"))
		if !ok {
			return httputil.Status(404, "")
		}
		return httputil.JSON(w, r, ep.Expanded(item.ID).EpisodeJSON)
	}))
	mux.Handle("PATCH /api/podcasts/{id}/episode/{episodeId}", h.onItem(h.updateEpisode))
	mux.Handle("DELETE /api/podcasts/{id}/episode/{episodeId}", h.onItem(h.removeEpisode))
}

// onItem is PodcastController.middleware (server/controllers/PodcastController.js:
// 503-530): unknown item 404, non-podcast item 500, no library access 403,
// then the update/delete permission by HTTP method.
func (h *Handler) onItem(fn func(http.ResponseWriter, *http.Request, items.Item) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, id := store.RequestUser(r), r.PathValue("id")
		item, err := h.loadItem(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			var other string
			if e := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraryItems WHERE id=?`, id).Scan(&other); e == nil {
				// PodcastController.middleware answers a bare 500 for a non-podcast
				// item (PodcastController.js:565); upstream logs nothing.
				logging.RequestDebug(r, "responding 500: library item "+id+" is a "+other+" item, not a podcast")
				return &httputil.Error{Status: 500}
			}
			return httputil.Status(404, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		perms := access.For(r.Context(), user)
		if !perms.IsAdmin() && !perms.Item(item.Access()) ||
			r.Method == http.MethodDelete && !perms.IsAdmin() && !perms.Delete() ||
			(r.Method == http.MethodPatch || r.Method == http.MethodPost) && !perms.IsAdmin() && !perms.Update() {
			return httputil.Status(403, "")
		}
		return fn(w, r, item)
	}
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		RSSFeed string `json:"rssFeed"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || validFeedURL(body.RSSFeed) == "" {
		return httputil.Status(400, `Invalid request body. "rssFeed" must be a valid URL`)
	}
	feed, err := h.getFeed(r.Context(), body.RSSFeed)
	if err != nil {
		return httputil.Status(404, "Podcast RSS feed request failed or invalid response data")
	}
	return httputil.JSON(w, r, map[string]any{"podcast": feed})
}

func (h *Handler) parseOPML(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		OPMLText string `json:"opmlText"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil || body.OPMLText == "" {
		return httputil.Status(400, "")
	}
	return httputil.JSON(w, r, map[string]any{"feeds": parseOPML(body.OPMLText)})
}

func (h *Handler) downloadEpisodes(w http.ResponseWriter, r *http.Request, item items.Item) error {
	if !access.AdminUser(store.RequestUser(r)) {
		return httputil.Status(403, "")
	}
	var episodes []Episode
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&episodes) != nil || len(episodes) == 0 {
		return httputil.Status(400, "")
	}
	if err := h.enqueue(item, episodes, false); err != nil {
		return httputil.Status(400, err.Error())
	}
	return httputil.OK(w, r)
}

func (h *Handler) searchEpisode(w http.ResponseWriter, r *http.Request, item items.Item) error {
	if str(item.Podcast.FeedURL) == "" {
		return httputil.Status(400, "Podcast does not have an RSS feed URL")
	}
	title := r.URL.Query().Get("title")
	if title == "" {
		logging.RequestDebug(r, "responding 500: query parameter title is empty")
		return &httputil.Error{Status: 500}
	}
	feed, err := h.getFeed(r.Context(), str(item.Podcast.FeedURL))
	if err != nil {
		return httputil.JSON(w, r, map[string]any{"episodes": nil})
	}
	return httputil.JSON(w, r, map[string]any{"episodes": matchEpisodes(feed.Episodes, title)})
}

func (h *Handler) matchEpisodes(w http.ResponseWriter, r *http.Request, item items.Item) error {
	updated, err := h.QuickMatchEpisodes(r.Context(), item.ID, r.URL.Query().Get("override") == "1")
	if err != nil {
		return httputil.Internal("Podcast episode matching failed", err)
	}
	if updated > 0 {
		if fresh, e := h.loadItem(r.Context(), item.ID); e == nil {
			h.emit.ToAdmins("item_updated", h.expanded(r.Context(), fresh))
		}
	}
	return httputil.JSON(w, r, map[string]any{"numEpisodesUpdated": updated})
}

func (h *Handler) checkNew(w http.ResponseWriter, r *http.Request, item items.Item) error {
	if !access.AdminUser(store.RequestUser(r)) {
		return httputil.Status(403, "")
	}
	if str(item.Podcast.FeedURL) == "" {
		return httputil.Status(400, "Podcast has no rss feed url")
	}
	feed, err := h.getFeed(r.Context(), str(item.Podcast.FeedURL))
	if err != nil {
		return httputil.JSON(w, r, map[string]any{"episodes": []any{}})
	}
	afterMS := int64(0)
	if t := item.Podcast.LastEpisodeCheck; t != nil {
		afterMS = *t
	}
	limit := 3
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil {
			limit = n
		}
	}
	found := filterNew(feed.Episodes, item, afterMS, limit)
	now := dbtime.Now()
	if _, err := h.db.ExecContext(r.Context(), `UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if len(found) > 0 {
		if err := h.enqueue(item, found, false); err != nil {
			logging.Errorf("[podcasts] manual episode check enqueue failed for %s: %v", item.ID, err)
		}
	}
	return httputil.JSON(w, r, map[string]any{"episodes": found})
}

func episodeOf(it items.Item, id string) (podcastread.Episode, bool) {
	for _, ep := range it.Podcast.Episodes {
		if ep.ID == id {
			return ep, true
		}
	}
	return podcastread.Episode{}, false
}

// knownEpisodes keys an item's episodes by guid and enclosure URL.
func knownEpisodes(it items.Item) map[string]bool {
	known := map[string]bool{}
	for _, ep := range it.Podcast.Episodes {
		if ep.GUID != "" {
			known["g:"+ep.GUID] = true
		}
		if ep.Enclosure != nil {
			known["u:"+ep.Enclosure.URL] = true
		}
	}
	return known
}

// loadItem loads a podcast item with its episodes; any other item is not found.
func (h *Handler) loadItem(ctx context.Context, id string) (items.Item, error) {
	it, err := items.LoadOne(ctx, h.db, id, items.Options{Episodes: true})
	if err == nil && it.Podcast == nil {
		err = sql.ErrNoRows
	}
	return it, err
}

func (h *Handler) expanded(ctx context.Context, it items.Item) items.Expanded {
	return it.Expanded(serversettings.Load(ctx, h.db).SortingPrefixes())
}

// deleteEpisode removes an episode row with its playlist entries and progress,
// drops its audio file from the item's libraryFiles and recounts numEpisodes.
func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type episodeMatch struct {
	Episode Episode `json:"episode"`
}

func matchEpisodes(episodes []Episode, query string) []episodeMatch {
	type score struct {
		ep Episode
		s  float64
	}
	var found []score
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, ep := range episodes {
		a := similarity(needle, strings.ToLower(ep.Title))
		b := similarity(needle, strings.ToLower(ep.Subtitle))
		if a > 0.6 || b > 0.6 {
			found = append(found, score{ep, a*.7 + b*.3})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].s > found[j].s })
	out := make([]episodeMatch, len(found))
	for i, x := range found {
		out[i] = episodeMatch{x.ep}
	}
	return out
}

// FetchFeedEpisodes parses a feed through the handler's HTTP client and network
// policy and returns the episodes in API shape.
func (h *Handler) FetchFeedEpisodes(ctx context.Context, feedURL string) ([]map[string]any, error) {
	feed, err := h.getFeed(ctx, feedURL)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(feed.Episodes)
	if err != nil {
		return nil, err
	}
	var episodes []map[string]any
	return episodes, json.Unmarshal(raw, &episodes)
}
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	distance := textdist.Levenshtein(a, b)
	maxLen := len([]rune(a))
	if n := len([]rune(b)); n > maxLen {
		maxLen = n
	}
	return 1 - float64(distance)/float64(maxLen)
}

func (h *Handler) Close() {
	if h.scheduler != nil {
		stopped := h.scheduler.Stop()
		<-stopped.Done()
	}
	h.queueMu.Lock()
	if h.current != nil && h.current.cancel != nil {
		h.current.cancel()
	}
	h.queue = nil
	h.queueMu.Unlock()
}

func stringLike(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	default:
		return "", false
	}
}
func anyString(v any) string { s, _ := stringLike(v); return s }
func stringArray(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok || s == "" {
			return []string{}
		}
		out = append(out, s)
	}
	return out
}
