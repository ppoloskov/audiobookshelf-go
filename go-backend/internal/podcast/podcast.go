// Package podcast owns podcasts fed by RSS: feed parsing, podcast creation and
// OPML import, the episode download queue, the auto-download schedule, the
// episode routes and the two podcast-only library routes.
package podcast

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/robfig/cron/v3"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

// NotifyFunc delivers a notification event (notify.Handler.TriggerEvent).
type NotifyFunc func(ctx context.Context, event string, data map[string]any) error

// Handler serves the podcast routes and runs the download queue and the
// auto-download schedule. One mutex guards the queue, the cron entries and the
// failure counts; Close stops them and waits for the goroutines.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	notify   NotifyFunc
	// announcePlaylists tells playlist owners about a deleted episode; may be nil.
	announcePlaylists func(ctx context.Context, changes []item.PlaylistChange)
	client            *http.Client
	maxFailedChecks   int
	ctx               context.Context // canceled by Close; every background job uses it
	stop              context.CancelFunc
	wg                sync.WaitGroup

	// mu guards the fields below. Emitters are called with mu held and must
	// not call back into the Handler (the realtime hub does not).
	mu      sync.Mutex
	closed  bool
	queue   []*download
	current *download
	cron    *cron.Cron
	entries map[string]cron.EntryID // item id -> auto-download cron entry
	failed  map[string]int          // item id -> consecutive failed feed checks
}

// New returns a Handler (announcePlaylists is item.Deps.AnnouncePlaylists) and schedules every podcast with auto-download on.
func New(d *db.DB, st *settings.Store, cfg config.Config, emit events.Emitter, notify NotifyFunc, announcePlaylists func(context.Context, []item.PlaylistChange)) *Handler {
	h := &Handler{db: d, settings: st, emit: emit, notify: notify, announcePlaylists: announcePlaylists, client: httpx.NewSafeClient(), maxFailedChecks: cfg.MaxFailedEpisodeChecks,
		cron: cronspec.New(), entries: map[string]cron.EntryID{}, failed: map[string]int{}}
	h.ctx, h.stop = context.WithCancel(context.Background())
	var ids []string
	err := d.Select(h.ctx, &ids, `SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.mediaType='podcast' AND p.autoDownloadEpisodes=1`)
	if err != nil {
		slog.Error("podcast: list auto-download podcasts", "error", err)
	}
	for _, id := range ids {
		h.Reschedule(h.ctx, id)
	}
	h.cron.Start()
	return h
}

// Close cancels the running download, drops the queue, stops the schedule and
// waits for every goroutine. It may be called again.
func (h *Handler) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed, h.queue = true, nil
	h.mu.Unlock()
	h.stop()
	<-h.cron.Stop().Done()
	h.wg.Wait()
}

// spawn runs fn in a goroutine that Close waits for; after Close it does nothing.
func (h *Handler) spawn(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		fn()
	}()
}

// Routes registers the podcast routes. The create, feed and OPML routes are
// admin only; the item routes check library access, then the update or delete
// permission by method (PodcastController.middleware); the library routes
// check library access.
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(pattern string, fn httpx.Handler) { mux.Handle(pattern, auth.Admin(fn)) }
	admin("POST /api/podcasts", h.createPodcast)
	admin("POST /api/podcasts/feed", h.feed)
	admin("POST /api/podcasts/opml/parse", h.parseOPML)
	admin("POST /api/podcasts/opml/create", h.createFromOPML)
	onItem := func(pattern string, fn func(http.ResponseWriter, *http.Request, *item.Item) error) {
		mux.Handle(pattern, h.onItem(fn))
	}
	onItem("GET /api/podcasts/{id}/downloads", h.itemDownloads)
	onItem("GET /api/podcasts/{id}/clear-queue", h.clearQueue)
	onItem("POST /api/podcasts/{id}/download-episodes", h.downloadEpisodes)
	onItem("GET /api/podcasts/{id}/search-episode", h.searchEpisode)
	onItem("POST /api/podcasts/{id}/match-episodes", h.matchEpisodes)
	onItem("GET /api/podcasts/{id}/checknew", h.checkNew)
	onItem("GET /api/podcasts/{id}/episode/{episodeId}", h.getEpisode)
	onItem("PATCH /api/podcasts/{id}/episode/{episodeId}", h.updateEpisode)
	onItem("DELETE /api/podcasts/{id}/episode/{episodeId}", h.removeEpisode)
	mux.Handle("GET /api/libraries/{id}/recent-episodes", httpx.Handler(h.recentEpisodes))
	mux.Handle("GET /api/libraries/{id}/episode-downloads", httpx.Handler(h.libraryDownloads))
}

// onItem loads the podcast item of the path and checks the caller: unknown item
// 404, a non-podcast item a bare 500 (PodcastController.js:565), no library
// access 403, then the update (PATCH, POST) or delete permission. Admins pass.
func (h *Handler) onItem(fn func(http.ResponseWriter, *http.Request, *item.Item) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		it, err := h.load(r.Context(), r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return httpx.Internal("database read failed", err)
		}
		if it.Podcast == nil {
			return httpx.Error(http.StatusInternalServerError, "")
		}
		user, access := auth.User(r.Context()), it.Access()
		if !user.IsAdmin() && (!user.CanAccessItem(access.LibraryID, access.Explicit, access.Tags) ||
			r.Method == http.MethodDelete && !user.CanDelete() ||
			(r.Method == http.MethodPatch || r.Method == http.MethodPost) && !user.CanUpdate()) {
			return httpx.Forbidden()
		}
		return fn(w, r, it)
	}
}

// load reads an item with its episodes.
func (h *Handler) load(ctx context.Context, id string) (*item.Item, error) {
	return item.Get(ctx, h.db, id, item.Options{Episodes: true})
}

// expanded is the item view sent in item_updated and item_added events.
func (h *Handler) expanded(it *item.Item) item.Expanded {
	return it.Expanded(h.settings.Server().SortingPrefixes)
}

// announce reloads the item and sends item_updated to the admins; a failed
// reload is logged and nothing is sent.
func (h *Handler) announce(ctx context.Context, itemID string) *item.Item {
	it, err := h.load(ctx, itemID)
	if err != nil {
		slog.Error("podcast: reload item", "item", itemID, "error", err)
		return nil
	}
	h.emit.ToAdmins("item_updated", h.expanded(it))
	return it
}
