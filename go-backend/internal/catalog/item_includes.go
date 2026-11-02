package catalog

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	itemapi "audiobookshelf-compat/internal/items"
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

func isAdminOrUp(user store.User) bool { return user.Type == "root" || user.Type == "admin" }

// requestItemIncludes reads include= from the request. mediaItemShare is only
// attached for admins (`!!user?.isAdminOrUp && include.includes('share')`).
func requestItemIncludes(r *http.Request, user store.User) itemIncludes {
	return itemIncludes{rssFeed: includeHas(r, "rssfeed"), share: isAdminOrUp(user) && includeHas(r, "share")}
}

// attachItemIncludes sets rssFeed (the item's feed, Feed.toOldJSONMinified)
// and mediaItemShare (an open share of the book) through set when present.
// Feeds belong to library items (Feed.js:416-421: entityType 'libraryItem',
// entityId the library item id); shares to the book's media id and only for
// books.
func (h *Handler) attachItemIncludes(ctx context.Context, inc itemIncludes, itemID string, media any, set func(key string, value any)) error {
	if inc.rssFeed {
		feed, ok, err := h.feedMinified(ctx, "libraryItem", itemID)
		if err != nil {
			return err
		}
		if ok {
			set("rssFeed", feed)
		}
	}
	if inc.share {
		book, isBook := media.(wire.BookMinified)
		if !isBook {
			return nil
		}
		share, err := h.openMediaItemShare(ctx, book.ID)
		if err != nil {
			return err
		}
		if share != nil {
			set("mediaItemShare", share)
		}
	}
	return nil
}

// openMediaItemShare is ShareManager.findByMediaItemId
// (server/managers/ShareManager.js:49-60): the open (unexpired) share of a
// media item without pash, userId and extraData; nil when there is none.
func (h *Handler) openMediaItemShare(ctx context.Context, mediaItemID string) (*wire.MediaItemShareForClient, error) {
	var share wire.MediaItemShareForClient
	var itemType, slug sql.NullString
	var expires, created, updated any
	var downloadable sql.NullBool
	err := h.db.QueryRowContext(ctx, `SELECT id,mediaItemId,mediaItemType,slug,expiresAt,isDownloadable,createdAt,updatedAt FROM mediaItemShares WHERE mediaItemId=? LIMIT 1`, mediaItemID).
		Scan(&share.ID, &share.MediaItemID, &itemType, &slug, &expires, &downloadable, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expiresMs, err := dateMillis(expires)
	if err != nil {
		return nil, err
	}
	if expiresMs != nil && *expiresMs <= time.Now().UnixMilli() {
		return nil, nil
	}
	createdMs, err := dateMillis(created)
	if err != nil {
		return nil, err
	}
	updatedMs, err := dateMillis(updated)
	if err != nil {
		return nil, err
	}
	share.MediaItemType, share.Slug, share.IsDownloadable = itemType.String, slug.String, downloadable.Valid && downloadable.Bool
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

// itemMediaAccess exposes the media id of a minified or expanded item.
func itemMediaAccess(media any) string {
	access, _ := itemapi.AccessMedia(media)
	return access.ID
}
