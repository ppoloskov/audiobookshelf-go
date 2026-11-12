package browse

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"

	"audiobookshelf-compat/internal/author"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
)

// shelf is one home-page shelf of GET /api/libraries/{id}/personalized
// (LibraryItem.getPersonalizedShelves, server/models/LibraryItem.js:384-491).
// Entities is []item.Minified, []seriesShelfEntry or []author.View by shelf
// type; Count is its length (a shelf without entities is left out).
type shelf struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	LabelStringKey string `json:"labelStringKey"`
	Type           string `json:"type"`
	Entities       any    `json:"entities"`
	Total          int    `json:"total"`
	Count          int    `json:"-"`
}

// seriesShelfEntry is an entity of the recent-series shelf: the series, its
// feed and its visible books, whose author and series names are blank.
type seriesShelfEntry struct {
	seriesView
	RSSFeed *feedMinified   `json:"rssFeed,omitempty"`
	Books   []item.Minified `json:"books"`
}

// shelfEnv is what every shelf of one request shares.
type shelfEnv struct {
	h        *Handler
	ctx      context.Context
	s        scope
	limit    int
	perm     string // the user's permission clause on the media alias, with its arguments
	permArgs []any
	since    db.Time // the cutoff of "recently added"
	prefixes []string
	extras   itemExtras
}

type shelfDef struct {
	id, label, key, typ string
	run                 func(*shelfEnv) (entities any, count, total int, err error)
}

func bookShelves() []shelfDef {
	return []shelfDef{
		{"continue-listening", "Continue Listening", "LabelContinueListening", "book", progressShelf(`mp.hideFromContinueListening=0 AND `+mpInProgress, false)},
		{"continue-reading", "Continue Reading", "LabelContinueReading", "book", progressShelf(`mp.hideFromContinueListening=0 AND `+mpInProgress, true)},
		{"continue-series", "Continue Series", "LabelContinueSeries", "book", (*shelfEnv).continueSeries},
		{"recently-added", "Recently Added", "LabelRecentlyAdded", "book", (*shelfEnv).recentlyAdded},
		{"recent-series", "Recent Series", "LabelRecentSeries", "series", (*shelfEnv).recentSeries},
		{"discover", "Discover", "LabelDiscover", "book", (*shelfEnv).discover},
		{"listen-again", "Listen Again", "LabelListenAgain", "book", progressShelf(mpFinished, false)},
		{"read-again", "Read Again", "LabelReadAgain", "book", progressShelf(mpFinished, true)},
		{"newest-authors", "Newest Authors", "LabelNewestAuthors", "authors", (*shelfEnv).newestAuthors},
	}
}

func podcastShelves() []shelfDef {
	return []shelfDef{
		{"continue-listening", "Continue Listening", "LabelContinueListening", "episode", episodeShelf(true, mpAudio+` AND mp.hideFromContinueListening=0`)},
		{"newest-episodes", "Newest Episodes", "LabelNewestEpisodes", "episode", episodeShelf(false, `ep.createdAt>=?`)},
		{"recently-added", "Recently Added", "LabelRecentlyAdded", "podcast", (*shelfEnv).recentPodcasts},
		{"listen-again", "Listen Again", "LabelListenAgain", "episode", episodeShelf(true, mpFinished)},
	}
}

// personalized is GET /api/libraries/{id}/personalized.
func (h *Handler) personalized(w http.ResponseWriter, r *http.Request, s scope) error {
	e := &shelfEnv{h: h, ctx: r.Context(), s: s, limit: cmp.Or(s.limit, 10), // `req.query.limit || 10` (LibraryController.js:891)
		since: db.FromMillis(time.Now().Add(-recentWindow).UnixMilli()), prefixes: h.settings.Server().SortingPrefixes,
		extras: extrasOf(includes(r), isAdmin(s.user))}
	var defs []shelfDef
	switch db.Val(s.lib.MediaType) {
	case "book":
		defs = bookShelves()
		e.perm, e.permArgs = s.user.MediaClause("b")
	case "podcast":
		defs = podcastShelves()
		e.perm, e.permArgs = s.user.MediaClause("p")
	default:
		return httpx.Error(http.StatusNotFound, "Library not found")
	}
	shelves := []shelf{}
	for _, def := range defs {
		entities, count, total, err := def.run(e)
		if err != nil {
			return httpx.Internal("database read failed", err)
		}
		if count > 0 {
			shelves = append(shelves, shelf{ID: def.id, Label: def.label, LabelStringKey: def.key, Type: def.typ, Entities: entities, Total: total, Count: count})
		}
	}
	return httpx.JSON(w, r, shelves)
}

// pick runs an item shelf query: its total, then the first limit ids. args are
// the arguments of query without the permission ones, which follow them.
func (e *shelfEnv) pick(query string, args ...any) (ids []string, total int, err error) {
	args = append(args, e.permArgs...)
	if err = e.h.db.Get(e.ctx, &total, "SELECT COUNT(*) FROM ("+query+")", args...); err != nil {
		return nil, 0, err
	}
	err = e.h.db.Select(e.ctx, &ids, query+` LIMIT ?`, append(args, e.limit)...)
	return ids, total, err
}

func (e *shelfEnv) load(ids []string, opt item.Options) ([]item.Item, error) {
	loaded, err := item.Load(e.ctx, e.h.db, ids, opt)
	if err == nil && len(loaded) != len(ids) {
		err = sql.ErrNoRows
	}
	return loaded, err
}

// items loads and renders ids as minified rows with the includes in want.
func (e *shelfEnv) items(ids []string, want itemExtras) ([]item.Item, []item.Minified, error) {
	loaded, err := e.load(ids, item.Options{})
	if err != nil {
		return nil, nil, err
	}
	rows := minify(loaded, e.prefixes)
	return loaded, rows, e.h.attach(e.ctx, e.s.user.ID, want, loaded, rows)
}

// itemShelf is a shelf of the items query selects.
func (e *shelfEnv) itemShelf(want itemExtras, query string, args ...any) (any, int, int, error) {
	ids, total, err := e.pick(query, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	_, rows, err := e.items(ids, want)
	return rows, len(rows), total, err
}

// progressShelf is a shelf of the user's books that match where, newest progress
// first. Books with audio and ebooks without audio (reading) are separate
// shelves of the same query, so the limit applies before the split.
func progressShelf(where string, reading bool) func(*shelfEnv) (any, int, int, error) {
	return func(e *shelfEnv) (any, int, int, error) {
		ids, total, err := e.pick(`SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN mediaProgresses mp ON mp.mediaItemId=b.id
			WHERE li.libraryId=? AND li.mediaType='book' AND mp.userId=? AND (`+where+`)`+e.perm+` ORDER BY mp.updatedAt DESC`, e.s.lib.ID, e.s.user.ID)
		if err != nil {
			return nil, 0, 0, err
		}
		_, rows, err := e.items(ids, e.extras)
		if err != nil {
			return nil, 0, 0, err
		}
		picked := []item.Minified{}
		for _, row := range rows {
			media, _ := row.Media.(item.BookMinified)
			if (!reading && media.NumTracks > 0) || (reading && media.NumTracks <= 0 && media.EbookFormat != nil) {
				picked = append(picked, row)
			}
		}
		return picked, len(picked), total, nil
	}
}

func (e *shelfEnv) recentlyAdded() (any, int, int, error) {
	return e.itemShelf(e.extras, `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType='book' AND li.createdAt>=?`+e.perm+` ORDER BY li.createdAt DESC`, e.s.lib.ID, e.since)
}

// maxFinishedSequence is the highest sequence the user finished in outer, a
// series id expression.
func maxFinishedSequence(outer string) string {
	return `(SELECT CAST(max(bs2.sequence) as FLOAT) FROM bookSeries bs2, mediaProgresses mp2 WHERE mp2.mediaItemId = bs2.bookId AND mp2.isFinished = 1 AND mp2.userId = ? AND bs2.seriesId = ` + outer + `)`
}

// continueSeries is getContinueSeriesLibraryItems (libraryItemsBookFilters.js:704-836):
// of each series the user has finished a book of and not started another, the
// next unfinished book. Only feeds are attached, never shares.
func (e *shelfEnv) continueSeries() (any, int, int, error) {
	user, later := e.s.user, db.Val(e.s.lib.Settings.V.OnlyShowLaterBooksInContinueSeries)
	var hidden []string
	if extra := user.ExtraData.V; extra != nil {
		hidden = extra.SeriesHideFromContinueListening
	}
	where, args := ` FROM series s WHERE s.libraryId = ?`, []any{e.s.lib.ID}
	if len(hidden) > 0 {
		where, args = where+` AND s.id NOT IN (?)`, append(args, hidden)
	}
	where += ` AND (SELECT count(*) FROM mediaProgresses mp, bookSeries bs WHERE bs.seriesId = s.id AND mp.mediaItemId = bs.bookId AND mp.userId = ? AND mp.isFinished = 1) >= 1`
	args = append(args, user.ID)
	unfinished := `(SELECT count(*) FROM bookSeries bs LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = bs.bookId AND mp.userId = ? WHERE bs.seriesId = s.id AND (mp.isFinished = 0 OR mp.isFinished IS NULL)`
	args = append(args, user.ID)
	if later {
		unfinished += ` AND CAST(bs.sequence as FLOAT) > ` + maxFinishedSequence("s.id")
		args = append(args, user.ID)
	}
	where += ` AND ` + unfinished + `) >= 1 AND (SELECT count(*) FROM mediaProgresses mp, bookSeries bs WHERE mp.mediaItemId = bs.bookId AND mp.userId = ? AND bs.seriesId = s.id AND mp.isFinished = 0 AND mp.currentTime > 0) = 0`
	args = append(args, user.ID)
	var total int
	if err := e.h.db.Get(e.ctx, &total, `SELECT count(*)`+where, args...); err != nil {
		return nil, 0, 0, err
	}
	var series []struct {
		ID   string  `db:"id"`
		Name *string `db:"name"`
	}
	err := e.h.db.Select(e.ctx, &series, `SELECT s.id, s.name`+where+` ORDER BY (SELECT max(mp.updatedAt) FROM bookSeries bs, mediaProgresses mp WHERE mp.mediaItemId = bs.bookId AND mp.userId = ? AND bs.seriesId = s.id) DESC LIMIT ?`,
		append(args, user.ID, e.limit)...)
	if err != nil || len(series) == 0 {
		return nil, 0, total, err
	}
	seriesIDs := make([]string, len(series))
	for i, s := range series {
		seriesIDs[i] = s.ID
	}
	// The unfinished books of these series, next first.
	candidateArgs := []any{user.ID, seriesIDs}
	candidateWhere := ""
	if later {
		candidateWhere, candidateArgs = ` AND CAST(bs.sequence AS FLOAT) > `+maxFinishedSequence("bs.seriesId"), append(candidateArgs, user.ID)
	}
	var candidates []seriesBook
	err = e.h.db.Select(e.ctx, &candidates, `SELECT bs.seriesId AS seriesId, li.id AS itemId, b.id AS bookId, bs.sequence AS sequence
		FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
		LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = b.id AND mp.userId = ?
		WHERE bs.seriesId IN (?) AND (mp.isFinished IS NULL OR mp.isFinished = 0)`+candidateWhere+e.perm+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, bs.rowid`,
		append(candidateArgs, e.permArgs...)...)
	if err != nil {
		return nil, 0, 0, err
	}
	var ids []string
	var chosen []item.SeriesRef
	for _, s := range series {
		if i := slices.IndexFunc(candidates, func(c seriesBook) bool { return c.SeriesID == s.ID }); i >= 0 {
			ids = append(ids, candidates[i].ItemID)
			chosen = append(chosen, item.SeriesRef{ID: s.ID, Name: db.Val(s.Name), Sequence: candidates[i].Sequence})
		}
	}
	_, rows, err := e.items(ids, itemExtras{rssFeed: e.extras.rssFeed})
	for i := range rows {
		if media, ok := rows[i].Media.(item.BookMinified); ok {
			// book.series is emptied before minifying, then metadata.series is the continued series.
			media.Metadata.SeriesName, media.Metadata.Series = "", chosen[i]
			rows[i].Media = media
		}
	}
	return rows, len(rows), total, err
}

// recentSeries is getSeriesMostRecentlyAdded (libraryFilters.js:207-317);
// upstream asks for 5.
func (e *shelfEnv) recentSeries() (any, int, int, error) {
	where, args := ` FROM series s WHERE s.libraryId = ? AND s.createdAt >= ?`, []any{e.s.lib.ID, e.since}
	if db.Val(e.s.lib.Settings.V.HideSingleBookSeries) {
		where += ` AND ` + seriesBookCount + ` > 1`
	}
	if e.perm != "" {
		where, args = where+` AND (SELECT count(*) FROM books b, bookSeries bs WHERE bs.seriesId = s.id AND bs.bookId = b.id`+e.perm+`) > 0`, append(args, e.permArgs...)
	}
	var total int
	if err := e.h.db.Get(e.ctx, &total, `SELECT count(*)`+where, args...); err != nil {
		return nil, 0, 0, err
	}
	var rows []db.Series
	if err := e.h.db.Select(e.ctx, &rows, `SELECT s.*`+where+` ORDER BY s.createdAt DESC LIMIT 5`, args...); err != nil {
		return nil, 0, 0, err
	}
	if len(rows) == 0 {
		return nil, 0, total, nil
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	linked, err := e.h.seriesBooks(e.ctx, e.s.user, orderLinked, ids)
	if err != nil {
		return nil, 0, 0, err
	}
	var feeds map[string]*feedMinified
	if e.extras.rssFeed {
		if feeds, err = e.h.feedsOf(e.ctx, "", ids); err != nil {
			return nil, 0, 0, err
		}
	}
	itemIDs := make([]string, len(linked))
	for i, b := range linked {
		itemIDs[i] = b.ItemID
	}
	loaded, err := e.load(slices.Compact(slices.Sorted(slices.Values(itemIDs))), item.Options{})
	if err != nil {
		return nil, 0, 0, err
	}
	byID := make(map[string]item.Minified, len(loaded))
	for _, it := range loaded {
		row := it.Minified(e.prefixes)
		media := row.Media.(item.BookMinified)
		media.Metadata.AuthorName, media.Metadata.AuthorNameLF, media.Metadata.SeriesName = "", "", ""
		row.Media = media
		byID[it.ID] = row
	}
	bookIDs := bookIDsBySequence(linked)
	entries := make([]seriesShelfEntry, len(rows))
	for i, row := range rows {
		entries[i] = seriesShelfEntry{seriesView: newSeriesView(row, e.prefixes), RSSFeed: feeds[row.ID], Books: []item.Minified{}}
		for _, id := range bookIDs[row.ID] {
			entries[i].Books = append(entries[i].Books, byID[id])
		}
	}
	return entries, len(entries), total, nil
}

// discover is getDiscoverLibraryItems (libraryItemsBookFilters.js:850-1010):
// random books the user has not started, taking only the first book of
// not-started series. Only feeds are attached.
func (e *shelfEnv) discover() (any, int, int, error) {
	var seriesIDs []string
	err := e.h.db.Select(e.ctx, &seriesIDs, `SELECT s.id FROM series s WHERE s.libraryId = ? AND (SELECT count(*) FROM bookSeries bs LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = bs.bookId WHERE bs.seriesId = s.id AND mp.userId = ? AND (mp.isFinished = 1 OR mp.currentTime > 0)) = 0 ORDER BY RANDOM() LIMIT ?`, e.s.lib.ID, e.s.user.ID, e.limit)
	if err != nil {
		return nil, 0, 0, err
	}
	var firstBooks []string
	if len(seriesIDs) > 0 {
		var books []seriesBook
		books, err = e.h.seriesBooks(e.ctx, e.s.user, orderSequence, seriesIDs)
		if err != nil {
			return nil, 0, 0, err
		}
		seen := map[string]bool{}
		for _, b := range books {
			if !seen[b.SeriesID] {
				seen[b.SeriesID] = true
				firstBooks = append(firstBooks, b.BookID)
			}
		}
	}
	seriesClause := `(SELECT COUNT(*) FROM bookSeries bs WHERE bs.bookId = b.id) = 0`
	args := []any{e.s.lib.ID, e.s.user.ID}
	if len(firstBooks) > 0 {
		seriesClause, args = `(`+seriesClause+` OR b.id IN (?))`, append(args, firstBooks)
	}
	from := ` FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' AND li.libraryId = ?
		LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = b.id AND mp.userId = ?
		WHERE (mp.isFinished IS NULL OR mp.isFinished = 0) AND (mp.currentTime IS NULL OR mp.currentTime = 0) AND ` + seriesClause + e.perm
	args = append(args, e.permArgs...)
	var total int
	if err := e.h.db.Get(e.ctx, &total, `SELECT count(DISTINCT b.id)`+from, args...); err != nil {
		return nil, 0, 0, err
	}
	var ids []string
	if err := e.h.db.Select(e.ctx, &ids, `SELECT li.id`+from+` GROUP BY b.id ORDER BY RANDOM() LIMIT ?`, append(args, e.limit)...); err != nil {
		return nil, 0, 0, err
	}
	_, rows, err := e.items(ids, itemExtras{rssFeed: e.extras.rssFeed})
	return rows, len(rows), total, err
}

// newestAuthors is getNewestAuthors (libraryFilters.js:334-368).
func (e *shelfEnv) newestAuthors() (any, int, int, error) {
	books := `(SELECT count(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id = ba.bookId WHERE ba.authorId = a.id` + e.perm + `)`
	where := ` FROM authors a WHERE a.libraryId = ? AND a.createdAt >= ? AND ` + books + ` > 0`
	whereArgs := append([]any{e.s.lib.ID, e.since}, e.permArgs...)
	var total int
	if err := e.h.db.Get(e.ctx, &total, `SELECT count(*)`+where, whereArgs...); err != nil {
		return nil, 0, 0, err
	}
	var rows []struct {
		db.Author
		NumBooks int `db:"numBooks"`
	}
	if err := e.h.db.Select(e.ctx, &rows, `SELECT a.*, `+books+` AS numBooks`+where+` ORDER BY a.createdAt DESC LIMIT ?`, append(append(append([]any{}, e.permArgs...), whereArgs...), e.limit)...); err != nil {
		return nil, 0, 0, err
	}
	views := make([]author.View, len(rows))
	for i, row := range rows {
		views[i] = authorView(row.Author, row.NumBooks)
	}
	return views, len(views), total, nil
}

// authorView is Author.toOldJSONExpanded(numBooks).
func authorView(a db.Author, numBooks int) author.View {
	return author.View{ID: a.ID, ASIN: a.ASIN, Name: a.Name, Description: a.Description, ImagePath: a.ImagePath, LibraryID: db.Val(a.LibraryID),
		AddedAt: a.CreatedAt.Millis(), UpdatedAt: a.UpdatedAt.Millis(), NumBooks: &numBooks}
}

// episodeShelf is a shelf of podcast episodes that match where, each rendered as
// its podcast with recentEpisode. With progress it joins the user's progress
// and orders by it; otherwise episodes are newest first and where takes the
// recent cutoff.
func episodeShelf(progress bool, where string) func(*shelfEnv) (any, int, int, error) {
	return func(e *shelfEnv) (any, int, int, error) {
		from, order, args := ` FROM podcastEpisodes ep JOIN podcasts p ON p.id=ep.podcastId JOIN libraryItems li ON li.mediaId=p.id`, "ep.createdAt", []any{}
		if progress {
			from, order, args = from+` JOIN mediaProgresses mp ON mp.mediaItemId=ep.id AND mp.userId=?`, "mp.updatedAt", append(args, e.s.user.ID)
		}
		from += ` WHERE li.libraryId=? AND li.mediaType='podcast' AND (` + where + `)` + e.perm
		args = append(args, e.s.lib.ID)
		if !progress {
			args = append(args, e.since)
		}
		args = append(args, e.permArgs...)
		var total int
		if err := e.h.db.Get(e.ctx, &total, `SELECT COUNT(DISTINCT ep.id)`+from, args...); err != nil {
			return nil, 0, 0, err
		}
		var picked []struct {
			ItemID    string `db:"itemId"`
			EpisodeID string `db:"episodeId"`
		}
		if err := e.h.db.Select(e.ctx, &picked, `SELECT li.id AS itemId, ep.id AS episodeId`+from+` ORDER BY `+order+` DESC LIMIT ?`, append(args, e.limit)...); err != nil {
			return nil, 0, 0, err
		}
		ids, episodeIDs := make([]string, len(picked)), make([]string, len(picked))
		for i, p := range picked {
			ids[i], episodeIDs[i] = p.ItemID, p.EpisodeID
		}
		loaded, err := e.load(ids, item.Options{Episodes: true, EpisodeIDs: episodeIDs})
		if err != nil {
			return nil, 0, 0, err
		}
		rows := minify(loaded, e.prefixes)
		for i, it := range loaded {
			for _, episode := range it.Podcast.Episodes {
				if episode.ID == episodeIDs[i] {
					rows[i].RecentEpisode = episode.Full(it.ID)
				}
			}
		}
		return rows, len(rows), total, nil
	}
}

// recentPodcasts is getLibraryItemsMostRecentlyAdded's podcast branch
// (libraryFilters.js:113-128). The row carries the item's size.
func (e *shelfEnv) recentPodcasts() (any, int, int, error) {
	ids, total, err := e.pick(`SELECT li.id FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=? AND li.createdAt>=?`+e.perm+` ORDER BY li.createdAt DESC`, e.s.lib.ID, e.since)
	if err != nil {
		return nil, 0, 0, err
	}
	_, rows, err := e.items(ids, e.extras)
	for i := range rows {
		if media, ok := rows[i].Media.(item.PodcastMinified); ok && media.Size == 0 {
			media.Size = db.Val(rows[i].Size)
			rows[i].Media = media
		}
	}
	return rows, len(rows), total, err
}
