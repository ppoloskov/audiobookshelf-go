package catalog

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	itemapi "audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
)

// validCollapseSubseriesSorts is VALID_COLLAPSE_SUBSERIES_SORTS
// (server/utils/libraryHelpers.js:9). Any other sort is dropped from the
// payload and the default "sequence" order applies.
var validCollapseSubseriesSorts = map[string]bool{
	"addedAt": true, "size": true, "birthtimeMs": true, "mtimeMs": true, "media.duration": true,
	"media.metadata.publishedYear": true, "media.metadata.authorName": true, "media.metadata.authorNameLF": true,
	"media.metadata.title": true, "sequence": true, "progress": true, "progress.createdAt": true,
	"progress.finishedAt": true, "random": true,
}

// subseriesSeries is one of a book's series with its bookSeries sequence.
type subseriesSeries struct {
	id, name, nameIgnorePrefix string
	sequence                   *string
}

// subseriesBook is one library item of the filtered series: the Book model
// with its libraryItem, as libraryHelpers.handleCollapseSubseries loads it.
type subseriesBook struct {
	access                   itemapi.MediaAccess
	itemID, libraryID        string
	title, titleIgnorePrefix string
	size                     sql.NullInt64
	duration                 sql.NullFloat64
	authorFirstLast          sql.NullString
	authorLastFirst          sql.NullString
	series                   []subseriesSeries
	collapsed                *subseriesCollapsed
}

// subseriesCollapsed is an entry of getSeriesFromBooks: another series the
// filtered series' books belong to, with those books ordered by their
// sequence in it.
type subseriesCollapsed struct {
	id, name, nameIgnorePrefix string
	books                      []*subseriesBook
	sequences                  map[string]*string // item id -> sequence in this series
	filterSeqs                 map[string]*string // item id -> sequence in the filtered series
}

// jsFloat renders a number the way String(n) does for ordinary magnitudes.
func jsFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// collapseSubseriesFilter reports whether the request takes upstream's
// "temporary way of handling collapse sub-series"
// (server/controllers/LibraryController.js:625-633): collapseseries=1 with a
// series.<base64> filter whose value is not "no-series". It returns the
// decoded series id (empty when the filter has no value, which matches no
// series); ok is false for other requests.
func collapseSubseriesFilter(r *http.Request) (seriesID string, ok bool) {
	filter := r.URL.Query().Get("filter")
	group, rest, hasDot := strings.Cut(filter, ".")
	if group != "series" || r.URL.Query().Get("collapseseries") != "1" {
		return "", false
	}
	if nodeBase64Decode(rest) == "no-series" {
		return "", false
	}
	if !hasDot {
		return "", true
	}
	value, _, _ := strings.Cut(rest, ".")
	return nodeBase64Decode(value), true
}

// nodeBase64Decode is libraryFilters.decode (libraryFilters.js:13-20):
// Buffer.from(decodeURIComponent(text), 'base64').toString(). Node's decoder
// accepts both alphabets and skips characters outside them.
func nodeBase64Decode(text string) string {
	var clean strings.Builder
	for _, c := range text {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean.WriteRune(c)
		case c == '-':
			clean.WriteByte('+')
		case c == '_':
			clean.WriteByte('/')
		case c == '=':
			// Decoding stops at the first padding character.
			decoded, _ := base64.RawStdEncoding.DecodeString(trimPartial(clean.String()))
			return string(decoded)
		}
	}
	decoded, _ := base64.RawStdEncoding.DecodeString(trimPartial(clean.String()))
	return string(decoded)
}

// trimPartial drops a trailing single character that cannot form a byte.
func trimPartial(s string) string {
	if len(s)%4 == 1 {
		return s[:len(s)-1]
	}
	return s
}

// collapseSubseries serves GET /api/libraries/:id/items for a series filter
// with collapseseries=1: libraryHelpers.handleCollapseSubseries
// (server/utils/libraryHelpers.js:105-264) as called from
// LibraryController.getLibraryItems (LibraryController.js:625-633). The
// series is looked up by id in any library; its books are filtered by the
// user's access and, when they belong to other series too, collapsed behind
// the first book of each of those series.
func (h *Handler) collapseSubseries(w http.ResponseWriter, r *http.Request, user store.User, libraryID, mediaType, seriesID string, page, limit int, ignorePrefix bool) {
	query := r.URL.Query()
	payload := wire.CollapsedSubseriesPage{
		Results: []any{}, Limit: limit, Page: page,
		SortDesc: query.Get("desc") == "1", FilterBy: query.Get("filter"), MediaType: mediaType,
		Minified: query.Get("minified") == "1", CollapseSeries: true, Include: normalizedInclude(r),
		Offset: page * limit,
	}
	sortBy := ""
	if query.Has("sort") {
		sortBy = query.Get("sort")
		if sortBy != "" && !validCollapseSubseriesSorts[sortBy] {
			sortBy = ""
		} else {
			payload.SortBy = &sortBy
		}
	}
	results, total, err := h.collapseSubseriesResults(r.Context(), user, libraryID, seriesID, sortBy, payload.SortDesc, page, limit, ignorePrefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	payload.Results, payload.Total = results, total
	writeJSON(w, r, http.StatusOK, payload)
}

func (h *Handler) collapseSubseriesResults(ctx context.Context, user store.User, libraryID, seriesID, sortBy string, desc bool, page, limit int, ignorePrefix bool) ([]any, int, error) {
	hideSingleBookSeries, err := h.hideSingleBookSeries(ctx, libraryID)
	if err != nil {
		return nil, 0, err
	}
	books, found, err := h.subseriesBooks(ctx, seriesID)
	if err != nil {
		return nil, 0, err
	}
	if !found {
		return []any{}, 0, nil
	}
	total := len(books)
	accessible := make([]*subseriesBook, 0, len(books))
	for _, b := range books {
		if userCanAccessLibrary(user, b.libraryID) && userCanAccessMedia(user, b.access) {
			accessible = append(accessible, b)
		}
	}
	items := accessible
	collapsedItems := collapseBookSeries(accessible, seriesID, hideSingleBookSeries)
	if !(len(collapsedItems) == 1 && collapsedItems[0].collapsed != nil) {
		items = collapsedItems
		total = len(items)
	}

	sortSubseriesItems(items, seriesID, sortBy, desc, ignorePrefix)

	if limit > 0 {
		// LibraryController.middleware coerces page and limit to numbers
		// (LibraryController.js:1490-1497), so this is plain arithmetic.
		start := page * limit
		end := start + limit
		if start > len(items) {
			start = len(items)
		}
		if end > len(items) {
			end = len(items)
		}
		items = items[start:end]
	}

	results := make([]any, 0, len(items))
	for _, li := range items {
		filtered := li.seriesByID(seriesID)
		item, err := h.ItemMinifiedJSON(ctx, li.itemID)
		if err != nil {
			return nil, 0, err
		}
		withBookMinified(item, func(media *wire.BookMinified) {
			media.Metadata.Series = &wire.BookSeries{ID: filtered.id, Name: filtered.name, Sequence: filtered.sequence}
		})
		if c := li.collapsed; c != nil {
			ids := make([]string, 0, len(c.books))
			for _, b := range c.books {
				ids = append(ids, b.itemID)
			}
			name := c.name
			item["collapsedSeries"] = &wire.CollapsedSubseries{
				ID: c.id, Name: &name, NameIgnorePrefix: c.nameIgnorePrefix, LibraryItemIDs: ids, NumBooks: len(c.books),
				SeriesSequenceList: seriesSequenceList(c),
			}
		}
		results = append(results, item)
	}
	return results, total, nil
}

func (b *subseriesBook) seriesByID(id string) subseriesSeries {
	for _, s := range b.series {
		if s.id == id {
			return s
		}
	}
	return subseriesSeries{}
}

// hideSingleBookSeries is library.settings.hideSingleBookSeries for the
// library of the request. handleCollapseSubseries receives req.library.
func (h *Handler) hideSingleBookSeries(ctx context.Context, libraryID string) (bool, error) {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, libraryID).Scan(&raw); err != nil {
		return false, err
	}
	hide, _ := decodeObject(raw)["hideSingleBookSeries"].(bool)
	return hide, nil
}

// subseriesBooks loads Series.findByPk(seriesId) with its books, their
// library items and all of their series (libraryHelpers.js:119-143). Books
// come in bookSeries(seriesId, bookId) index order and each book's series in
// bookSeries(bookId, seriesId) order, the orders SQLite returns for
// Sequelize's join without an ORDER BY.
func (h *Handler) subseriesBooks(ctx context.Context, seriesID string) ([]*subseriesBook, bool, error) {
	var exists int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM series WHERE id=?`, seriesID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT li.id, li.libraryId, li.size, li.authorNamesFirstLast, li.authorNamesLastFirst, b.id, b.title, b.titleIgnorePrefix, b.duration
		FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book'
		WHERE bs.seriesId=? ORDER BY bs.bookId`, seriesID)
	if err != nil {
		return nil, false, err
	}
	var books []*subseriesBook
	var bookIDs []string
	for rows.Next() {
		var b subseriesBook
		var libraryID, title, titleIgnorePrefix sql.NullString
		var bookID string
		if err := rows.Scan(&b.itemID, &libraryID, &b.size, &b.authorFirstLast, &b.authorLastFirst, &bookID, &title, &titleIgnorePrefix, &b.duration); err != nil {
			rows.Close()
			return nil, false, err
		}
		b.libraryID, b.title, b.titleIgnorePrefix = libraryID.String, title.String, titleIgnorePrefix.String
		books = append(books, &b)
		bookIDs = append(bookIDs, bookID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	for i, b := range books {
		media, err := h.bookMinified(ctx, bookIDs[i], b.itemID)
		if err != nil {
			return nil, false, err
		}
		b.access = itemapi.MediaAccess{ID: media.ID, Explicit: media.Metadata.Explicit, Tags: media.Tags}
		seriesRows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId=? ORDER BY bs.seriesId`, bookIDs[i])
		if err != nil {
			return nil, false, err
		}
		for seriesRows.Next() {
			var s subseriesSeries
			var name sql.NullString
			if err := seriesRows.Scan(&s.id, &name, &s.sequence); err != nil {
				seriesRows.Close()
				return nil, false, err
			}
			s.name = name.String
			b.series = append(b.series, s)
		}
		err = seriesRows.Err()
		seriesRows.Close()
		if err != nil {
			return nil, false, err
		}
		// Prefix lookups read server settings; the pool has one connection,
		// so they wait until the series cursor is closed.
		for j := range b.series {
			b.series[j].nameIgnorePrefix = h.titlePrefix(ctx, b.series[j].name)
		}
	}
	return books, true, nil
}

// collapseBookSeries is libraryHelpers.collapseBookSeries (js:70-97): books
// that share another series are replaced by one entry per series, placed
// where the series' first book was.
func collapseBookSeries(books []*subseriesBook, filterSeries string, hideSingleBookSeries bool) []*subseriesBook {
	var others []*subseriesCollapsed
	for _, c := range seriesFromBooks(books, filterSeries, hideSingleBookSeries) {
		if c.id != filterSeries {
			others = append(others, c)
		}
	}
	out := make([]*subseriesBook, 0, len(books))
	for _, li := range books {
		for _, c := range others {
			if c.books[0] == li {
				clone := *li
				clone.collapsed = c
				out = append(out, &clone)
			}
		}
		inSeries := false
		for _, c := range others {
			for _, b := range c.books {
				if b == li {
					inSeries = true
				}
			}
		}
		if !inSeries {
			out = append(out, li)
		}
	}
	return out
}

// seriesFromBooks is libraryHelpers.getSeriesFromBooks (js:19-67): every
// series of every book, in first-seen order, with its books ordered by their
// sequence (naturalSort, where a missing sequence compares as "null").
func seriesFromBooks(books []*subseriesBook, filterSeries string, hideSingleBookSeries bool) []*subseriesCollapsed {
	var order []*subseriesCollapsed
	byID := map[string]*subseriesCollapsed{}
	for _, li := range books {
		for _, s := range li.series {
			c := byID[s.id]
			if c == nil {
				c = &subseriesCollapsed{id: s.id, name: s.name, nameIgnorePrefix: s.nameIgnorePrefix, sequences: map[string]*string{}, filterSeqs: map[string]*string{}}
				byID[s.id] = c
				order = append(order, c)
			}
			c.books = append(c.books, li)
			c.sequences[li.itemID] = s.sequence
			c.filterSeqs[li.itemID] = li.seriesByID(filterSeries).sequence
		}
	}
	if hideSingleBookSeries {
		kept := order[:0]
		for _, c := range order {
			if len(c.books) > 1 {
				kept = append(kept, c)
			}
		}
		order = kept
	}
	for _, c := range order {
		sort.SliceStable(c.books, func(i, j int) bool {
			return textsort.NaturalBaseCompare(jsNullable(c.sequences[c.books[i].itemID]), jsNullable(c.sequences[c.books[j].itemID])) < 0
		})
	}
	return order
}

// jsNullable is how Intl.Collator.compare sees a possibly-null string: null
// is converted to the text "null".
func jsNullable(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

// sortSubseriesItems is the fast-sort call at libraryHelpers.js:174-224. A
// sort key list is applied in order, each with its own direction, a tie
// falling through to the next key; equal items keep their order.
func sortSubseriesItems(items []*subseriesBook, seriesID, sortBy string, desc, ignorePrefix bool) {
	dir := 1
	if desc {
		dir = -1
	}
	titleKey := func(li *subseriesBook) string {
		if c := li.collapsed; c != nil {
			if ignorePrefix && c.nameIgnorePrefix != "" {
				return c.nameIgnorePrefix
			}
			if !ignorePrefix && c.name != "" {
				return c.name
			}
		}
		if ignorePrefix {
			return li.titleIgnorePrefix
		}
		return li.title
	}
	type key struct {
		order int
		value func(*subseriesBook) string
	}
	var keys []key
	if sortBy == "" || sortBy == "sequence" {
		keys = []key{
			{dir, func(li *subseriesBook) string { return jsNullable(li.seriesByID(seriesID).sequence) }},
			{dir, titleKey},
		}
	} else {
		if sortBy != "media.metadata.title" {
			keys = append(keys, key{1, func(li *subseriesBook) string {
				if c := li.collapsed; c != nil {
					if ignorePrefix {
						return c.nameIgnorePrefix
					}
					return c.name
				}
				return ""
			}})
		}
		keys = append(keys, key{dir, func(li *subseriesBook) string {
			switch sortBy {
			case "media.metadata.title":
				return titleKey(li)
			case "media.metadata.authorName":
				return li.authorFirstLast.String
			case "media.metadata.authorNameLF":
				return li.authorLastFirst.String
			case "size":
				if li.size.Valid {
					return strconv.FormatInt(li.size.Int64, 10)
				}
			case "media.duration":
				if li.duration.Valid {
					return jsFloat(li.duration.Float64)
				}
			}
			// The remaining sorts read properties the Sequelize LibraryItem
			// instance does not have (addedAt, birthtimeMs, mtimeMs,
			// progress, random) or that sit on the Book model without a
			// `metadata` object (media.metadata.publishedYear); they all
			// evaluate to '' and leave the order unchanged.
			return ""
		}})
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, k := range keys {
			if c := textsort.NaturalBaseCompare(k.value(items[i]), k.value(items[j])); c != 0 {
				return c*k.order < 0
			}
		}
		return false
	})
}

var sequenceNumber = regexp.MustCompile(`^(\d+|\d+\.\d*|\d*\.\d+)$`)

// seriesSequenceList is collapsedSeries.seriesSequenceList
// (libraryHelpers.js:236-257): the filtered series' sequences of the books
// the collapsed series stands for, with consecutive whole numbers merged
// into ranges ("1-3, 5").
func seriesSequenceList(c *subseriesCollapsed) string {
	var sequences []string
	for _, b := range c.books {
		if s := c.filterSeqs[b.itemID]; s != nil && *s != "" {
			sequences = append(sequences, *s)
		}
	}
	sort.SliceStable(sequences, func(i, j int) bool { return textsort.NaturalBaseCompare(sequences[i], sequences[j]) < 0 })
	type seqRange struct {
		start, end       string
		startNum, endNum float64
		isNumber         bool
	}
	var ranges []*seqRange
	for _, s := range sequences {
		cur := &seqRange{start: s, end: s}
		if sequenceNumber.MatchString(s) {
			n, _ := strconv.ParseFloat(s, 64)
			cur.isNumber, cur.startNum, cur.endNum = true, n, n
		}
		if last := len(ranges) - 1; last >= 0 && cur.isNumber && ranges[last].isNumber && ranges[last].endNum+1 == cur.startNum {
			ranges[last].endNum = cur.startNum
			continue
		}
		ranges = append(ranges, cur)
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		render := func(num float64, text string) string {
			if r.isNumber {
				return jsFloat(num)
			}
			return text
		}
		start, end := render(r.startNum, r.start), render(r.endNum, r.end)
		if start == end {
			parts = append(parts, start)
		} else {
			parts = append(parts, start+"-"+end)
		}
	}
	return strings.Join(parts, ", ")
}
