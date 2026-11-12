package browse

import (
	"context"
	"strings"

	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/library"
)

// collapsedSeriesView is libraryItem.collapsedSeries on a collapsed page
// (libraryItemsBookFilters.js:661-668); the web client reads id, name,
// nameIgnorePrefix, numBooks and libraryItemIds.
type collapsedSeriesView struct {
	ID               string   `json:"id"`
	Name             *string  `json:"name"`
	NameIgnorePrefix *string  `json:"nameIgnorePrefix"`
	Sequence         *string  `json:"sequence"`
	NumBooks         int64    `json:"numBooks"`
	LibraryItemIDs   []string `json:"libraryItemIds"`
}

// collapsedSeries is the result of collapsing series on a book page
// (getCollapseSeriesBooksToExclude, libraryItemsBookFilters.js:312-353): each
// series shows as its first book, the other books of the series are hidden.
type collapsedSeries struct {
	hidden []string // book ids hidden behind their series' first book
	// kept are the bookSeries ids whose book stands for its series, in
	// discovery order; byBook lists them per book.
	kept   []string
	byBook map[string][]*seriesEntry
}

// seriesEntry is one kept bookSeries row with the series it stands for.
type seriesEntry struct {
	collapsedSeriesView
	linkID, linkCreatedAt string
}

// seriesBookRow is one book of a series as the collapsing query reads it.
type seriesBookRow struct {
	SeriesID         string  `db:"seriesId"`
	Name             *string `db:"name"`
	NameIgnorePrefix *string `db:"nameIgnorePrefix"`
	NumBooks         int64   `db:"numBooks"`
	BookID           string  `db:"bookId"`
	LinkID           string  `db:"linkId"`
	LinkCreatedAt    string  `db:"linkCreatedAt"`
	Sequence         *string `db:"sequence"`
	ItemID           string  `db:"itemId"`
}

// collapseSeries collapses a book library page. Candidate books use the
// request filter and the permission clause, except that progress and
// missing-authors filters apply to the series' books without it
// (libraryItemsBookFilters.js:562-570). Rows come in sequence order, books
// without a sequence last.
func (h *Handler) collapseSeries(ctx context.Context, s library.Scope, filter, filterClause string, filterArgs []any, permClause string, permArgs []any) (collapsedSeries, error) {
	group, raw, _ := strings.Cut(filter, ".")
	where, args := filterClause+permClause, append(append([]any{s.Library.ID}, filterArgs...), permArgs...)
	if value := decodeValue(raw); group == "progress" || (group == "missing" && value == "authors") {
		where, args = filterClause, append([]any{s.Library.ID}, filterArgs...)
	}
	var rows []seriesBookRow
	err := h.db.Select(ctx, &rows, `SELECT s.id AS seriesId, s.name AS name, s.nameIgnorePrefix AS nameIgnorePrefix,
		(SELECT count(*) FROM bookSeries bs2 WHERE bs2.seriesId = s.id) AS numBooks, b.id AS bookId, bs.id AS linkId,
		bs.createdAt AS linkCreatedAt, bs.sequence AS sequence, li.id AS itemId
		FROM series s JOIN bookSeries bs ON bs.seriesId = s.id JOIN books b ON b.id = bs.bookId
		JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' AND li.libraryId = ?
		WHERE 1=1`+where+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, bs.rowid`, args...)
	if err != nil {
		return collapsedSeries{}, err
	}
	// Series in first-appearance order, each with its books in row order.
	var order []string
	groups := map[string][]seriesBookRow{}
	for _, row := range rows {
		if _, ok := groups[row.SeriesID]; !ok {
			order = append(order, row.SeriesID)
		}
		groups[row.SeriesID] = append(groups[row.SeriesID], row)
	}
	out := collapsedSeries{byBook: map[string][]*seriesEntry{}}
	included, excluded := map[string]bool{}, map[string]bool{}
	var hidden []string
	for _, seriesID := range order {
		books := groups[seriesID]
		itemIDs := make([]string, len(books))
		for i, row := range books {
			itemIDs[i] = row.ItemID
		}
		found := false
		for _, row := range books {
			switch {
			case !found && !included[row.BookID]:
				included[row.BookID], found = true, true
				delete(excluded, row.BookID)
				entry := &seriesEntry{linkID: row.LinkID, linkCreatedAt: row.LinkCreatedAt, collapsedSeriesView: collapsedSeriesView{
					ID: row.SeriesID, Name: row.Name, NameIgnorePrefix: row.NameIgnorePrefix, Sequence: row.Sequence, NumBooks: row.NumBooks, LibraryItemIDs: itemIDs}}
				out.kept = append(out.kept, row.LinkID)
				out.byBook[row.BookID] = append(out.byBook[row.BookID], entry)
			case !excluded[row.BookID] && !included[row.BookID]:
				excluded[row.BookID] = true
				hidden = append(hidden, row.BookID)
			}
		}
	}
	for _, id := range hidden {
		if excluded[id] { // a book hidden in one series may be the first of another
			out.hidden = append(out.hidden, id)
		}
	}
	return out, nil
}

// seriesOf is the collapsed series a result book stands for: the first of its
// series (by link creation) whose link was kept; nil when none.
func (c collapsedSeries) seriesOf(it item.Item) *collapsedSeriesView {
	if it.Book == nil {
		return nil
	}
	var first *seriesEntry
	for _, entry := range c.byBook[it.Book.ID] {
		if first == nil || entry.linkCreatedAt < first.linkCreatedAt {
			first = entry
		}
	}
	if first == nil {
		return nil
	}
	return &first.collapsedSeriesView
}

// titleOrder is the display-title sort used when collapsing series: a kept
// book sorts by the name of its series (libraryItemsBookFilters.js:596-600).
func (c collapsedSeries) titleOrder(ignorePrefix, desc bool) (string, []any) {
	seriesColumn, itemColumn, dir := "s.name", "li.title", "ASC"
	if ignorePrefix {
		seriesColumn, itemColumn = "s.nameIgnorePrefix", "li.titleIgnorePrefix"
	}
	if desc {
		dir = "DESC"
	}
	return ` ORDER BY IFNULL((SELECT ` + seriesColumn + ` FROM bookSeries AS bs, series AS s WHERE bs.seriesId = s.id AND bs.bookId = b.id AND bs.id IN (?)), ` + itemColumn + `) COLLATE NOCASE ` + dir, []any{c.kept}
}
