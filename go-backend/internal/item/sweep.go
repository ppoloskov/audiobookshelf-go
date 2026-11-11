package item

import (
	"cmp"
	"context"
	"log/slog"
	"slices"

	"audiobookshelf-compat/internal/feeds"
)

// removed is the payload of author_removed and series_removed.
type removed struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
}

// orphan is a parent row a sweep deleted.
type orphan struct {
	RowID     int64  `db:"rowid"`
	ID        string `db:"id"`
	LibraryID string `db:"libraryId"`
}

// feedRow is the part of a feeds row the rss_feed_closed payload carries.
type feedRow struct {
	ID              string  `db:"id"`
	EntityType      *string `db:"entityType"`
	EntityID        *string `db:"entityId"`
	FeedURL         *string `db:"feedURL"`
	Title           *string `db:"title"`
	Description     *string `db:"description"`
	PreventIndexing *bool   `db:"preventIndexing"`
	OwnerName       *string `db:"ownerName"`
	OwnerEmail      *string `db:"ownerEmail"`
}

// sweepAuthors is ApiRouter.checkRemoveAuthorsWithNoBooks (ApiRouter.js:
// 480-520): an author with no book and no asin, description or image is
// removed and announced with author_removed.
func (h *Handler) sweepAuthors(ctx context.Context, ids []string) {
	guard := `COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND `
	h.sweep(ctx, "authors", "bookAuthors", "authorId", guard, ids, "author_removed", h.deps.Filters.RemoveAuthor)
}

// sweepSeries is ApiRouter.checkRemoveEmptySeries (ApiRouter.js:417-478): an
// empty series is removed, announced with series_removed and loses its open
// RSS feeds, each announced with rss_feed_closed.
func (h *Handler) sweepSeries(ctx context.Context, ids []string) {
	for _, id := range h.sweep(ctx, "series", "bookSeries", "seriesId", "", ids, "series_removed", h.deps.Filters.RemoveSeries) {
		closed, err := h.closeFeeds(context.WithoutCancel(ctx), id)
		if err != nil {
			slog.Error("close feeds of removed series", "series", id, "error", err)
		}
		for _, feed := range closed {
			h.emit.ToAll("rss_feed_closed", feed)
		}
	}
}

// sweep deletes the parents in ids that no junction row references (and that
// pass guard) in ONE statement, so a book linked by a concurrent request
// between "is it orphaned?" and the delete keeps its parent. Only rows this
// statement removed are forgotten and announced. It runs after the edit
// committed and ignores the request's cancellation: a client that disconnects
// must not leave orphans. It returns the removed ids in creation order.
func (h *Handler) sweep(ctx context.Context, parent, junction, col, guard string, ids []string, event string, forget func(libraryID, id string)) []string {
	ids = slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "" }))))
	if len(ids) == 0 {
		return nil
	}
	var orphans []orphan
	query := `DELETE FROM ` + parent + ` WHERE id IN (?) AND ` + guard + `NOT EXISTS(SELECT 1 FROM ` + junction + ` j WHERE j.` + col + `=` + parent + `.id) RETURNING rowid, id, COALESCE(libraryId,'') AS libraryId`
	if err := h.db.Select(context.WithoutCancel(ctx), &orphans, query, ids); err != nil {
		slog.Error("remove orphans", "table", parent, "error", err)
		return nil
	}
	slices.SortFunc(orphans, func(a, b orphan) int { return cmp.Compare(a.RowID, b.RowID) })
	out := make([]string, len(orphans))
	for i, o := range orphans {
		forget(o.LibraryID, o.ID)
		out[i] = o.ID
		h.emit.ToAll(event, removed{o.ID, o.LibraryID})
	}
	return out
}

// closeFeeds deletes every feed of an entity and returns them as the
// rss_feed_closed payload (RssFeedManager.handleCloseFeed, :355-361).
func (h *Handler) closeFeeds(ctx context.Context, entityID string) ([]feeds.Minified, error) {
	var rows []feedRow
	if err := h.db.Select(ctx, &rows, `SELECT id, entityType, entityId, feedURL, title, description, preventIndexing, ownerName, ownerEmail FROM feeds WHERE entityId = ? ORDER BY rowid`, entityID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM feeds WHERE entityId = ?`, entityID); err != nil {
		return nil, err
	}
	out := make([]feeds.Minified, len(rows))
	for i, f := range rows {
		out[i] = feeds.Minified{ID: f.ID, EntityType: f.EntityType, EntityID: f.EntityID, FeedURL: f.FeedURL,
			Meta: feeds.MinifiedMeta{Title: f.Title, Description: f.Description, PreventIndexing: f.PreventIndexing, OwnerName: f.OwnerName, OwnerEmail: f.OwnerEmail}}
	}
	return out, nil
}
