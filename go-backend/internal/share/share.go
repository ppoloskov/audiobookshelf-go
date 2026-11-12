// Package share serves media-item shares and the RSS feeds the server publishes.
// The public routes (/public/share, /feed) carry no user: the slug, the share
// session cookie or the episode id is the capability.
package share

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/media"
)

// Handler owns the routes of this package and the in-memory share playback
// sessions. Build one with New.
type Handler struct {
	db       *db.DB
	emit     events.Emitter
	base     string // router base path, prefixed to URLs the handler writes
	sessions *sessions
}

// New returns a Handler. routerBasePath is the configured ROUTER_BASE_PATH
// ("" when unset). A nil emit discards events.
func New(d *db.DB, emit events.Emitter, routerBasePath string) *Handler {
	return &Handler{db: d, emit: emit, base: routerBasePath, sessions: &sessions{byID: map[string]*session{}}}
}

// Routes registers the routes on mux. Admin routes are wrapped in auth.Admin;
// the /feed and /public/share routes are public (the runtime mounts them
// without a user).
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(pattern string, f httpx.Handler) { mux.Handle(pattern, auth.Admin(f)) }
	admin("GET /api/feeds", h.listFeeds)
	for kind, path := range map[entityKind]string{itemFeed: "item", collectionFeed: "collection", seriesFeed: "series"} {
		admin("POST /api/feeds/"+path+"/{id}/open", func(w http.ResponseWriter, r *http.Request) error { return h.openFeed(w, r, kind) })
	}
	admin("POST /api/feeds/{id}/close", h.closeFeed)
	admin("POST /api/share/mediaitem", h.createShare)
	admin("DELETE /api/share/mediaitem/{id}", h.deleteShare)

	mux.Handle("GET /feed/{slug}", httpx.Handler(h.publicFeed))
	mux.Handle("GET /feed/{slug}/{tail...}", httpx.Handler(h.publicFeed))
	mux.Handle("GET /public/share/{slug}", httpx.Handler(h.publicShare))
	mux.Handle("GET /public/share/{slug}/cover", httpx.Handler(h.shareCover))
	mux.Handle("GET /public/share/{slug}/track/{index}", httpx.Handler(h.shareTrack))
	mux.Handle("GET /public/share/{slug}/download", httpx.Handler(h.shareDownload))
	mux.Handle("PATCH /public/share/{slug}/progress", httpx.Handler(h.shareProgress))
}

// hostPrefix is the request origin plus the router base path when the request
// URL carried it (req.originalHostPrefix, server/Server.js:324). Feed XML
// links are absolute with it.
func hostPrefix(r *http.Request, base string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if strings.Contains(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	prefix := ""
	if strings.HasPrefix(r.RequestURI, base) {
		prefix = base
	}
	return scheme + "://" + r.Host + prefix
}

// serveMedia serves a file with Range support; filename, if not empty, makes
// it an attachment. Audio takes the MIME type of its extension
// (ShareController.js:209); anything else is left to http.ServeContent.
func serveMedia(w http.ResponseWriter, r *http.Request, path, filename string) error {
	if mimeType := media.KnownAudioMime(filepath.Ext(path)); mimeType != "" {
		w.Header().Set("Content-Type", mimeType)
	}
	return httpx.Download(w, r, path, filename)
}

// isoTime is a JavaScript Date as JSON.stringify writes it.
func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// orDefault is s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// nilIfEmpty is a pointer to s, nil when s is empty.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
