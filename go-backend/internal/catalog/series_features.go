package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func normalizedInclude(r *http.Request) string {
	values := strings.Split(r.URL.Query().Get("include"), ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, ",")
}
func includeHas(r *http.Request, value string) bool {
	for _, item := range strings.Split(normalizedInclude(r), ",") {
		if item == value {
			return true
		}
	}
	return false
}

func (h *Handler) feedMinified(ctx context.Context, entityType, entityID string) (feeds.Minified, bool, error) {
	// An empty entityType matches upstream RssFeedManager.findFeedForEntityId
	// (server/managers/RssFeedManager.js:59), which filters on entityId only.
	feed, err := sqlc.New(h.db).GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityType: entityType, EntityID: entityID})
	if errors.Is(err, sql.ErrNoRows) {
		return feeds.Minified{}, false, nil
	}
	if err != nil {
		return feeds.Minified{}, false, err
	}
	return feeds.FromSummary(feed), true, nil
}

func (h *Handler) seriesMatchesFilter(ctx context.Context, seriesID, raw string, user store.User) (bool, error) {
	if raw == "" {
		return true, nil
	}
	group, value, ok := strings.Cut(raw, ".")
	if !ok {
		return true, nil
	}
	permission, args := access.For(ctx, user).MediaClause("b")
	base := ` FROM books b JOIN bookSeries bs ON bs.bookId=b.id WHERE bs.seriesId=?`
	query := `SELECT COUNT(*)` + base
	queryArgs := []any{seriesID}
	switch group {
	case "genres", "tags", "narrators", "authors", "publishers", "languages":
		clause, clauseArgs, err := filterSQL(raw, false, user)
		if err != nil {
			return false, err
		}
		query += clause
		queryArgs = append(queryArgs, clauseArgs...)
	case "progress":
		value, _ = decodeFilterValue(value)
		switch value {
		case "not-finished":
			query += ` AND (NOT EXISTS(SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? ) OR EXISTS(SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? AND COALESCE(mp.isFinished,0)=0))`
			queryArgs = append(queryArgs, user.ID, user.ID)
		case "finished":
			query = `SELECT COUNT(*) FROM books b JOIN bookSeries bs ON bs.bookId=b.id LEFT JOIN mediaProgresses mp ON mp.mediaItemId=b.id AND mp.userId=? WHERE bs.seriesId=? AND (mp.isFinished IS NULL OR mp.isFinished=0)`
			queryArgs = []any{user.ID, seriesID}
		case "not-started":
			query = `SELECT COUNT(*) FROM books b JOIN bookSeries bs ON bs.bookId=b.id LEFT JOIN mediaProgresses mp ON mp.mediaItemId=b.id AND mp.userId=? WHERE bs.seriesId=? AND (mp.isFinished=1 OR mp.currentTime>0)`
			queryArgs = []any{user.ID, seriesID}
		case "in-progress":
			query = `SELECT COUNT(*) FROM books b JOIN bookSeries bs ON bs.bookId=b.id JOIN mediaProgresses mp ON mp.mediaItemId=b.id AND mp.userId=? WHERE bs.seriesId=? AND (mp.currentTime>0 OR mp.ebookProgress>0) AND mp.isFinished=0`
			queryArgs = []any{user.ID, seriesID}
		default:
			return true, nil
		}
	default:
		return true, nil
	}
	query += permission
	queryArgs = append(queryArgs, args...)
	var count int
	if err := h.db.QueryRowContext(ctx, query, queryArgs...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
