package user

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

func (h *Handler) readableItem(ctx context.Context, me *auth.Principal, itemID string) error {
	_, exists, allowed, err := me.ReadItem(ctx, h.db, itemID)
	switch {
	case err != nil:
		return err
	case !exists:
		return httpx.NotFound()
	case !allowed:
		return httpx.Forbidden()
	}
	return nil
}

func (h *Handler) getBookmarks(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	bookmarks, itemID := me.Bookmarks.V, r.PathValue("id")
	if itemID != "" {
		if err := h.readableItem(r.Context(), me, itemID); err != nil {
			return err
		}
		bookmarks = slices.DeleteFunc(slices.Clone(bookmarks), func(b db.Bookmark) bool { return b.LibraryItemID != itemID })
	}
	return httpx.JSON(w, r, wrap("bookmarks", append([]db.Bookmark{}, bookmarks...)))
}

type bookmarkRequest struct {
	Time  *float64 `json:"time"`
	Title string   `json:"title"`
}

func (h *Handler) bookmarkBody(w http.ResponseWriter, r *http.Request, me *auth.Principal) (itemID string, req bookmarkRequest, err error) {
	itemID = r.PathValue("id")
	if err = h.readableItem(r.Context(), me, itemID); err != nil {
		return "", req, err
	}
	if req, err = httpx.Decode[bookmarkRequest](w, r); err != nil {
		return "", req, err
	}
	if req.Time == nil || req.Title == "" {
		return "", req, httpx.BadRequest("Invalid time or title")
	}
	return itemID, req, nil
}

func bookmarkIndex(bookmarks []db.Bookmark, itemID string, t float64) int {
	return slices.IndexFunc(bookmarks, func(b db.Bookmark) bool { return b.LibraryItemID == itemID && b.Time == t })
}

func (h *Handler) editBookmarks(ctx context.Context, me *auth.Principal, edit func(*[]db.Bookmark) error) error {
	user, _, err := h.editUser(ctx, me.ID, func(_ context.Context, u *db.User) error { return edit(&u.Bookmarks.V) }, "bookmarks")
	if err == nil {
		_, err = h.emitUserUpdate(ctx, user)
	}
	return err
}

func (h *Handler) createBookmark(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	itemID, req, err := h.bookmarkBody(w, r, me)
	if err != nil {
		return err
	}
	var saved db.Bookmark
	err = h.editBookmarks(r.Context(), me, func(bookmarks *[]db.Bookmark) error {
		if i := bookmarkIndex(*bookmarks, itemID, *req.Time); i >= 0 {
			(*bookmarks)[i].Title = req.Title
			saved = (*bookmarks)[i]
			return nil
		}
		saved = db.Bookmark{LibraryItemID: itemID, Time: *req.Time, Title: req.Title, CreatedAt: time.Now().UnixMilli()}
		*bookmarks = append(*bookmarks, saved)
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, saved)
}

// updateBookmark is PATCH /api/me/item/{id}/bookmark: it retitles the bookmark at that time.
func (h *Handler) updateBookmark(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	itemID, req, err := h.bookmarkBody(w, r, me)
	if err != nil {
		return err
	}
	var saved db.Bookmark
	err = h.editBookmarks(r.Context(), me, func(bookmarks *[]db.Bookmark) error {
		i := bookmarkIndex(*bookmarks, itemID, *req.Time)
		if i < 0 {
			return httpx.NotFound()
		}
		(*bookmarks)[i].Title = req.Title
		saved = (*bookmarks)[i]
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, saved)
}

func (h *Handler) deleteBookmark(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	itemID := r.PathValue("id")
	if err := h.readableItem(r.Context(), me, itemID); err != nil {
		return err
	}
	at, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil || math.IsNaN(at) || math.IsInf(at, 0) {
		return httpx.BadRequest("Invalid time")
	}
	err = h.editBookmarks(r.Context(), me, func(bookmarks *[]db.Bookmark) error {
		i := bookmarkIndex(*bookmarks, itemID, at)
		if i < 0 {
			return httpx.NotFound()
		}
		*bookmarks = slices.Delete(*bookmarks, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	return ok(w)
}
