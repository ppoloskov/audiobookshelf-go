// Package playback runs listening sessions: starting, syncing and closing them
// (in memory while open, persisted once listened), offline session upload,
// direct and HLS-transcoded audio, the admin session history, the device record
// and the user's media progress rules.
package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"monstrobook/internal/auth"
	"monstrobook/internal/config"
	"monstrobook/internal/db"
	"monstrobook/internal/events"
	"monstrobook/internal/httpx"
	"monstrobook/internal/settings"
)

const (
	defaultIdleTTL      = 30 * time.Minute
	defaultReapInterval = 5 * time.Minute
	maxLocalIDs         = 10000
)

// Handler owns the playback routes and the open sessions.
type Handler struct {
	db           *db.DB
	settings     *settings.Store
	cfg          config.Config
	emit         events.Emitter
	progress     *Progress
	idleTTL      time.Duration
	reapInterval time.Duration
	ctx          context.Context
	stop         context.CancelFunc
	wg           sync.WaitGroup

	mu       sync.Mutex // guards sessions, localIDs and the mutable fields of every session
	sessions map[string]*session
	localIDs map[string]string // (userID + "\x00" + client play_local_* id) -> the server id it was stored under
}

// New returns a Handler; settings supplies the sorting prefixes of the item view
// a session carries.
func New(d *db.DB, settings *settings.Store, cfg config.Config, emit events.Emitter) *Handler {
	return newHandler(d, settings, cfg, emit, defaultIdleTTL, defaultReapInterval)
}

func newHandler(d *db.DB, settings *settings.Store, cfg config.Config, emit events.Emitter, idleTTL, reapInterval time.Duration) *Handler {
	ctx, stop := context.WithCancel(context.Background())
	h := &Handler{
		db: d, settings: settings, cfg: cfg, emit: emit, progress: NewProgress(d),
		idleTTL: idleTTL, reapInterval: reapInterval,
		ctx: ctx, stop: stop,
		sessions: map[string]*session{}, localIDs: map[string]string{},
	}
	h.startReaper()
	return h
}

func (h *Handler) startReaper() {
	if h.idleTTL <= 0 || h.reapInterval <= 0 {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(h.reapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-h.ctx.Done():
				return
			case now := <-ticker.C:
				h.reapIdle(now)
			}
		}
	}()
}

// Progress is the media progress store the playback routes write through.
func (h *Handler) Progress() *Progress { return h.progress }

type userHandler func(w http.ResponseWriter, r *http.Request, user *auth.Principal) error

// api runs next for an authenticated user; the runtime has authenticated the
// request before it reaches the mux.
func api(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user := auth.User(r.Context())
		if user == nil {
			return httpx.Error(http.StatusUnauthorized, "Unauthorized")
		}
		return next(w, r, user)
	}
}

// Routes registers the routes. The track and HLS routes are capability URLs that
// need no user (the session id is the secret) and answer a wrong method 405; all
// others need an authenticated user, and each handler states what else it checks.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("/public/session/{id}/track/{index}", httpx.Handler(h.track))
	mux.Handle("/hls/{id}/{name}", httpx.Handler(h.hls))
	mux.Handle("POST /api/items/{id}/play", api(h.start))
	mux.Handle("POST /api/items/{id}/play/{episodeId}", api(h.start))
	mux.Handle("GET /api/sessions", api(h.history))
	mux.Handle("GET /api/sessions/open", api(h.openSessions))
	mux.Handle("POST /api/sessions/batch/delete", auth.Admin(api(h.batchDelete)))
	mux.Handle("DELETE /api/sessions/{id}", api(h.deleteHistory))
	mux.Handle("POST /api/session/local", api(h.localSync))
	mux.Handle("POST /api/session/local-all", api(h.localSyncAll))
	mux.Handle("GET /api/session/{id}", api(h.getSession))
	mux.Handle("POST /api/session/{id}/sync", api(h.sync))
	mux.Handle("POST /api/session/{id}/close", api(h.closeSession))
}

// ok answers 200 "OK".
func ok(w http.ResponseWriter) error { return httpx.Text(w, http.StatusOK, "OK") }

// decodeBody decodes an optional JSON body: clients close a session without one.
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	data, err := readBody(w, r, httpx.DefaultBodyLimit)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		var zero T
		return zero, err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return httpx.Decode[T](w, r)
}

// readBody reads the request body, at most limit bytes (413 beyond).
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return data, nil
	case errors.As(err, &tooLarge):
		return nil, httpx.Error(http.StatusRequestEntityTooLarge, "Request body too large")
	}
	return nil, httpx.BadRequest("Invalid request body")
}
