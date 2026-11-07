package itemwrite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/store"
)

// Env is what the follow-up of a relation edit touches once its transaction has
// committed.
type Env struct {
	DB      *sql.DB
	Filters *filterdata.Cache
	Emit    events.Emitter
}

func (e Env) emit(event string, payload any) {
	if e.Emit != nil {
		e.Emit("*", event, payload)
	}
}

// Announce patches the filter data with the newly linked series and authors
// and sends the events the edits collected (LibraryItemController.js:240-265).
func (e Env) Announce(libraryID string, c *Changes) {
	for _, se := range c.SeriesAdded {
		e.Filters.AddSeries(libraryID, se.Name, se.ID)
	}
	for _, ev := range c.Events {
		e.emit(ev.Name, ev.Payload)
	}
	for _, au := range c.AuthorsAdded {
		e.Filters.AddAuthor(libraryID, au.Name, au.ID)
	}
}

// Sweep removes the given authors and then series that no book uses any more.
func (e Env) Sweep(ctx context.Context, authorIDs, seriesIDs []string) {
	e.SweepAuthors(ctx, authorIDs)
	e.SweepSeries(ctx, seriesIDs)
}

// SweepAuthors is ApiRouter.checkRemoveAuthorsWithNoBooks (ApiRouter.js:480-520):
// an author with no book and no asin, description or image is removed and
// announced with author_removed.
func (e Env) SweepAuthors(ctx context.Context, ids []string) {
	guard := `COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND `
	e.sweep(ctx, "authors", "bookAuthors", "authorId", guard, ids, "author_removed", e.Filters.RemoveAuthor)
}

// SweepSeries is ApiRouter.checkRemoveEmptySeries (ApiRouter.js:417-478): an
// empty series is removed, announced with series_removed, and loses its open
// RSS feeds (RssFeedManager.closeFeedsForEntityIds).
func (e Env) SweepSeries(ctx context.Context, ids []string) {
	for _, id := range e.sweep(ctx, "series", "bookSeries", "seriesId", "", ids, "series_removed", e.Filters.RemoveSeries) {
		e.closeFeeds(ctx, id)
	}
}

func (e Env) sweep(ctx context.Context, parent, junction, col, guard string, ids []string, event string, forget func(libraryID, id string)) []string {
	seen := map[string]bool{}
	var args []any
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			args = append(args, id)
		}
	}
	if len(args) == 0 {
		return nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, err := e.DB.QueryContext(ctx, fmt.Sprintf(`SELECT id,libraryId FROM %[1]s WHERE id IN (%[2]s) AND %[3]sNOT EXISTS(SELECT 1 FROM %[4]s j WHERE j.%[5]s=%[1]s.id) ORDER BY rowid`, parent, marks, guard, junction, col), args...)
	if err != nil {
		return nil
	}
	type orphan struct{ id, libraryID string }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if rows.Scan(&o.id, &o.libraryID) == nil {
			orphans = append(orphans, o)
		}
	}
	rows.Close()
	var removed []string
	for _, o := range orphans {
		if _, err := e.DB.ExecContext(ctx, `DELETE FROM `+parent+` WHERE id=?`, o.id); err != nil {
			continue
		}
		forget(o.libraryID, o.id)
		removed = append(removed, o.id)
		e.emit(event, map[string]any{"id": o.id, "libraryId": o.libraryID})
	}
	return removed
}

// closeFeeds closes every feed of an entity: the row goes and rss_feed_closed
// carries the feed (RssFeedManager.handleCloseFeed, :355-361).
func (e Env) closeFeeds(ctx context.Context, entityID string) {
	q := sqlc.New(e.DB)
	for {
		feed, err := q.GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityID: entityID})
		if err != nil || q.DeleteFeed(ctx, feed.ID) != nil {
			return
		}
		e.emit("rss_feed_closed", map[string]any{"id": feed.ID, "entityType": store.NullString(feed.Entitytype), "entityId": store.NullString(feed.Entityid), "feedUrl": store.NullString(feed.Feedurl), "meta": map[string]any{"title": store.NullString(feed.Title), "description": store.NullString(feed.Description), "preventIndexing": feed.Preventindexing.Valid && feed.Preventindexing.Int64 != 0, "ownerName": store.NullString(feed.Ownername), "ownerEmail": store.NullString(feed.Owneremail)}})
	}
}
