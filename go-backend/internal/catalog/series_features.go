package catalog

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/store"
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

func (h *Handler) feedMinified(ctx context.Context, entityType, entityID string) (map[string]any, bool, error) {
	var id, kind, entity, feedURL sql.NullString
	var title, description, ownerName, ownerEmail sql.NullString
	var prevent sql.NullInt64
	err := h.db.QueryRowContext(ctx, `SELECT id,entityType,entityId,feedURL,title,description,preventIndexing,ownerName,ownerEmail FROM feeds WHERE entityType=? AND entityId=? LIMIT 1`, entityType, entityID).Scan(&id, &kind, &entity, &feedURL, &title, &description, &prevent, &ownerName, &ownerEmail)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return map[string]any{"id": id.String, "entityType": nullString(kind), "entityId": nullString(entity), "feedUrl": nullString(feedURL), "meta": map[string]any{"title": nullString(title), "description": nullString(description), "preventIndexing": prevent.Valid && prevent.Int64 != 0, "ownerName": nullString(ownerName), "ownerEmail": nullString(ownerEmail)}}, true, nil
}

func (h *Handler) seriesMatchesFilter(ctx context.Context, seriesID, raw string, user store.User) (bool, error) {
	if raw == "" {
		return true, nil
	}
	group, value, ok := strings.Cut(raw, ".")
	if !ok {
		return true, nil
	}
	value, _ = decodeFilterValue(value)
	permission, args := userMediaClause(user, "book")
	base := ` FROM books b JOIN bookSeries bs ON bs.bookId=b.id WHERE bs.seriesId=?`
	query := `SELECT COUNT(*)` + base
	queryArgs := []any{seriesID}
	switch group {
	case "genres", "tags", "narrators":
		query += ` AND EXISTS(SELECT 1 FROM json_each(b.` + group + `) WHERE json_each.value=?)`
		queryArgs = append(queryArgs, value)
	case "authors":
		query += ` AND EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId=?)`
		queryArgs = append(queryArgs, value)
	case "publishers", "languages":
		column := map[string]string{"publishers": "publisher", "languages": "language"}[group]
		query += ` AND b.` + column + `=?`
		queryArgs = append(queryArgs, value)
	case "progress":
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

func (h *Handler) visibleSeriesItems(ctx context.Context, seriesID string, user store.User) ([]map[string]any, error) {
	permission, args := userMediaClause(user, "book")
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,bs.sequence FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id WHERE bs.seriesId=?`+permission+` ORDER BY CASE WHEN bs.sequence IS NULL OR bs.sequence='' THEN 1 ELSE 0 END,bs.sequence COLLATE NOCASE`, append([]any{seriesID}, args...)...)
	if err != nil {
		return nil, err
	}
	type ref struct{ id, sequence string }
	refs := []ref{}
	for rows.Next() {
		var itemID string
		var sequence sql.NullString
		if err := rows.Scan(&itemID, &sequence); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref{itemID, sequence.String})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]map[string]any, 0, len(refs))
	for _, row := range refs {
		item, err := h.ItemMinifiedJSON(ctx, row.id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
