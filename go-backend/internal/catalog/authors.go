// Author routes mirroring server/controllers/AuthorController.js (pinned
// v2.37.2), registered on the ServeMux by Routes (routes.go): detail with the
// author page's items and series, rename/merge, delete, image read/upload/
// delete and the Audnexus match.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
)

// AuthorFinder is AuthorFinder.js: the metadata provider lookups behind
// POST /api/authors/:id/match.
type AuthorFinder interface {
	FindAuthorByASIN(ctx context.Context, asin, region string) *wire.AuthorMatch
	FindAuthorByName(ctx context.Context, name, region string) *wire.AuthorMatch
}

// authorRecord is a row of authors.
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
func (a authorRecord) toOldJSON() (wire.Author, error) {
	added, err := dbtime.OptionalMillis(a.createdAt)
	if err != nil || added == nil {
		return wire.Author{}, fmt.Errorf("author %s createdAt: %v", a.id, err)
	}
	updated, err := dbtime.OptionalMillis(a.updatedAt)
	if err != nil || updated == nil {
		return wire.Author{}, fmt.Errorf("author %s updatedAt: %v", a.id, err)
	}
	return wire.Author{
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
func (h *Handler) authorExpanded(ctx context.Context, authorID string) (wire.AuthorExpanded, error) {
	a, err := h.loadAuthor(ctx, authorID)
	if err != nil {
		return wire.AuthorExpanded{}, err
	}
	oldJSON, err := a.toOldJSON()
	if err != nil {
		return wire.AuthorExpanded{}, err
	}
	n, err := h.authorNumBooks(ctx, authorID)
	return wire.AuthorExpanded{Author: oldJSON, NumBooks: n}, err
}

// requestUser returns the authenticated user the server attached to the request.
func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

// userAllows is `!!user.permissions?.<key> && user.isActive` (User.js:560-571)
// for the update, delete, upload and download permissions.
func userAllows(user store.User, key string) bool {
	var permissions struct {
		Update   any `json:"update"`
		Delete   any `json:"delete"`
		Upload   any `json:"upload"`
		Download any `json:"download"`
	}
	_ = json.Unmarshal(user.Permissions, &permissions)
	var granted any
	switch key {
	case "update":
		granted = permissions.Update
	case "delete":
		granted = permissions.Delete
	case "upload":
		granted = permissions.Upload
	case "download":
		granted = permissions.Download
	}
	return user.IsActive && jsTruthy(granted)
}

// authorRequest is AuthorController.middleware (AuthorController.js:427-442):
// an unknown author or one in a library the user cannot access is a 404, and
// DELETE needs the delete permission, PATCH and POST the update permission.
func (h *Handler) authorRequest(w http.ResponseWriter, r *http.Request, user store.User) (authorRecord, bool) {
	a, err := h.loadAuthor(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendStatus(w, http.StatusNotFound)
		return a, false
	}
	if err != nil {
		logging.RequestError(r, "database read failed", err)
		httputil.WriteError(w, http.StatusInternalServerError, "database read failed")
		return a, false
	}
	if !userCanAccessLibrary(user, a.libraryID) {
		httputil.SendStatus(w, http.StatusNotFound)
		return a, false
	}
	switch {
	case r.Method == http.MethodDelete && !userAllows(user, "delete"):
		logging.Warnf(`[AuthorController] User "%s" attempted to delete without permission`, user.Username)
		httputil.SendStatus(w, http.StatusForbidden)
		return a, false
	case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && !userAllows(user, "update"):
		logging.Warnf(`[AuthorController] User "%s" attempted to update without permission`, user.Username)
		httputil.SendStatus(w, http.StatusForbidden)
		return a, false
	}
	return a, true
}

func (h *Handler) authorServerError(w http.ResponseWriter, r *http.Request, message string, err error) {
	logging.RequestError(r, message, err)
	httputil.WriteError(w, http.StatusInternalServerError, message)
}

// authorItemIDs are the library item ids of an author's books in the order of
// Database.libraryItemModel.getForAuthor (LibraryItem.js:567 over
// libraryItemsBookFilters.getFilteredLibraryItems, 'authors' group, addedAt
// descending): only items the user may see (explicit and tag permissions).
func (h *Handler) authorItemIDs(ctx context.Context, user store.User, author authorRecord) ([]string, error) {
	permissionSQL, permissionArgs := userMediaClause(user, "book")
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
	if len(itemIDs) == 0 || h.emit == nil {
		return
	}
	items := make([]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, err := h.ItemJSON(ctx, id, true)
		if err != nil {
			logging.Errorf("[AuthorController] items_updated: item %s: %v", id, err)
			continue
		}
		items = append(items, item)
	}
	h.emit("*", "items_updated", items)
}

// saveMetadataFiles is libraryItem.saveMetadataFile for each item.
func (h *Handler) saveMetadataFiles(ctx context.Context, itemIDs []string) error {
	if h.itemChanged == nil {
		return nil
	}
	for _, id := range itemIDs {
		if err := h.itemChanged(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// authorFindOne is AuthorController.findOne (AuthorController.js:40-88).
func (h *Handler) authorFindOne(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	a, ok := h.authorRequest(w, r, user)
	if !ok {
		return
	}
	include := strings.Split(r.URL.Query().Get("include"), ",")
	oldJSON, err := a.toOldJSON()
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	detail := wire.AuthorDetail{Author: oldJSON}
	if sliceHas(include, "items") {
		ids, err := h.authorItemIDs(r.Context(), user, a)
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		items := make([]json.RawMessage, 0, len(ids))
		if sliceHas(include, "series") {
			groups, err := h.authorSeriesGroups(r.Context(), ids)
			if err != nil {
				h.authorServerError(w, r, "database read failed", err)
				return
			}
			detail.Series = &groups
		}
		for _, id := range ids {
			item, err := h.ItemMinifiedJSON(r.Context(), id)
			if err != nil {
				h.authorServerError(w, r, "database read failed", err)
				return
			}
			raw, err := json.Marshal(item)
			if err != nil {
				h.authorServerError(w, r, "internal error", err)
				return
			}
			items = append(items, raw)
		}
		detail.LibraryItems = &items
	}
	writeExpressJSON(w, http.StatusOK, detail)
}

func sliceHas(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

// authorSeriesGroups groups the items into their series
// (AuthorController.js:49-81): each series' items are minified copies whose
// metadata.series names the series and the item's sequence, sorted by
// sequence with the natural comparer; groups keep first-seen order.
func (h *Handler) authorSeriesGroups(ctx context.Context, itemIDs []string) ([]wire.AuthorSeriesGroup, error) {
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
	for _, itemID := range itemIDs {
		rows, err := h.db.QueryContext(ctx, `SELECT s.id,s.name,s.nameIgnorePrefix,bs.sequence FROM libraryItems li JOIN bookSeries bs ON bs.bookId=li.mediaId JOIN series s ON s.id=bs.seriesId WHERE li.id=? ORDER BY bs.createdAt ASC, bs.rowid`, itemID)
		if err != nil {
			return nil, err
		}
		type seriesRow struct {
			id                   string
			name, prefix, number sql.NullString
		}
		var memberships []seriesRow
		for rows.Next() {
			var s seriesRow
			if err := rows.Scan(&s.id, &s.name, &s.prefix, &s.number); err != nil {
				rows.Close()
				return nil, err
			}
			memberships = append(memberships, s)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(memberships) == 0 {
			continue
		}
		base, err := h.ItemMinifiedJSON(ctx, itemID)
		if err != nil {
			return nil, err
		}
		media, ok := base["media"].(wire.BookMinified)
		if !ok {
			return nil, fmt.Errorf("item %s has no book media", itemID)
		}
		for _, s := range memberships {
			withSeries := maps.Clone(base)
			m := media
			m.Metadata.Series = wire.BookSeriesRef{ID: s.id, Name: store.NullStringPtr(s.name), NameIgnorePrefix: store.NullStringPtr(s.prefix), Sequence: store.NullStringPtr(s.number)}
			withSeries["media"] = m
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
	out := make([]wire.AuthorSeriesGroup, 0, len(order))
	for _, id := range order {
		g := groups[id]
		sort.SliceStable(g.entries, func(i, j int) bool {
			return textsort.NaturalBaseCompare(jsNullable(g.entries[i].sequence), jsNullable(g.entries[j].sequence)) < 0
		})
		items := make([]json.RawMessage, len(g.entries))
		for i, e := range g.entries {
			items[i] = e.raw
		}
		out = append(out, wire.AuthorSeriesGroup{ID: g.id, Name: g.name, Items: items})
	}
	return out, nil
}

// jsonBodyObject is express.json for a handler that reads named keys: a
// request whose content type is not JSON has an empty body object, and a body
// that is not a JSON object has no keys.
func jsonBodyObject(r *http.Request) map[string]json.RawMessage {
	body := map[string]json.RawMessage{}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		return body
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return body
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) == nil && parsed != nil {
		return parsed
	}
	return body
}

// stringOrNull reads a body value that is a string or JSON null.
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
	args := append(append([]any{}, u.values...), dbNow(), authorID)
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
func (h *Handler) authorUpdate(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	a, ok := h.authorRequest(w, r, user)
	if !ok {
		return
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
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request payload. No valid keys found")
		return
	}
	newName, hasName := payload["name"]
	authorNameUpdate := hasName && !samePointer(newName, store.NullStringPtr(a.name))
	var newLastFirst *string
	if authorNameUpdate {
		newLastFirst = authorLastFirst(newName)
	}

	// An author of the same name in the library absorbs this one.
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
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			h.authorServerError(w, r, "database read failed", err)
			return
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
			h.authorServerError(w, r, "database write failed", err)
			return
		}
		numBooks := 0
		if authorNameUpdate {
			itemIDs, err := h.authorBookItems(ctx, a.id)
			if err != nil {
				h.authorServerError(w, r, "database read failed", err)
				return
			}
			numBooks = len(itemIDs)
			if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
				h.authorServerError(w, r, "metadata file write failed", err)
				return
			}
			h.emitItemsUpdated(ctx, itemIDs)
		} else {
			var err error
			if numBooks, err = h.authorNumBooks(ctx, a.id); err != nil {
				h.authorServerError(w, r, "database read failed", err)
				return
			}
		}
		saved, err := h.loadAuthor(ctx, a.id)
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		oldJSON, err := saved.toOldJSON()
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		if h.emit != nil {
			h.emit("*", "author_updated", wire.AuthorExpanded{Author: oldJSON, NumBooks: numBooks})
		}
	}
	saved, err := h.loadAuthor(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	oldJSON, err := saved.toOldJSON()
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	writeExpressJSON(w, http.StatusOK, wire.AuthorUpdateResult{Author: oldJSON, Updated: &hasUpdated})
}

// authorMerge is the merge branch of AuthorController.update
// (AuthorController.js:129-175): every book of the renamed author moves to the
// existing author of that name, the renamed author is removed.
func (h *Handler) authorMerge(w http.ResponseWriter, r *http.Request, a authorRecord, existingID string) {
	ctx := r.Context()
	existing, err := h.loadAuthor(ctx, existingID)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	logging.Infof(`[AuthorController] Merging author "%s" with "%s"`, a.name.String, existing.name.String) // AuthorController.js:130
	itemIDs, err := h.authorBookItems(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if len(itemIDs) > 0 {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			h.authorServerError(w, r, "database write failed", err)
			return
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(ctx, `SELECT bookId FROM bookAuthors WHERE authorId=? ORDER BY rowid`, a.id)
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		var bookIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				h.authorServerError(w, r, "database read failed", err)
				return
			}
			bookIDs = append(bookIDs, id)
		}
		if err := rows.Close(); err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		// bookAuthorModel.removeByIds then bulkCreate({ ignoreDuplicates: true })
		// (AuthorController.js:151-152).
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE authorId=?`, a.id); err != nil {
			h.authorServerError(w, r, "database write failed", err)
			return
		}
		now := dbNow()
		for _, bookID := range bookIDs {
			linkID := makeUUID()
			if linkID == "" {
				h.authorServerError(w, r, "internal error", errors.New("generate book author id"))
				return
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, linkID, bookID, existing.id, now); err != nil {
				h.authorServerError(w, r, "database write failed", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			h.authorServerError(w, r, "database write failed", err)
			return
		}
		if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
			h.authorServerError(w, r, "metadata file write failed", err)
			return
		}
		h.emitItemsUpdated(ctx, itemIDs)
	}
	oldAuthor, err := a.toOldJSON()
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.id); err != nil {
		h.authorServerError(w, r, "database write failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "author_removed", oldAuthor)
	}
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID) // AuthorController.js:164
	expanded, err := h.authorExpanded(ctx, existing.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "author_updated", expanded)
	}
	merged := true
	writeExpressJSON(w, http.StatusOK, wire.AuthorUpdateResult{Author: expanded.Author, Merged: &merged})
}

// purgeImageCache is CacheManager.purgeImageCache (CacheManager.js:97-126): it
// removes every cached image whose name starts with the entity id.
func (h *Handler) purgeImageCache(entityID string) {
	if entityID == "" {
		return
	}
	dir := h.imageCachePath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), entityID) {
			logging.Debugf("[CacheManager] Going to purge %s", entry.Name())
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				logging.Errorf(`[CacheManager] Failed to remove cache "%s" %v`, filepath.Join(dir, entry.Name()), err)
			}
		}
	}
}

// imageCachePath mirrors CacheManager.ImageCachePath (CacheManager.js:29).
func (h *Handler) imageCachePath() string {
	return filepath.Join(h.cfg.MetadataPath, "cache", "images")
}

// authorDelete is AuthorController.delete (AuthorController.js:233-258).
func (h *Handler) authorDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authorRequest(w, r, requestUser(r))
	if !ok {
		return
	}
	ctx := r.Context()
	logging.Infof(`[AuthorController] Removing author "%s"`, a.name.String)
	if a.imagePath.Valid && a.imagePath.String != "" {
		h.purgeImageCache(a.id)
	}
	itemIDs, err := h.authorBookItems(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	oldAuthor, err := a.toOldJSON()
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, a.id); err != nil {
		h.authorServerError(w, r, "database write failed", err)
		return
	}
	if err := h.saveMetadataFiles(ctx, itemIDs); err != nil {
		h.authorServerError(w, r, "metadata file write failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "author_removed", oldAuthor)
	}
	h.filters.RemoveAuthor(oldAuthor.LibraryID, oldAuthor.ID)
	httputil.SendStatus(w, http.StatusOK)
}

// authorImagePath is the file AuthorFinder.saveAuthorImage writes
// (AuthorFinder.js:43-66): <metadata>/authors/<id>.png for a url ending in
// "png", otherwise .jpg.
func (h *Handler) authorImagePath(authorID, imageURL string) string {
	parts := strings.Split(strings.ToLower(imageURL), ".")
	ext := "jpg"
	if parts[len(parts)-1] == "png" {
		ext = "png"
	}
	return path.Join(filepath.ToSlash(filepath.Join(h.cfg.MetadataPath, "authors")), authorID+"."+ext)
}

// saveAuthorImage is AuthorFinder.saveAuthorImage: it downloads the image
// (fileUtils.downloadImageFile: image/* but not svg) and returns the written
// path, or the error message upstream would send.
func (h *Handler) saveAuthorImage(ctx context.Context, authorID, imageURL string) (string, string) {
	authorDir := filepath.Join(h.cfg.MetadataPath, "authors")
	if err := os.MkdirAll(authorDir, 0o755); err != nil {
		return "", err.Error()
	}
	outputPath := h.authorImagePath(authorID, imageURL)
	if err := downloadImageFile(ctx, imageURL, filepath.FromSlash(outputPath)); err != nil {
		message := axiosMessage(err)
		if message == "" {
			message = "Unknown error"
		}
		logging.Errorf(`[AuthorFinder] Download image file failed for "%s" %s`, imageURL, message) // AuthorFinder.js:61
		return "", message
	}
	return outputPath, ""
}

// downloadImageFile is fileUtils.downloadFile with the image content-type
// filter (fileUtils.js:292-358): a GET with a 30 second timeout through the
// SSRF filter whose 2xx image/* (not svg) response body is written to filepath.
func downloadImageFile(ctx context.Context, rawURL, filepath string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("Invalid URL")
	}
	transport := coverURLTransport()
	if httputil.SSRFFilterDisabled(u.Hostname()) {
		transport.DialContext = (&net.Dialer{}).DialContext
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("Maximum number of redirects exceeded")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("Invalid URL")
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Request failed with status code %d", response.StatusCode)
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return fmt.Errorf("Invalid content type %q", contentType)
	}
	file, err := os.Create(filepath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// axiosMessage maps a Go transport error onto the message axios/Node report
// for the same failure (err.message in AuthorFinder.saveAuthorImage).
func axiosMessage(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout of 30000ms exceeded"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "getaddrinfo ENOTFOUND " + dnsErr.Name
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(err, syscall.ECONNREFUSED) && opErr.Addr != nil {
		return "connect ECONNREFUSED " + opErr.Addr.String()
	}
	return err.Error()
}

// authorUploadImage is AuthorController.uploadImage (AuthorController.js:267-304).
func (h *Handler) authorUploadImage(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	a, ok := h.authorRequest(w, r, user)
	if !ok {
		return
	}
	if !userAllows(user, "upload") {
		logging.Warnf(`User "%s" attempted to upload an image without permission`, user.Username)
		httputil.SendStatus(w, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
	rawURL, present := body["url"]
	var imageURL string
	var isString bool
	if present {
		isString = json.Unmarshal(rawURL, &imageURL) == nil
	}
	// `!req.body.url`: absent, null, false, 0 and "" are all falsy.
	var generic any
	if present {
		_ = json.Unmarshal(rawURL, &generic)
	}
	if !present || !jsTruthy(generic) {
		logging.Errorf("[AuthorController] Invalid request payload. 'url' not in request body")
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request payload. 'url' not in request body")
		return
	}
	if !isString || (!strings.HasPrefix(imageURL, "http:") && !strings.HasPrefix(imageURL, "https:")) {
		shown := imageURL
		if !isString {
			shown = jsString(generic)
		}
		logging.Errorf(`[AuthorController] Invalid request payload. Invalid url "%s"`, shown)
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf(`Invalid request payload. Invalid url "%s"`, shown))
		return
	}
	logging.Debugf(`[AuthorController] Requesting download author image from url "%s"`, imageURL)
	imagePath, message := h.saveAuthorImage(ctx, a.id, imageURL)
	if message != "" {
		httputil.WriteError(w, http.StatusBadRequest, message)
		return
	}
	if a.imagePath.Valid && a.imagePath.String != "" {
		h.purgeImageCache(a.id)
	}
	update := &authorColumnUpdate{}
	update.force("imagePath", &imagePath) // changed('imagePath', true): bump updatedAt
	if err := update.save(ctx, h.db, a.id); err != nil {
		h.authorServerError(w, r, "database write failed", err)
		return
	}
	expanded, err := h.authorExpanded(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "author_updated", expanded)
	}
	writeExpressJSON(w, http.StatusOK, wire.AuthorImageResult{Author: expanded.Author})
}

// jsString is how a JavaScript template literal renders a JSON value.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return fmt.Sprint(x)
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", x), ".0")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// authorDeleteImage is AuthorController.deleteImage (AuthorController.js:313-329).
func (h *Handler) authorDeleteImage(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authorRequest(w, r, requestUser(r))
	if !ok {
		return
	}
	ctx := r.Context()
	if !a.imagePath.Valid || a.imagePath.String == "" {
		logging.Errorf(`[AuthorController] Author "%s" has no imagePath set`, a.imagePath.String)
		httputil.WriteError(w, http.StatusBadRequest, "Author has no image path set")
		return
	}
	logging.Infof(`[AuthorController] Removing image for author "%s" at "%s"`, a.name.String, a.imagePath.String)
	h.purgeImageCache(a.id)
	// CoverManager.removeFile (CoverManager.js:34-44).
	if _, err := os.Stat(a.imagePath.String); err != nil {
		logging.Warnf("[CoverManager] Attempting to remove file that does not exist %s", a.imagePath.String)
	} else if err := os.Remove(a.imagePath.String); err != nil {
		h.authorServerError(w, r, "internal error", err)
		return
	}
	update := &authorColumnUpdate{}
	update.set("imagePath", store.NullStringPtr(a.imagePath), nil)
	if err := update.save(ctx, h.db, a.id); err != nil {
		h.authorServerError(w, r, "database write failed", err)
		return
	}
	expanded, err := h.authorExpanded(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "author_updated", expanded)
	}
	writeExpressJSON(w, http.StatusOK, wire.AuthorImageResult{Author: expanded.Author})
}

var asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// authorMatch is AuthorController.match (AuthorController.js:337-383).
func (h *Handler) authorMatch(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authorRequest(w, r, requestUser(r))
	if !ok {
		return
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
	bodyString := func(key string) string {
		var s string
		_ = json.Unmarshal(body[key], &s)
		return s
	}
	region := bodyString("region")
	if region == "" {
		region = "us"
	}
	var data *wire.AuthorMatch
	if h.authorFinder != nil {
		if asin := bodyString("asin"); asin != "" && asinPattern.MatchString(strings.ToUpper(asin)) {
			data = h.authorFinder.FindAuthorByASIN(ctx, asin, region)
		} else {
			data = h.authorFinder.FindAuthorByName(ctx, bodyString("q"), region)
		}
	}
	if data == nil {
		httputil.WriteError(w, http.StatusNotFound, "Author not found")
		return
	}
	logging.Debugf(`[AuthorController] match author with "%s"`, firstNonEmpty(bodyString("q"), bodyString("asin")))
	update := &authorColumnUpdate{}
	hasUpdates := false
	if data.ASIN != "" && (!a.asin.Valid || a.asin.String != data.ASIN) {
		asin := data.ASIN
		update.set("asin", store.NullStringPtr(a.asin), &asin)
		hasUpdates = true
	}
	// Only updates the image if there was none before or the ASIN was updated.
	if data.Image != nil && *data.Image != "" && (!a.imagePath.Valid || a.imagePath.String == "" || hasUpdates) {
		h.purgeImageCache(a.id)
		if imagePath, message := h.saveAuthorImage(ctx, a.id, *data.Image); message == "" {
			update.set("imagePath", store.NullStringPtr(a.imagePath), &imagePath)
			hasUpdates = true
		}
	}
	if data.Description != "" && (!a.description.Valid || a.description.String != data.Description) {
		description := data.Description
		update.set("description", store.NullStringPtr(a.description), &description)
		hasUpdates = true
	}
	if hasUpdates {
		if err := update.save(ctx, h.db, a.id); err != nil {
			h.authorServerError(w, r, "database write failed", err)
			return
		}
		expanded, err := h.authorExpanded(ctx, a.id)
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		if h.emit != nil {
			h.emit("*", "author_updated", expanded)
		}
	}
	saved, err := h.loadAuthor(ctx, a.id)
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	oldJSON, err := saved.toOldJSON()
	if err != nil {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	writeExpressJSON(w, http.StatusOK, wire.AuthorMatchResult{Updated: hasUpdates, Author: oldJSON})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var uuidPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`)

// isUUID is utils.isUUID, uuid.validate.
func isUUID(s string) bool { return uuidPattern.MatchString(s) }

// authorImage is AuthorController.getImage (AuthorController.js:391-419) over
// CacheManager.handleAuthorCache (CacheManager.js:155-190). The route is
// public (Auth.js:21).
func (h *Handler) authorImage(w http.ResponseWriter, r *http.Request) {
	authorID := r.PathValue("id")
	query := r.URL.Query()
	if query.Get("raw") != "" {
		a, err := h.loadAuthor(r.Context(), authorID)
		if errors.Is(err, sql.ErrNoRows) {
			logging.Warnf(`[AuthorController] Author "%s" not found`, authorID)
			httputil.SendStatus(w, http.StatusNotFound)
			return
		}
		if err != nil {
			h.authorServerError(w, r, "database read failed", err)
			return
		}
		if !a.imagePath.Valid || a.imagePath.String == "" {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.name.String, a.imagePath.String)
			httputil.SendStatus(w, http.StatusNotFound)
			return
		}
		if _, err := os.Stat(a.imagePath.String); err != nil {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.name.String, a.imagePath.String)
			httputil.SendStatus(w, http.StatusNotFound)
			return
		}
		h.coverRaw(w, r, a.imagePath.String)
		return
	}
	format := query.Get("format")
	if format == "" {
		if reqSupportsWebp(r.Header.Get("Accept")) {
			format = "webp"
		} else {
			format = "jpeg"
		}
	}
	widthValue := clampCoverDimension(query.Get("width"), 4096)
	heightValue := clampCoverDimension(query.Get("height"), 4096)
	if format != "webp" && format != "jpeg" && format != "png" || !isUUID(authorID) {
		httputil.SendStatus(w, http.StatusBadRequest)
		return
	}
	width, height := 400, -1
	if widthValue != nil {
		width = *widthValue
	}
	if heightValue != nil {
		height = *heightValue
	}
	w.Header().Set("Content-Type", "image/"+format)
	cacheName := fmt.Sprintf("%s_%d", authorID, width)
	if heightValue != nil {
		cacheName += fmt.Sprintf("x%d", *heightValue)
	}
	cachePath := filepath.Join(h.imageCachePath(), cacheName+"."+format)
	if _, err := os.Stat(cachePath); err == nil {
		streamCoverCache(w, r, cachePath)
		return
	}
	a, err := h.loadAuthor(r.Context(), authorID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.authorServerError(w, r, "database read failed", err)
		return
	}
	if err != nil || !a.imagePath.Valid || a.imagePath.String == "" {
		httputil.SendStatus(w, http.StatusNotFound)
		return
	}
	if _, err := os.Stat(a.imagePath.String); err != nil {
		httputil.SendStatus(w, http.StatusNotFound)
		return
	}
	if err := os.MkdirAll(h.imageCachePath(), 0o755); err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, http.StatusInternalServerError)
		return
	}
	if err := resizeImage(r.Context(), a.imagePath.String, cachePath, width, height); err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, http.StatusInternalServerError)
		return
	}
	streamCoverCache(w, r, cachePath)
}
