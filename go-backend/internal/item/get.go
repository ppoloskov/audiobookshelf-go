package item

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// get is LibraryItemController.findOne: the full item, or with ?expanded=1 the
// expanded one plus the include= entities. Order: 404, then 403.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	it, err := Get(ctx, h.db, r.PathValue("id"), Options{Episodes: true})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if a := it.Access(); !user.CanAccessItem(a.LibraryID, a.Explicit, a.Tags) {
		return httpx.Forbidden()
	}
	if r.URL.Query().Get("expanded") != "1" {
		return httpx.JSON(w, r, it.Full())
	}
	out := it.Expanded(h.prefixes())
	if err := h.includes(r, user, &out); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, out)
}

// includes applies the include= entities of LibraryItemController.findOne
// (js:71-96). Upstream splits include on "," without trimming or lowercasing.
func (h *Handler) includes(r *http.Request, user *auth.Principal, out *Expanded) error {
	ctx := r.Context()
	list := strings.Split(r.URL.Query().Get("include"), ",")
	has := func(name string) bool { return slices.Contains(list, name) }
	if has("progress") {
		progress, err := h.progress(ctx, user.ID, out.ID, r.URL.Query().Get("episode"))
		if err != nil {
			return err
		}
		out.UserMediaProgress = orNull(progress, progress != nil)
	}
	if has("rssfeed") {
		// Like RssFeedManager.findFeedForEntityId (:59): the first feed of the entity, of any type.
		var feed feedRow
		err := h.db.Get(ctx, &feed, `SELECT `+feedColumns+` FROM feeds WHERE entityId = ? LIMIT 1`, out.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out.RSSFeed = orNull(feed.view(), err == nil)
	}
	if out.MediaType == "podcast" && has("downloads") && h.deps.Downloads != nil {
		queued, current := h.deps.Downloads.EpisodeDownloads(out.ID)
		out.EpisodeDownloadsQueued = queued
		if current != nil {
			out.EpisodesDownloading = []EpisodeDownload{*current}
		}
	}
	// mediaItemShare is ShareManager.findByMediaItemId: null without an open share.
	if has("share") && user.IsAdmin() && out.MediaType == "book" {
		share, err := h.openShare(ctx, out.MediaID())
		if err != nil {
			return err
		}
		out.MediaItemShare = orNull(share, share != nil)
	}
	return nil
}

// orNull is v when present, else an explicit JSON null.
func orNull(v any, present bool) any {
	if present {
		return v
	}
	return JSONNull
}

// MediaProgress is the legacy media progress object
// (server/models/MediaProgress.js:155 getOldMediaProgress). Key order follows
// upstream. EpisodeID is set only for podcast episodes; every other nullable
// scalar marshals as null.
type MediaProgress struct {
	ID                        string   `json:"id"`
	UserID                    string   `json:"userId"`
	LibraryItemID             *string  `json:"libraryItemId"`
	EpisodeID                 *string  `json:"episodeId"`
	MediaItemID               string   `json:"mediaItemId"`
	MediaItemType             string   `json:"mediaItemType"`
	Duration                  *float64 `json:"duration"`
	Progress                  float64  `json:"progress"`
	CurrentTime               *float64 `json:"currentTime"`
	IsFinished                bool     `json:"isFinished"`
	HideFromContinueListening bool     `json:"hideFromContinueListening"`
	EbookLocation             *string  `json:"ebookLocation"`
	EbookProgress             *float64 `json:"ebookProgress"`
	LastUpdate                *int64   `json:"lastUpdate"`
	StartedAt                 *int64   `json:"startedAt"`
	FinishedAt                *int64   `json:"finishedAt"`
}

// progress is User.getOldMediaProgress (User.js:716): the user's first
// progress (index order) of the library item, restricted to episodeID when
// given; nil when absent.
func (h *Handler) progress(ctx context.Context, userID, itemID, episodeID string) (*MediaProgress, error) {
	var row db.MediaProgress
	err := h.db.Get(ctx, &row, `SELECT * FROM mediaProgresses WHERE userId = ? AND json_extract(extraData,'$.libraryItemId') = ? AND (? = '' OR mediaItemId = ?) ORDER BY mediaItemId, isFinished, currentTime LIMIT 1`, userID, itemID, episodeID, episodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := MediaProgress{
		ID: row.ID, UserID: userID, LibraryItemID: &itemID, MediaItemID: db.Val(row.MediaItemID), MediaItemType: db.Val(row.MediaItemType),
		Duration: row.Duration, CurrentTime: db.Ptr(db.Val(row.CurrentTime)), IsFinished: bool(row.IsFinished),
		HideFromContinueListening: bool(row.HideFromContinueListening), EbookLocation: row.EbookLocation, EbookProgress: row.EbookProgress,
	}
	if extra := row.ExtraData.V; extra != nil && extra.Progress != nil {
		out.Progress = *extra.Progress
	}
	if out.MediaItemType == "podcastEpisode" {
		out.EpisodeID = &out.MediaItemID
	}
	out.LastUpdate, out.StartedAt = millis(row.UpdatedAt), millis(row.CreatedAt)
	out.FinishedAt = millisNonZero(row.FinishedAt)
	return &out, nil
}

// shareView is libraryItem.mediaItemShare: MediaItemShare.toJSON() without
// pash, userId and extraData, so its dates are ISO strings.
type shareView struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  string  `json:"mediaItemType"`
	Slug           string  `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	IsDownloadable bool    `json:"isDownloadable"`
	CreatedAt      *string `json:"createdAt"`
	UpdatedAt      *string `json:"updatedAt"`
}

// openShare is ShareManager.findByMediaItemId (ShareManager.js:49-60): the
// unexpired share of a media item, nil when there is none.
func (h *Handler) openShare(ctx context.Context, mediaID string) (*shareView, error) {
	var row db.MediaItemShare
	err := h.db.Get(ctx, &row, `SELECT * FROM mediaItemShares WHERE mediaItemId = ? LIMIT 1`, mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.ExpiresAt.IsZero() && row.ExpiresAt.Millis() <= time.Now().UnixMilli() {
		return nil, nil
	}
	iso := func(t db.Time) *string {
		if t.IsZero() {
			return nil
		}
		return db.Ptr(t.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return &shareView{ID: row.ID, MediaItemID: db.Val(row.MediaItemID), MediaItemType: db.Val(row.MediaItemType), Slug: db.Val(row.Slug),
		ExpiresAt: iso(row.ExpiresAt), IsDownloadable: bool(row.IsDownloadable), CreatedAt: iso(row.CreatedAt), UpdatedAt: iso(row.UpdatedAt)}, nil
}
