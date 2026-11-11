package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/mediafiles"
	"context"
	"database/sql"
)

func (h *Handler) env() itemwrite.Env {
	return itemwrite.Env{DB: h.db, Filters: h.filters, Emit: h.emit}
}

func (h *Handler) saveMetadataFiles(ctx context.Context, itemIDs []string) error {
	if h.deps.ItemChanged == nil {
		return nil
	}
	for _, id := range itemIDs {
		if err := h.deps.ItemChanged(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// itemUpdated announces item_updated with the expanded item
// (SocketAuthority.libraryItemEmitter).
func (h *Handler) itemUpdated(ctx context.Context, itemID string) error {
	if h.emit == nil { // nobody listens: skip building the expanded item
		return nil
	}
	expanded, err := h.expandedItem(ctx, itemID)
	if err != nil {
		return err
	}
	h.emit.ToAll("item_updated", expanded)
	return nil
}

// ItemChanged is the end of an item mutation: the metadata file is rewritten
// and item_updated carries the expanded item. The admin tag and genre routes
// use it too.
func (h *Handler) ItemChanged(ctx context.Context, itemID string) error {
	if err := h.saveMetadataFiles(ctx, []string{itemID}); err != nil {
		return err
	}
	return h.itemUpdated(ctx, itemID)
}

// coverChanged follows an UPDATE of an item's coverPath: when it changed a row
// the item is stamped, its cover cache dropped and item_updated sent.
func (h *Handler) coverChanged(ctx context.Context, res sql.Result, itemID string) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if _, err := h.db.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbtime.Now(), itemID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	mediafiles.PurgeCoverCache(h.coverCachePath(), itemID)
	if err := h.itemUpdated(ctx, itemID); err != nil {
		return httputil.Internal("database read failed", err)
	}
	return nil
}
