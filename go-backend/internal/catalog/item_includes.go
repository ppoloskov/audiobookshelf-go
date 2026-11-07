package catalog

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"database/sql"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// itemIncludes are the include= entities the library item queries attach to
// each item they return: libraryItemsBookFilters.getFilteredLibraryItems
// (server/utils/queries/libraryItemsBookFilters.js:407-408, 670-677) and the
// continue-series and discover queries (js:707, 886), then copied onto the
// old JSON by libraryFilters (server/utils/queries/libraryFilters.js:62-67,
// 99-107, 146-154, 176-181, 385-390) and LibraryItem.getByFilterAndSort
// (server/models/LibraryItem.js:308-326).
type itemIncludes struct {
	rssFeed bool
	share   bool
}

// requestItemIncludes reads include= from the request. mediaItemShare is only
// attached for admins (`!!user?.isAdminOrUp && include.includes('share')`).
func requestItemIncludes(r *http.Request, user store.User) itemIncludes {
	return itemIncludes{rssFeed: includeHas(r, "rssfeed"), share: access.AdminUser(user) && includeHas(r, "share")}
}

// attachItemIncludes sets rssFeed (the item's feed, Feed.toOldJSONMinified)
// and mediaItemShare (an open share of the book) on a rendered item's extras
// when present. Feeds belong to library items (Feed.js:416-421: entityType
// 'libraryItem', entityId the library item id); shares to the book's media id
// and only for books.
func (h *Handler) attachItemIncludes(ctx context.Context, inc itemIncludes, head libitem.Head, mediaID string, extras *libitem.Extras) error {
	if inc.rssFeed {
		feed, ok, err := h.feedMinified(ctx, "libraryItem", head.ID)
		if err != nil {
			return err
		}
		if ok {
			extras.RSSFeed = feed
		}
	}
	if inc.share && head.MediaType == "book" {
		share, err := h.openMediaItemShare(ctx, mediaID)
		if err != nil {
			return err
		}
		if share != nil {
			extras.MediaItemShare = share
		}
	}
	return nil
}

// openMediaItemShare is ShareManager.findByMediaItemId
// (server/managers/ShareManager.js:49-60): the open (unexpired) share of a
// media item without pash, userId and extraData; nil when there is none.
func (h *Handler) openMediaItemShare(ctx context.Context, mediaItemID string) (*wire.MediaItemShareForClient, error) {
	row, err := h.queries().GetMediaItemShareSummaryByMediaID(ctx, mediaItemID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var share wire.MediaItemShareForClient
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
