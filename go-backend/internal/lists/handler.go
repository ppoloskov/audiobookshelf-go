package lists

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

// Handler owns authenticated HTTP routes and persistence for playlists and
// collections. Runtime supplies the existing authorization decisions.
type Handler struct {
	db               *sql.DB
	items            items.Reader
	emit             events.Emitter
	canView          func(context.Context, store.User, string) (bool, error)
	canAccessLibrary func(store.User, string) bool
}

func New(db *sql.DB, itemReader items.Reader, emit events.Emitter, canView func(context.Context, store.User, string) (bool, error), canAccessLibrary func(store.User, string) bool) *Handler {
	return &Handler{db: db, items: itemReader, emit: emit, canView: canView, canAccessLibrary: canAccessLibrary}
}

func (h *Handler) queries() *sqlc.Queries { return sqlc.New(h.db) }

func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

var stripMarkup = regexp.MustCompile(`<[^>]*>`)

func cleanName(value string) string {
	return html.UnescapeString(stripMarkup.ReplaceAllString(value, ""))
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func playlistError(code int) error {
	if code == http.StatusNotFound {
		return httputil.Status(code, "Playlist not found")
	}
	return httputil.Status(code, "")
}

func (h *Handler) collection(ctx context.Context, id string, user store.User) (map[string]any, string, error) {
	return Collection(ctx, h.db, h.items, id, func(ctx context.Context, itemID string) (bool, error) {
		return h.canView(ctx, user, itemID)
	})
}

func (h *Handler) collectionLibraryID(ctx context.Context, id string) (string, error) {
	libraryID, err := h.queries().GetCollectionLibraryID(ctx, id)
	return libraryID.String, err
}

func (h *Handler) collectionRoot(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	ids, err := h.queries().ListCollectionIDs(r.Context())
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	out := make([]any, 0)
	for _, id := range ids {
		dto, libraryID, err := h.collection(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return writeInternalError(w, r, "database read failed", err)
		}
		if h.canAccessLibrary(user, libraryID) {
			out = append(out, dto)
		}
	}
	return httputil.JSON(w, r, map[string]any{"collections": out})
}

func (h *Handler) getCollection(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	dto, libraryID, err := h.collection(r.Context(), id, user)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !h.canAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	}
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if strings.Contains(","+r.URL.Query().Get("include")+",", ",rssfeed,") {
		feed, err := h.collectionRSSFeed(r, id)
		if err != nil {
			return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database read failed", err)
	}
	if !h.canAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	}
	if !boolValue(store.DecodeJSONObject(user.Permissions)["delete"]) {
		return httputil.Status(http.StatusForbidden, "")
	}
	dto, _, _ := h.collection(r.Context(), id, user)
	if err := h.queries().DeleteCollection(r.Context(), id); err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	if h.emit != nil {
		h.emit("*", "collection_removed", dto)
	}
	return httputil.OK(w, r)
}

func (h *Handler) getPlaylist(w http.ResponseWriter, r *http.Request) error {
	item, _, code, err := h.ownedPlaylist(r, r.PathValue("id"), requestUser(r))
	if code != 0 {
		return playlistError(code)
	}
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, item)
}

// writeInternalError answers a failed database operation with net/http's
// plain-text 500 (the framing clients already see) and logs the cause. It
// returns nil because the response is written.
func writeInternalError(w http.ResponseWriter, r *http.Request, message string, err error) error {
	logging.RequestError(r, message, err)
	return plainError(w, message, http.StatusInternalServerError)
}

// plainError answers with net/http's text/plain framing, which the validation
// messages of these routes have always used. It returns nil: the response is written.
func plainError(w http.ResponseWriter, message string, status int) error {
	http.Error(w, message, status)
	return nil
}
