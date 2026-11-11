package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"audiobookshelf-compat/internal/items"
)

// Test-only helpers of the read-side tests that still live here; the routes
// that used them in production moved to internal/item.

func (h *Handler) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (h *Handler) fullItem(ctx context.Context, itemID string) (items.Full, error) {
	it, err := items.LoadOne(ctx, h.db, itemID, items.Options{Episodes: true})
	return it.Full(), err
}

// minifiedItems renders ids in the shelf shape in id order; a missing id is
// sql.ErrNoRows.
func (h *Handler) minifiedItems(ctx context.Context, ids []string) ([]items.Minified, error) {
	loaded, err := h.loadItems(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	if len(loaded) != len(ids) {
		return nil, sql.ErrNoRows
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]items.Minified, len(loaded))
	for i, it := range loaded {
		out[i] = it.Minified(prefixes)
	}
	return out, nil
}

// sameJSON reports whether got and want are the same JSON value.
func sameJSON(got, want string) bool {
	var g, w any
	return json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
}
