package item

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/wire"
)

// DownloadQueue reports a podcast item's episode downloads: the ones queued
// and the one running now when it belongs to the item.
type DownloadQueue interface {
	EpisodeDownloads(libraryItemID string) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload)
}

// BookFinder is the provider search behind a quick match.
type BookFinder interface {
	SearchBooks(ctx context.Context, query wire.BookSearch) []wire.BookMatch
	SearchPodcasts(ctx context.Context, term string) []wire.PodcastMatch
}

// Deps are the collaborators the item routes use outside this package, each a
// genuine upward call. Filters is required; every other field may be nil or empty.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner.
	Filters *filterdata.Cache
	// Downloads feeds GET /api/items/{id}?include=downloads.
	Downloads DownloadQueue
	// Books searches the metadata providers for a quick match.
	Books BookFinder
	// WriteMetadata rewrites the item's metadata.json sidecar.
	WriteMetadata func(ctx context.Context, itemID string) error
	// ScheduleChanged re-evaluates a podcast's auto-download cron entry.
	ScheduleChanged func(ctx context.Context, itemID string)
	// PodcastEpisodes quick matches a podcast's episodes against its feed and
	// returns how many it updated.
	PodcastEpisodes func(ctx context.Context, itemID string, overrideDetails bool) (int, error)
	// SaveCover downloads a quick-match cover and returns the stored path.
	SaveCover func(ctx context.Context, itemID, itemPath string, withItem bool, imageURL string) (string, error)
	// CoverCacheDir and ItemMetadataDir are cleaned when an item is deleted;
	// an empty ItemMetadataDir leaves item metadata alone.
	CoverCacheDir, ItemMetadataDir string
}

// Handler serves the item read, edit, match and delete routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	deps     Deps
}

// New returns a Handler.
func New(d *db.DB, settings *settings.Store, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: d, settings: settings, emit: emit, deps: deps}
}

// Routes registers the routes. Every route needs an authenticated user; the
// handlers state their further permission checks.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/items/{id}", h.authed(h.get))
	mux.Handle("DELETE /api/items/{id}", h.authed(h.delete))
	mux.Handle("PATCH /api/items/{id}/media", h.authed(h.updateMedia))
	mux.Handle("POST /api/items/{id}/match", h.authed(h.match))
	mux.Handle("POST /api/items/batch/get", h.authed(h.batchGet))
	mux.Handle("POST /api/items/batch/delete", h.authed(h.batchDelete))
	mux.Handle("POST /api/items/batch/update", h.authed(h.batchUpdate))
	mux.Handle("POST /api/items/batch/quickmatch", h.authed(h.batchQuickMatch))
}

type userHandler func(w http.ResponseWriter, r *http.Request, user *auth.Principal) error

// authed runs next for the authenticated principal (401 without one).
func (h *Handler) authed(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user := auth.User(r.Context()); user != nil {
			return next(w, r, user)
		}
		return httpx.Error(http.StatusUnauthorized, "")
	}
}

func (h *Handler) prefixes() []string { return h.settings.Server().SortingPrefixes }

// itemRef is the part of a libraryItems row the write routes work on.
type itemRef struct {
	ID        string  `db:"id"`
	LibraryID string  `db:"libraryId"`
	MediaType string  `db:"mediaType"`
	MediaID   string  `db:"mediaId"`
	Path      string  `db:"path"`
	IsFile    db.Bool `db:"isFile"`
}

const itemRefColumns = `li.id, COALESCE(li.libraryId,'') AS libraryId, COALESCE(li.mediaType,'') AS mediaType, COALESCE(li.mediaId,'') AS mediaId, COALESCE(li.path,'') AS path, li.isFile`

// loadRef reads the item row: 404 when there is none.
func (h *Handler) loadRef(ctx context.Context, id string) (itemRef, error) {
	var ref itemRef
	err := h.db.Get(ctx, &ref, `SELECT `+itemRefColumns+` FROM libraryItems li WHERE li.id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ref, httpx.NotFound()
	}
	if err != nil {
		return ref, httpx.Internal("database read failed", err)
	}
	return ref, nil
}

// refsInQueryOrder reads the existing items among ids in the order Sequelize
// returns them for LibraryItem.findAllExpandedWhere (LibraryItem.js:150-183):
// by the first joined row, ordered by bookAuthors then bookSeries creation
// time. Batch get answers in this order and batch quick match works in it.
func (h *Handler) refsInQueryOrder(ctx context.Context, ids []string) ([]itemRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []itemRef
	err := h.db.Select(ctx, &rows, `SELECT `+itemRefColumns+` FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId AND li.mediaType='book' LEFT JOIN bookAuthors ba ON ba.bookId=b.id LEFT JOIN bookSeries bs ON bs.bookId=b.id WHERE li.id IN (?) ORDER BY ba.createdAt ASC, bs.createdAt ASC`, ids)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	refs := rows[:0]
	for _, row := range rows {
		if !seen[row.ID] {
			seen[row.ID] = true
			refs = append(refs, row)
		}
	}
	return refs, nil
}

// canAccessAll is ensureUserCanAccessLibraryItemsForBatch
// (LibraryItemController.js:48-56): every item must pass the library, explicit
// content and tag rules.
func (h *Handler) canAccessAll(ctx context.Context, user *auth.Principal, refs []itemRef) (bool, error) {
	for _, ref := range refs {
		_, exists, allowed, err := user.ReadItem(ctx, h.db, ref.ID)
		if err != nil {
			return false, err
		}
		if !exists || !allowed {
			return false, nil
		}
	}
	return true, nil
}
