package catalog

import (
	"audiobookshelf-compat/internal/items"
	"context"
	"database/sql"
)

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
