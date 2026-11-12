// Package author serves the author routes (AuthorController.js, v2.37.2):
// detail, rename and merge, delete, metadata match and the author image. It
// also exports the author JSON (View) and the library author list for the
// library routes.
package author

import (
	"context"
	"log/slog"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/metadata"
	"audiobookshelf-compat/internal/settings"
)

// Finder looks an author up at the metadata provider (Audnexus).
type Finder interface {
	FindAuthorByASIN(ctx context.Context, asin, region string) *metadata.AuthorResult
	FindAuthorByName(ctx context.Context, name, region string) *metadata.AuthorResult
}

// Deps are the collaborators the author routes use outside this package.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner.
	Filters *filterdata.Cache
	// Authors answers POST /api/authors/{id}/match; nil finds nothing.
	Authors Finder
	// WriteMetadata rewrites the item's metadata.json sidecar.
	WriteMetadata func(ctx context.Context, itemID string) error
	// ImageDir holds the author image files; ImageCacheDir the resized copies.
	ImageDir, ImageCacheDir string
}

// Handler serves the author routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	deps     Deps
	client   *http.Client // fetches author images from user-supplied urls
}

// New returns a Handler. Reads the SSRF environment once (httpx.NewSafeClient).
func New(d *db.DB, settings *settings.Store, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: d, settings: settings, emit: emit, deps: deps, client: httpx.NewSafeClient()}
}

// Routes registers the routes. Authorisation: every route but GET .../image
// (public, like covers) needs an authenticated user whose library access
// covers the author (404 otherwise); DELETE needs the delete permission, PATCH
// and POST the update permission, and POST .../image also the upload permission.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/authors/{id}", h.authed(h.get))
	mux.Handle("PATCH /api/authors/{id}", h.authed(h.update))
	mux.Handle("DELETE /api/authors/{id}", h.authed(h.delete))
	mux.Handle("POST /api/authors/{id}/match", h.authed(h.match))
	mux.Handle("GET /api/authors/{id}/image", httpx.Handler(h.image))
	mux.Handle("POST /api/authors/{id}/image", h.authed(h.uploadImage))
	mux.Handle("DELETE /api/authors/{id}/image", h.authed(h.deleteImage))
}

type userHandler func(w http.ResponseWriter, r *http.Request, user *auth.Principal) error

// authed runs next for the authenticated principal (401 without one).
func (h *Handler) authed(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user := auth.User(r.Context()); user != nil {
			return next(w, r, user)
		}
		return httpx.Error(http.StatusUnauthorized, "")
	}
}

// load is AuthorController.middleware (js:427-442): an unknown author or one in
// a library the user cannot access is a 404, then the method's permission.
func (h *Handler) load(r *http.Request, user *auth.Principal) (db.Author, error) {
	var a db.Author
	err := h.db.Get(r.Context(), &a, `SELECT * FROM authors WHERE id = ?`, r.PathValue("id"))
	if isNoRows(err) || (err == nil && !user.CanAccessLibrary(db.Val(a.LibraryID))) {
		return a, httpx.NotFound()
	}
	if err != nil {
		return a, httpx.Internal("database read failed", err)
	}
	switch {
	case r.Method == http.MethodDelete && !user.CanDelete():
		slog.Warn("user attempted to delete an author without permission", "user", db.Val(user.Username))
		return a, httpx.Forbidden()
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !user.CanUpdate():
		slog.Warn("user attempted to update an author without permission", "user", db.Val(user.Username))
		return a, httpx.Forbidden()
	}
	return a, nil
}
