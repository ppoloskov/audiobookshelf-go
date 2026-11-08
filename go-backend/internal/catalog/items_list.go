package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"net/http"
)

func (h *Handler) libraryItems(w http.ResponseWriter, r *http.Request, lib libraryScope) error {
	user, libraryID, mediaType := lib.user, lib.id, lib.mediaType
	page, limit := lib.page, lib.limit
	var err error
	collapse := r.URL.Query().Get("collapseseries") == "1"
	include := normalizedInclude(r)
	settings := serversettings.Load(r.Context(), h.db)
	ignorePrefix := settings.IgnorePrefix()
	if seriesID, ok := collapseSubseriesFilter(r); ok {
		return h.collapseSubseries(w, r, lib, seriesID, ignorePrefix)
	}
	filterValue := r.URL.Query().Get("filter")
	mediaTable, err := lib.mediaTable()
	if err != nil {
		return err
	}
	podcast := mediaType == "podcast"
	sortBy, desc := r.URL.Query().Get("sort"), r.URL.Query().Get("desc") == "1"
	order, orderArgs, err := orderSQL(sortBy, filterValue, desc, ignorePrefix, podcast, user)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, err.Error())
	}
	filterWhere, filterArgs, err := filterSQL(filterValue, podcast, user)
	if err != nil {
		return httputil.Status(http.StatusNotImplemented, err.Error())
	}
	var total int
	permissionSQL, permissionArgs := access.For(r.Context(), user).MediaClause("b")
	var collapsed collapsedSeries
	if collapse && mediaType == "book" {
		if collapsed, err = h.collapseSeries(r.Context(), libraryID, filterValue, filterWhere, filterArgs, permissionSQL, permissionArgs); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if len(collapsed.excludeBookIDs) > 0 {
			permissionSQL += ` AND b.id NOT IN (` + store.Placeholders(len(collapsed.excludeBookIDs)) + `)`
			for _, id := range collapsed.excludeBookIDs {
				permissionArgs = append(permissionArgs, id)
			}
		}
		if r.URL.Query().Get("sort") == "media.metadata.title" {
			order, orderArgs = collapsedTitleOrder(collapsed, ignorePrefix, r.URL.Query().Get("desc") == "1")
		}
	}
	whereSQL := filterWhere + permissionSQL
	countArgs := append([]any{libraryID, mediaType}, filterArgs...)
	countArgs = append(countArgs, permissionArgs...)
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraryItems li JOIN `+mediaTable+` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?`+whereSQL, countArgs...).Scan(&total); err != nil {
		return httputil.Internal("database read failed", err)
	}
	query := `SELECT li.id
	FROM libraryItems li JOIN ` + mediaTable + ` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?` + whereSQL + order
	args := append([]any{libraryID, mediaType}, filterArgs...)
	args = append(args, permissionArgs...)
	args = append(args, orderArgs...)
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, page*limit)
	}
	pageIDs, err := h.queryStrings(r.Context(), query, args...)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	pageItems, err := libitem.Load(r.Context(), h.db, pageIDs, libitem.Options{})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	prefixes := settings.SortingPrefixes()
	results := make([]libitem.Minified, 0, len(pageItems))
	for _, it := range pageItems {
		item := it.ListRow(prefixes)
		if err := h.attachItemIncludes(r.Context(), requestItemIncludes(r, user), item.Head, it.MediaID, &item.Extras); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if mediaType == "podcast" && includeHas(r, "numepisodesincomplete") {
			incomplete, err := h.numEpisodesIncomplete(r.Context(), user.ID, it.MediaID)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			item.NumEpisodesIncomplete = incomplete
		}
		if collapse && mediaType == "book" {
			series, err := h.collapsedSeriesJSON(r.Context(), it.MediaID, collapsed)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			if series != nil {
				item.CollapsedSeries = series
			}
		}
		results = append(results, item)
	}
	payload := map[string]any{
		"results": results, "total": total, "limit": limit, "page": page,
		"sortDesc": r.URL.Query().Get("desc") == "1", "mediaType": mediaType,
		"minified": r.URL.Query().Get("minified") == "1", "collapseseries": collapse, "include": include,
		"offset": page * limit,
	}
	if v := r.URL.Query().Get("sort"); v != "" {
		payload["sortBy"] = v
	}
	if v := r.URL.Query().Get("filter"); v != "" {
		payload["filterBy"] = v
	}
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
}
