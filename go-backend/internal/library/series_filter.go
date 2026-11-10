package library

import (
	"context"
	"strings"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/store"
)

// seriesMatchesFilter reports whether a series has a visible book matching the
// request filter. Progress and missing-authors semantics mirror
// libraryItemsBookFilters.js.
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
		clause, clauseArgs, err := FilterSQL(raw, false, user)
		if err != nil {
			return false, err
		}
		query += clause
		queryArgs = append(queryArgs, clauseArgs...)
	case "progress":
		value = decodeFilterValue(value)
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
