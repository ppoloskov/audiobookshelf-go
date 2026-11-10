package people

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
)

func (h *Handler) series(w http.ResponseWriter, r *http.Request, lib library.Scope) error {
	user, libraryID := lib.User, lib.ID
	// A podcast library has no series rows, so the shared query below yields
	// the empty list upstream returns (seriesFilters.getFilteredSeries).
	filter := r.URL.Query().Get("filter")
	hideSingle := lib.Settings.HideSingleBookSeries
	permissionSQL, permissionArgs := access.For(r.Context(), user).MediaClause("b")
	hasPermissionFilter := permissionSQL != ""
	rows, err := h.db.QueryContext(r.Context(), `SELECT s.id,s.name,s.nameIgnorePrefix,s.description,s.createdAt,s.updatedAt,s.libraryId,(SELECT COUNT(*) FROM bookSeries bs WHERE bs.seriesId=s.id) AS bookCount,(SELECT COALESCE(SUM(b.duration),0) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS duration,(SELECT MAX(b.createdAt) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS recent FROM series s WHERE s.libraryId=?`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type seriesRow struct {
		id, libraryID                 string
		name, nameIgnore, description sql.NullString
		created, updated, recent      any
		count                         int64
		duration                      float64
	}
	all := make([]seriesRow, 0)
	for rows.Next() {
		var s seriesRow
		if err := rows.Scan(&s.id, &s.name, &s.nameIgnore, &s.description, &s.created, &s.updated, &s.libraryID, &s.count, &s.duration, &s.recent); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	if hideSingle || hasPermissionFilter {
		visibleSeries := all[:0]
		for _, s := range all {
			if hideSingle && s.count <= 1 {
				continue
			}
			if hasPermissionFilter {
				var visible int64
				query := `SELECT COUNT(*) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=?` + permissionSQL
				args := append([]any{s.id}, permissionArgs...)
				if err := h.db.QueryRowContext(r.Context(), query, args...).Scan(&visible); err != nil {
					return httputil.Internal("database read failed", err)
				}
				if visible == 0 {
					continue
				}
			}
			visibleSeries = append(visibleSeries, s)
		}
		all = visibleSeries
	}
	if filter != "" {
		filtered := all[:0]
		for _, s := range all {
			ok, e := h.seriesMatchesFilter(r.Context(), s.id, filter, user)
			if e != nil {
				return httputil.Internal("database read failed", e)
			}
			if ok {
				filtered = append(filtered, s)
			}
		}
		all = filtered
	}
	sortBy := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("desc") == "1"
	ignorePrefix := serversettings.Load(r.Context(), h.db).IgnorePrefix()
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		var less bool
		switch sortBy {
		case "name":
			an, bn := a.name.String, b.name.String
			if ignorePrefix {
				an = a.nameIgnore.String
				bn = b.nameIgnore.String
			}
			less = naturalLess(an, bn)
		case "addedAt":
			less = dateSortKey(a.created) < dateSortKey(b.created)
		case "numBooks":
			less = a.count < b.count
		case "totalDuration":
			less = a.duration < b.duration
		case "lastBookAdded":
			less = dateSortKey(a.recent) < dateSortKey(b.recent)
		default:
			return false
		}
		if desc {
			return !less
		}
		return less
	})
	total := len(all)
	page, limit := lib.Page, lib.Limit
	if limit > 0 {
		all = pageOf(all, page, limit)
	}
	results := make([]SeriesListEntry, 0, len(all))
	prefixes := h.sortingPrefixes(r.Context())
	for _, s := range all {
		view, err := NewSeriesView(SeriesRecord{ID: s.id, LibraryID: s.libraryID, Name: s.name, Description: s.description, CreatedAt: s.created, UpdatedAt: s.updated}, prefixes)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		entry := SeriesListEntry{SeriesView: view}
		if sortBy == "totalDuration" {
			entry.TotalDuration = s.duration
		}
		if entry.Books, err = h.seriesBooks(r.Context(), s.id, user); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if includeHas(r, "rssfeed") {
			if feed, ok, e := h.feedMinified(r.Context(), "series", s.id); e != nil {
				return httputil.Internal("database read failed", e)
			} else if ok {
				entry.RSSFeed = &feed
			}
		}
		results = append(results, entry)
	}
	payload := map[string]any{"results": results, "total": total, "limit": limit, "page": page, "sortDesc": desc, "minified": r.URL.Query().Get("minified") == "1", "include": normalizedInclude(r)}
	if filter != "" {
		payload["filterBy"] = filter
	}
	if sortBy != "" {
		payload["sortBy"] = sortBy
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) seriesBooks(ctx context.Context, seriesID string, user store.User) ([]items.Minified, error) {
	permission, args := access.For(ctx, user).MediaClause("b")
	queryArgs := append([]any{seriesID}, args...)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,bs.sequence FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id WHERE bs.seriesId=?`+permission, queryArgs...)
	if err != nil {
		return nil, err
	}
	type bookRef struct{ id, sequence string }
	refs := make([]bookRef, 0)
	for rows.Next() {
		var ref bookRef
		var sequence sql.NullString
		if err := rows.Scan(&ref.id, &sequence); err != nil {
			rows.Close()
			return nil, err
		}
		ref.sequence = sequence.String
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.SliceStable(refs, func(i, j int) bool { return textsort.SequenceLess(refs[i].sequence, refs[j].sequence) })
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.id
	}
	return h.minifiedItems(ctx, ids)
}

func (h *Handler) seriesByID(w http.ResponseWriter, r *http.Request, lib library.Scope) error {
	s, err := LoadSeries(r.Context(), h.db, r.PathValue("seriesId"))
	if errors.Is(err, sql.ErrNoRows) || s.LibraryID != lib.ID {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	items, err := SeriesItemsFor(r.Context(), h.db, lib.User, s)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(items) == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	detail, err := SeriesDetailFor(r.Context(), h.db, lib.User, s, items, includeHas(r, "progress"), includeHas(r, "rssfeed"), h.sortingPrefixes(r.Context()))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, detail)
}

func dateSortKey(v any) int64 {
	ms, _ := dbtime.OptionalMillis(v)
	if ms == nil {
		return 0
	}
	return *ms
}
