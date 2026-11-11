package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"audiobookshelf-compat/internal/db"
)

// MediaClause is the SQL restriction for list queries: an " AND ..." clause
// hiding explicit media and media whose tags the principal may not see, on the
// media table aliased alias (books b, podcasts p). It selects exactly the media
// CanAccessContent admits. The tag list is one slice argument, so run the
// query through db.DB (whose Select expands it), not a bare *sql.DB:
//
//	clause, args := p.MediaClause("b")
//	d.Select(ctx, &rows, "SELECT ... FROM books b WHERE ..."+clause, append(args0, args...)...)
//
// An empty allow-list admits nothing: upstream's list queries skip the filter
// there and so disagree with its own item check; we fail closed.
func (p *Principal) MediaClause(alias string) (clause string, args []any) {
	if p == nil {
		return " AND 0", nil
	}
	perms := p.perms()
	if !perms.AccessExplicitContent || !p.active() {
		clause += " AND " + alias + ".explicit=0"
	}
	switch {
	case perms.AccessAllTags:
	case len(perms.ItemTagsSelected) > 0:
		compare := ">0"
		if perms.SelectedTagsNotAccessible {
			compare = "=0"
		}
		clause += " AND (SELECT count(*) FROM json_each(" + alias + ".tags) WHERE json_each.value IN (?))" + compare
		args = []any{perms.ItemTagsSelected}
	case !perms.SelectedTagsNotAccessible:
		clause += " AND 0"
	}
	return clause, args
}

// ItemAccess is what permission checks read of one library item.
type ItemAccess struct {
	LibraryID string
	Explicit  bool
	Tags      []string
}

// ReadItem loads the permission inputs of library item itemID and reports
// whether it exists and whether the principal may read it
// (User.checkCanAccessLibraryItem). An item without its media row does not
// exist. allowed is false when exists is false.
func (p *Principal) ReadItem(ctx context.Context, d *db.DB, itemID string) (access ItemAccess, exists, allowed bool, err error) {
	var row struct {
		LibraryID *string           `db:"libraryId"` // ON DELETE SET NULL
		MediaID   *string           `db:"mediaId"`
		Explicit  db.Bool           `db:"explicit"`
		Tags      db.JSON[[]string] `db:"tags"`
	}
	err = d.Get(ctx, &row, `SELECT li.libraryId, COALESCE(b.id,p.id) AS mediaId, COALESCE(b.explicit,p.explicit) AS explicit, COALESCE(b.tags,p.tags) AS tags
FROM libraryItems li
LEFT JOIN books b ON li.mediaType='book' AND b.id=li.mediaId
LEFT JOIN podcasts p ON li.mediaType='podcast' AND p.id=li.mediaId
WHERE li.id=?`, itemID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.MediaID == nil) {
		return ItemAccess{}, false, false, nil
	}
	if err != nil {
		return ItemAccess{}, false, false, fmt.Errorf("load item access %s: %w", itemID, err)
	}
	access = ItemAccess{LibraryID: db.Val(row.LibraryID), Explicit: bool(row.Explicit), Tags: row.Tags.V}
	return access, true, p.CanAccessItem(access.LibraryID, access.Explicit, access.Tags), nil
}
