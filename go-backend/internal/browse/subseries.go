package browse

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/text"
)

// The page of a series filter with collapseseries=1 is upstream's "temporary
// way of handling collapse sub-series" (LibraryController.js:625-633,
// libraryHelpers.js:105-264): the books of the filtered series, where books
// that share another series stand for that series. The web client reads
// collapsedSeries (name, nameIgnorePrefix, libraryItemIds, numBooks,
// seriesSequenceList) from it.

// validSubseriesSorts is VALID_COLLAPSE_SUBSERIES_SORTS (libraryHelpers.js:9);
// any other sort is dropped from the payload and the sequence order applies.
var validSubseriesSorts = map[string]bool{
	"addedAt": true, "size": true, "birthtimeMs": true, "mtimeMs": true, "media.duration": true,
	"media.metadata.publishedYear": true, "media.metadata.authorName": true, "media.metadata.authorNameLF": true,
	"media.metadata.title": true, "sequence": true, "progress": true, "progress.createdAt": true,
	"progress.finishedAt": true, "random": true,
}

// subseriesFilter reports whether the request is such a page and, if so, the
// decoded series id (empty when the filter has no value, which matches no
// series).
func subseriesFilter(q url.Values) (seriesID string, ok bool) {
	group, rest, hasDot := strings.Cut(q.Get("filter"), ".")
	if group != "series" || q.Get("collapseseries") != "1" || decodeValue(rest) == "no-series" {
		return "", false
	}
	if !hasDot {
		return "", true
	}
	value, _, _ := strings.Cut(rest, ".")
	return decodeValue(value), true
}

// subBook is a book of the filtered series; collapsed is set on the entry
// that stands for another series.
type subBook struct {
	it        item.Item
	collapsed *subSeries
}

// subSeries is another series the filtered series' books belong to, with those
// books in sequence order.
type subSeries struct {
	id, name, nameIgnorePrefix string
	books                      []*subBook
	sequence                   map[string]*string // item id -> sequence in this series
	filterSequence             map[string]*string // item id -> sequence in the filtered series
}

func (b *subBook) ref(seriesID string) item.SeriesRef {
	for _, ref := range b.it.Book.Series {
		if ref.ID == seriesID {
			return ref
		}
	}
	return item.SeriesRef{}
}

// collapsedSubseries is libraryItem.collapsedSeries of such a page
// (libraryHelpers.js:233-258).
type collapsedSubseries struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	NameIgnorePrefix   string   `json:"nameIgnorePrefix"`
	LibraryItemIDs     []string `json:"libraryItemIds"`
	NumBooks           int      `json:"numBooks"`
	SeriesSequenceList string   `json:"seriesSequenceList"`
}

func (h *Handler) subseriesPage(w http.ResponseWriter, r *http.Request, s library.Scope, seriesID string, ignorePrefix bool, prefixes []string) error {
	ctx, q := r.Context(), r.URL.Query()
	out := newItemsPage(q, s, true, includes(r))
	sortBy := q.Get("sort")
	if q.Has("sort") {
		out.SortBy = &sortBy
		if sortBy != "" && !validSubseriesSorts[sortBy] {
			out.SortBy, sortBy = nil, ""
		}
	}
	var ids []string
	err := h.db.Select(ctx, &ids, `SELECT li.id FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book'
		WHERE bs.seriesId=? AND EXISTS (SELECT 1 FROM series WHERE id=?) ORDER BY bs.rowid`, seriesID, seriesID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	loaded, err := item.Load(ctx, h.db, ids, item.Options{})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	var books []*subBook
	for _, it := range loaded {
		if a := it.Access(); s.User.CanAccessItem(a.LibraryID, a.Explicit, a.Tags) {
			books = append(books, &subBook{it: it})
		}
	}
	entries := collapseBooks(books, seriesID, db.Val(s.Library.Settings.V.HideSingleBookSeries), prefixes)
	// A single collapsed entry is shown uncollapsed.
	if len(entries) != 1 || entries[0].collapsed == nil {
		books = entries
	}
	out.Total = len(books)
	sortSubBooks(books, seriesID, sortBy, q.Get("desc") == "1", ignorePrefix)
	if s.Limit > 0 {
		books = page(books, s.Page, s.Limit)
	}
	out.Results = make([]item.Minified, len(books))
	for i, b := range books {
		row := b.it.Minified(prefixes)
		if media, ok := row.Media.(item.BookMinified); ok {
			media.Metadata.Series = b.ref(seriesID)
			row.Media = media
		}
		if c := b.collapsed; c != nil {
			itemIDs := make([]string, len(c.books))
			for j, member := range c.books {
				itemIDs[j] = member.it.ID
			}
			row.CollapsedSeries = collapsedSubseries{ID: c.id, Name: c.name, NameIgnorePrefix: c.nameIgnorePrefix, LibraryItemIDs: itemIDs,
				NumBooks: len(c.books), SeriesSequenceList: c.sequenceList()}
		}
		out.Results[i] = row
	}
	return httpx.JSON(w, r, out)
}

// collapseBooks is libraryHelpers.collapseBookSeries (js:70-97): books that
// share another series are replaced by one entry per series, placed where the
// series' first book was.
func collapseBooks(books []*subBook, filterSeries string, hideSingle bool, prefixes []string) []*subBook {
	var others []*subSeries
	for _, c := range seriesOfBooks(books, filterSeries, hideSingle, prefixes) {
		if c.id != filterSeries {
			others = append(others, c)
		}
	}
	out := make([]*subBook, 0, len(books))
	for _, b := range books {
		inSeries := false
		for _, c := range others {
			if c.books[0] == b {
				out = append(out, &subBook{it: b.it, collapsed: c})
			}
			inSeries = inSeries || slices.Contains(c.books, b)
		}
		if !inSeries {
			out = append(out, b)
		}
	}
	return out
}

// seriesOfBooks is libraryHelpers.getSeriesFromBooks (js:19-67): every series of
// every book, in first-seen order, with its books ordered by sequence (a
// missing sequence compares as "null", as in upstream's naturalSort).
func seriesOfBooks(books []*subBook, filterSeries string, hideSingle bool, prefixes []string) []*subSeries {
	var order []*subSeries
	byID := map[string]*subSeries{}
	for _, b := range books {
		for _, ref := range b.it.Book.Series {
			c := byID[ref.ID]
			if c == nil {
				c = &subSeries{id: ref.ID, name: ref.Name, nameIgnorePrefix: text.PrefixAtEnd(ref.Name, prefixes),
					sequence: map[string]*string{}, filterSequence: map[string]*string{}}
				byID[ref.ID] = c
				order = append(order, c)
			}
			c.books = append(c.books, b)
			c.sequence[b.it.ID] = ref.Sequence
			c.filterSequence[b.it.ID] = b.ref(filterSeries).Sequence
		}
	}
	if hideSingle {
		order = slices.DeleteFunc(order, func(c *subSeries) bool { return len(c.books) <= 1 })
	}
	for _, c := range order {
		sort.SliceStable(c.books, func(i, j int) bool {
			return text.NaturalBaseCompare(nullable(c.sequence[c.books[i].it.ID]), nullable(c.sequence[c.books[j].it.ID])) < 0
		})
	}
	return order
}

// nullable is how Intl.Collator sees a possibly-null string: null is "null".
func nullable(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

// sortSubBooks is the fast-sort call at libraryHelpers.js:174-224: keys apply
// in order, a tie falls through to the next key, equal entries keep their
// order. Sorts that read properties upstream's Sequelize instance lacks
// (addedAt, birthtimeMs, mtimeMs, progress, random, publishedYear) are all
// "" and leave the order unchanged.
func sortSubBooks(books []*subBook, seriesID, sortBy string, desc, ignorePrefix bool) {
	dir := 1
	if desc {
		dir = -1
	}
	title := func(b *subBook) string {
		if c := b.collapsed; c != nil {
			if ignorePrefix && c.nameIgnorePrefix != "" {
				return c.nameIgnorePrefix
			}
			if !ignorePrefix && c.name != "" {
				return c.name
			}
		}
		if ignorePrefix {
			return db.Val(b.it.Book.TitleIgnorePrefix)
		}
		return db.Val(b.it.Book.Title)
	}
	type key struct {
		dir   int
		value func(*subBook) string
	}
	var keys []key
	if sortBy == "" || sortBy == "sequence" {
		keys = []key{{dir, func(b *subBook) string { return nullable(b.ref(seriesID).Sequence) }}, {dir, title}}
	} else {
		if sortBy != "media.metadata.title" {
			// Collapsed entries after plain books, by series name.
			keys = append(keys, key{1, func(b *subBook) string {
				if c := b.collapsed; c == nil {
					return ""
				} else if ignorePrefix {
					return c.nameIgnorePrefix
				} else {
					return c.name
				}
			}})
		}
		keys = append(keys, key{dir, func(b *subBook) string {
			switch sortBy {
			case "media.metadata.title":
				return title(b)
			case "media.metadata.authorName":
				return db.Val(b.it.AuthorNamesFirstLast)
			case "media.metadata.authorNameLF":
				return db.Val(b.it.AuthorNamesLastFirst)
			case "size":
				if b.it.Size != nil {
					return strconv.FormatInt(*b.it.Size, 10)
				}
			case "media.duration":
				if d := b.it.Book.Duration; d != nil {
					return strconv.FormatFloat(*d, 'f', -1, 64)
				}
			}
			return ""
		}})
	}
	sort.SliceStable(books, func(i, j int) bool {
		for _, k := range keys {
			if c := text.NaturalBaseCompare(k.value(books[i]), k.value(books[j])); c != 0 {
				return c*k.dir < 0
			}
		}
		return false
	})
}

var sequenceNumber = regexp.MustCompile(`^(\d+|\d+\.\d*|\d*\.\d+)$`)

// sequenceList is collapsedSeries.seriesSequenceList (libraryHelpers.js:236-257):
// the filtered series' sequences of the books the series stands for, with
// consecutive whole numbers merged into ranges ("1-3, 5").
func (c *subSeries) sequenceList() string {
	var sequences []string
	for _, b := range c.books {
		if s := c.filterSequence[b.it.ID]; s != nil && *s != "" {
			sequences = append(sequences, *s)
		}
	}
	sort.SliceStable(sequences, func(i, j int) bool { return text.NaturalBaseCompare(sequences[i], sequences[j]) < 0 })
	type span struct {
		start, end       string
		startNum, endNum float64
		numeric          bool
	}
	var spans []*span
	for _, s := range sequences {
		cur := &span{start: s, end: s}
		if sequenceNumber.MatchString(s) {
			n, _ := strconv.ParseFloat(s, 64) // matched above, so it parses
			cur.numeric, cur.startNum, cur.endNum = true, n, n
		}
		if last := len(spans) - 1; last >= 0 && cur.numeric && spans[last].numeric && spans[last].endNum+1 == cur.startNum {
			spans[last].endNum = cur.startNum
			continue
		}
		spans = append(spans, cur)
	}
	parts := make([]string, len(spans))
	for i, sp := range spans {
		start, end := sp.start, sp.end
		if sp.numeric {
			start, end = strconv.FormatFloat(sp.startNum, 'f', -1, 64), strconv.FormatFloat(sp.endNum, 'f', -1, 64)
		}
		if parts[i] = start; start != end {
			parts[i] = start + "-" + end
		}
	}
	return strings.Join(parts, ", ")
}
