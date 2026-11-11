// Batch item lookups shared by the library download route.
package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/store"
	"context"
)

// itemsExpandedWhere is LibraryItem.findAllExpandedWhere({ id: ids })
// (LibraryItem.js:150-183): the existing items among ids in the order
// Sequelize returns them, by the first joined row ordered by bookAuthor then
// bookSeries creation time (see batchGet).
func (h *Handler) itemsExpandedWhere(ctx context.Context, ids []string) ([]itemScope, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT `+itemScopeColumns+` FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId AND li.mediaType='book' LEFT JOIN bookAuthors ba ON ba.bookId=b.id LEFT JOIN bookSeries bs ON bs.bookId=b.id WHERE li.id IN (`+store.Placeholders(len(ids))+`) ORDER BY ba.createdAt ASC, bs.createdAt ASC`, store.StringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	items := make([]itemScope, 0, len(ids))
	for rows.Next() {
		item, err := scanItemScope(rows)
		if err != nil {
			return nil, err
		}
		if !seen[item.id] {
			seen[item.id] = true
			items = append(items, item)
		}
	}
	return items, rows.Err()
}

// userCanAccessItems is ensureUserCanAccessLibraryItemsForBatch
// (LibraryItemController.js:48-56): every item must pass
// User.checkCanAccessLibraryItem (library, explicit content and tags).
func (h *Handler) userCanAccessItems(ctx context.Context, user store.User, items []itemScope) (bool, error) {
	perms := access.For(ctx, user)
	for _, item := range items {
		if !perms.Library(item.libraryID) {
			return false, nil
		}
		table := map[string]string{"book": "books", "podcast": "podcasts"}[item.mediaType]
		if table == "" {
			return false, nil
		}
		permissionSQL, permissionArgs := perms.MediaClause("b")
		var visible int
		query := `SELECT COUNT(*) FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId WHERE li.id=?` + permissionSQL
		if err := h.db.QueryRowContext(ctx, query, append([]any{item.id}, permissionArgs...)...).Scan(&visible); err != nil {
			return false, err
		}
		if visible == 0 {
			return false, nil
		}
	}
	return true, nil
}
