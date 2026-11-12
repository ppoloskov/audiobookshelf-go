package author

import (
	"context"
	"database/sql"
	"errors"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
)

// View is Author.toOldJSON; with NumBooks it is toOldJSONExpanded, the
// payload of author_updated. NumBooks is omitted when nil.
type View struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	NumBooks    *int    `json:"numBooks,omitempty"`
}

// viewOf is the JSON of a row; numBooks may be nil.
func viewOf(a db.Author, numBooks *int) View {
	return View{ID: a.ID, ASIN: a.ASIN, Name: a.Name, Description: a.Description, ImagePath: a.ImagePath,
		LibraryID: db.Val(a.LibraryID), AddedAt: a.CreatedAt.Millis(), UpdatedAt: a.UpdatedAt.Millis(), NumBooks: numBooks}
}

// Listed is an entry of the library author list: the expanded author and its
// lastFirst name.
type Listed struct {
	View
	LastFirst *string `json:"lastFirst"`
}

// List is a library's authors with the number of books the user may see
// (explicit and tag rules). Only admins see authors without such a book.
func List(ctx context.Context, d *db.DB, user *auth.Principal, libraryID string) ([]Listed, error) {
	clause, args := user.MediaClause("b")
	var rows []struct {
		db.Author
		NumBooks int `db:"numBooks"`
	}
	err := d.Select(ctx, &rows, `SELECT a.*, (SELECT COUNT(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id=ba.bookId WHERE ba.authorId=a.id`+clause+`) AS numBooks FROM authors a WHERE a.libraryId = ? ORDER BY a.rowid`, append(args, libraryID)...)
	if err != nil {
		return nil, err
	}
	out := make([]Listed, 0, len(rows))
	for _, row := range rows {
		if user.IsAdmin() || row.NumBooks > 0 {
			out = append(out, Listed{viewOf(row.Author, &row.NumBooks), row.LastFirst})
		}
	}
	return out, nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// same reports whether two nullable texts are equal (nil only equals nil).
func same(a, b *string) bool { return (a == nil) == (b == nil) && db.Val(a) == db.Val(b) }

// count is the number of books the author has.
func (h *Handler) count(ctx context.Context, authorID string) (int, error) {
	var n int
	err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM bookAuthors WHERE authorId = ?`, authorID)
	return n, err
}

// save writes the changed columns of a, stamps updatedAt and drops the filter
// menus of the author's library (the name is a menu entry).
func (h *Handler) save(ctx context.Context, a *db.Author, cols ...string) error {
	a.UpdatedAt = db.Now()
	if err := h.db.Update(ctx, "authors", a, append(cols, "updatedAt")...); err != nil {
		return err
	}
	h.deps.Filters.Invalidate(db.Val(a.LibraryID))
	return nil
}

// announce sends author_updated with the author's book count.
func (h *Handler) announce(ctx context.Context, a db.Author) error {
	n, err := h.count(ctx, a.ID)
	if err != nil {
		return err
	}
	h.emit.ToAll("author_updated", viewOf(a, &n))
	return nil
}
