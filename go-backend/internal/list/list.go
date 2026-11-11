// Package list serves collections (shared, per library, books only) and
// playlists (per user, books or podcast episodes).
package list

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
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

// remove deletes the members of owner whose media id is in media, renumbers
// the rest and returns how many were deleted and remain. Call it in a transaction.
func (t linkTable) remove(ctx context.Context, d *db.DB, owner string, media []string) (removed, remaining int, err error) {
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
	_, err = t.renumber(ctx, d, kept)
	return len(links) - len(kept), len(kept), err
}
