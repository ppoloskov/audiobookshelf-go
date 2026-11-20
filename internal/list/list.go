// Package list serves collections (shared, per library, books only) and
// playlists (per user, books or podcast episodes).
package list

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"regexp"
	"slices"

	"monstrobook/internal/auth"
	"monstrobook/internal/db"
	"monstrobook/internal/events"
	"monstrobook/internal/httpx"
	"monstrobook/internal/ids"
	"monstrobook/internal/item"
	"monstrobook/internal/settings"
)

// Handler owns the collection and playlist routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
}

// New returns a Handler; settings supplies the sorting prefixes of item views.
func New(d *db.DB, settings *settings.Store, emit events.Emitter) *Handler {
	return &Handler{db: d, settings: settings, emit: emit}
}

type userHandler func(w http.ResponseWriter, r *http.Request, user *auth.Principal) error

// Routes registers the routes. All need an authenticated user; each handler
// states the further permission it checks.
func (h *Handler) Routes(mux *http.ServeMux) {
	// get answers HEAD with 404 as Express does.
	get := func(next userHandler) httpx.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			if r.Method == http.MethodHead {
				return httpx.NotFound()
			}
			return h.write(next)(w, r)
		}
	}
	mux.Handle("GET /api/collections", get(h.listCollections))
	mux.Handle("POST /api/collections", h.write(h.createCollection))
	mux.Handle("GET /api/collections/{id}", get(h.getCollection))
	mux.Handle("PATCH /api/collections/{id}", h.write(h.updateCollection))
	mux.Handle("DELETE /api/collections/{id}", h.write(h.deleteCollection))
	mux.Handle("POST /api/collections/{id}/book", h.write(h.addCollectionBook))
	mux.Handle("DELETE /api/collections/{id}/book/{itemID}", h.write(h.removeCollectionBook))
	mux.Handle("POST /api/collections/{id}/batch/{action}", h.write(h.batchCollectionBooks))
	mux.Handle("GET /api/playlists", get(h.listPlaylists))
	mux.Handle("POST /api/playlists", h.write(h.createPlaylist))
	mux.Handle("GET /api/playlists/{id}", get(h.getPlaylist))
	mux.Handle("PATCH /api/playlists/{id}", h.write(h.updatePlaylist))
	mux.Handle("DELETE /api/playlists/{id}", h.write(h.deletePlaylist))
	mux.Handle("POST /api/playlists/{id}/{action}", h.write(h.playlistAction))
	mux.Handle("POST /api/playlists/{id}/batch/add", h.write(h.addPlaylistItems))
	mux.Handle("POST /api/playlists/{id}/batch/remove", h.write(h.removePlaylistItems))
	mux.Handle("DELETE /api/playlists/{id}/item/{itemPath...}", h.write(h.removePlaylistItem))
	mux.Handle("GET /api/libraries/{libraryID}/collections", get(h.libraryCollections))
	mux.Handle("GET /api/libraries/{libraryID}/playlists", get(h.libraryPlaylists))
}

// write runs next for the authenticated principal (401 without one).
func (h *Handler) write(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user := auth.User(r.Context()); user != nil {
			return next(w, r, user)
		}
		return httpx.Error(http.StatusUnauthorized, "")
	}
}

type listOp uint8

const (
	opRead listOp = iota
	opUpdate
	opDelete
)

// container parameterizes the shared persistence, access, view, and event rules
// of collections and playlists so CRUD and membership handlers share one path.
type container[R, V any] struct {
	h              *Handler
	kind           string // "collection" or "playlist"; drives event names
	table          string // "collections" or "playlists"
	notFound       string
	links          linkTable
	clearEmptyDesc bool
	reorderUpdates bool // true if reordering items also bumps updatedAt
	access         func(user *auth.Principal, row R, op listOp) error
	libraryID      func(R) string
	metaPtrs       func(*R) (name, desc **string, updatedAt *db.Time)
	viewOf         func(context.Context, *auth.Principal, R) (V, error)
	emit           func(V, string)
}

func (c container[R, V]) open(ctx context.Context, user *auth.Principal, id string, op listOp) (R, error) {
	var row R
	err := c.h.db.Get(ctx, &row, `SELECT * FROM `+c.table+` WHERE id=?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, httpx.Error(http.StatusNotFound, c.notFound)
	}
	if err != nil {
		return row, err
	}
	return row, c.access(user, row, op)
}

func openBody[Req, R, V any](c container[R, V], w http.ResponseWriter, r *http.Request, user *auth.Principal, op listOp) (R, Req, error) {
	row, err := c.open(r.Context(), user, r.PathValue("id"), op)
	if err != nil {
		var zero Req
		return row, zero, err
	}
	req, err := httpx.Decode[Req](w, r)
	return row, req, err
}

func (c container[R, V]) list(ctx context.Context, user *auth.Principal, where string, args ...any) ([]V, error) {
	var rows []R
	if err := c.h.db.Select(ctx, &rows, `SELECT * FROM `+c.table+` `+where, args...); err != nil {
		return nil, err
	}
	return accessibleViews(rows, user, c.libraryID, func(r R) (V, error) { return c.viewOf(ctx, user, r) })
}

func (c container[R, V]) finish(w http.ResponseWriter, r *http.Request, user *auth.Principal, id, event string, adjust func(context.Context, *V)) error {
	var row R
	if err := c.h.db.Get(r.Context(), &row, `SELECT * FROM `+c.table+` WHERE id=?`, id); err != nil {
		return err
	}
	view, err := c.viewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	if adjust != nil {
		adjust(r.Context(), &view)
	}
	if event != "" {
		c.emit(view, event)
	}
	return httpx.JSON(w, r, view)
}

func (c container[R, V]) create(w http.ResponseWriter, r *http.Request, user *auth.Principal, buildRow func(id string, now db.Time) R, entries []newEntry) error {
	id, now := "", db.Now()
	err := c.h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		if id, err = ids.New(); err != nil {
			return err
		}
		row := buildRow(id, now)
		if err = c.h.db.Insert(ctx, c.table, &row); err != nil {
			return err
		}
		return c.links.append(ctx, c.h.db, id, 0, entries)
	})
	if err != nil {
		return err
	}
	return c.finish(w, r, user, id, c.kind+"_added", nil)
}

func (c container[R, V]) update(w http.ResponseWriter, r *http.Request, user *auth.Principal, row R, id string, reqName, reqDesc *string, wanted []string, missing int, beforeReorder func(context.Context) error) error {
	metaChanged, moved := false, false
	err := c.h.db.Tx(r.Context(), func(ctx context.Context) error {
		name, desc, updatedAt := c.metaPtrs(&row)
		metaChanged = patchListMeta(name, desc, reqName, reqDesc, c.clearEmptyDesc)
		if len(wanted) > 0 {
			if beforeReorder != nil {
				if err := beforeReorder(ctx); err != nil {
					return err
				}
			}
			var err error
			if moved, err = c.links.reorder(ctx, c.h.db, id, wanted, missing); err != nil {
				return err
			}
		}
		if metaChanged || (c.reorderUpdates && moved) {
			*updatedAt = db.Now()
			return c.h.db.Update(ctx, c.table, &row, "name", "description", "updatedAt")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.finish(w, r, user, id, eventIf(metaChanged || moved, c.kind+"_updated"), nil)
}

func (c container[R, V]) remove(w http.ResponseWriter, r *http.Request, user *auth.Principal, beforeDelete func(context.Context, R) error, afterDelete func()) error {
	id := r.PathValue("id")
	row, err := c.open(r.Context(), user, id, opDelete)
	if err != nil {
		return err
	}
	view, err := c.viewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	err = c.h.db.Tx(r.Context(), func(ctx context.Context) error {
		if beforeDelete != nil {
			if err := beforeDelete(ctx, row); err != nil {
				return err
			}
		}
		_, err := c.h.db.Exec(ctx, `DELETE FROM `+c.table+` WHERE id=?`, id)
		return err
	})
	if err != nil {
		return err
	}
	if afterDelete != nil {
		afterDelete()
	}
	c.emit(view, c.kind+"_removed")
	return httpx.Status(w, http.StatusOK)
}

func (c container[R, V]) addEntries(w http.ResponseWriter, r *http.Request, user *auth.Principal, id string, entries []newEntry, dupMsg string, adjust func(int) func(context.Context, *V)) error {
	added := 0
	err := c.h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		added, err = c.links.add(ctx, c.h.db, id, entries, dupMsg)
		return err
	})
	if err != nil {
		return err
	}
	var adj func(context.Context, *V)
	if adjust != nil {
		adj = adjust(added)
	}
	return c.finish(w, r, user, id, eventIf(added > 0, c.kind+"_updated"), adj)
}

var markup = regexp.MustCompile(`<[^>]*>`)

// cleanName strips tags and unescapes entities before a name is stored
// (persisted value, as upstream).
func cleanName(value string) string { return html.UnescapeString(markup.ReplaceAllString(value, "")) }

// emptyAsNull is d unless it is empty: an empty description is stored as NULL.
func emptyAsNull(d *string) *string {
	if d == nil || *d == "" {
		return nil
	}
	return d
}

// eventIf is name when changed, else "" (no event).
func eventIf(changed bool, name string) string {
	if changed {
		return name
	}
	return ""
}

// patchListMeta applies optional name (tag-stripped, ignoring empty) and
// description updates, reporting whether either field changed.
func patchListMeta(dstName, dstDesc **string, reqName, reqDesc *string, clearEmptyDesc bool) bool {
	changed := false
	if name := cleanName(db.Val(reqName)); name != "" && name != db.Val(*dstName) {
		*dstName, changed = &name, true
	}
	if reqDesc != nil && *reqDesc != db.Val(*dstDesc) {
		if clearEmptyDesc {
			*dstDesc = emptyAsNull(reqDesc)
		} else {
			*dstDesc = reqDesc
		}
		changed = true
	}
	return changed
}

// validateCreate cleans name and checks that name, libraryID and library
// access are valid for creating a collection or playlist.
func validateCreate(user *auth.Principal, rawName *string, libraryID, badReqMsg string) (string, error) {
	name := cleanName(db.Val(rawName))
	if name == "" || libraryID == "" {
		return "", httpx.BadRequest(badReqMsg)
	}
	if !user.CanAccessLibrary(libraryID) {
		return "", httpx.Forbidden()
	}
	return name, nil
}

// accessibleViews builds views for rows whose library the user may access.
func accessibleViews[R, V any](rows []R, user *auth.Principal, libID func(R) string, build func(R) (V, error)) ([]V, error) {
	views := make([]V, 0, len(rows))
	for _, row := range rows {
		if !user.CanAccessLibrary(libID(row)) {
			continue
		}
		view, err := build(row)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// loadItems loads library items by id and returns them with the server sorting prefixes.
func (h *Handler) loadItems(ctx context.Context, itemIDs []string, opts item.Options) ([]item.Item, []string, error) {
	loaded, err := item.Load(ctx, h.db, itemIDs, opts)
	if err != nil {
		return nil, nil, err
	}
	return loaded, h.settings.Server().SortingPrefixes, nil
}

// indexItems indexes loaded library items by library item id.
func indexItems(items []item.Item) map[string]item.Item {
	byID := make(map[string]item.Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	return byID
}

// itemRef is what list membership depends on of a library item.
type itemRef struct {
	ID        string `db:"id"`
	MediaID   string `db:"mediaId"`
	MediaType string `db:"mediaType"`
	LibraryID string `db:"libraryId"`
}

// itemRefs reads library items by id; unknown ids are absent from the result.
func (h *Handler) itemRefs(ctx context.Context, ids ...string) (map[string]itemRef, error) {
	refs := make(map[string]itemRef, len(ids))
	for chunk := range slices.Chunk(ids, 500) {
		var rows []itemRef
		err := h.db.Select(ctx, &rows, `SELECT id, COALESCE(mediaId,'') AS mediaId, COALESCE(mediaType,'') AS mediaType, COALESCE(libraryId,'') AS libraryId FROM libraryItems WHERE id IN (?)`, chunk)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			refs[row.ID] = row
		}
	}
	return refs, nil
}

// libraryPage checks a /api/libraries/{libraryID}/... listing: 403 when the
// library is closed to the user, 404 when it does not exist, 400 for a
// negative limit or page.
func (h *Handler) libraryPage(r *http.Request, user *auth.Principal) (libraryID string, limit, page int, err error) {
	libraryID, n := r.PathValue("libraryID"), 0
	if !user.CanAccessLibrary(libraryID) {
		return "", 0, 0, httpx.Forbidden()
	}
	if err := h.db.Get(r.Context(), &n, `SELECT COUNT(*) FROM libraries WHERE id=?`, libraryID); err != nil {
		return "", 0, 0, err
	}
	if n == 0 {
		return "", 0, 0, httpx.Error(http.StatusNotFound, "Library not found")
	}
	limit, page = httpx.QueryInt(r, "limit", 0), httpx.QueryInt(r, "page", 0)
	if limit < 0 {
		return "", 0, 0, httpx.BadRequest("Invalid request. limit must be a positive integer")
	}
	if page < 0 {
		return "", 0, 0, httpx.BadRequest("Invalid request. page must be a positive integer")
	}
	return libraryID, limit, page, nil
}

// pageOf is the window of all selected by limit and page; limit 0 selects all.
func pageOf[T any](all []T, limit, page int) []T {
	if limit <= 0 {
		return all
	}
	start := min(page*limit, len(all))
	return all[start:min(start+limit, len(all))]
}

// newEntry is a resolved member to append to a collection or playlist.
type newEntry struct{ mediaID, mediaType string }

// linkTable is an ordered membership table (collectionBooks or
// playlistMediaItems); the names are constants, never input.
type linkTable struct{ table, mediaCol, ownerCol string }

var (
	collectionLinks = linkTable{"collectionBooks", "bookId", "collectionId"}
	playlistLinks   = linkTable{"playlistMediaItems", "mediaItemId", "playlistId"}
)

type link struct {
	ID      string `db:"id"`
	MediaID string `db:"mediaId"`
	Order   int64  `db:"order"`
}

func (t linkTable) list(ctx context.Context, d *db.DB, owner string) ([]link, error) {
	var links []link
	err := d.Select(ctx, &links, `SELECT id, COALESCE(`+t.mediaCol+`,'') AS mediaId, COALESCE("order",0) AS "order" FROM `+t.table+` WHERE `+t.ownerCol+`=? ORDER BY "order", rowid`, owner)
	return links, err
}

// append inserts entries into owner starting at order have + 1.
func (t linkTable) append(ctx context.Context, d *db.DB, owner string, have int, entries []newEntry) error {
	now := db.Now()
	for i, e := range entries {
		id, err := ids.New()
		if err != nil {
			return err
		}
		order := int64(have + i + 1)
		if t.table == collectionLinks.table {
			err = d.Insert(ctx, t.table, &db.CollectionBook{ID: id, Order: &order, CreatedAt: now, BookID: &e.mediaID, CollectionID: &owner})
		} else {
			err = d.Insert(ctx, t.table, &db.PlaylistMediaItem{ID: id, MediaItemID: &e.mediaID, MediaItemType: &e.mediaType, Order: &order, CreatedAt: now, PlaylistID: &owner})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// add appends entries not yet in owner, deduplicating within the batch. If
// dupMsg is non-empty, encountering an already-present member returns a 400
// with dupMsg instead of skipping it.
func (t linkTable) add(ctx context.Context, d *db.DB, owner string, entries []newEntry, dupMsg string) (int, error) {
	links, err := t.list(ctx, d, owner)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(links)+len(entries))
	for _, l := range links {
		seen[l.MediaID] = true
	}
	fresh := make([]newEntry, 0, len(entries))
	for _, e := range entries {
		if seen[e.mediaID] {
			if dupMsg != "" {
				return 0, httpx.BadRequest(dupMsg)
			}
			continue
		}
		seen[e.mediaID] = true
		fresh = append(fresh, e)
	}
	if err := t.append(ctx, d, owner, len(links), fresh); err != nil {
		return 0, err
	}
	return len(fresh), nil
}

// renumber gives links the orders 1..n in slice order and reports whether any
// row changed.
func (t linkTable) renumber(ctx context.Context, d *db.DB, links []link) (changed bool, err error) {
	for i, l := range links {
		if l.Order != int64(i+1) {
			if _, err := d.Exec(ctx, `UPDATE `+t.table+` SET "order"=? WHERE id=?`, i+1, l.ID); err != nil {
				return false, err
			}
			changed = true
		}
	}
	return changed, nil
}

// reorder orders the members of owner like wanted (media ids); a member that is
// not wanted ranks as missing (-1 sorts it first, len(wanted) last, as upstream
// does for collections and playlists respectively).
func (t linkTable) reorder(ctx context.Context, d *db.DB, owner string, wanted []string, missing int) (bool, error) {
	links, err := t.list(ctx, d, owner)
	if err != nil {
		return false, err
	}
	rank := func(l link) int {
		if i := slices.Index(wanted, l.MediaID); i >= 0 {
			return i
		}
		return missing
	}
	slices.SortStableFunc(links, func(a, b link) int { return rank(a) - rank(b) })
	return t.renumber(ctx, d, links)
}

// remove deletes the members of owner whose media id is in media, optionally
// renumbers the remaining rows, and returns how many were deleted and remain.
// Call it in a transaction.
func (t linkTable) remove(ctx context.Context, d *db.DB, owner string, media []string, recompact bool) (removed, remaining int, err error) {
	links, err := t.list(ctx, d, owner)
	if err != nil {
		return 0, 0, err
	}
	kept := links[:0:0]
	for _, l := range links {
		if !slices.Contains(media, l.MediaID) {
			kept = append(kept, l)
		} else if _, err := d.Exec(ctx, `DELETE FROM `+t.table+` WHERE id=?`, l.ID); err != nil {
			return 0, 0, err
		}
	}
	if recompact {
		_, err = t.renumber(ctx, d, kept)
	}
	return len(links) - len(kept), len(kept), err
}
