package library

import (
	"audiobookshelf-compat/internal/store"
	"context"
	"strings"
)

// collapsedSeriesInfo is the per-bookSeries record of upstream
// getCollapseSeriesBooksToExclude (server/utils/queries/libraryItemsBookFilters.js:312-353).
type collapsedSeriesInfo struct {
	numBooks       int64
	libraryItemIDs []string
}

type collapsedSeries struct {
	// excludeBookIDs are series books hidden behind their series' first book.
	excludeBookIDs []string
	// include maps the kept bookSeries ids to their series details, and
	// includeIDs keeps them in discovery order for SQL.
	include    map[string]collapsedSeriesInfo
	includeIDs []string
}

// collapseSeries reproduces getCollapseSeriesBooksToExclude for a book
// library. Candidate books use the request filter and the user's permission
// clause, except that progress and missing-authors filters apply to the
// series' books without the permission clause (libraryItemsBookFilters.js:562-570).
// Rows are ordered by CAST(sequence AS FLOAT) ASC NULLS LAST; Sequelize builds
// series in first-appearance order with their books in row order.
func (h *Handler) collapseSeries(ctx context.Context, libraryID, filterValue, filterSQL string, filterArgs []any, permissionSQL string, permissionArgs []any) (collapsedSeries, error) {
	group, raw, _ := strings.Cut(filterValue, ".")
	value := DecodeFilterValue(raw)
	where := filterSQL + permissionSQL
	args := append([]any{libraryID}, filterArgs...)
	args = append(args, permissionArgs...)
	if group == "progress" || (group == "missing" && value == "authors") {
		where = filterSQL
		args = append([]any{libraryID}, filterArgs...)
	}
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, (SELECT count(*) FROM bookSeries bs2 WHERE bs2.seriesId = s.id), b.id, bs.id, li.id
		FROM series s JOIN bookSeries bs ON bs.seriesId = s.id JOIN books b ON b.id = bs.bookId
		JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' AND li.libraryId = ?
		WHERE 1=1`+where+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST`, args...)
	if err != nil {
		return collapsedSeries{}, err
	}
	type seriesBook struct{ bookID, bookSeriesID, itemID string }
	type seriesGroup struct {
		numBooks int64
		books    []seriesBook
	}
	order := make([]string, 0)
	groups := map[string]*seriesGroup{}
	for rows.Next() {
		var seriesID string
		var numBooks int64
		var b seriesBook
		if err := rows.Scan(&seriesID, &numBooks, &b.bookID, &b.bookSeriesID, &b.itemID); err != nil {
			rows.Close()
			return collapsedSeries{}, err
		}
		g, ok := groups[seriesID]
		if !ok {
			g = &seriesGroup{numBooks: numBooks}
			groups[seriesID] = g
			order = append(order, seriesID)
		}
		g.books = append(g.books, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return collapsedSeries{}, err
	}
	rows.Close()

	out := collapsedSeries{include: map[string]collapsedSeriesInfo{}}
	included := map[string]bool{}
	excluded := map[string]bool{}
	for _, seriesID := range order {
		g := groups[seriesID]
		itemIDs := make([]string, 0, len(g.books))
		for _, b := range g.books {
			itemIDs = append(itemIDs, b.itemID)
		}
		found := false
		for _, b := range g.books {
			if !found && !included[b.bookID] {
				included[b.bookID] = true
				out.include[b.bookSeriesID] = collapsedSeriesInfo{numBooks: g.numBooks, libraryItemIDs: itemIDs}
				out.includeIDs = append(out.includeIDs, b.bookSeriesID)
				delete(excluded, b.bookID)
				found = true
			} else if !excluded[b.bookID] && !included[b.bookID] {
				excluded[b.bookID] = true
				out.excludeBookIDs = append(out.excludeBookIDs, b.bookID)
			}
		}
	}
	// Re-filter to keep upstream's array order after removals.
	kept := out.excludeBookIDs[:0]
	for _, id := range out.excludeBookIDs {
		if excluded[id] {
			kept = append(kept, id)
		}
	}
	out.excludeBookIDs = kept
	return out, nil
}

// collapsedSeriesJSON is libraryItem.collapsedSeries for one result book: the
// first of the book's series whose bookSeries row was kept
// (libraryItemsBookFilters.js:657-670).
func (h *Handler) collapsedSeriesJSON(ctx context.Context, bookID string, c collapsedSeries) (*collapsedSeriesView, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, s.nameIgnorePrefix, bs.sequence, bs.id FROM bookSeries bs JOIN series s ON s.id = bs.seriesId WHERE bs.bookId = ? ORDER BY bs.createdAt ASC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, bookSeriesID string
		var name, nameIgnorePrefix, sequence *string
		if err := rows.Scan(&id, &name, &nameIgnorePrefix, &sequence, &bookSeriesID); err != nil {
			return nil, err
		}
		info, ok := c.include[bookSeriesID]
		if !ok {
			continue
		}
		return &collapsedSeriesView{ID: id, Name: name, NameIgnorePrefix: nameIgnorePrefix, Sequence: sequence, NumBooks: info.numBooks, LibraryItemIDs: info.libraryItemIDs}, nil
	}
	return nil, rows.Err()
}

// collapsedTitleOrder is the display_title sort used when collapsing series
// and sorting by title (libraryItemsBookFilters.js:596-600, 283-286).
func collapsedTitleOrder(c collapsedSeries, ignorePrefix, desc bool) (string, []any) {
	seriesColumn, itemColumn := "s.name", "li.title"
	if ignorePrefix {
		seriesColumn, itemColumn = "s.nameIgnorePrefix", "li.titleIgnorePrefix"
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	args := make([]any, 0, len(c.includeIDs))
	for _, id := range c.includeIDs {
		args = append(args, id)
	}
	in := store.Placeholders(len(args))
	return ` ORDER BY IFNULL((SELECT ` + seriesColumn + ` FROM bookSeries AS bs, series AS s WHERE bs.seriesId = s.id AND bs.bookId = b.id AND bs.id IN (` + in + `)), ` + itemColumn + `) COLLATE NOCASE ` + dir, args
}
