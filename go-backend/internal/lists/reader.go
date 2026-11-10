// Package lists reads playlist and collection data shared by list handlers.
package lists

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
)

// Collection loads the collection response map and its library ID. canView is
// evaluated in collection order for each member item; the visible members are
// loaded with one items.Load.
func Collection(ctx context.Context, db *sql.DB, id string, canView func(context.Context, string) (bool, error)) (map[string]any, string, error) {
	queries := sqlc.New(db)
	collection, err := queries.GetCollection(ctx, id)
	if err != nil {
		return nil, "", err
	}
	itemIDs, err := queries.ListCollectionBookItemIDs(ctx, id)
	if err != nil {
		return nil, "", err
	}
	visible := itemIDs[:0:0]
	for _, itemID := range itemIDs {
		allowed, err := canView(ctx, itemID)
		if err != nil {
			return nil, "", err
		}
		if allowed {
			visible = append(visible, itemID)
		}
	}
	loaded, err := items.Load(ctx, db, visible, items.Options{})
	if err != nil {
		return nil, "", err
	}
	prefixes := serversettings.Load(ctx, db).SortingPrefixes()
	books := make([]any, len(loaded))
	for i, it := range loaded {
		books[i] = it.Expanded(prefixes)
	}
	return map[string]any{
		"id": id, "libraryId": collection.Libraryid.String, "name": collection.Name.String,
		"description": store.NullString(collection.Description), "books": books,
		"lastUpdate": dbtime.Millis(collection.Updatedat), "createdAt": dbtime.Millis(collection.Createdat),
	}, collection.Libraryid.String, nil
}
