// Author routes mirroring server/controllers/AuthorController.js (pinned
// v2.37.2), registered on the ServeMux by Routes (routes.go): detail with the
// author page's items and series, rename/merge, delete, image read/upload/
// delete and the Audnexus match.
package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// AuthorFinder is AuthorFinder.js: the metadata provider lookups behind
// POST /api/authors/:id/match.
type AuthorFinder interface {
	FindAuthorByASIN(ctx context.Context, asin, region string) *wire.AuthorMatch
	FindAuthorByName(ctx context.Context, name, region string) *wire.AuthorMatch
}

type authorRecord struct {
	id, libraryID                                 string
	name, lastFirst, asin, description, imagePath sql.NullString
	createdAt, updatedAt                          any
}

const authorColumns = `id,name,lastFirst,asin,description,imagePath,libraryId,createdAt,updatedAt`

func scanAuthor(row interface{ Scan(...any) error }) (authorRecord, error) {
	var a authorRecord
	err := row.Scan(&a.id, &a.name, &a.lastFirst, &a.asin, &a.description, &a.imagePath, &a.libraryID, &a.createdAt, &a.updatedAt)
	return a, err
}

func (h *Handler) loadAuthor(ctx context.Context, id string) (authorRecord, error) {
	return scanAuthor(h.db.QueryRowContext(ctx, `SELECT `+authorColumns+` FROM authors WHERE id=?`, id))
}

// toOldJSON is Author.toOldJSON (Author.js:177-188).
func (a authorRecord) toOldJSON() (authorView, error) {
	added, err := dbtime.OptionalMillis(a.createdAt)
	if err != nil || added == nil {
		return authorView{}, fmt.Errorf("author %s createdAt: %v", a.id, err)
	}
	updated, err := dbtime.OptionalMillis(a.updatedAt)
	if err != nil || updated == nil {
		return authorView{}, fmt.Errorf("author %s updatedAt: %v", a.id, err)
	}
	return authorView{
		ID: a.id, ASIN: store.NullStringPtr(a.asin), Name: store.NullStringPtr(a.name),
		Description: store.NullStringPtr(a.description), ImagePath: store.NullStringPtr(a.imagePath),
		LibraryID: a.libraryID, AddedAt: *added, UpdatedAt: *updated,
	}, nil
}

func (h *Handler) authorNumBooks(ctx context.Context, authorID string) (int, error) {
	var n int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return n, err
}

// authorExpanded is Author.toOldJSONExpanded(numBooks) read fresh from the row.
func (h *Handler) authorExpanded(ctx context.Context, authorID string) (authorWithBooks, error) {
	a, err := h.loadAuthor(ctx, authorID)
	if err != nil {
		return authorWithBooks{}, err
	}
	oldJSON, err := a.toOldJSON()
	if err != nil {
		return authorWithBooks{}, err
	}
	n, err := h.authorNumBooks(ctx, authorID)
	return authorWithBooks{authorView: oldJSON, NumBooks: n}, err
}

// authorRequest is AuthorController.middleware (AuthorController.js:427-442):
// an unknown author or one in a library the user cannot access is a 404, and
// DELETE needs the delete permission, PATCH and POST the update permission.
func (h *Handler) authorRequest(r *http.Request, user store.User) (authorRecord, error) {
	a, err := h.loadAuthor(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return a, httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return a, httputil.Internal("database read failed", err)
	}
	if !access.For(r.Context(), user).Library(a.libraryID) {
		return a, httputil.Status(http.StatusNotFound, "")
	}
	switch {
	case r.Method == http.MethodDelete && !access.For(r.Context(), user).Delete():
		logging.Warnf(`[AuthorController] User "%s" attempted to delete without permission`, user.Username)
		return a, httputil.Status(http.StatusForbidden, "")
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !access.For(r.Context(), user).Update():
		logging.Warnf(`[AuthorController] User "%s" attempted to update without permission`, user.Username)
		return a, httputil.Status(http.StatusForbidden, "")
	}
	return a, nil
}

// authorItemIDs are the library item ids of an author's books in the order of
// Database.libraryItemModel.getForAuthor (LibraryItem.js:567 over
// libraryItemsBookFilters.getFilteredLibraryItems, 'authors' group, addedAt
// descending): only items the user may see (explicit and tag permissions).
func (h *Handler) authorItemIDs(ctx context.Context, user store.User, author authorRecord) ([]string, error) {
	permissionSQL, permissionArgs := access.For(ctx, user).MediaClause("b")
	args := append([]any{author.libraryID, author.id}, permissionArgs...)
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
	user := requestUser(r)
	a, err := h.authorRequest(r, user)
	if err != nil {
		return err
	}
	include := strings.Split(r.URL.Query().Get("include"), ",")
	oldJSON, err := a.toOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	detail := authorDetail{authorView: oldJSON}
	if sliceHas(include, "items") {
		ids, err := h.authorItemIDs(r.Context(), user, a)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		items := make([]json.RawMessage, 0, len(ids))
		if sliceHas(include, "series") {
			groups, err := h.authorSeriesGroups(r.Context(), ids)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			detail.Series = &groups
		}
		loaded, err := libitem.Load(r.Context(), h.db, ids, libitem.Options{})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		prefixes := h.sortingPrefixes(r.Context())
		for _, it := range loaded {
			raw, err := json.Marshal(it.Minified(prefixes))
			if err != nil {
				return httputil.Internal("internal error", err)
			}
			items = append(items, raw)
		}
		detail.LibraryItems = &items
	}
	return httputil.JSON(w, r, detail)
}

// authorSeriesGroups groups the items into their series
// (AuthorController.js:49-81): each series' items are minified copies whose
// metadata.series names the series and the item's sequence, sorted by
// sequence with the natural comparer; groups keep first-seen order.
func (h *Handler) authorSeriesGroups(ctx context.Context, itemIDs []string) ([]authorSeriesGroup, error) {
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
		rows, err := h.db.QueryContext(ctx, `SELECT li.id,s.id,s.name,s.nameIgnorePrefix,bs.sequence FROM libraryItems li JOIN bookSeries bs ON bs.bookId=li.mediaId JOIN series s ON s.id=bs.seriesId WHERE li.id IN (`+placeholders(len(itemIDs))+`) ORDER BY bs.createdAt ASC, bs.rowid`, args...)
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
	loaded, err := libitem.Load(ctx, h.db, itemIDs, libitem.Options{})
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	for _, it := range loaded {
		if len(memberships[it.ID]) == 0 {
			continue
		}
		base := it.Minified(prefixes)
		media, ok := base.Media.(libitem.BookMinified)
		if !ok {
			return nil, fmt.Errorf("item %s has no book media", it.ID)
		}
		for _, s := range memberships[it.ID] {
			m := media
			m.Metadata.Series = bookSeriesRef{ID: s.id, Name: store.NullStringPtr(s.name), NameIgnorePrefix: store.NullStringPtr(s.prefix), Sequence: store.NullStringPtr(s.number)}
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
	out := make([]authorSeriesGroup, 0, len(order))
	for _, id := range order {
		g := groups[id]
		sort.SliceStable(g.entries, func(i, j int) bool {
			return textsort.NaturalBaseCompare(jsNullable(g.entries[i].sequence), jsNullable(g.entries[j].sequence)) < 0
		})
		items := make([]json.RawMessage, len(g.entries))
		for i, e := range g.entries {
			items[i] = e.raw
		}
		out = append(out, authorSeriesGroup{ID: g.id, Name: g.name, Items: items})
	}
	return out, nil
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

// authorColumnUpdate collects the changed columns of an author the way
// Sequelize's set()/changed() does, and saves them with a fresh updatedAt.
type authorColumnUpdate struct {
	columns []string
	values  []any
}

func (u *authorColumnUpdate) set(column string, current, next *string) bool {
	if samePointer(current, next) {
		return false
	}
	u.columns = append(u.columns, column)
	if next == nil {
		u.values = append(u.values, nil)
	} else {
		u.values = append(u.values, *next)
	}
	return true
}

// force marks a column changed even when the value is the same
// (changed(column, true)): the update then still bumps updatedAt.
func (u *authorColumnUpdate) force(column string, next *string) {
	for i, c := range u.columns {
		if c == column {
			u.values[i] = store.StringPtrValue(next)
			return
		}
	}
	u.columns = append(u.columns, column)
	u.values = append(u.values, store.StringPtrValue(next))
}

func (u *authorColumnUpdate) save(ctx context.Context, db ctxExecer, authorID string) error {
	if len(u.columns) == 0 {
		return nil
	}
	sets := make([]string, 0, len(u.columns)+1)
	for _, c := range u.columns {
		sets = append(sets, c+"=?")
	}
	sets = append(sets, "updatedAt=?")
	args := append(append([]any{}, u.values...), dbtime.Now(), authorID)
	_, err := db.ExecContext(ctx, `UPDATE authors SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

type ctxExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
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
	user := requestUser(r)
	a, err := h.authorRequest(r, user)
	if err != nil {
		return err
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
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
	authorNameUpdate := hasName && !samePointer(newName, store.NullStringPtr(a.name))
	var newLastFirst *string
	if authorNameUpdate {
		newLastFirst = authorLastFirst(newName)
	}

	if authorNameUpdate {
		var existingID string
		var err error
		if newName == nil {
			err = h.db.QueryRowContext(ctx, `SELECT id FROM authors WHERE id<>? AND name IS NULL AND libraryId=? LIMIT 1`, a.id, a.libraryID).Scan(&existingID)
		} else {
			err = h.db.QueryRowContext(ctx, `SELECT id FROM authors WHERE id<>? AND name=? AND libraryId=? LIMIT 1`, a.id, *newName, a.libraryID).Scan(&existingID)
		}
		if err == nil {
			h.authorMerge(w, r, a, existingID)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return httputil.Internal("database read failed", err)
		}
	}

	update := &authorColumnUpdate{}
	current := map[string]*string{"name": store.NullStringPtr(a.name), "description": store.NullStringPtr(a.description), "asin": store.NullStringPtr(a.asin)}
	for _, key := range []string{"name", "description", "asin"} {
		if value, present := payload[key]; present {
			update.set(key, current[key], value)
		}
	}
	if authorNameUpdate {
		update.set("lastFirst", store.NullStringPtr(a.lastFirst), newLastFirst)
	} else if !a.lastFirst.Valid || a.lastFirst.String == "" {
		update.set("lastFirst", store.NullStringPtr(a.lastFirst), authorLastFirst(store.NullStringPtr(a.name)))
	}
	hasUpdated := len(update.columns) > 0
	if hasUpdated {
		if err := update.save(ctx, h.db, a.id); err != nil {
			return httputil.Internal("database write failed", err)
		}
		numBooks := 0
		if authorNameUpdate {
			itemIDs, err := h.authorBookItems(ctx, a.id)
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
			if numBooks, err = h.authorNumBooks(ctx, a.id); err != nil {
				return httputil.Internal("database read failed", err)
			}
		}
		saved, err := h.loadAuthor(ctx, a.id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		oldJSON, err := saved.toOldJSON()
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		h.emit.ToAll("author_updated", authorWithBooks{authorView: oldJSON, NumBooks: numBooks})
	}
	saved, err := h.loadAuthor(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldJSON, err := saved.toOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, authorUpdateResult{Author: oldJSON, Updated: &hasUpdated})
}

// authorMerge is the merge branch of AuthorController.update
// (AuthorController.js:129-175): every book of the renamed author moves to the
// existing author of that name, the renamed author is removed.
func (h *Handler) authorMerge(w http.ResponseWriter, r *http.Request, a authorRecord, existingID string) error {
	ctx := r.Context()
	existing, err := h.loadAuthor(ctx, existingID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	logging.Infof(`[AuthorController] Merging author "%s" with "%s"`, a.name.String, existing.name.String) // AuthorController.js:130
	itemIDs, err := h.authorBookItems(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(itemIDs) > 0 {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT bookId FROM bookAuthors WHERE authorId=? ORDER BY rowid`, a.id)
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE authorId=?`, a.id); err != nil {
			return httputil.Internal("database write failed", err)
		}
		now := dbtime.Now()
		for _, bookID := range bookIDs {
			linkID := uuid.NewString()
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, linkID, bookID, existing.id, now); err != nil {
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
	oldAuthor, err := a.toOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	h.emit.ToAll("author_removed", oldAuthor)
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID) // AuthorController.js:164
	expanded, err := h.authorExpanded(ctx, existing.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAll("author_updated", expanded)
	merged := true
	return httputil.JSON(w, r, authorUpdateResult{Author: expanded.authorView, Merged: &merged})
}

// authorDelete is AuthorController.delete (AuthorController.js:233-258).
func (h *Handler) authorDelete(w http.ResponseWriter, r *http.Request) error {
	a, err := h.authorRequest(r, requestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	logging.Infof(`[AuthorController] Removing author "%s"`, a.name.String)
	if a.imagePath.Valid && a.imagePath.String != "" {
		h.purgeImageCache(a.id)
	}
	itemIDs, err := h.authorBookItems(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	oldAuthor, err := a.toOldJSON()
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
		return httputil.Internal("metadata file write failed", err)
	}
	h.emit.ToAll("author_removed", oldAuthor)
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID)
	return httputil.OK(w, r)
}
