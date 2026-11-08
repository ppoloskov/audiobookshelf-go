package sharing

import (
	"audiobookshelf-compat/internal/access"
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
func (s shareRow) wire() MediaItemShare {
	out := MediaItemShare{
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
func (h *Handler) createShare(w http.ResponseWriter, r *http.Request, u store.User) error {
	if !access.AdminUser(u) {
		return httputil.Status(403, "")
	}
	var req createShareRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	slug, okSlug := jsonString(req.Slug)
	mediaType, okType := jsonString(req.MediaItemType)
	mediaID, okID := jsonString(req.MediaItemID)
	if !okSlug || strings.TrimSpace(slug) == "" || !okType || !okID {
		return httputil.Status(400, "Missing or invalid required fields")
	}
	var expiresMillis float64
	if err := json.Unmarshal(req.ExpiresAt, &expiresMillis); err != nil || string(req.ExpiresAt) == "null" || expiresMillis < 0 {
		return httputil.Status(400, "Invalid expiration date")
	}
	if mediaType != "book" && mediaType != "podcastEpisode" {
		return httputil.Status(400, "Invalid media item type")
	}
	ctx := r.Context()
	q := sqlc.New(h.db)
	existingMedia, err := q.GetShareMediaIDBySlugOrMedia(ctx, sqlc.GetShareMediaIDBySlugOrMediaParams{Slug: slug, MediaID: mediaID})
	if err == nil {
		if existingMedia.String == mediaID {
			return httputil.Status(409, "Item is already shared")
		}
		return httputil.Status(409, "Slug is already in use")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return httputil.Internal("Internal server error", err)
	}
	// The media table is chosen by type; books and podcastEpisodes are not
	// sharing tables, so this existence check stays a plain query.
	table := "books"
	if mediaType == "podcastEpisode" {
		table = "podcastEpisodes"
	}
	var exists int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id=?`, mediaID).Scan(&exists); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	if exists == 0 {
		return httputil.Status(404, "Media item not found")
	}
	id, err := ids.New()
	if err != nil {
		return httputil.Internal("Internal server error", err)
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
		return httputil.Internal("Internal server error", err)
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
	return nil
}

// deleteShare is ShareController.deleteMediaItemShare and
// ShareManager.removeMediaItemShare.
func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request, u store.User) error {
	if !access.AdminUser(u) {
		return httputil.Status(403, "")
	}
	id := r.PathValue("id")
	share, err := shareResult(sqlc.New(h.db).GetMediaItemShare(r.Context(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "Media item share not found")
	}
	if err != nil {
		return httputil.Internal("Internal server error", err)
	}
	if err = sqlc.New(h.db).DeleteMediaItemShare(r.Context(), id); err != nil {
		return httputil.Internal("Internal server error", err)
	}
	h.dropShareSessions(id)
	if h.emit != nil {
		h.emit("@admins", "share_closed", share.wire())
	}
	return httputil.Empty(204)
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
	return shareResult(sqlc.New(h.db).GetMediaItemShareBySlug(ctx, slug))
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
		_ = sqlc.New(h.db).DeleteMediaItemShare(ctx, share.ID)
		h.dropShareSessions(share.ID)
		if h.emit != nil {
			h.emit("@admins", "share_closed", share.wire())
		}
		return shareRow{}, errShareGone
	}
	return share, nil
}
