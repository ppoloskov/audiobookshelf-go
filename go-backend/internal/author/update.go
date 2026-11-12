package author

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	idgen "audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/text"
)

// optional is a text key of a request body: Set when the key was present with
// a string or null (null clears the column, like the web client's emptied
// field). A key of any other type is ignored, as upstream's update does.
type optional struct {
	Set bool
	Val *string
}

// UnmarshalJSON records a string or null and ignores any other type.
func (o *optional) UnmarshalJSON(data []byte) error {
	var s *string
	if json.Unmarshal(data, &s) == nil {
		o.Set, o.Val = true, s
	}
	return nil
}

// result is the body of PATCH and match: {author, updated}, {author, merged}
// or {updated, author}.
type result struct {
	Author  View  `json:"author"`
	Updated *bool `json:"updated,omitempty"`
	Merged  *bool `json:"merged,omitempty"`
}

// lastFirst is the stored "Last, First" form of a name; nil for no name.
func lastFirst(name *string) *string {
	if db.Val(name) == "" {
		return nil
	}
	return db.Ptr(text.ToLastFirst(*name))
}

// update is AuthorController.update (js:96-224). A name another author of the
// library already has merges this author into that one.
func (h *Handler) update(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	body, err := httpx.Decode[struct{ Name, Description, ASIN optional }](w, r)
	if err != nil {
		return err
	}
	if !body.Name.Set && !body.Description.Set && !body.ASIN.Set {
		return httpx.BadRequest("Invalid request payload. No valid keys found")
	}
	renamed := body.Name.Set && !same(body.Name.Val, a.Name)
	if renamed {
		var other string
		err := h.db.Get(ctx, &other, `SELECT id FROM authors WHERE id <> ? AND name IS ? AND libraryId = ? LIMIT 1`, a.ID, body.Name.Val, a.LibraryID)
		if err == nil {
			return h.merge(w, r, a, other)
		}
		if !isNoRows(err) {
			return httpx.Internal("database read failed", err)
		}
	}
	var cols []string
	for _, f := range []struct {
		column string
		dst    **string
		next   optional
	}{{"name", &a.Name, body.Name}, {"description", &a.Description, body.Description}, {"asin", &a.ASIN, body.ASIN}} {
		if f.next.Set && !same(*f.dst, f.next.Val) {
			*f.dst = f.next.Val
			cols = append(cols, f.column)
		}
	}
	// lastFirst follows a rename and is backfilled when missing.
	if lf := lastFirst(a.Name); (renamed || db.Val(a.LastFirst) == "") && !same(a.LastFirst, lf) {
		a.LastFirst = lf
		cols = append(cols, "lastFirst")
	}
	updated := len(cols) > 0
	if updated {
		if err := h.save(ctx, &a, cols...); err != nil {
			return httpx.Internal("database write failed", err)
		}
		if renamed {
			if err := h.renamed(ctx, a.ID); err != nil {
				return httpx.Internal("metadata file write failed", err)
			}
		}
		if err := h.announce(ctx, a); err != nil {
			return httpx.Internal("database read failed", err)
		}
	}
	return httpx.JSON(w, r, result{Author: viewOf(a, nil), Updated: &updated})
}

// renamed tells the items of an author whose name changed.
func (h *Handler) renamed(ctx context.Context, authorID string) error {
	ids, err := h.bookItems(ctx, authorID)
	if err != nil {
		return err
	}
	return h.itemsChanged(ctx, ids)
}

// merge is the merge branch of update (js:129-175): every book of a moves to
// the author target and a is removed.
func (h *Handler) merge(w http.ResponseWriter, r *http.Request, a db.Author, targetID string) error {
	ctx := r.Context()
	var target db.Author
	if err := h.db.Get(ctx, &target, `SELECT * FROM authors WHERE id = ?`, targetID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	slog.Info("merging author", "name", db.Val(a.Name), "into", db.Val(target.Name))
	itemIDs, err := h.bookItems(ctx, a.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var bookIDs []string
		if err := h.db.Select(ctx, &bookIDs, `SELECT bookId FROM bookAuthors WHERE authorId = ? ORDER BY rowid`, a.ID); err != nil {
			return err
		}
		now := db.Now()
		for _, bookID := range bookIDs { // INSERT OR IGNORE: a book that has both authors keeps one link
			if _, err := h.db.Exec(ctx, `INSERT OR IGNORE INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, idgen.Must(), bookID, targetID, now); err != nil {
				return err
			}
		}
		_, err := h.db.Exec(ctx, `DELETE FROM authors WHERE id = ?`, a.ID) // its links go with it
		return err
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.itemsChanged(ctx, itemIDs); err != nil {
		return httpx.Internal("metadata file write failed", err)
	}
	h.emit.ToAll("author_removed", viewOf(a, nil))
	h.deps.Filters.Invalidate(db.Val(a.LibraryID))
	if err := h.announce(ctx, target); err != nil {
		return httpx.Internal("database read failed", err)
	}
	merged := true
	return httpx.JSON(w, r, result{Author: viewOf(target, nil), Merged: &merged})
}
