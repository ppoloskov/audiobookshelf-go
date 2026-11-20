package browse

import (
	"net/http"
	"net/url"
	"strings"

	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/item"
	"monstrobook/internal/library"
)

// itemsPage is GET /api/libraries/{id}/items (LibraryController.getLibraryItems).
// SortBy is a pointer because the series-collapsing page sends an empty one.
type itemsPage struct {
	Results        []item.Minified `json:"results"`
	Total          int             `json:"total"`
	Limit          int             `json:"limit"`
	Page           int             `json:"page"`
	SortBy         *string         `json:"sortBy,omitempty"`
	SortDesc       bool            `json:"sortDesc"`
	FilterBy       string          `json:"filterBy,omitempty"`
	MediaType      string          `json:"mediaType"`
	Minified       bool            `json:"minified"`
	CollapseSeries bool            `json:"collapseseries"`
	Include        string          `json:"include"`
	Offset         int             `json:"offset"`
}

// newItemsPage fills the echo of the request.
func newItemsPage(q url.Values, s library.Scope, collapse bool, include []string) itemsPage {
	p := itemsPage{Results: []item.Minified{}, Limit: s.Limit, Page: s.Page, SortDesc: q.Get("desc") == "1",
		FilterBy: q.Get("filter"), MediaType: db.Val(s.Library.MediaType), Minified: q.Get("minified") == "1",
		CollapseSeries: collapse, Include: strings.Join(include, ","), Offset: s.Page * s.Limit}
	if sortBy := q.Get("sort"); sortBy != "" {
		p.SortBy = &sortBy
	}
	return p
}

// items serves the filtered, sorted and paged item list.
func (h *Handler) items(w http.ResponseWriter, r *http.Request, s library.Scope) error {
	ctx, q := r.Context(), r.URL.Query()
	collapse := q.Get("collapseseries") == "1"
	include := includes(r)
	server := h.settings.Server()
	if seriesID, ok := subseriesFilter(q); ok {
		return h.subseriesPage(w, r, s, seriesID, server.SortingIgnorePrefix, server.SortingPrefixes)
	}
	table, err := s.MediaTable()
	if err != nil {
		return err
	}
	filter, sortBy, desc := q.Get("filter"), q.Get("sort"), q.Get("desc") == "1"
	seriesID, hasSeriesFilter := seriesFilterID(filter)
	order, orderArgs, err := orderSQL(sortBy, filter, desc, server.SortingIgnorePrefix, s.Podcast(), s.User)
	if err != nil {
		return err
	}
	filterClause, filterArgs, err := filterSQL(filter, s.Podcast(), s.User)
	if err != nil {
		return err
	}
	permClause, permArgs := s.User.MediaClause("b")
	var collapsed collapsedSeries
	if collapse && !s.Podcast() {
		if collapsed, err = h.collapseSeries(ctx, s, filter, filterClause, filterArgs, permClause, permArgs); err != nil {
			return httpx.Internal("database read failed", err)
		}
		if len(collapsed.hidden) > 0 {
			permClause += ` AND b.id NOT IN (?)`
			permArgs = append(permArgs, collapsed.hidden)
		}
		if sortBy == "media.metadata.title" || (sortBy == "sequence" && !strings.HasPrefix(filter, "series.")) {
			order, orderArgs = collapsed.titleOrder(server.SortingIgnorePrefix, desc)
		}
	}
	joinFeed, countExpr, selectCol := "", "*", "li.id"
	if filter == "feed-open" {
		// Join feeds directly (like upstream's required feed include) so SQLite
		// scans feeds first instead of libraryItems.
		joinFeed = ` JOIN feeds f ON f.entityType='libraryItem' AND f.entityId=li.id`
		countExpr, selectCol = "DISTINCT li.id", "DISTINCT li.id"
		filterClause, filterArgs = "", nil
	}
	from := ` FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId` + joinFeed + ` WHERE li.libraryId=? AND li.mediaType=?` + filterClause + permClause
	args := append(append([]any{s.Library.ID, db.Val(s.Library.MediaType)}, filterArgs...), permArgs...)
	out := newItemsPage(q, s, collapse, include)
	if err := h.db.Get(ctx, &out.Total, `SELECT COUNT(`+countExpr+`)`+from, args...); err != nil {
		return httpx.Internal("database read failed", err)
	}
	query, args := pagedQuery(`SELECT `+selectCol+from+order, append(args, orderArgs...), s)
	var ids []string
	if err := h.db.Select(ctx, &ids, query, args...); err != nil {
		return httpx.Internal("database read failed", err)
	}
	loaded, err := item.Load(ctx, h.db, ids, item.Options{})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	rows := make([]item.Minified, len(loaded))
	for i, it := range loaded {
		rows[i] = it.ListRow(server.SortingPrefixes)
		if series := collapsed.seriesOf(it); series != nil {
			rows[i].CollapsedSeries = series
		}
		if !s.Podcast() && hasSeriesFilter {
			if ref, ok := seriesRefOf(it, seriesID); ok {
				if ref.Sequence != nil && *ref.Sequence == "" {
					ref.Sequence = nil
				}
				updateBookMedia(&rows[i], func(m *item.BookMinified) { m.Metadata.Series = ref })
			}
		}
	}
	extras := extrasOf(include, isAdmin(s.User))
	extras.rssFeed = extras.rssFeed || filter == "feed-open"
	if err := h.attach(ctx, s.User.ID, extras, loaded, rows); err != nil {
		return httpx.Internal("database read failed", err)
	}
	out.Results = rows
	return httpx.JSON(w, r, out)
}
