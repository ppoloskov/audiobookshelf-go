package browse

import (
	"net/http"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
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
func newItemsPage(q url.Values, s scope, collapse bool, include []string) itemsPage {
	p := itemsPage{Results: []item.Minified{}, Limit: s.limit, Page: s.page, SortDesc: q.Get("desc") == "1",
		FilterBy: q.Get("filter"), MediaType: db.Val(s.lib.MediaType), Minified: q.Get("minified") == "1",
		CollapseSeries: collapse, Include: strings.Join(include, ","), Offset: s.page * s.limit}
	if sortBy := q.Get("sort"); sortBy != "" {
		p.SortBy = &sortBy
	}
	return p
}

// items serves the filtered, sorted and paged item list.
func (h *Handler) items(w http.ResponseWriter, r *http.Request, s scope) error {
	ctx, q := r.Context(), r.URL.Query()
	collapse := q.Get("collapseseries") == "1"
	include := includes(r)
	server := h.settings.Server()
	if seriesID, ok := subseriesFilter(q); ok {
		return h.subseriesPage(w, r, s, seriesID, server.SortingIgnorePrefix, server.SortingPrefixes)
	}
	table, err := s.mediaTable()
	if err != nil {
		return err
	}
	filter, mediaType := q.Get("filter"), db.Val(s.lib.MediaType)
	order, orderArgs, err := orderSQL(q.Get("sort"), filter, q.Get("desc") == "1", server.SortingIgnorePrefix, s.podcast(), s.user)
	if err != nil {
		return err
	}
	filterClause, filterArgs, err := filterSQL(filter, s.podcast(), s.user)
	if err != nil {
		return err
	}
	permClause, permArgs := s.user.MediaClause("b")
	var collapsed collapsedSeries
	if collapse && !s.podcast() {
		if collapsed, err = h.collapseSeries(ctx, s, filter, filterClause, filterArgs, permClause, permArgs); err != nil {
			return httpx.Internal("database read failed", err)
		}
		if len(collapsed.hidden) > 0 {
			permClause += ` AND b.id NOT IN (?)`
			permArgs = append(permArgs, collapsed.hidden)
		}
		if q.Get("sort") == "media.metadata.title" {
			order, orderArgs = collapsed.titleOrder(server.SortingIgnorePrefix, q.Get("desc") == "1")
		}
	}
	from := ` FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?` + filterClause + permClause
	args := append([]any{s.lib.ID, mediaType}, filterArgs...)
	args = append(args, permArgs...)
	out := newItemsPage(q, s, collapse, include)
	if err := h.db.Get(ctx, &out.Total, `SELECT COUNT(*)`+from, args...); err != nil {
		return httpx.Internal("database read failed", err)
	}
	query, args := `SELECT li.id`+from+order, append(args, orderArgs...)
	if s.limit > 0 {
		query, args = query+` LIMIT ? OFFSET ?`, append(args, s.limit, s.page*s.limit)
	}
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
	}
	if err := h.attach(ctx, s.user.ID, extrasOf(include, isAdmin(s.user)), loaded, rows); err != nil {
		return httpx.Internal("database read failed", err)
	}
	out.Results = rows
	return httpx.JSON(w, r, out)
}
