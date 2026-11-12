package browse

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/text"
)

// seriesView is Series.toOldJSON (server/models/Series.js:168-178). The
// prefix-free name is computed from the name, not read from the column, which a
// rename (PATCH) leaves unchanged.
type seriesView struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Description      *string `json:"description"`
	AddedAt          int64   `json:"addedAt"`
	UpdatedAt        int64   `json:"updatedAt"`
	LibraryID        string  `json:"libraryId"`
}

func newSeriesView(s db.Series, prefixes []string) seriesView {
	v := seriesView{ID: s.ID, Name: s.Name, Description: s.Description, AddedAt: s.CreatedAt.Millis(), UpdatedAt: s.UpdatedAt.Millis(), LibraryID: db.Val(s.LibraryID)}
	if s.Name != nil {
		v.NameIgnorePrefix = db.Ptr(text.PrefixAtEnd(*s.Name, prefixes))
	}
	return v
}

// seriesBook is a book of a series that the user may see.
type seriesBook struct {
	SeriesID string  `db:"seriesId"`
	ItemID   string  `db:"itemId"`
	BookID   string  `db:"bookId"`
	Sequence *string `db:"sequence"`
}

// Orders of seriesBooks: by link creation, or by sequence as a number with
// books without one last (getLibraryItemsForSeries, libraryItemsBookFilters.js:1079).
const (
	orderLinked   = `bs.rowid`
	orderSequence = `CAST(bs.sequence AS FLOAT) ASC NULLS LAST, li.rowid`
)

// seriesBooks lists the books of the series that the user may see, in the given
// SQL order. It is the one query for "the books of a series with the
// permission clause".
func (h *Handler) seriesBooks(ctx context.Context, user *auth.Principal, order string, seriesIDs []string) ([]seriesBook, error) {
	clause, args := user.MediaClause("b")
	var rows []seriesBook
	err := h.db.Select(ctx, &rows, `SELECT bs.seriesId AS seriesId, li.id AS itemId, b.id AS bookId, bs.sequence AS sequence
		FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book'
		WHERE bs.seriesId IN (?)`+clause+` ORDER BY `+order, append([]any{seriesIDs}, args...)...)
	return rows, err
}

// bookIDsBySequence groups rows by series and orders each group by sequence
// (natural order, none last), keeping the row order of ties.
func bookIDsBySequence(rows []seriesBook) map[string][]string {
	sort.SliceStable(rows, func(i, j int) bool {
		return text.SequenceLess(db.Val(rows[i].Sequence), db.Val(rows[j].Sequence))
	})
	groups := map[string][]string{}
	for _, row := range rows {
		groups[row.SeriesID] = append(groups[row.SeriesID], row.ItemID)
	}
	return groups
}

// seriesListEntry is one result of GET /api/libraries/{id}/series.
type seriesListEntry struct {
	seriesView
	TotalDuration float64         `json:"totalDuration,omitempty"`
	Books         []item.Minified `json:"books"`
	RSSFeed       *feedMinified   `json:"rssFeed,omitempty"`
}

type seriesPage struct {
	Results  []seriesListEntry `json:"results"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Page     int               `json:"page"`
	SortBy   string            `json:"sortBy,omitempty"`
	SortDesc bool              `json:"sortDesc"`
	FilterBy string            `json:"filterBy,omitempty"`
	Minified bool              `json:"minified"`
	Include  string            `json:"include"`
}

// Series sort keys. A podcast library has no series rows, so the same query
// yields the empty list.
const (
	seriesBookCount = `(SELECT COUNT(*) FROM bookSeries bs WHERE bs.seriesId=s.id)`
	seriesDuration  = `(SELECT COALESCE(SUM(b.duration),0) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id)`
	seriesLastAdded = `(SELECT MAX(b.createdAt) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id)`
)

// seriesFilter turns the series filter into clauses on the books b of a
// series: has is for "the series has a visible book where", none for "the
// series has no book where". An unknown group or value filters nothing.
func seriesFilter(filter string, user *auth.Principal) (has string, hasArgs []any, none string, noneArgs []any, err error) {
	group, value, hasValue := strings.Cut(filter, ".")
	if !hasValue {
		return "", nil, "", nil, nil
	}
	switch group {
	case "genres", "tags", "narrators", "authors", "publishers", "languages":
		has, hasArgs, err = filterSQL(filter, false, user)
	case "progress":
		switch decodeValue(value) {
		case "not-finished", "in-progress":
			has, hasArgs, err = filterSQL(filter, false, user)
		case "finished": // no book is unfinished (seriesFilters.js:83)
			none, noneArgs, err = filterSQL("progress."+encodeValue("not-finished"), false, user)
		case "not-started": // no book is started (seriesFilters.js:87)
			none, noneArgs, err = bind(hasProgress(false, mpStarted), "", user)
		}
	}
	return has, hasArgs, none, noneArgs, err
}

// seriesList is GET /api/libraries/{id}/series.
func (h *Handler) seriesList(w http.ResponseWriter, r *http.Request, s library.Scope) error {
	ctx, q := r.Context(), r.URL.Query()
	sortBy, desc, filter := q.Get("sort"), q.Get("desc") == "1", q.Get("filter")
	server := h.settings.Server()
	has, hasArgs, none, noneArgs, err := seriesFilter(filter, s.User)
	if err != nil {
		return err
	}
	perm, permArgs := s.User.MediaClause("b")
	where, args := ` FROM series s WHERE s.libraryId=?`, []any{s.Library.ID}
	if db.Val(s.Library.Settings.V.HideSingleBookSeries) {
		where += ` AND ` + seriesBookCount + `>1`
	}
	const books = `(SELECT 1 FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id`
	if has != "" || perm != "" {
		where, args = where+` AND EXISTS `+books+has+perm+`)`, append(append(args, hasArgs...), permArgs...)
	}
	if none != "" {
		where, args = where+` AND NOT EXISTS `+books+none+`)`, append(args, noneArgs...)
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	key := map[string]string{"name": `s.name COLLATE NOCASE`, "addedAt": `s.createdAt`, "numBooks": seriesBookCount,
		"totalDuration": seriesDuration, "lastBookAdded": seriesLastAdded}[sortBy]
	if server.SortingIgnorePrefix && sortBy == "name" {
		key = `s.nameIgnorePrefix COLLATE NOCASE`
	}
	order := ` ORDER BY s.rowid`
	if key != "" {
		order = ` ORDER BY ` + key + ` ` + dir + `, s.rowid ` + dir // ties by creation, like upstream's stable ORDER BY
	}
	duration := `0`
	if sortBy == "totalDuration" {
		duration = seriesDuration
	}
	out := seriesPage{Results: []seriesListEntry{}, Limit: s.Limit, Page: s.Page, SortBy: sortBy, SortDesc: desc, FilterBy: filter,
		Minified: q.Get("minified") == "1", Include: strings.Join(includes(r), ",")}
	if err := h.db.Get(ctx, &out.Total, `SELECT COUNT(*)`+where, args...); err != nil {
		return httpx.Internal("database read failed", err)
	}
	query := `SELECT s.*, ` + duration + ` AS totalDuration` + where + order
	if s.Limit > 0 {
		query, args = query+` LIMIT ? OFFSET ?`, append(args, s.Limit, s.Page*s.Limit)
	}
	var rows []struct {
		db.Series
		TotalDuration float64 `db:"totalDuration"`
	}
	if err := h.db.Select(ctx, &rows, query, args...); err != nil {
		return httpx.Internal("database read failed", err)
	}
	entries, err := h.listEntries(ctx, s.User, slices.Contains(includes(r), "rssfeed"), server.SortingPrefixes, rows)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	out.Results = entries
	return httpx.JSON(w, r, out)
}

// listEntries renders the series of a page with their visible books (in
// sequence order) and, when asked, their feeds, in three queries.
func (h *Handler) listEntries(ctx context.Context, user *auth.Principal, withFeed bool, prefixes []string, rows []struct {
	db.Series
	TotalDuration float64 `db:"totalDuration"`
}) ([]seriesListEntry, error) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	linked, err := h.seriesBooks(ctx, user, orderLinked, ids)
	if err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(linked))
	for i, row := range linked {
		itemIDs[i] = row.ItemID
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{})
	if err != nil {
		return nil, err
	}
	minified := make(map[string]item.Minified, len(loaded))
	for _, it := range loaded {
		minified[it.ID] = it.Minified(prefixes)
	}
	var feeds map[string]*feedMinified
	if withFeed {
		if feeds, err = h.feedsOf(ctx, "series", ids); err != nil {
			return nil, err
		}
	}
	bookIDs := bookIDsBySequence(linked)
	out := make([]seriesListEntry, len(rows))
	for i, row := range rows {
		out[i] = seriesListEntry{seriesView: newSeriesView(row.Series, prefixes), TotalDuration: row.TotalDuration, Books: []item.Minified{}, RSSFeed: feeds[row.ID]}
		for _, id := range bookIDs[row.ID] {
			out[i].Books = append(out[i].Books, minified[id])
		}
	}
	return out, nil
}

// seriesProgress is the include=progress object of the series detail.
type seriesProgress struct {
	LibraryItemIDs         []string `json:"libraryItemIds"`
	LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
	IsFinished             bool     `json:"isFinished"`
}

// seriesDetail is the body of GET /api/series/{id} and
// GET /api/libraries/{id}/series/{seriesId}: the series, then progress and
// rssFeed when asked for. RSSFeed is open: null when the series has no feed.
type seriesDetail struct {
	seriesView
	Progress *seriesProgress `json:"progress,omitempty"`
	RSSFeed  any             `json:"rssFeed,omitempty"`
}

// accessibleSeries is SeriesController.middleware (js:95-119): an unknown
// series, one in a library the user may not access, or one without a book the
// user may see, is a 404. It returns the visible books in sequence order. The
// library check is stricter than upstream, which only looks at the books.
func (h *Handler) accessibleSeries(ctx context.Context, user *auth.Principal, id string) (db.Series, []seriesBook, error) {
	var series db.Series
	switch err := h.db.Get(ctx, &series, `SELECT * FROM series WHERE id = ?`, id); {
	case errors.Is(err, sql.ErrNoRows):
		return series, nil, httpx.NotFound()
	case err != nil:
		return series, nil, httpx.Internal("database read failed", err)
	}
	if !user.CanAccessLibrary(db.Val(series.LibraryID)) {
		slog.Warn("user attempted to access a series of a library they cannot access", "user", db.Val(user.Username), "series", id)
		return series, nil, httpx.NotFound()
	}
	books, err := h.seriesBooks(ctx, user, orderSequence, []string{id})
	if err != nil {
		return series, nil, httpx.Internal("database read failed", err)
	}
	if len(books) == 0 {
		slog.Warn("user attempted to access a series with no accessible books", "user", db.Val(user.Username), "series", id)
		return series, nil, httpx.NotFound()
	}
	return series, books, nil
}

func (h *Handler) detail(ctx context.Context, user *auth.Principal, series db.Series, books []seriesBook, include []string) (seriesDetail, error) {
	out := seriesDetail{seriesView: newSeriesView(series, h.settings.Server().SortingPrefixes)}
	if slices.Contains(include, "progress") {
		progress := seriesProgress{LibraryItemIDs: []string{}, LibraryItemIDsFinished: []string{}}
		var finished []string
		bookIDs := make([]string, len(books))
		for i, b := range books {
			bookIDs[i] = b.BookID
		}
		if err := h.db.Select(ctx, &finished, `SELECT mediaItemId FROM mediaProgresses WHERE userId = ? AND mediaItemId IN (?) AND isFinished = 1`, user.ID, bookIDs); err != nil {
			return out, err
		}
		for _, b := range books {
			progress.LibraryItemIDs = append(progress.LibraryItemIDs, b.ItemID)
			if slices.Contains(finished, b.BookID) {
				progress.LibraryItemIDsFinished = append(progress.LibraryItemIDsFinished, b.ItemID)
			}
		}
		progress.IsFinished = len(progress.LibraryItemIDsFinished) == len(progress.LibraryItemIDs)
		out.Progress = &progress
	}
	if slices.Contains(include, "rssfeed") {
		feeds, err := h.feedsOf(ctx, "", []string{series.ID})
		if err != nil {
			return out, err
		}
		out.RSSFeed = item.JSONNull
		if feed := feeds[series.ID]; feed != nil {
			out.RSSFeed = feed
		}
	}
	return out, nil
}

// seriesGet is GET /api/series/{id}.
func (h *Handler) seriesGet(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	series, books, err := h.accessibleSeries(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	return h.respondDetail(w, r, user, series, books)
}

// seriesInLibrary is GET /api/libraries/{id}/series/{seriesId}: a series of
// another library is a 404.
func (h *Handler) seriesInLibrary(w http.ResponseWriter, r *http.Request, s library.Scope) error {
	series, books, err := h.accessibleSeries(r.Context(), s.User, r.PathValue("seriesId"))
	if err != nil {
		return err
	}
	if db.Val(series.LibraryID) != s.Library.ID {
		return httpx.NotFound()
	}
	return h.respondDetail(w, r, s.User, series, books)
}

func (h *Handler) respondDetail(w http.ResponseWriter, r *http.Request, user *auth.Principal, series db.Series, books []seriesBook) error {
	out, err := h.detail(r.Context(), user, series, books, includes(r))
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, out)
}

// seriesPatchRequest is the body of PATCH /api/series/{id}; nil is absent (a
// null changes nothing, as in the old server).
type seriesPatchRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// seriesPatch is PATCH /api/series/{id} (SeriesController.update): a change is
// saved and announced with series_updated. nameIgnorePrefix is not recomputed.
func (h *Handler) seriesPatch(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	series, _, err := h.accessibleSeries(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !user.CanUpdate() {
		slog.Warn("user attempted to update a series without permission", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	req, err := httpx.Decode[seriesPatchRequest](w, r)
	if err != nil {
		return err
	}
	if req.Name == nil && req.Description == nil {
		return httpx.BadRequest("No valid fields to update")
	}
	var changed []string
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if err := h.db.Get(ctx, &series, `SELECT * FROM series WHERE id = ?`, series.ID); err != nil {
			return err
		}
		if req.Name != nil && (series.Name == nil || *series.Name != *req.Name) {
			series.Name, changed = req.Name, append(changed, "name")
		}
		if req.Description != nil && (series.Description == nil || *series.Description != *req.Description) {
			series.Description, changed = req.Description, append(changed, "description")
		}
		if len(changed) == 0 {
			return nil
		}
		series.UpdatedAt = db.Now()
		return h.db.Update(ctx, "series", &series, append(changed, "updatedAt")...)
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	view := newSeriesView(series, h.settings.Server().SortingPrefixes)
	if len(changed) > 0 {
		h.filters.Invalidate(db.Val(series.LibraryID)) // the series menu of the library's filter data
		h.emit.ToAll("series_updated", view)
	}
	return httpx.JSON(w, r, view)
}
