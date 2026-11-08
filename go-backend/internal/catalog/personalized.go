package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"cmp"
	"database/sql"
	"net/http"
)

// A shelf is a definition: its identity and one query. personalized runs the
// queries of a library's shelves, loads every item they name with a single
// libitem.Load, and renders the shelves in definition order. A shelf without
// entities is omitted, as in the reference controller
// (LibraryItem.getPersonalizedShelves, server/models/LibraryItem.js:384-491).
type shelfDef struct {
	id, label, key, typ string
	query               func(*shelfEnv) (shelfQuery, error)
}

// shelfQuery is what a shelf shows: the library items it needs loaded, its
// total, and how it renders them. build gets those items in ids order.
type shelfQuery struct {
	ids   []string
	total int
	build func([]shelfItem) ([]any, error)
}

// shelfItem is a loaded item and its shelf rendering.
type shelfItem struct {
	min libitem.Minified
	src libitem.Item
}

// shelfEnv is the request every shelf query shares.
type shelfEnv struct {
	h         *Handler
	r         *http.Request
	user      store.User
	libraryID string
	settings  librarySettings
	limit     int
	podcast   bool
	perm      string // the user's media permission clause, with its arguments
	permArgs  []any
	memo      map[string]shelfQuery
	episodes  []string // episode ids the episode shelves show; only these are loaded
}

var bookShelves = []shelfDef{
	{"continue-listening", "Continue Listening", "LabelContinueListening", "book", progressBooks(`mp.hideFromContinueListening=0 AND mp.isFinished=0 AND (mp.currentTime>0 OR mp.ebookProgress>0)`, false)},
	{"continue-reading", "Continue Reading", "LabelContinueReading", "book", progressBooks(`mp.hideFromContinueListening=0 AND mp.isFinished=0 AND (mp.currentTime>0 OR mp.ebookProgress>0)`, true)},
	{"continue-series", "Continue Series", "LabelContinueSeries", "book", (*shelfEnv).continueSeries},
	{"recently-added", "Recently Added", "LabelRecentlyAdded", "book", func(e *shelfEnv) (shelfQuery, error) {
		return e.itemQuery(`SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType='book' AND li.createdAt>=?`+e.perm+` ORDER BY li.createdAt DESC`, e.libraryID, sixtyDaysAgo())
	}},
	{"recent-series", "Recent Series", "LabelRecentSeries", "series", (*shelfEnv).recentSeries},
	{"discover", "Discover", "LabelDiscover", "book", (*shelfEnv).discover},
	{"listen-again", "Listen Again", "LabelListenAgain", "book", progressBooks(`mp.isFinished=1`, false)},
	{"read-again", "Read Again", "LabelReadAgain", "book", progressBooks(`mp.isFinished=1`, true)},
	{"newest-authors", "Newest Authors", "LabelNewestAuthors", "authors", (*shelfEnv).newestAuthors},
}

var podcastShelves = []shelfDef{
	{"continue-listening", "Continue Listening", "LabelContinueListening", "episode", episodeShelf(true, `mp.isFinished=0 AND mp.currentTime>0 AND mp.hideFromContinueListening=0`)},
	{"newest-episodes", "Newest Episodes", "LabelNewestEpisodes", "episode", episodeShelf(false, `ep.createdAt>=?`)},
	{"recently-added", "Recently Added", "LabelRecentlyAdded", "podcast", (*shelfEnv).recentPodcasts},
	{"listen-again", "Listen Again", "LabelListenAgain", "episode", episodeShelf(true, `mp.isFinished=1`)},
}

func (h *Handler) personalized(w http.ResponseWriter, r *http.Request, lib libraryScope) error {
	user, libraryID, mediaType := lib.user, lib.id, lib.mediaType
	limit := cmp.Or(lib.limit, 10) // `req.query.limit || 10` (LibraryController.js:891)
	e := &shelfEnv{h: h, r: r, user: user, libraryID: libraryID, settings: lib.settings, limit: limit, podcast: mediaType == "podcast", memo: map[string]shelfQuery{}, episodes: []string{}}
	defs := bookShelves
	switch mediaType {
	case "book":
		e.perm, e.permArgs = access.For(r.Context(), user).MediaClause("b")
	case "podcast":
		defs = podcastShelves
		e.perm, e.permArgs = access.For(r.Context(), user).MediaClause("p")
	default:
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	queries := make([]shelfQuery, len(defs))
	var ids []string
	for i, def := range defs {
		q, err := def.query(e)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		queries[i] = q
		ids = append(ids, q.ids...)
	}
	items, err := e.load(ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	shelves := make([]shelf, 0, len(defs))
	for i, def := range defs {
		q := queries[i]
		entities, err := q.build(items[:len(q.ids)])
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		items = items[len(q.ids):]
		if len(entities) > 0 {
			shelves = append(shelves, shelf{ID: def.id, Label: def.label, LabelStringKey: def.key, Type: def.typ, Entities: entities, Total: q.total})
		}
	}
	httputil.WriteJSON(w, http.StatusOK, shelves, httputil.ForRequest(r))
	return nil
}

// load renders ids in the shelf shape in id order; a missing id is
// sql.ErrNoRows. Podcast libraries load the episode shelves' episodes too.
func (e *shelfEnv) load(ids []string) ([]shelfItem, error) {
	loaded, err := libitem.Load(e.r.Context(), e.h.db, ids, libitem.Options{Episodes: e.podcast, EpisodeIDs: e.episodes})
	if err != nil {
		return nil, err
	}
	if len(loaded) != len(ids) {
		return nil, sql.ErrNoRows
	}
	prefixes := e.h.sortingPrefixes(e.r.Context())
	out := make([]shelfItem, len(loaded))
	for i, it := range loaded {
		out[i] = shelfItem{it.Minified(prefixes), it}
	}
	return out, nil
}

// itemQuery is a shelf of library items: query selects their ids in shelf
// order, and its arguments are args followed by the permission arguments.
// Shelves that differ only in how they render share one query by its text.
func (e *shelfEnv) itemQuery(query string, args ...any) (shelfQuery, error) {
	if q, ok := e.memo[query]; ok {
		return q, nil
	}
	ctx := e.r.Context()
	args = append(args, e.permArgs...)
	q := shelfQuery{build: e.attach(requestItemIncludes(e.r, e.user))}
	if err := e.h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+query+")", args...).Scan(&q.total); err != nil {
		return q, err
	}
	var err error
	q.ids, err = e.h.queryStrings(ctx, query+` LIMIT ?`, append(args, e.limit)...)
	e.memo[query] = q
	return q, err
}

// attach renders items with their include= entities.
func (e *shelfEnv) attach(inc itemIncludes) func([]shelfItem) ([]any, error) {
	return func(items []shelfItem) ([]any, error) {
		out := make([]any, len(items))
		for i := range items {
			item := items[i].min
			if err := e.h.attachItemIncludes(e.r.Context(), inc, item.Head, item.MediaID(), &item.Extras); err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	}
}

// progressBooks is a shelf of the user's books that match where, most recent
// progress first. Audiobooks and ebooks without audio (reading) are separate
// shelves of the same query.
func progressBooks(where string, reading bool) func(*shelfEnv) (shelfQuery, error) {
	return func(e *shelfEnv) (shelfQuery, error) {
		q, err := e.itemQuery(`SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN mediaProgresses mp ON mp.mediaItemId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND mp.userId=? AND `+where+e.perm+` ORDER BY mp.updatedAt DESC`, e.libraryID, e.user.ID)
		attach := q.build
		q.build = func(items []shelfItem) ([]any, error) {
			var picked []shelfItem
			for _, item := range items {
				media, _ := item.min.Media.(libitem.BookMinified)
				hasEbook := len(media.EbookFormat) > 0 && string(media.EbookFormat) != "null"
				if (!reading && media.NumTracks > 0) || (reading && media.NumTracks <= 0 && hasEbook) {
					picked = append(picked, item)
				}
			}
			return attach(picked)
		}
		return q, err
	}
}

// episodeShelf is a shelf of podcast episodes that match where, each rendered
// as its podcast with recentEpisode. progress joins the user's progress and
// orders by it; otherwise episodes are newest first and where takes the
// recent cutoff.
func episodeShelf(progress bool, where string) func(*shelfEnv) (shelfQuery, error) {
	return func(e *shelfEnv) (shelfQuery, error) {
		from, order, args := ` FROM podcastEpisodes ep JOIN podcasts p ON p.id=ep.podcastId JOIN libraryItems li ON li.mediaId=p.id`, "ep.createdAt", []any{}
		if progress {
			from, order, args = from+` JOIN mediaProgresses mp ON mp.mediaItemId=ep.id AND mp.userId=?`, "mp.updatedAt", append(args, e.user.ID)
		}
		from += ` WHERE li.libraryId=? AND li.mediaType='podcast' AND ` + where + e.perm
		args = append(args, e.libraryID)
		if !progress {
			args = append(args, sixtyDaysAgo())
		}
		args = append(args, e.permArgs...)
		var q shelfQuery
		ctx := e.r.Context()
		if err := e.h.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ep.id)`+from, args...).Scan(&q.total); err != nil {
			return q, err
		}
		rows, err := e.h.db.QueryContext(ctx, `SELECT li.id,ep.id`+from+` ORDER BY `+order+` DESC LIMIT ?`, append(args, e.limit)...)
		if err != nil {
			return q, err
		}
		defer rows.Close()
		var episodeIDs []string
		for rows.Next() {
			var itemID, episodeID string
			if err := rows.Scan(&itemID, &episodeID); err != nil {
				return q, err
			}
			q.ids, episodeIDs = append(q.ids, itemID), append(episodeIDs, episodeID)
		}
		e.episodes = append(e.episodes, episodeIDs...)
		q.build = func(items []shelfItem) ([]any, error) {
			out := make([]any, len(items))
			for i, item := range items {
				for _, episode := range item.src.Podcast.Episodes {
					if episode.ID == episodeIDs[i] {
						item.min.RecentEpisode = episode.JSON(item.src.ID)
					}
				}
				out[i] = item.min
			}
			return out, nil
		}
		return q, rows.Err()
	}
}

// recentPodcasts is LibraryFilters.getLibraryItemsMostRecentlyAdded's podcast
// branch (server/utils/queries/libraryFilters.js:113-128).
func (e *shelfEnv) recentPodcasts() (shelfQuery, error) {
	q, err := e.itemQuery(`SELECT li.id FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+e.perm+` ORDER BY li.createdAt DESC`, e.libraryID, sixtyDaysAgo())
	attach := q.build
	q.build = func(items []shelfItem) ([]any, error) {
		out, err := attach(items)
		for i := range out {
			item := out[i].(libitem.Minified)
			media, _ := item.Media.(libitem.PodcastMedia)
			if item.Size != nil && *item.Size != 0 && (media.Size == nil || *media.Size == 0) {
				size := *item.Size
				media.Size = &size
				item.Media = media
			}
			if err == nil && includeHas(e.r, "numepisodesincomplete") {
				item.NumEpisodesIncomplete, err = e.h.numEpisodesIncomplete(e.r.Context(), e.user.ID, media.ID)
			}
			out[i] = item
		}
		return out, err
	}
	return q, err
}
