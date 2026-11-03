package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// shareRow is one mediaItemShares row as ShareManager keeps it in memory.
type shareRow struct {
	ID             string
	MediaItemID    string
	MediaItemType  string
	Slug           string
	ExpiresAt      time.Time // zero: permanent
	CreatedAt      time.Time
	UpdatedAt      time.Time
	IsDownloadable sql.NullBool
}

// shareFromRow maps a mediaItemShares row from the generated queries.
func shareFromRow(r sqlc.Mediaitemshare) shareRow {
	return shareRow{
		ID: r.ID, MediaItemID: r.Mediaitemid.String, MediaItemType: r.Mediaitemtype.String, Slug: r.Slug.String,
		ExpiresAt: asTime(r.Expiresat), CreatedAt: asTime(r.Createdat), UpdatedAt: asTime(r.Updatedat),
		IsDownloadable: sql.NullBool{Bool: r.Isdownloadable.Int64 != 0, Valid: r.Isdownloadable.Valid},
	}
}

// shareResult is shareFromRow for a generated query's (row, error) result.
func shareResult(row sqlc.Mediaitemshare, err error) (shareRow, error) {
	if err != nil {
		return shareRow{}, err
	}
	return shareFromRow(row), nil
}

// wire is MediaItemShare.toJSONForClient: Date attributes as ISO strings.
func (s shareRow) wire() wire.MediaItemShare {
	out := wire.MediaItemShare{
		ID: s.ID, MediaItemID: s.MediaItemID, MediaItemType: s.MediaItemType, Slug: s.Slug,
		CreatedAt: dbtime.JSDate(s.CreatedAt), UpdatedAt: dbtime.JSDate(s.UpdatedAt),
	}
	if !s.ExpiresAt.IsZero() {
		exp := dbtime.JSDate(s.ExpiresAt)
		out.ExpiresAt = &exp
	}
	if s.IsDownloadable.Valid {
		out.IsDownloadable = &s.IsDownloadable.Bool
	}
	return out
}

type createShareRequest struct {
	Slug           json.RawMessage `json:"slug"`
	ExpiresAt      json.RawMessage `json:"expiresAt"`
	MediaItemType  json.RawMessage `json:"mediaItemType"`
	MediaItemID    json.RawMessage `json:"mediaItemId"`
	IsDownloadable json.RawMessage `json:"isDownloadable"`
}

// createShare is ShareController.createMediaItemShare
// (server/controllers/ShareController.js:263). A share that is already
// expired when it is created is dropped from the database again by
// ShareManager.scheduleMediaItemShare, but the request still answers 201 and
// emits share_open.
func (h *Handler) createShare(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		httputil.SendStatus(w, 403, httputil.WithoutETag())
		return
	}
	var req createShareRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	slug, okSlug := jsonString(req.Slug)
	mediaType, okType := jsonString(req.MediaItemType)
	mediaID, okID := jsonString(req.MediaItemID)
	if !okSlug || strings.TrimSpace(slug) == "" || !okType || !okID {
		httputil.SendText(w, 400, "Missing or invalid required fields", httputil.WithoutETag())
		return
	}
	var expiresMillis float64
	if err := json.Unmarshal(req.ExpiresAt, &expiresMillis); err != nil || string(req.ExpiresAt) == "null" || expiresMillis < 0 {
		httputil.SendText(w, 400, "Invalid expiration date", httputil.WithoutETag())
		return
	}
	if mediaType != "book" && mediaType != "podcastEpisode" {
		httputil.SendText(w, 400, "Invalid media item type", httputil.WithoutETag())
		return
	}
	ctx := r.Context()
	q := h.queries()
	existingMedia, err := q.GetShareMediaIDBySlugOrMedia(ctx, sqlc.GetShareMediaIDBySlugOrMediaParams{Slug: slug, MediaID: mediaID})
	if err == nil {
		if existingMedia.String == mediaID {
			httputil.SendText(w, 409, "Item is already shared", httputil.WithoutETag())
		} else {
			httputil.SendText(w, 409, "Slug is already in use", httputil.WithoutETag())
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	// The media table is chosen by type; books and podcastEpisodes are not
	// sharing tables, so this existence check stays a plain query.
	table := "books"
	if mediaType == "podcastEpisode" {
		table = "podcastEpisodes"
	}
	var exists int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id=?`, mediaID).Scan(&exists); err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	if exists == 0 {
		httputil.SendText(w, 404, "Media item not found", httputil.WithoutETag())
		return
	}
	id, err := ids.New()
	if err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	share := shareRow{ID: id, MediaItemID: mediaID, MediaItemType: mediaType, Slug: slug, CreatedAt: now, UpdatedAt: now}
	var expires any
	if expiresMillis > 0 {
		share.ExpiresAt = time.UnixMilli(int64(expiresMillis)).UTC()
		expires = dbtime.Format(share.ExpiresAt)
	}
	var downloadable sql.NullInt64
	var dl bool
	if string(req.IsDownloadable) != "null" && json.Unmarshal(req.IsDownloadable, &dl) == nil {
		share.IsDownloadable = sql.NullBool{Bool: dl, Valid: true}
		downloadable = sql.NullInt64{Int64: boolInt64(dl), Valid: true}
	}
	if err = q.InsertMediaItemShare(ctx, sqlc.InsertMediaItemShareParams{
		ID: id, MediaID: mediaID, MediaType: mediaType, Slug: slug, Expiresat: expires, Isdownloadable: downloadable,
		Createdat: dbtime.Format(now), Updatedat: dbtime.Format(now), UserID: u.ID,
	}); err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	if !share.ExpiresAt.IsZero() && !share.ExpiresAt.After(time.Now()) {
		_ = q.DeleteMediaItemShare(ctx, id)
	}
	if h.emit != nil {
		h.emit("@admins", "share_open", share.wire())
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(share.wire())
}

// deleteShare is ShareController.deleteMediaItemShare and
// ShareManager.removeMediaItemShare.
func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		httputil.SendStatus(w, 403, httputil.WithoutETag())
		return
	}
	id := routeID(r.URL.Path, "")
	share, err := shareResult(h.queries().GetMediaItemShare(r.Context(), id))
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendText(w, 404, "Media item share not found", httputil.WithoutETag())
		return
	}
	if err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	if err = h.queries().DeleteMediaItemShare(r.Context(), id); err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	h.dropShareSessions(id)
	if h.emit != nil {
		h.emit("@admins", "share_closed", share.wire())
	}
	httputil.SendStatus(w, 204, httputil.WithoutETag())
}

func (h *Handler) dropShareSessions(shareID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, s := range h.sessions {
		if s.ShareID == shareID {
			logging.Infof("[ShareManager] Closing share playback session %q", s.Title) // ShareManager.js:40
			delete(h.sessions, key)
		}
	}
}

var errShareGone = errors.New("share expired")

// findShare is ShareManager.findBySlug.
func (h *Handler) findShare(ctx context.Context, slug string) (shareRow, error) {
	return shareResult(h.queries().GetMediaItemShareBySlug(ctx, slug))
}

// loadShare is findShare plus the expiry check of
// ShareController.getMediaItemShareBySlug: an expired share is removed (and
// share_closed emitted) and reported as errShareGone.
func (h *Handler) loadShare(ctx context.Context, slug string) (shareRow, error) {
	share, err := h.findShare(ctx, slug)
	if err != nil {
		return shareRow{}, err
	}
	if !share.ExpiresAt.IsZero() && share.ExpiresAt.Before(time.Now()) {
		_ = h.queries().DeleteMediaItemShare(ctx, share.ID)
		h.dropShareSessions(share.ID)
		if h.emit != nil {
			h.emit("@admins", "share_closed", share.wire())
		}
		return shareRow{}, errShareGone
	}
	return share, nil
}
