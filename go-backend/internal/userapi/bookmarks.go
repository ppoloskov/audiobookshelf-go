package userapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) getBookmarks(w http.ResponseWriter, r *http.Request, user store.User) error {
	libraryItemID := r.PathValue("id")
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	if libraryItemID == "" {
		return httputil.JSON(w, r, map[string]any{"bookmarks": bookmarks})
	}
	if err := h.requireItemAccess(r, user, libraryItemID); err != nil {
		return err
	}
	filtered := make([]any, 0)
	for _, entry := range bookmarks {
		if bm, ok := entry.(map[string]any); ok && bm["libraryItemId"] == libraryItemID {
			filtered = append(filtered, bm)
		}
	}
	return httputil.JSON(w, r, map[string]any{"bookmarks": filtered})
}

func (h *Handler) saveBookmark(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID := r.PathValue("id")
	if err := h.requireItemAccess(r, user, itemID); err != nil {
		return err
	}
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	timeValue, ok := jsonvalue.Number(body["time"])
	if !ok {
		return httputil.Status(400, "Invalid time")
	}
	title, ok := body["title"].(string)
	if !ok || title == "" {
		return httputil.Status(400, "Invalid title")
	}
	index := bookmarkIndex(bookmarks, itemID, timeValue)
	if r.Method == http.MethodPatch && index < 0 {
		return httputil.Status(404, "")
	}
	var entry map[string]any
	if index < 0 {
		entry = map[string]any{"libraryItemId": itemID, "time": timeValue, "title": title, "createdAt": time.Now().UnixMilli()}
		bookmarks = append(bookmarks, entry)
	} else {
		entry, _ = bookmarks[index].(map[string]any)
		entry["title"] = title
	}
	if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
		return httputil.Internal("database write failed", err)
	}
	user.Bookmarks, _ = json.Marshal(bookmarks)
	h.emitUserUpdate(r.Context(), user)
	return httputil.JSON(w, r, entry)
}

func (h *Handler) deleteBookmark(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID := r.PathValue("id")
	if err := h.requireItemAccess(r, user, itemID); err != nil {
		return err
	}
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	timeValue, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil || math.IsNaN(timeValue) || math.IsInf(timeValue, 0) {
		return httputil.Status(400, "Invalid time")
	}
	index := bookmarkIndex(bookmarks, itemID, timeValue)
	if index < 0 {
		return httputil.Status(404, "")
	}
	bookmarks = append(bookmarks[:index], bookmarks[index+1:]...)
	if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
		return httputil.Internal("database write failed", err)
	}
	user.Bookmarks, _ = json.Marshal(bookmarks)
	h.emitUserUpdate(r.Context(), user)
	return httputil.OK(w, r)
}

func (h *Handler) requireItemAccess(r *http.Request, user store.User, itemID string) error {
	exists, allowed, err := access.ItemAccess(r.Context(), h.db, user, itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !exists {
		return httputil.Status(404, "")
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	return nil
}

func (h *Handler) saveBookmarks(ctx context.Context, userID string, bookmarks []any) error {
	encoded, err := json.Marshal(bookmarks)
	if err != nil {
		return err
	}
	err = sqlc.New(h.db).UpdateUserBookmarks(ctx, sqlc.UpdateUserBookmarksParams{Bookmarks: store.Text(string(encoded)), Updatedat: dbtime.Now(), ID: userID})
	return err
}

func bookmarkIndex(bookmarks []any, item string, t float64) int {
	for i, v := range bookmarks {
		if bm, ok := v.(map[string]any); ok && bm["libraryItemId"] == item {
			if n, ok := jsonvalue.Number(bm["time"]); ok && n == t {
				return i
			}
		}
	}
	return -1
}
