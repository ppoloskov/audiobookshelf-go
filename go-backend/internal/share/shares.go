package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// shareView is the share object of POST /api/share/mediaitem, of the
// share_open and share_closed events and of GET /public/share/{slug}
// (MediaItemShare.toJSONForClient): no pash, userId or extraData. Dates are
// ISO strings, as Sequelize Dates reach JSON.stringify; ExpiresAt is null for
// a permanent share.
type shareView struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  string  `json:"mediaItemType"`
	Slug           string  `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
	IsDownloadable bool    `json:"isDownloadable"`
}

func viewOf(s db.MediaItemShare) shareView {
	v := shareView{
		ID: s.ID, MediaItemID: db.Val(s.MediaItemID), MediaItemType: db.Val(s.MediaItemType), Slug: db.Val(s.Slug),
		CreatedAt: isoTime(s.CreatedAt.Time), UpdatedAt: isoTime(s.UpdatedAt.Time), IsDownloadable: bool(s.IsDownloadable),
	}
	if !s.ExpiresAt.IsZero() {
		v.ExpiresAt = db.Ptr(isoTime(s.ExpiresAt.Time))
	}
	return v
}

// createShareRequest is the body of POST /api/share/mediaitem. ExpiresAt is
// epoch milliseconds, 0 for a permanent share.
type createShareRequest struct {
	Slug           string `json:"slug"`
	ExpiresAt      *int64 `json:"expiresAt"`
	MediaItemType  string `json:"mediaItemType"`
	MediaItemID    string `json:"mediaItemId"`
	IsDownloadable bool   `json:"isDownloadable"`
}

// createShare is ShareController.createMediaItemShare. Admin only. A share
// that is already expired is answered 201 and announced but never stored.
func (h *Handler) createShare(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createShareRequest](w, r)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(req.Slug) == "" || req.MediaItemType == "" || req.MediaItemID == "":
		return httpx.BadRequest("Missing or invalid required fields")
	case req.ExpiresAt == nil || *req.ExpiresAt < 0:
		return httpx.BadRequest("Invalid expiration date")
	case req.MediaItemType != "book" && req.MediaItemType != "podcastEpisode":
		return httpx.BadRequest("Invalid media item type")
	}
	mediaTable := "books"
	if req.MediaItemType == "podcastEpisode" {
		mediaTable = "podcastEpisodes"
	}
	now := db.Now()
	share := db.MediaItemShare{
		MediaItemID: &req.MediaItemID, MediaItemType: &req.MediaItemType, Slug: &req.Slug,
		IsDownloadable: db.Bool(req.IsDownloadable), CreatedAt: now, UpdatedAt: now, UserID: &auth.User(r.Context()).ID,
	}
	if *req.ExpiresAt > 0 {
		share.ExpiresAt = db.FromMillis(*req.ExpiresAt)
	}
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		var taken []db.MediaItemShare
		if err := h.db.Select(ctx, &taken, `SELECT * FROM mediaItemShares WHERE slug = ? OR mediaItemId = ?`, req.Slug, req.MediaItemID); err != nil {
			return fmt.Errorf("find conflicting shares: %w", err)
		}
		for _, other := range taken {
			if db.Val(other.MediaItemID) == req.MediaItemID {
				return httpx.Error(http.StatusConflict, "Item is already shared")
			}
		}
		if len(taken) > 0 {
			return httpx.Error(http.StatusConflict, "Slug is already in use")
		}
		var found int
		if err := h.db.Get(ctx, &found, `SELECT COUNT(*) FROM `+mediaTable+` WHERE id = ?`, req.MediaItemID); err != nil {
			return fmt.Errorf("find media item: %w", err)
		}
		if found == 0 {
			return httpx.Error(http.StatusNotFound, "Media item not found")
		}
		var err error
		if share.ID, err = ids.New(); err != nil {
			return fmt.Errorf("new share id: %w", err)
		}
		if !share.ExpiresAt.IsZero() && !share.ExpiresAt.After(time.Now()) {
			return nil // already expired: answered and announced, never stored
		}
		return h.db.Insert(ctx, "mediaItemShares", &share)
	})
	if err != nil {
		return err
	}
	h.emit.ToAdmins("share_open", viewOf(share))
	return httpx.JSONStatus(w, r, http.StatusCreated, viewOf(share))
}

// deleteShare is ShareController.deleteMediaItemShare. Admin only. It closes
// the share's playback sessions.
func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var share db.MediaItemShare
	err := h.db.Get(r.Context(), &share, `SELECT * FROM mediaItemShares WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusNotFound, "Media item share not found")
	}
	if err != nil {
		return fmt.Errorf("load share: %w", err)
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM mediaItemShares WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete share: %w", err)
	}
	h.sessions.dropShare(id)
	h.emit.ToAdmins("share_closed", viewOf(share))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// loadShare returns the share of slug. A share past its expiry is removed on
// this first access, its sessions are closed and share_closed is emitted
// (ShareManager removes expired shares; here the removal is lazy). Missing and
// expired shares are a 404.
func (h *Handler) loadShare(ctx context.Context, slug string) (db.MediaItemShare, error) {
	var share db.MediaItemShare
	err := h.db.Get(ctx, &share, `SELECT * FROM mediaItemShares WHERE slug = ?`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return share, httpx.Error(http.StatusNotFound, "Media item share not found")
	}
	if err != nil {
		return share, fmt.Errorf("load share: %w", err)
	}
	if share.ExpiresAt.IsZero() || !share.ExpiresAt.Before(time.Now()) {
		return share, nil
	}
	result, err := h.db.Exec(ctx, `DELETE FROM mediaItemShares WHERE id = ?`, share.ID)
	if err != nil {
		return share, fmt.Errorf("remove expired share: %w", err)
	}
	h.sessions.dropShare(share.ID)
	// Only the request that removed the row announces it.
	if n, _ := result.RowsAffected(); n > 0 { // a driver without a count is treated as "removed elsewhere"
		h.emit.ToAdmins("share_closed", viewOf(share))
	}
	return share, httpx.Error(http.StatusNotFound, "Media item share not found")
}
