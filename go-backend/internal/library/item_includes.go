package library

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/httputil"
)

// ItemIncludes are the include= entities the library item queries attach to
// each item they return: libraryItemsBookFilters.getFilteredLibraryItems
// (server/utils/queries/libraryItemsBookFilters.js:407-408, 670-677) and the
// continue-series and discover queries (js:707, 886), then copied onto the
// old JSON by libraryFilters (server/utils/queries/libraryFilters.js:62-67,
// 99-107, 146-154, 176-181, 385-390) and LibraryItem.getByFilterAndSort
// (server/models/LibraryItem.js:308-326).
type ItemIncludes struct {
	RSSFeed bool
	Share   bool
}

// RequestItemIncludes reads include= from the request. mediaItemShare is only
// attached for admins (`!!user?.isAdminOrUp && include.includes('share')`).
func RequestItemIncludes(r *http.Request, user store.User) ItemIncludes {
	return ItemIncludes{RSSFeed: httputil.IncludeHas(r, "rssfeed"), Share: access.AdminUser(user) && httputil.IncludeHas(r, "share")}
}

// AttachItemIncludes sets rssFeed (the item's feed, Feed.toOldJSONMinified)
// and mediaItemShare (an open share of the book) on a rendered item's extras
// when present. Feeds belong to library items (Feed.js:416-421: entityType
// 'libraryItem', entityId the library item id); shares to the book's media id
// and only for books.
func AttachItemIncludes(ctx context.Context, db *sql.DB, inc ItemIncludes, head items.Head, mediaID string, extras *items.Extras) error {
	if inc.RSSFeed {
		feed, ok, err := feeds.LoadMinified(ctx, db, "libraryItem", head.ID)
		if err != nil {
			return err
		}
		if ok {
			extras.RSSFeed = feed
		}
	}
	if inc.Share && head.MediaType == "book" {
		share, err := OpenMediaItemShare(ctx, db, mediaID)
		if err != nil {
			return err
		}
		if share != nil {
			extras.MediaItemShare = share
		}
	}
	return nil
}

// OpenMediaItemShare is ShareManager.findByMediaItemId
// (server/managers/ShareManager.js:49-60): the open (unexpired) share of a
// media item without pash, userId and extraData; nil when there is none.
func OpenMediaItemShare(ctx context.Context, db *sql.DB, mediaItemID string) (*MediaItemShareView, error) {
	row, err := sqlc.New(db).GetMediaItemShareSummaryByMediaID(ctx, mediaItemID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var share MediaItemShareView
	share.ID, share.MediaItemID = row.ID, row.Mediaitemid.String
	created, updated := row.Createdat, row.Updatedat
	expiresMs, err := dbtime.OptionalMillis(row.Expiresat)
	if err != nil {
		return nil, err
	}
	if expiresMs != nil && *expiresMs <= time.Now().UnixMilli() {
		return nil, nil
	}
	createdMs, err := dbtime.OptionalMillis(created)
	if err != nil {
		return nil, err
	}
	updatedMs, err := dbtime.OptionalMillis(updated)
	if err != nil {
		return nil, err
	}
	share.MediaItemType, share.Slug = row.Mediaitemtype.String, row.Slug.String
	share.IsDownloadable = row.Isdownloadable.Valid && row.Isdownloadable.Int64 != 0
	share.ExpiresAt, share.CreatedAt, share.UpdatedAt = isoMillis(expiresMs), isoMillis(createdMs), isoMillis(updatedMs)
	return &share, nil
}

// isoMillis renders a Date the way JSON.stringify does.
func isoMillis(ms *int64) *string {
	if ms == nil {
		return nil
	}
	text := time.UnixMilli(*ms).UTC().Format("2006-01-02T15:04:05.000Z")
	return &text
}

// MediaItemShareView is libraryItem.mediaItemShare, the open share of a book
// as ShareManager.findByMediaItemId returns it (server/managers/ShareManager.js:49-60):
// MediaItemShare.toJSON() without pash, userId and extraData, so its dates
// serialize as ISO strings.
type MediaItemShareView struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  string  `json:"mediaItemType"`
	Slug           string  `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	IsDownloadable bool    `json:"isDownloadable"`
	CreatedAt      *string `json:"createdAt"`
	UpdatedAt      *string `json:"updatedAt"`
}
