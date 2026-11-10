package feeds

import (
	"context"
	"database/sql"
	"errors"

	"audiobookshelf-compat/internal/db/sqlc"
)

// CloseEntity removes every RSS feed attached to entityID inside tx and
// returns the snapshots callers emit after committing the surrounding change.
func CloseEntity(ctx context.Context, tx *sql.Tx, entityID string) ([]Minified, error) {
	q := sqlc.New(tx)
	closed := []Minified{}
	for {
		feed, err := q.GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityID: entityID})
		if errors.Is(err, sql.ErrNoRows) {
			return closed, nil
		}
		if err != nil {
			return nil, err
		}
		if err := q.DeleteFeed(ctx, feed.ID); err != nil {
			return nil, err
		}
		closed = append(closed, FromSummary(feed))
	}
}
