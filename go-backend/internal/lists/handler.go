package lists

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strconv"
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

func notImplemented(w http.ResponseWriter) {
	httputil.ErrorJSON(w, http.StatusNotImplemented, "This route is not implemented in the experimental Go backend")
}

var stripMarkup = regexp.MustCompile(`<[^>]*>`)

func cleanName(value string) string {
	return html.UnescapeString(stripMarkup.ReplaceAllString(value, ""))
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func sendEntityNotFound(w http.ResponseWriter, message string) {
	sendEntityNotFoundStatus(w, http.StatusNotFound, message)
}

func sendEntityNotFoundStatus(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.Header().Set("ETag", httputil.ExpressETag([]byte(message)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

func respondPlaylistError(w http.ResponseWriter, code int) {
	if code == http.StatusNotFound {
		sendEntityNotFound(w, "Playlist not found")
		return
	}
	httputil.SendStatus(w, code)
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

func (h *Handler) collectionRoot(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	ids, err := h.queries().ListCollectionIDs(r.Context())
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	out := make([]any, 0)
	for _, id := range ids {
		dto, libraryID, err := h.collection(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		if h.canAccessLibrary(user, libraryID) {
			out = append(out, dto)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"collections": out})
}

func (h *Handler) collectionID(w http.ResponseWriter, r *http.Request) {
	user, id := requestUser(r), r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		dto, libraryID, err := h.collection(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !h.canAccessLibrary(user, libraryID) {
			sendEntityNotFound(w, "Collection not found")
			return
		}
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		if strings.Contains(","+r.URL.Query().Get("include")+",", ",rssfeed,") {
			feed, err := h.collectionRSSFeed(r, id)
			if err != nil {
				writeInternalError(w, r, "database read failed", err)
				return
			}
			if feed != nil {
				dto["rssFeed"] = feed
			}
		}
		httputil.WriteJSON(w, http.StatusOK, dto)
	case http.MethodPatch:
		h.updateCollection(w, r, user, id)
	case http.MethodDelete:
		libraryID, err := h.collectionLibraryID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			sendEntityNotFound(w, "Collection not found")
			return
		} else if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		if !h.canAccessLibrary(user, libraryID) {
			sendEntityNotFound(w, "Collection not found")
			return
		}
		if !boolValue(store.DecodeJSONObject(user.Permissions)["delete"]) {
			httputil.SendStatus(w, http.StatusForbidden)
			return
		}
		dto, _, _ := h.collection(r.Context(), id, user)
		if err := h.queries().DeleteCollection(r.Context(), id); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		if h.emit != nil {
			h.emit("*", "collection_removed", dto)
		}
		httputil.SendStatus(w, http.StatusOK)
	}
}

func (h *Handler) playlistID(w http.ResponseWriter, r *http.Request) {
	user, id := requestUser(r), r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		item, _, code, err := h.ownedPlaylist(r, id, user)
		if code != 0 {
			respondPlaylistError(w, code)
			return
		}
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, item)
	case http.MethodPatch:
		h.updatePlaylist(w, r, id, user)
	case http.MethodDelete:
		h.deletePlaylist(w, r, id, user)
	}
}

func writeInternalError(w http.ResponseWriter, r *http.Request, message string, err error) {
	logging.RequestError(r, message, err)
	http.Error(w, message, http.StatusInternalServerError)
}
