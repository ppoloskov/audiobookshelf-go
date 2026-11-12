package author

import (
	"context"
	"fmt"
	"log/slog"

	"audiobookshelf-compat/internal/item"
)

// bookItems are the library items of all the author's books, unfiltered
// (Author.getAllLibraryItemsForAuthor, js:71-108).
func (h *Handler) bookItems(ctx context.Context, authorID string) ([]string, error) {
	var ids []string
	err := h.db.Select(ctx, &ids, `SELECT li.id FROM bookAuthors ba JOIN libraryItems li ON li.mediaId=ba.bookId WHERE ba.authorId = ? ORDER BY ba.rowid`, authorID)
	return ids, err
}

// writeMetadata rewrites metadata.json of each item.
func (h *Handler) writeMetadata(ctx context.Context, itemIDs []string) error {
	if h.deps.WriteMetadata == nil {
		return nil
	}
	for _, id := range itemIDs {
		if err := h.deps.WriteMetadata(ctx, id); err != nil {
			return fmt.Errorf("write metadata of item %s: %w", id, err)
		}
	}
	return nil
}

// itemsChanged ends an edit of an author name: metadata.json is rewritten for
// each item and items_updated carries the expanded items.
func (h *Handler) itemsChanged(ctx context.Context, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	if err := h.writeMetadata(ctx, itemIDs); err != nil {
		return err
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{Episodes: true})
	if err != nil {
		slog.Error("items_updated", "error", err) // the edit is done; only the event is lost
		return nil
	}
	expanded := make([]item.Expanded, len(loaded))
	for i, it := range loaded {
		expanded[i] = it.Expanded(h.settings.Server().SortingPrefixes)
	}
	h.emit.ToAll("items_updated", expanded)
	return nil
}
