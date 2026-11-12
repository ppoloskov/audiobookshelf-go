package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/serversettings"
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

// Entity tags are opaque validators; their hash algorithm is not a client contract.
func requireOpaqueETag(t *testing.T, tag string) {
	t.Helper()
	value := strings.TrimPrefix(tag, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		t.Fatalf("invalid entity tag %q", tag)
	}
	for _, b := range []byte(value[1 : len(value)-1]) {
		if b < 0x21 || b == '"' || b == 0x7f {
			t.Fatalf("invalid entity tag %q", tag)
		}
	}
}

// Read-side test helpers; the routes that used them in production moved to internal/item.

func (h *Handler) loadItems(ctx context.Context, ids []string, episodes bool) ([]items.Item, error) {
	return items.Load(ctx, h.db, ids, items.Options{Episodes: episodes})
}

// expandedItems renders ids in the expanded shape in id order; missing ids are
// skipped.
func (h *Handler) expandedItems(ctx context.Context, ids []string) ([]items.Expanded, error) {
	loaded, err := h.loadItems(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]items.Expanded, len(loaded))
	for i, it := range loaded {
		out[i] = it.Expanded(prefixes)
	}
	return out, nil
}

func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
}

// expandedItem is the expanded item. Callers enforce permissions.
func (h *Handler) expandedItem(ctx context.Context, itemID string) (items.Expanded, error) {
	out, err := h.expandedItems(ctx, []string{itemID})
	if err == nil && len(out) == 0 {
		err = sql.ErrNoRows
	}
	if err != nil {
		return items.Expanded{}, err
	}
	return out[0], nil
}
