// Package admin implements the administrative routes that belong to no other
// domain: settings, statistics, tags, genres, uploads, filesystem and cache.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
)

// Handler owns the routes of this package. Build one with New.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	cfg      config.Config
	getenv   func(string) string
	deps     Deps
}

// Deps are the collaborators of the routes that act outside this package.
type Deps struct {
	TaxonomyChanged func()                                         // drops the catalog's filter data after a tag or genre edit
	ItemChanged     func(ctx context.Context, itemID string) error // writes the metadata file and sends item_updated
	WatcherUpdate   func(libraryID, path, kind string)             // applies one watcher notification (add, unlink, rename)
}

// New returns a Handler. getenv supplies ALLOW_IFRAME (os.Getenv in production).
func New(d *db.DB, store *settings.Store, cfg config.Config, getenv func(string) string, deps Deps) *Handler {
	return &Handler{db: d, settings: store, cfg: cfg, getenv: getenv, deps: deps}
}

// Routes registers the routes on mux. Only validate-cron, pathexists and upload
// are open to non-admins (the last two need the upload permission).
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(pattern string, f httpx.Handler) { mux.Handle(pattern, auth.Admin(f)) }
	admin("PATCH /api/settings", h.patchSettings)
	admin("PATCH /api/sorting-prefixes", h.sortingPrefixes)
	admin("GET /api/auth-settings", h.authSettings)
	admin("PATCH /api/auth-settings", h.patchAuthSettings)
	admin("GET /api/logger-data", h.loggerData)
	admin("POST /api/cache/purge", h.purgeCache(h.cfg.Dirs().Cache))
	admin("POST /api/cache/items/purge", h.purgeCache(h.cfg.Dirs().ItemCache))
	admin("GET /api/stats/server", h.serverStats)
	admin("GET /api/stats/year/{year}", h.yearStats)
	admin("POST /api/watcher/update", h.updateWatchedPath)
	for _, kind := range []string{"tags", "genres"} {
		list := func(w http.ResponseWriter, r *http.Request) error { return h.listTaxonomy(w, r, kind) }
		rename := func(w http.ResponseWriter, r *http.Request) error { return h.renameTaxonomy(w, r, kind) }
		remove := func(w http.ResponseWriter, r *http.Request) error { return h.deleteTaxonomy(w, r, kind) }
		admin("GET /api/"+kind, list)
		admin("POST /api/"+kind+"/rename", rename)
		admin("DELETE /api/"+kind+"/{name}", remove)
	}
	admin("GET /api/filesystem", h.filesystem)

	mux.Handle("POST /api/validate-cron", httpx.Handler(h.validateCron))
	mux.Handle("POST /api/filesystem/pathexists", httpx.Handler(h.pathExists))
	mux.Handle("POST /api/upload", httpx.Handler(h.upload))
}

func (h *Handler) validateCron(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		Expression string `json:"expression"`
	}](w, r)
	if err != nil {
		return err
	}
	if body.Expression == "" {
		return httpx.BadRequest("")
	}
	if cronspec.Validate(body.Expression) != nil {
		return httpx.BadRequest("Invalid cron expression")
	}
	return httpx.Status(w, http.StatusOK)
}

func (h *Handler) loggerData(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, r, struct {
		CurrentDailyLogs []logging.Entry `json:"currentDailyLogs"`
	}{logging.Default().Entries()})
}

// purgeCache empties root and recreates the cache folders (CacheManager.js:129,139).
func (h *Handler) purgeCache(root string) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		slog.Info("[CacheManager] Purging cache", "path", root)
		dirs := h.cfg.Dirs()
		err := os.RemoveAll(root)
		for _, dir := range []string{root, dirs.CoverCache, dirs.ImageCache, dirs.ItemCache} {
			err = errors.Join(err, os.MkdirAll(dir, 0o755))
		}
		if err != nil {
			return httpx.Internal("Failed to purge cache", err)
		}
		return httpx.Status(w, http.StatusOK)
	}
}

func (h *Handler) updateWatchedPath(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		LibraryID string `json:"libraryId"`
		Path      string `json:"path"`
		Type      string `json:"type"`
		OldPath   string `json:"oldPath"`
	}](w, r)
	if err != nil {
		return err
	}
	validType := body.Type == "add" || body.Type == "unlink" || (body.Type == "rename" && body.OldPath != "")
	if body.LibraryID == "" || body.Path == "" || !validType {
		return httpx.BadRequest("")
	}
	h.deps.WatcherUpdate(body.LibraryID, body.Path, body.Type)
	return httpx.Status(w, http.StatusOK)
}
