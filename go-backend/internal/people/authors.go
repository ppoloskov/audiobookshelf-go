// Author routes mirroring server/controllers/AuthorController.js (pinned
// v2.37.2), registered on the ServeMux by Routes (routes.go): detail with the
// author page's items and series, rename/merge, delete, image read/upload/
// delete and the Audnexus match.
package people

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/sqlupdate"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

type AuthorRecord struct {
	ID, LibraryID                                 string
	Name, LastFirst, ASIN, Description, ImagePath sql.NullString
	CreatedAt, UpdatedAt                          any
}

const AuthorColumns = `id,name,lastFirst,asin,description,imagePath,libraryId,createdAt,updatedAt`

func ScanAuthor(row interface{ Scan(...any) error }) (AuthorRecord, error) {
	var a AuthorRecord
	err := row.Scan(&a.ID, &a.Name, &a.LastFirst, &a.ASIN, &a.Description, &a.ImagePath, &a.LibraryID, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// LoadAuthor reads one author row.
func LoadAuthor(ctx context.Context, db *sql.DB, id string) (AuthorRecord, error) {
	return ScanAuthor(db.QueryRowContext(ctx, `SELECT `+AuthorColumns+` FROM authors WHERE id=?`, id))
}

// ToOldJSON is Author.toOldJSON (Author.js:177-188).
func (a AuthorRecord) ToOldJSON() (AuthorView, error) {
	added, err := dbtime.OptionalMillis(a.CreatedAt)
	if err != nil || added == nil {
		return AuthorView{}, fmt.Errorf("author %s createdAt: %v", a.ID, err)
	}
	updated, err := dbtime.OptionalMillis(a.UpdatedAt)
	if err != nil || updated == nil {
		return AuthorView{}, fmt.Errorf("author %s updatedAt: %v", a.ID, err)
	}
	return AuthorView{
		ID: a.ID, ASIN: store.NullStringPtr(a.ASIN), Name: store.NullStringPtr(a.Name),
		Description: store.NullStringPtr(a.Description), ImagePath: store.NullStringPtr(a.ImagePath),
		LibraryID: a.LibraryID, AddedAt: *added, UpdatedAt: *updated,
	}, nil
}

func (h *Handler) authorNumBooks(ctx context.Context, authorID string) (int, error) {
	var n int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return n, err
}

// AuthorExpanded is Author.toOldJSONExpanded(numBooks) read fresh from the row.
func AuthorExpanded(ctx context.Context, db *sql.DB, authorID string) (AuthorWithBooks, error) {
	a, err := LoadAuthor(ctx, db, authorID)
	if err != nil {
		return AuthorWithBooks{}, err
	}
	oldJSON, err := a.ToOldJSON()
	if err != nil {
		return AuthorWithBooks{}, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return AuthorWithBooks{AuthorView: oldJSON, NumBooks: n}, err
}

// AuthorRequest is AuthorController.middleware (AuthorController.js:427-442):
// an unknown author or one in a library the user cannot access is a 404, and
// DELETE needs the delete permission, PATCH and POST the update permission.
func AuthorRequest(ctx context.Context, db *sql.DB, r *http.Request, user store.User) (AuthorRecord, error) {
	a, err := LoadAuthor(ctx, db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return a, httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return a, httputil.Internal("database read failed", err)
	}
	if !access.For(ctx, user).Library(a.LibraryID) {
		return a, httputil.Status(http.StatusNotFound, "")
	}
	switch {
	case r.Method == http.MethodDelete && !access.For(ctx, user).Delete():
		logging.Warnf(`[AuthorController] User "%s" attempted to delete without permission`, user.Username)
		return a, httputil.Status(http.StatusForbidden, "")
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !access.For(ctx, user).Update():
		logging.Warnf(`[AuthorController] User "%s" attempted to update without permission`, user.Username)
		return a, httputil.Status(http.StatusForbidden, "")
	}
	return a, nil
}

// authorItemIDs are the library item ids of an author's books in the order of
// Database.libraryItemModel.getForAuthor (LibraryItem.js:567 over
// libraryItemsBookFilters.getFilteredLibraryItems, 'authors' group, addedAt
// descending): only items the user may see (explicit and tag permissions).
func (h *Handler) authorItemIDs(ctx context.Context, user store.User, author AuthorRecord) ([]string, error) {
	permissionSQL, permissionArgs := access.For(ctx, user).MediaClause("b")
	args := append([]any{author.LibraryID, author.ID}, permissionArgs...)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType='book' AND EXISTS (SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId=?)`+permissionSQL+` ORDER BY li.createdAt DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// authorBookItems are the library item ids of every book of an author, with no
// permission filter: Author.getAllLibraryItemsForAuthor (Author.js:71-108).
func (h *Handler) authorBookItems(ctx context.Context, authorID string) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT li.id FROM bookAuthors ba JOIN libraryItems li ON li.mediaId=ba.bookId WHERE ba.authorId=? ORDER BY ba.rowid`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// emitItemsUpdated is SocketAuthority.libraryItemsEmitter('items_updated', ...):
// the hub filters the expanded items per client.
func (h *Handler) emitItemsUpdated(ctx context.Context, itemIDs []string) {
	if len(itemIDs) == 0 {
		return
	}
	expanded, err := h.expandedItems(ctx, itemIDs)
	if err != nil {
		logging.Errorf("[AuthorController] items_updated: %v", err)
		return
	}
	items := make([]any, len(expanded))
	for i, item := range expanded {
		items[i] = item
	}
	h.emit.ToAll("items_updated", items)
}

// authorFindOne is AuthorController.findOne (AuthorController.js:40-88).
func (h *Handler) authorFindOne(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	a, err := AuthorRequest(r.Context(), h.db, r, user)
	if err != nil {
		return err
	}
	include := strings.Split(r.URL.Query().Get("include"), ",")
	oldJSON, err := a.ToOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	detail := AuthorDetail{AuthorView: oldJSON}
	if slices.Contains(include, "items") {
		ids, err := h.authorItemIDs(r.Context(), user, a)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		rawItems := make([]json.RawMessage, 0, len(ids))
		if slices.Contains(include, "series") {
			groups, err := h.AuthorSeriesGroups(r.Context(), ids)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			detail.Series = &groups
		}
		loaded, err := items.Load(r.Context(), h.db, ids, items.Options{})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		prefixes := h.sortingPrefixes(r.Context())
		for _, it := range loaded {
			raw, err := json.Marshal(it.Minified(prefixes))
			if err != nil {
				return httputil.Internal("internal error", err)
			}
			rawItems = append(rawItems, raw)
		}
		detail.LibraryItems = &rawItems
	}
	return httputil.JSON(w, r, detail)
}

// AuthorSeriesGroups groups the items into their series
// (AuthorController.js:49-81): each series' items are minified copies whose
// metadata.series names the series and the item's sequence, sorted by
// sequence with the natural comparer; groups keep first-seen order.
func (h *Handler) AuthorSeriesGroups(ctx context.Context, itemIDs []string) ([]AuthorSeriesGroup, error) {
	type entry struct {
		raw      json.RawMessage
		sequence *string
	}
	type group struct {
		id      string
		name    *string
		entries []entry
	}
	var order []string
	groups := map[string]*group{}
	type membership struct {
		id                   string
		name, prefix, number sql.NullString
	}
	memberships := map[string][]membership{}
	if len(itemIDs) > 0 {
		args := make([]any, len(itemIDs))
		for i, id := range itemIDs {
			args[i] = id
		}
		rows, err := h.db.QueryContext(ctx, `SELECT li.id,s.id,s.name,s.nameIgnorePrefix,bs.sequence FROM libraryItems li JOIN bookSeries bs ON bs.bookId=li.mediaId JOIN series s ON s.id=bs.seriesId WHERE li.id IN (`+store.Placeholders(len(itemIDs))+`) ORDER BY bs.createdAt ASC, bs.rowid`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var itemID string
			var m membership
			if err := rows.Scan(&itemID, &m.id, &m.name, &m.prefix, &m.number); err != nil {
				rows.Close()
				return nil, err
			}
			memberships[itemID] = append(memberships[itemID], m)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	loaded, err := items.Load(ctx, h.db, itemIDs, items.Options{})
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	for _, it := range loaded {
		if len(memberships[it.ID]) == 0 {
			continue
		}
		base := it.Minified(prefixes)
		media, ok := base.Media.(books.BookMinified)
		if !ok {
			return nil, fmt.Errorf("item %s has no book media", it.ID)
		}
		for _, s := range memberships[it.ID] {
			m := media
			m.Metadata.Series = BookSeriesRef{ID: s.id, Name: store.NullStringPtr(s.name), NameIgnorePrefix: store.NullStringPtr(s.prefix), Sequence: store.NullStringPtr(s.number)}
			withSeries := base
			withSeries.Media = m
			raw, err := json.Marshal(withSeries)
			if err != nil {
				return nil, err
			}
			g := groups[s.id]
			if g == nil {
				g = &group{id: s.id, name: store.NullStringPtr(s.name)}
				groups[s.id] = g
				order = append(order, s.id)
			}
			g.entries = append(g.entries, entry{raw: raw, sequence: store.NullStringPtr(s.number)})
		}
	}
	out := make([]AuthorSeriesGroup, 0, len(order))
	for _, id := range order {
		g := groups[id]
		sort.SliceStable(g.entries, func(i, j int) bool {
			return textsort.NaturalBaseCompare(jsNullable(g.entries[i].sequence), jsNullable(g.entries[j].sequence)) < 0
		})
		items := make([]json.RawMessage, len(g.entries))
		for i, e := range g.entries {
			items[i] = e.raw
		}
		out = append(out, AuthorSeriesGroup{ID: g.id, Name: g.name, Items: items})
	}
	return out, nil
}

func jsNullable(value *string) string {
	if value == nil {
		return "null"
	}
	return *value
}

func stringOrNull(raw json.RawMessage) (value *string, ok bool) {
	if string(raw) == "null" {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	return &s, true
}

func samePointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// AuthorColumnUpdate is the author-table specialization of sqlupdate.Update.
type AuthorColumnUpdate struct{ sqlupdate.Update }

// Save writes the changed author columns with a fresh updatedAt.
func (u *AuthorColumnUpdate) Save(ctx context.Context, db *sql.DB, authorID string) error {
	return u.Update.Save(ctx, db, "authors", authorID)
}

func authorLastFirst(name *string) *string {
	if name == nil || *name == "" {
		return nil
	}
	lf := nameparse.ToLastFirst(*name)
	return &lf
}

// authorUpdate is AuthorController.update (AuthorController.js:96-224).
func (h *Handler) authorUpdate(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	a, err := AuthorRequest(r.Context(), h.db, r, user)
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := httputil.DecodeObjectFields(r)
	payload := map[string]*string{}
	for _, key := range []string{"name", "description", "asin"} {
		if raw, present := body[key]; present {
			if value, valid := stringOrNull(raw); valid {
				payload[key] = value
			}
		}
	}
	if len(payload) == 0 {
		logging.Errorf("[AuthorController] Invalid request payload. No valid keys found")
		return httputil.Status(http.StatusBadRequest, "Invalid request payload. No valid keys found")
	}
	newName, hasName := payload["name"]
	authorNameUpdate := hasName && !samePointer(newName, store.NullStringPtr(a.Name))
	var newLastFirst *string
	if authorNameUpdate {
		newLastFirst = authorLastFirst(newName)
	}

	if authorNameUpdate {
		var existingID string
		var err error
		if newName == nil {
			err = h.db.QueryRowContext(ctx, `SELECT id FROM authors WHERE id<>? AND name IS NULL AND libraryId=? LIMIT 1`, a.ID, a.LibraryID).Scan(&existingID)
		} else {
			err = h.db.QueryRowContext(ctx, `SELECT id FROM authors WHERE id<>? AND name=? AND libraryId=? LIMIT 1`, a.ID, *newName, a.LibraryID).Scan(&existingID)
		}
		if err == nil {
			h.authorMerge(w, r, a, existingID)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return httputil.Internal("database read failed", err)
		}
	}

	update := &AuthorColumnUpdate{}
	current := map[string]*string{"name": store.NullStringPtr(a.Name), "description": store.NullStringPtr(a.Description), "asin": store.NullStringPtr(a.ASIN)}
	for _, key := range []string{"name", "description", "asin"} {
		if value, present := payload[key]; present {
			update.Change(key, current[key], value)
		}
	}
	if authorNameUpdate {
		update.Change("lastFirst", store.NullStringPtr(a.LastFirst), newLastFirst)
	} else if !a.LastFirst.Valid || a.LastFirst.String == "" {
		update.Change("lastFirst", store.NullStringPtr(a.LastFirst), authorLastFirst(store.NullStringPtr(a.Name)))
	}
	hasUpdated := len(update.Columns) > 0
	if hasUpdated {
		if err := update.Save(ctx, h.db, a.ID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		numBooks := 0
		if authorNameUpdate {
			itemIDs, err := h.authorBookItems(ctx, a.ID)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			numBooks = len(itemIDs)
			if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
				return httputil.Internal("metadata file write failed", err)
			}
			h.emitItemsUpdated(ctx, itemIDs)
		} else {
			var err error
			if numBooks, err = h.authorNumBooks(ctx, a.ID); err != nil {
				return httputil.Internal("database read failed", err)
			}
		}
		saved, err := LoadAuthor(ctx, h.db, a.ID)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		oldJSON, err := saved.ToOldJSON()
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		h.emit.ToAll("author_updated", AuthorWithBooks{AuthorView: oldJSON, NumBooks: numBooks})
	}
	saved, err := LoadAuthor(ctx, h.db, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := saved.ToOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, AuthorUpdateResult{Author: oldJSON, Updated: &hasUpdated})
}

// authorMerge is the merge branch of AuthorController.update
// (AuthorController.js:129-175): every book of the renamed author moves to the
// existing author of that name, the renamed author is removed.
func (h *Handler) authorMerge(w http.ResponseWriter, r *http.Request, a AuthorRecord, existingID string) error {
	ctx := r.Context()
	existing, err := LoadAuthor(ctx, h.db, existingID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	logging.Infof(`[AuthorController] Merging author "%s" with "%s"`, a.Name.String, existing.Name.String) // AuthorController.js:130
	itemIDs, err := h.authorBookItems(ctx, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(itemIDs) > 0 {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT bookId FROM bookAuthors WHERE authorId=? ORDER BY rowid`, a.ID)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		var bookIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return httputil.Internal("database read failed", err)
			}
			bookIDs = append(bookIDs, id)
		}
		if err := rows.Close(); err != nil {
			return httputil.Internal("database read failed", err)
		}
		// bookAuthorModel.removeByIds then bulkCreate({ ignoreDuplicates: true })
		// (AuthorController.js:151-152).
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE authorId=?`, a.ID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		now := dbtime.Now()
		for _, bookID := range bookIDs {
			linkID := ids.Must()
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, linkID, bookID, existing.ID, now); err != nil {
				return httputil.Internal("database write failed", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
			return httputil.Internal("metadata file write failed", err)
		}
		h.emitItemsUpdated(ctx, itemIDs)
	}
	oldAuthor, err := a.ToOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.ID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	h.emit.ToAll("author_removed", oldAuthor)
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID) // AuthorController.js:164
	expanded, err := AuthorExpanded(ctx, h.db, existing.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAll("author_updated", expanded)
	merged := true
	return httputil.JSON(w, r, AuthorUpdateResult{Author: expanded.AuthorView, Merged: &merged})
}

// authorDelete is AuthorController.delete (AuthorController.js:233-258).
func (h *Handler) authorDelete(w http.ResponseWriter, r *http.Request) error {
	a, err := AuthorRequest(r.Context(), h.db, r, store.RequestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	logging.Infof(`[AuthorController] Removing author "%s"`, a.Name.String)
	if a.ImagePath.Valid && a.ImagePath.String != "" {
		h.purgeImageCache(a.ID)
	}
	itemIDs, err := h.authorBookItems(ctx, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldAuthor, err := a.ToOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.ID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
		return httputil.Internal("metadata file write failed", err)
	}
	h.emit.ToAll("author_removed", oldAuthor)
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID)
	return httputil.OK(w, r)
}
