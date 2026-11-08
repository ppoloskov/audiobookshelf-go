package itemwrite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

// Env is what the follow-up of a relation edit touches once its transaction has
// committed.
type Env struct {
	DB      *sql.DB
	Filters *filterdata.Cache
	Emit    events.Emitter
}

// Announce patches the filter data with the newly linked series and authors
// and sends the events the edits collected (LibraryItemController.js:240-265).
// It needs no database, so a caller without an Env can use it.
func (c *Changes) Announce(libraryID string, filters *filterdata.Cache, emit events.Emitter) {
	for _, se := range c.SeriesAdded {
		filters.AddSeries(libraryID, se.Name, se.ID)
	}
	for _, ev := range c.Events {
		emit.ToAll(ev.Name, ev.Payload)
	}
	for _, au := range c.AuthorsAdded {
		filters.AddAuthor(libraryID, au.Name, au.ID)
	}
}

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

// sweep deletes the parents in ids that no junction row references (and that
// pass guard) in ONE statement, so a book linked by a concurrent request
// between "is it orphaned?" and the delete keeps its parent: the junction's
// ON DELETE CASCADE would otherwise drop that new link. Only rows this
// statement removed are forgotten and announced, so overlapping sweeps never
// announce a removal twice. It runs after the edit committed and ignores the
// request's cancellation: a client that disconnects must not leave orphans.
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
	ctx = context.WithoutCancel(ctx)
	marks := store.Placeholders(len(args))
	rows, err := e.DB.QueryContext(ctx, fmt.Sprintf(`DELETE FROM %[1]s WHERE id IN (%[2]s) AND %[3]sNOT EXISTS(SELECT 1 FROM %[4]s j WHERE j.%[5]s=%[1]s.id) RETURNING rowid,id,libraryId`, parent, marks, guard, junction, col), args...)
	if err != nil {
		logging.Errorf("[itemwrite] remove orphan %s: %v", parent, err)
		return nil
	}
	type orphan struct {
		rowid         int64
		id, libraryID string
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		var libraryID sql.NullString
		if err := rows.Scan(&o.rowid, &o.id, &libraryID); err != nil {
			logging.Errorf("[itemwrite] remove orphan %s: %v", parent, err)
			continue
		}
		o.libraryID = libraryID.String
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		logging.Errorf("[itemwrite] remove orphan %s: %v", parent, err)
	}
	rows.Close()
	slices.SortFunc(orphans, func(a, b orphan) int { return cmp.Compare(a.rowid, b.rowid) })
	removed := make([]string, 0, len(orphans))
	for _, o := range orphans {
		forget(o.libraryID, o.id)
		removed = append(removed, o.id)
		e.Emit.ToAll(event, map[string]any{"id": o.id, "libraryId": o.libraryID})
	}
	return removed
}

// closeFeeds closes every feed of an entity: the row goes and rss_feed_closed
// carries the feed (RssFeedManager.handleCloseFeed, :355-361).
func (e Env) closeFeeds(ctx context.Context, entityID string) {
	ctx = context.WithoutCancel(ctx)
	q := sqlc.New(e.DB)
	for {
		feed, err := q.GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityID: entityID})
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err == nil {
			err = q.DeleteFeed(ctx, feed.ID)
		}
		if err != nil {
			logging.Errorf("[itemwrite] close feeds of %s: %v", entityID, err)
			return
		}
		e.Emit.ToAll("rss_feed_closed", feeds.FromSummary(feed))
	}
}
