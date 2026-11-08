package lists

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

// Handler owns authenticated HTTP routes and persistence for playlists and
// collections.
type Handler struct {
	db   *sql.DB
	emit events.Emitter
}

func New(db *sql.DB, emit events.Emitter) *Handler {
	return &Handler{db: db, emit: emit}
}

func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

var stripMarkup = regexp.MustCompile(`<[^>]*>`)

func cleanName(value string) string {
	return html.UnescapeString(stripMarkup.ReplaceAllString(value, ""))
}

func playlistError(code int) error {
	if code == http.StatusNotFound {
		return httputil.Status(code, "Playlist not found")
	}
	return httputil.Status(code, "")
}

func (h *Handler) collection(ctx context.Context, id string, user store.User) (map[string]any, string, error) {
	return Collection(ctx, h.db, id, func(ctx context.Context, itemID string) (bool, error) {
		return access.CanReadItem(ctx, h.db, user, itemID)
	})
}

func (h *Handler) collectionLibraryID(ctx context.Context, id string) (string, error) {
	libraryID, err := sqlc.New(h.db).GetCollectionLibraryID(ctx, id)
	return libraryID.String, err
}

func (h *Handler) collectionRoot(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	ids, err := sqlc.New(h.db).ListCollectionIDs(r.Context())
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	out := make([]any, 0)
	for _, id := range ids {
		dto, libraryID, err := h.collection(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if access.For(r.Context(), user).Library(libraryID) {
			out = append(out, dto)
		}
	}
	return httputil.JSON(w, r, map[string]any{"collections": out})
}

func (h *Handler) getCollection(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	dto, libraryID, err := h.collection(r.Context(), id, user)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !access.For(r.Context(), user).Library(libraryID) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if strings.Contains(","+r.URL.Query().Get("include")+",", ",rssfeed,") {
		feed, err := feeds.OfEntity(r.Context(), h.db, "collection", id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if feed != nil {
			dto["rssFeed"] = feed
		}
	}
	return httputil.JSON(w, r, dto)
}

func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	libraryID, err := h.collectionLibraryID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !access.For(r.Context(), user).Library(libraryID) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	}
	if !access.For(r.Context(), user).Delete() {
		return httputil.Status(http.StatusForbidden, "")
	}
	dto, _, _ := h.collection(r.Context(), id, user)
	if err := sqlc.New(h.db).DeleteCollection(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	h.emit.ToAll("collection_removed", dto)
	return httputil.OK(w, r)
}

func (h *Handler) getPlaylist(w http.ResponseWriter, r *http.Request) error {
	item, _, code, err := h.ownedPlaylist(r, r.PathValue("id"), requestUser(r))
	if code != 0 {
		return playlistError(code)
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, item)
}
