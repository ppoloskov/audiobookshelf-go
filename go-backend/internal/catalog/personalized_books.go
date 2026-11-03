package catalog

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// Book home-page shelves that LibraryItem.getPersonalizedShelves
// (server/models/LibraryItem.js:384-491) adds for book libraries besides the
// progress and recently-added shelves.

type bookLibrarySettings struct {
	HideSingleBookSeries               bool `json:"hideSingleBookSeries"`
	OnlyShowLaterBooksInContinueSeries bool `json:"onlyShowLaterBooksInContinueSeries"`
}

func (h *Handler) bookLibrarySettings(ctx context.Context, libraryID string) bookLibrarySettings {
	var raw []byte
	var settings bookLibrarySettings
	if h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, libraryID).Scan(&raw) == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	return settings
}

func sixtyDaysAgo() string { return dbtime.Format(time.Now().Add(-60 * 24 * time.Hour)) }

func withBookMinified(item map[string]any, edit func(*wire.BookMinified)) {
	if media, ok := item["media"].(wire.BookMinified); ok {
		edit(&media)
		item["media"] = media
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
func (h *Handler) continueSeriesShelf(r *http.Request, user store.User, libraryID string, limit int) ([]any, int, error) {
	ctx := r.Context()
	settings := h.bookLibrarySettings(ctx, libraryID)
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

	var count int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	selectArgs := append([]any{user.ID, user.ID}, args...)
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, `+maxSequence+` AS maxSequence, (SELECT max(mp.updatedAt) FROM bookSeries bs, mediaProgresses mp WHERE mp.mediaItemId = bs.bookId AND mp.userId = ? AND bs.seriesId = s.id) AS recent_progress`+where+` ORDER BY recent_progress DESC LIMIT ?`, append(selectArgs, limit)...)
	if err != nil {
		return nil, 0, err
	}
	type seriesRow struct {
		id, name    string
		maxSequence *float64
	}
	series := make([]seriesRow, 0)
	for rows.Next() {
		var s seriesRow
		var name *string
		if err := rows.Scan(&s.id, &name, &s.maxSequence, new(any)); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if name != nil {
			s.name = *name
		}
		series = append(series, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()

	permission, permissionArgs := userMediaClause(user, "book")
	items := make([]any, 0, len(series))
	for _, s := range series {
		// The series' bookSeries rows whose book the user has not finished,
		// ordered by CAST(sequence AS FLOAT) ASC NULLS LAST.
		q := `SELECT li.id, bs.sequence FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
			LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = b.id AND mp.userId = ?
			WHERE bs.seriesId = ? AND (mp.isFinished IS NULL OR mp.isFinished = 0)` + permission + ` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST`
		bookRows, err := h.db.QueryContext(ctx, q, append([]any{user.ID, s.id}, permissionArgs...)...)
		if err != nil {
			return nil, 0, err
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
				return nil, 0, err
			}
			candidates = append(candidates, c)
		}
		if err := bookRows.Err(); err != nil {
			bookRows.Close()
			return nil, 0, err
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
		chosen := candidates[index]
		item, err := h.ItemMinifiedJSON(ctx, chosen.itemID)
		if err != nil {
			return nil, 0, err
		}
		// The continue-series and discover queries only load feeds; they never
		// set mediaItemShare (libraryItemsBookFilters.js:707-826, 886-1017).
		if err := h.attachItemIncludes(ctx, itemIncludes{rssFeed: includeHas(r, "rssfeed")}, chosen.itemID, item["media"], func(k string, v any) { item[k] = v }); err != nil {
			return nil, 0, err
		}
		name := s.name
		withBookMinified(item, func(media *wire.BookMinified) {
			// book.series = [] before toOldJSONMinified, then metadata.series
			// is replaced with the continued series.
			media.Metadata.SeriesName = ""
			media.Metadata.Series = &wire.BookSeries{ID: s.id, Name: name, Sequence: chosen.sequence}
		})
		items = append(items, item)
	}
	return items, count, nil
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

// recentSeriesShelf mirrors libraryFilters.getSeriesMostRecentlyAdded
// (server/utils/queries/libraryFilters.js:207-317); upstream asks for 5.
func (h *Handler) recentSeriesShelf(r *http.Request, user store.User, libraryID string, includeRSS bool) ([]any, int, error) {
	ctx := r.Context()
	settings := h.bookLibrarySettings(ctx, libraryID)
	permission, permissionArgs := userMediaClause(user, "book")
	where := ` FROM series s WHERE s.libraryId = ? AND s.createdAt >= ?`
	args := []any{libraryID, sixtyDaysAgo()}
	if settings.HideSingleBookSeries {
		where += ` AND (SELECT count(*) FROM books b, bookSeries bs WHERE bs.seriesId = s.id AND bs.bookId = b.id) > 1`
	}
	if permission != "" {
		where += ` AND (SELECT count(*) FROM books b, bookSeries bs WHERE bs.seriesId = s.id AND bs.bookId = b.id` + permission + `) > 0`
		args = append(args, permissionArgs...)
	}
	var count int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT s.id, s.name, s.description, s.createdAt, s.updatedAt, s.libraryId`+where+` ORDER BY s.createdAt DESC LIMIT 5`, args...)
	if err != nil {
		return nil, 0, err
	}
	entries := make([]wire.SeriesShelfEntry, 0)
	for rows.Next() {
		var e wire.SeriesShelfEntry
		var created, updated any
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &created, &updated, &e.LibraryID); err != nil {
			rows.Close()
			return nil, 0, err
		}
		e.AddedAt, _ = dbtime.OptionalMillis(created)
		e.UpdatedAt, _ = dbtime.OptionalMillis(updated)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()

	out := make([]any, 0, len(entries))
	for _, e := range entries {
		if e.Name != nil {
			prefixed := h.titlePrefix(ctx, *e.Name)
			e.NameIgnorePrefix = &prefixed
		}
		if includeRSS {
			if feed, ok, err := h.feedMinified(ctx, "", e.ID); err != nil {
				return nil, 0, err
			} else if ok {
				e.RSSFeed = feed
			}
		}
		bookRows, err := h.db.QueryContext(ctx, `SELECT li.id, bs.sequence FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE bs.seriesId = ?`+permission, append([]any{e.ID}, permissionArgs...)...)
		if err != nil {
			return nil, 0, err
		}
		type member struct {
			itemID   string
			sequence *string
		}
		members := make([]member, 0)
		for bookRows.Next() {
			var m member
			if err := bookRows.Scan(&m.itemID, &m.sequence); err != nil {
				bookRows.Close()
				return nil, 0, err
			}
			members = append(members, m)
		}
		if err := bookRows.Err(); err != nil {
			bookRows.Close()
			return nil, 0, err
		}
		bookRows.Close()
		// a.sequence.localeCompare(b.sequence, undefined, {numeric: true,
		// sensitivity: 'base'}); a missing sequence sorts last.
		sort.SliceStable(members, func(i, j int) bool {
			a, b := members[i].sequence, members[j].sequence
			if a == nil || *a == "" {
				return false
			}
			if b == nil || *b == "" {
				return true
			}
			return naturalCompare(*a, *b) < 0
		})
		e.Books = make([]any, 0, len(members))
		for _, m := range members {
			item, err := h.ItemMinifiedJSON(ctx, m.itemID)
			if err != nil {
				return nil, 0, err
			}
			withBookMinified(item, func(media *wire.BookMinified) {
				media.Metadata.AuthorName = ""
				media.Metadata.AuthorNameLF = ""
				media.Metadata.SeriesName = ""
			})
			e.Books = append(e.Books, item)
		}
		out = append(out, e)
	}
	return out, count, nil
}

// naturalCompare approximates ICU localeCompare with numeric collation and
// base sensitivity: digit runs compare numerically, other text
// case-insensitively.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, ra := leadingDigits(a)
		db, rb := leadingDigits(b)
		if da != "" && db != "" {
			na, _ := strconv.ParseFloat(da, 64)
			nb, _ := strconv.ParseFloat(db, 64)
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			a, b = ra, rb
			continue
		}
		ca, cb := unicode.ToLower(rune(a[0])), unicode.ToLower(rune(b[0]))
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func leadingDigits(s string) (string, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], s[i:]
}

// discoverShelf mirrors libraryItemsBookFilters.getDiscoverLibraryItems
// (server/utils/queries/libraryItemsBookFilters.js:850-1010): random books the
// user has not started, taking only the first book of not-started series.
func (h *Handler) discoverShelf(r *http.Request, user store.User, libraryID string, limit int) ([]any, int, error) {
	ctx := r.Context()
	permission, permissionArgs := userMediaClause(user, "book")
	seriesIDs, err := h.queryStrings(ctx, `SELECT s.id FROM series s WHERE s.libraryId = ? AND (SELECT count(*) FROM bookSeries bs LEFT OUTER JOIN mediaProgresses mp ON mp.mediaItemId = bs.bookId WHERE bs.seriesId = s.id AND mp.userId = ? AND (mp.isFinished = 1 OR mp.currentTime > 0)) = 0 ORDER BY RANDOM() LIMIT ?`, libraryID, user.ID, limit)
	if err != nil {
		return nil, 0, err
	}
	firstBooks := make([]any, 0, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		ids, err := h.queryStrings(ctx, `SELECT b.id FROM bookSeries bs JOIN books b ON b.id = bs.bookId WHERE bs.seriesId = ?`+permission+` ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST LIMIT 1`, append([]any{seriesID}, permissionArgs...)...)
		if err != nil {
			return nil, 0, err
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
	args := append([]any{libraryID, user.ID}, firstBooks...)
	args = append(args, permissionArgs...)
	var count int
	if err := h.db.QueryRowContext(ctx, `SELECT count(DISTINCT b.id)`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	itemIDs, err := h.queryStrings(ctx, `SELECT li.id`+where+` GROUP BY b.id ORDER BY RANDOM() LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	items := make([]any, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, err := h.ItemMinifiedJSON(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		if err := h.attachItemIncludes(ctx, itemIncludes{rssFeed: includeHas(r, "rssfeed")}, id, item["media"], func(k string, v any) { item[k] = v }); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, count, nil
}

// newestAuthorsShelf mirrors libraryFilters.getNewestAuthors
// (server/utils/queries/libraryFilters.js:334-368).
func (h *Handler) newestAuthorsShelf(r *http.Request, user store.User, libraryID string, limit int) ([]wire.AuthorShelfEntry, int, error) {
	ctx := r.Context()
	permission, permissionArgs := userMediaClause(user, "book")
	books := `(SELECT count(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id = ba.bookId WHERE ba.authorId = a.id` + permission + `)`
	where := ` FROM authors a WHERE a.libraryId = ? AND a.createdAt >= ? AND ` + books + ` > 0`
	args := append([]any{libraryID, sixtyDaysAgo()}, permissionArgs...)
	var count int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT a.id, a.asin, a.name, a.description, a.imagePath, a.libraryId, a.createdAt, a.updatedAt, `+books+where+` ORDER BY a.createdAt DESC LIMIT ?`, append(append(append([]any{}, permissionArgs...), args...), limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	authors := make([]wire.AuthorShelfEntry, 0)
	for rows.Next() {
		var a wire.AuthorShelfEntry
		var created, updated any
		if err := rows.Scan(&a.ID, &a.ASIN, &a.Name, &a.Description, &a.ImagePath, &a.LibraryID, &created, &updated, &a.NumBooks); err != nil {
			return nil, 0, err
		}
		a.AddedAt, _ = dbtime.OptionalMillis(created)
		a.UpdatedAt, _ = dbtime.OptionalMillis(updated)
		authors = append(authors, a)
	}
	return authors, count, rows.Err()
}
