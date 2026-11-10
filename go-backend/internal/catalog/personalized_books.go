package catalog

import (
	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/textsort"
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Book home-page shelves that LibraryItem.getPersonalizedShelves
// (server/models/LibraryItem.js:384-491) adds for book libraries besides the
// progress and recently-added shelves.

func sixtyDaysAgo() string { return dbtime.Format(time.Now().Add(-60 * 24 * time.Hour)) }

func editBookMetadata(item *items.Minified, edit func(metadata *books.BookMetadataMinified)) {
	if media, ok := item.Media.(books.BookMinified); ok {
		edit(&media.Metadata)
		item.Media = media
	}
}

func (h *Handler) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// continueSeriesShelf mirrors libraryItemsBookFilters.getContinueSeriesLibraryItems
// (server/utils/queries/libraryItemsBookFilters.js:704-836).
type seriesRow struct {
	id, name    string
	maxSequence *float64
}

func (e *shelfEnv) continueSeries() (shelfQuery, error) {
	h, ctx, user, libraryID, limit := e.h, e.r.Context(), e.user, e.libraryID, e.limit
	var q shelfQuery
	settings := e.settings
	var extra struct {
		SeriesHideFromContinueListening []string `json:"seriesHideFromContinueListening"`
	}
	_ = json.Unmarshal(user.ExtraData, &extra)

	maxSequence := `(SELECT CAST(max(bs.sequence) as FLOAT) FROM bookSeries bs, mediaProgresses mp WHERE mp.mediaItemId = bs.bookId AND mp.isFinished = 1 AND mp.userId = ? AND bs.seriesId = s.id)`
	notFinished := `(SELECT count(*) FROM bookSeries bs LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = bs.bookId AND mp.userId = ? WHERE bs.seriesId = s.id AND (mp.isFinished = 0 OR mp.isFinished IS NULL)`
	notFinishedArgs := []any{user.ID}
	if settings.OnlyShowLaterBooksInContinueSeries {
		notFinished += ` AND CAST(bs.sequence as FLOAT) > ` + maxSequence
		notFinishedArgs = append(notFinishedArgs, user.ID)
	}
	notFinished += `)`
	where := ` FROM series s WHERE s.libraryId = ?`
	args := []any{libraryID}
	if len(extra.SeriesHideFromContinueListening) > 0 {
		where += ` AND s.id NOT IN (?` + strings.Repeat(",?", len(extra.SeriesHideFromContinueListening)-1) + `)`
		for _, id := range extra.SeriesHideFromContinueListening {
			args = append(args, id)
		}
	}
	where += ` AND (SELECT count(*) FROM mediaProgresses mp, bookSeries bs WHERE bs.seriesId = s.id AND mp.mediaItemId = bs.bookId AND mp.userId = ? AND mp.isFinished = 1) >= 1`
	args = append(args, user.ID)
	where += ` AND ` + notFinished + ` >= 1`
	args = append(args, notFinishedArgs...)
	where += ` AND (SELECT count(*) FROM mediaProgresses mp, bookSeries bs WHERE mp.mediaItemId = bs.bookId AND mp.userId = ? AND bs.seriesId = s.id AND mp.isFinished = 0 AND mp.currentTime > 0) = 0`
	args = append(args, user.ID)

	if err := h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&q.total); err != nil {
		return q, err
	}
	selectArgs := append([]any{user.ID, user.ID}, args...)
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, `+maxSequence+` AS maxSequence, (SELECT max(mp.updatedAt) FROM bookSeries bs, mediaProgresses mp WHERE mp.mediaItemId = bs.bookId AND mp.userId = ? AND bs.seriesId = s.id) AS recent_progress`+where+` ORDER BY recent_progress DESC LIMIT ?`, append(selectArgs, limit)...)
	if err != nil {
		return q, err
	}
	series := make([]seriesRow, 0)
	for rows.Next() {
		var s seriesRow
		var name *string
		if err := rows.Scan(&s.id, &name, &s.maxSequence, new(any)); err != nil {
			rows.Close()
			return q, err
		}
		if name != nil {
			s.name = *name
		}
		series = append(series, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return q, err
	}
	rows.Close()

	permission, permissionArgs := e.perm, e.permArgs
	type chosenBook struct {
		series   seriesRow
		itemID   string
		sequence *string
	}
	chosenBooks := make([]chosenBook, 0, len(series))
	for _, s := range series {
		// The series' bookSeries rows whose book the user has not finished,
		// ordered by CAST(sequence AS FLOAT) ASC NULLS LAST.
		candidateSQL := `SELECT li.id, bs.sequence FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
			LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = b.id AND mp.userId = ?
			WHERE bs.seriesId = ? AND (mp.isFinished IS NULL OR mp.isFinished = 0)` + permission + ` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST`
		bookRows, err := h.db.QueryContext(ctx, candidateSQL, append([]any{user.ID, s.id}, permissionArgs...)...)
		if err != nil {
			return q, err
		}
		type candidate struct {
			itemID   string
			sequence *string
		}
		candidates := make([]candidate, 0)
		for bookRows.Next() {
			var c candidate
			if err := bookRows.Scan(&c.itemID, &c.sequence); err != nil {
				bookRows.Close()
				return q, err
			}
			candidates = append(candidates, c)
		}
		if err := bookRows.Err(); err != nil {
			bookRows.Close()
			return q, err
		}
		bookRows.Close()
		if len(candidates) == 0 {
			continue
		}
		index := 0
		if settings.OnlyShowLaterBooksInContinueSeries {
			index = -1
			for i, c := range candidates {
				// parseFloat(sequence) > maxSequence; NaN and null compare false.
				if c.sequence == nil || s.maxSequence == nil {
					continue
				}
				if v, ok := jsParseFloat(*c.sequence); ok && v > *s.maxSequence {
					index = i
					break
				}
			}
			if index == -1 {
				continue
			}
		}
		chosenBooks = append(chosenBooks, chosenBook{s, candidates[index].itemID, candidates[index].sequence})
	}
	for _, c := range chosenBooks {
		q.ids = append(q.ids, c.itemID)
	}
	// The continue-series query only loads feeds; it never sets mediaItemShare
	// (libraryItemsBookFilters.js:707-826).
	attach := e.attach(itemIncludes{rssFeed: includeHas(e.r, "rssfeed")})
	q.build = func(items []shelfItem) ([]any, error) {
		for i := range items {
			c := chosenBooks[i]
			editBookMetadata(&items[i].min, func(metadata *books.BookMetadataMinified) {
				// book.series = [] before toOldJSONMinified, then metadata.series
				// is replaced with the continued series.
				metadata.SeriesName = ""
				metadata.Series = &bookSeries{ID: c.series.id, Name: c.series.name, Sequence: c.sequence}
			})
		}
		return attach(items)
	}
	return q, nil
}

// jsParseFloat approximates JavaScript parseFloat: the longest leading
// decimal prefix after whitespace.
func jsParseFloat(s string) (float64, bool) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	end := 0
	seenDigit, seenDot, seenExp := false, false, false
	for end < len(s) {
		c := s[end]
		switch {
		case c >= '0' && c <= '9':
			seenDigit = true
		case (c == '+' || c == '-') && (end == 0 || s[end-1] == 'e' || s[end-1] == 'E'):
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
		case (c == 'e' || c == 'E') && seenDigit && !seenExp:
			seenExp = true
		default:
			goto done
		}
		end++
	}
done:
	for end > 0 {
		if v, err := strconv.ParseFloat(s[:end], 64); err == nil {
			return v, true
		}
		end--
	}
	return math.NaN(), false
}

// recentSeries mirrors libraryFilters.getSeriesMostRecentlyAdded
// (server/utils/queries/libraryFilters.js:207-317); upstream asks for 5.
func (e *shelfEnv) recentSeries() (shelfQuery, error) {
	h, ctx, permission, permissionArgs := e.h, e.r.Context(), e.perm, e.permArgs
	var q shelfQuery
	prefixes := h.sortingPrefixes(ctx)
	where := ` FROM series s WHERE s.libraryId = ? AND s.createdAt >= ?`
	args := []any{e.libraryID, sixtyDaysAgo()}
	if e.settings.HideSingleBookSeries {
		where += ` AND (SELECT count(*) FROM books b, bookSeries bs WHERE bs.seriesId = s.id AND bs.bookId = b.id) > 1`
	}
	if permission != "" {
		where += ` AND (SELECT count(*) FROM books b, bookSeries bs WHERE bs.seriesId = s.id AND bs.bookId = b.id` + permission + `) > 0`
		args = append(args, permissionArgs...)
	}
	if err := h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&q.total); err != nil {
		return q, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, s.description, s.createdAt, s.updatedAt, s.libraryId`+where+` ORDER BY s.createdAt DESC LIMIT 5`, args...)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	var entries []seriesShelfEntry
	for rows.Next() {
		var rec seriesRecord
		if err := rows.Scan(&rec.id, &rec.name, &rec.description, &rec.createdAt, &rec.updatedAt, &rec.libraryID); err != nil {
			return q, err
		}
		view, err := newSeriesView(rec, prefixes)
		if err != nil {
			return q, err
		}
		entries = append(entries, seriesShelfEntry{seriesView: view})
	}
	if err := rows.Err(); err != nil {
		return q, err
	}
	rows.Close()

	// Each entry's visible books by sequence; all of them load with the other
	// shelves' items.
	members := make([]int, len(entries))
	for i := range entries {
		entry := &entries[i]
		if includeHas(e.r, "rssfeed") {
			if feed, ok, err := h.feedMinified(ctx, "", entry.ID); err != nil {
				return q, err
			} else if ok {
				entry.RSSFeed = feed
			}
		}
		type member struct {
			itemID   string
			sequence *string
		}
		var books []member
		bookRows, err := h.db.QueryContext(ctx, `SELECT li.id, bs.sequence FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE bs.seriesId = ?`+permission, append([]any{entry.ID}, permissionArgs...)...)
		if err != nil {
			return q, err
		}
		for bookRows.Next() {
			var m member
			if err := bookRows.Scan(&m.itemID, &m.sequence); err != nil {
				bookRows.Close()
				return q, err
			}
			books = append(books, m)
		}
		err = bookRows.Err()
		bookRows.Close()
		if err != nil {
			return q, err
		}
		// a.sequence.localeCompare(b.sequence, undefined, {numeric: true,
		// sensitivity: 'base'}); a missing sequence sorts last.
		sort.SliceStable(books, func(i, j int) bool {
			return textsort.SequenceLess(deref(books[i].sequence), deref(books[j].sequence))
		})
		members[i] = len(books)
		for _, m := range books {
			q.ids = append(q.ids, m.itemID)
		}
	}
	q.build = func(items []shelfItem) ([]any, error) {
		out := make([]any, len(entries))
		for i := range entries {
			entries[i].Books = make([]any, 0, members[i])
			for _, item := range items[:members[i]] {
				editBookMetadata(&item.min, func(metadata *books.BookMetadataMinified) {
					metadata.AuthorName, metadata.AuthorNameLF, metadata.SeriesName = "", "", ""
				})
				entries[i].Books = append(entries[i].Books, item.min)
			}
			items = items[members[i]:]
			out[i] = entries[i]
		}
		return out, nil
	}
	return q, nil
}

// discover mirrors libraryItemsBookFilters.getDiscoverLibraryItems
// (server/utils/queries/libraryItemsBookFilters.js:850-1010): random books the
// user has not started, taking only the first book of not-started series.
func (e *shelfEnv) discover() (shelfQuery, error) {
	h, ctx, permission := e.h, e.r.Context(), e.perm
	var q shelfQuery
	seriesIDs, err := h.queryStrings(ctx, `SELECT s.id FROM series s WHERE s.libraryId = ? AND (SELECT count(*) FROM bookSeries bs LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = bs.bookId WHERE bs.seriesId = s.id AND mp.userId = ? AND (mp.isFinished = 1 OR mp.currentTime > 0)) = 0 ORDER BY RANDOM() LIMIT ?`, e.libraryID, e.user.ID, e.limit)
	if err != nil {
		return q, err
	}
	firstBooks := make([]any, 0, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		ids, err := h.queryStrings(ctx, `SELECT b.id FROM bookSeries bs JOIN books b ON b.id = bs.bookId WHERE bs.seriesId = ?`+permission+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST LIMIT 1`, append([]any{seriesID}, e.permArgs...)...)
		if err != nil {
			return q, err
		}
		if len(ids) == 1 {
			firstBooks = append(firstBooks, ids[0])
		}
	}
	seriesClause := `(SELECT COUNT(*) FROM bookSeries bs WHERE bs.bookId = b.id) = 0`
	if len(firstBooks) > 0 {
		seriesClause = `(` + seriesClause + ` OR b.id IN (?` + strings.Repeat(",?", len(firstBooks)-1) + `))`
	}
	where := ` FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' AND li.libraryId = ?
		LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = b.id AND mp.userId = ?
		WHERE (mp.isFinished IS NULL OR mp.isFinished = 0) AND (mp.currentTime IS NULL OR mp.currentTime = 0) AND ` + seriesClause + permission
	args := append(append([]any{e.libraryID, e.user.ID}, firstBooks...), e.permArgs...)
	if err := h.db.QueryRowContext(ctx, `SELECT count(DISTINCT b.id)`+where, args...).Scan(&q.total); err != nil {
		return q, err
	}
	q.ids, err = h.queryStrings(ctx, `SELECT li.id`+where+` GROUP BY b.id ORDER BY RANDOM() LIMIT ?`, append(args, e.limit)...)
	// Discover only loads feeds; it never sets mediaItemShare (js:886-1017).
	q.build = e.attach(itemIncludes{rssFeed: includeHas(e.r, "rssfeed")})
	return q, err
}

// newestAuthors mirrors libraryFilters.getNewestAuthors
// (server/utils/queries/libraryFilters.js:334-368).
func (e *shelfEnv) newestAuthors() (shelfQuery, error) {
	var q shelfQuery
	ctx, permission := e.r.Context(), e.perm
	books := `(SELECT count(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id = ba.bookId WHERE ba.authorId = a.id` + permission + `)`
	where := ` FROM authors a WHERE a.libraryId = ? AND a.createdAt >= ? AND ` + books + ` > 0`
	args := append([]any{e.libraryID, sixtyDaysAgo()}, e.permArgs...)
	if err := e.h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&q.total); err != nil {
		return q, err
	}
	rows, err := e.h.db.QueryContext(ctx, `SELECT a.id, a.asin, a.name, a.description, a.imagePath, a.libraryId, a.createdAt, a.updatedAt, `+books+where+` ORDER BY a.createdAt DESC LIMIT ?`, append(append(append([]any{}, e.permArgs...), args...), e.limit)...)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	var authors []any
	for rows.Next() {
		var a authorRecord
		var numBooks int
		if err := rows.Scan(&a.id, &a.asin, &a.name, &a.description, &a.imagePath, &a.libraryID, &a.createdAt, &a.updatedAt, &numBooks); err != nil {
			return q, err
		}
		view, err := a.toOldJSON()
		if err != nil {
			return q, err
		}
		authors = append(authors, authorWithBooks{view, numBooks})
	}
	q.build = func([]shelfItem) ([]any, error) { return authors, nil }
	return q, rows.Err()
}
