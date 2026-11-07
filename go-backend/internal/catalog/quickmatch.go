// Quick match mirroring server/scanner/Scanner.js (pinned v2.37.2)
// quickMatchLibraryItem and matchLibraryItems, and the routes that drive it:
// POST /api/items/:id/match, POST /api/items/batch/quickmatch and
// GET /api/libraries/:id/matchall. The provider searches are injected
// (BookFinder), the podcast episode matching is the scanner's.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// BookFinder is the provider search behind a quick match (BookFinder.search,
// PodcastFinder.search).
type BookFinder interface {
	SearchBooks(ctx context.Context, query wire.BookSearch) []wire.BookMatch
	SearchPodcasts(ctx context.Context, term string) []wire.PodcastMatch
}

// quickMatchOptions is Scanner.QuickMatchOptions (Scanner.js:14-22).
type quickMatchOptions struct {
	provider, title, author, isbn, asin string
	overrideCover, overrideDetails      *bool
}

func optionalBool(v bool) *bool { return &v }

// jsTruthyRaw is JavaScript truthiness of a JSON value (absent counts as undefined).
func jsTruthyRaw(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	return json.Unmarshal(raw, &v) == nil && jsTruthy(v)
}

// matchString is a match field as a string: strings as they are, numbers as JS prints them.
func matchString(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	if v == nil {
		return ""
	}
	return jsString(v)
}

// matchList is `Array.isArray(v) ? [...v] : v.split(',').map(trim).filter(Boolean)`.
func matchList(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var out []string
	for _, part := range strings.Split(matchString(raw), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// quickMatchItem is the stored state of the item being matched.
type quickMatchItem struct {
	id, libraryID, path, mediaType, mediaID string
	isFile                                  bool
	media                                   storedRow
	authors                                 []quickMatchAuthor
	series                                  []quickMatchSeries
}

type quickMatchAuthor struct{ id, name string }
type quickMatchSeries struct {
	id, name string
	sequence sql.NullString
}

func (h *Handler) loadQuickMatchItem(ctx context.Context, itemID string) (quickMatchItem, error) {
	item := quickMatchItem{id: itemID}
	var isFile sql.NullInt64
	if err := h.db.QueryRowContext(ctx, `SELECT libraryId,path,mediaType,mediaId,isFile FROM libraryItems WHERE id=?`, itemID).Scan(&item.libraryID, &item.path, &item.mediaType, &item.mediaID, &isFile); err != nil {
		return item, err
	}
	item.isFile = isFile.Int64 != 0
	table := "books"
	if item.mediaType == "podcast" {
		table = "podcasts"
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if item.media, err = rowColumns(ctx, tx, table, item.mediaID); err != nil {
		return item, err
	}
	if item.mediaType != "book" {
		return item, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId=? ORDER BY ba.createdAt,ba.rowid`, item.mediaID)
	if err != nil {
		return item, err
	}
	for rows.Next() {
		var a quickMatchAuthor
		if err := rows.Scan(&a.id, &a.name); err != nil {
			rows.Close()
			return item, err
		}
		item.authors = append(item.authors, a)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId=? ORDER BY bs.createdAt,bs.rowid`, item.mediaID)
	if err != nil {
		return item, err
	}
	for rows.Next() {
		var s quickMatchSeries
		if err := rows.Scan(&s.id, &s.name, &s.sequence); err != nil {
			rows.Close()
			return item, err
		}
		item.series = append(item.series, s)
	}
	rows.Close()
	return item, nil
}

func (m quickMatchItem) authorName() string {
	names := make([]string, len(m.authors))
	for i, a := range m.authors {
		names[i] = a.name
	}
	return strings.Join(names, ", ")
}

func (m quickMatchItem) seriesName() string {
	names := make([]string, len(m.series))
	for i, s := range m.series {
		names[i] = s.name
		if s.sequence.Valid && s.sequence.String != "" {
			names[i] += " #" + s.sequence.String
		}
	}
	return strings.Join(names, ", ")
}

func storedText(v any) string {
	if s, ok := storedString(v).(string); ok {
		return s
	}
	return ""
}

func storedNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}

// serverSettings reads the server-settings row.
func (h *Handler) serverSettings(ctx context.Context) map[string]json.RawMessage {
	var raw []byte
	settings := map[string]json.RawMessage{}
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	return settings
}

func settingTrue(settings map[string]json.RawMessage, key string) bool {
	return string(settings[key]) == "true"
}

// saveCoverFromURL is CoverManager.downloadCoverFromUrlNew (CoverManager.js:
// 300-345): the cover is stored with the item when the server setting says so,
// otherwise under the metadata items folder.
func (h *Handler) saveCoverFromURL(ctx context.Context, itemID, itemPath string, withItem bool, imageURL string) (string, string) {
	coverDir := filepath.Join(h.cfg.MetadataPath, "items", itemID)
	if withItem && itemPath != "" {
		coverDir = itemPath
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return "", "Failed to fetch image from url"
	}
	tempPath := filepath.Join(coverDir, "cover")
	ext, message := downloadCoverFromURL(ctx, imageURL, tempPath)
	if message != "" {
		return "", message
	}
	coverFullPath := filepath.Join(coverDir, "cover."+ext)
	if err := os.Rename(tempPath, coverFullPath); err != nil {
		return "", "Failed to fetch image from url"
	}
	removeOldCovers(coverDir, "."+ext)
	h.purgeCoverCache(itemID)
	logging.Infof(`[CoverManager] Downloaded libraryItem cover "%s" from url "%s"`, coverFullPath, imageURL)
	return coverFullPath, ""
}

// quickMatchLibraryItem is Scanner.quickMatchLibraryItem (Scanner.js:37-137).
func (h *Handler) quickMatchLibraryItem(ctx context.Context, itemID string, options quickMatchOptions) (wire.QuickMatchResult, error) {
	item, err := h.loadQuickMatchItem(ctx, itemID)
	if err != nil {
		return wire.QuickMatchResult{}, err
	}
	settings := h.serverSettings(ctx)
	provider := options.provider
	if provider == "" {
		provider = "google"
	}
	searchTitle := options.title
	if searchTitle == "" {
		searchTitle = storedText(item.media.get("title"))
	}
	searchAuthor := options.author
	if searchAuthor == "" && item.mediaType == "book" {
		searchAuthor = item.authorName()
	}
	if options.overrideCover == nil && options.overrideDetails == nil && settingTrue(settings, "scannerPreferMatchedMetadata") {
		options.overrideCover, options.overrideDetails = optionalBool(true), optionalBool(true)
	}
	overrideCover := options.overrideCover != nil && *options.overrideCover
	overrideDetails := options.overrideDetails != nil && *options.overrideDetails
	withItem := settingTrue(settings, "storeCoverWithItem")

	columns := &quickMatchColumns{current: item.media}
	hasUpdated := false
	coverPath := ""
	var tx *sql.Tx
	finish := func() {
		if tx != nil {
			tx.Rollback()
		}
	}
	defer finish()
	// Read before the transaction: the database may be limited to one connection.
	prefixes := h.sortingPrefixes(ctx)
	var events []syncEvent
	var removedAuthors, removedSeries []string
	var addedAuthors, addedSeries []filterdata.Named

	switch item.mediaType {
	case "book":
		isbn, asin := options.isbn, options.asin
		if isbn == "" {
			isbn = storedText(item.media.get("isbn"))
		}
		if asin == "" {
			asin = storedText(item.media.get("asin"))
		}
		results := h.finder.SearchBooks(ctx, wire.BookSearch{Provider: provider, Title: searchTitle, Author: searchAuthor, ISBN: isbn, ASIN: asin, Duration: storedNumber(item.media.get("duration")), MaxFuzzySearches: 2})
		if len(results) == 0 {
			warning := fmt.Sprintf("No %s match found", provider)
			return wire.QuickMatchResult{Warning: &warning}, nil
		}
		match := results[0]
		if cover := matchString(match.Cover); jsTruthyRaw(match.Cover) && (storedText(item.media.get("coverPath")) == "" || overrideCover) {
			logging.Debugf(`[Scanner] Updating cover "%s"`, cover)
			path := item.path
			if item.isFile {
				path = ""
			}
			if saved, message := h.saveCoverFromURL(ctx, item.id, path, withItem, cover); message != "" {
				logging.Warnf(`[Scanner] Match cover "%s" failed to use: %s`, cover, message)
			} else {
				coverPath, hasUpdated = saved, true
			}
		}
		var err error
		if tx, err = h.db.BeginTx(ctx, nil); err != nil {
			return wire.QuickMatchResult{}, err
		}
		columns.bookDetails(match, overrideDetails)
		authorsChanged, e := h.quickMatchAuthors(ctx, tx, item, match, overrideDetails, &events, &removedAuthors, &addedAuthors)
		if e != nil {
			return wire.QuickMatchResult{}, e
		}
		seriesChanged, e := h.quickMatchSeries(ctx, tx, item, match, overrideDetails, prefixes, &events, &removedSeries, &addedSeries)
		if e != nil {
			return wire.QuickMatchResult{}, e
		}
		hasUpdated = hasUpdated || authorsChanged || seriesChanged
	case "podcast":
		results := h.finder.SearchPodcasts(ctx, searchTitle)
		if len(results) == 0 {
			warning := fmt.Sprintf("No %s match found", provider)
			return wire.QuickMatchResult{Warning: &warning}, nil
		}
		match := results[0]
		if cover := matchString(match.Cover); jsTruthyRaw(match.Cover) && (storedText(item.media.get("coverPath")) == "" || overrideCover) {
			logging.Debugf(`[Scanner] Updating cover "%s"`, cover)
			if saved, message := h.saveCoverFromURL(ctx, item.id, item.path, withItem, cover); message != "" {
				logging.Warnf(`[Scanner] Match cover "%s" failed to use: %s`, cover, message)
			} else {
				coverPath, hasUpdated = saved, true
			}
		}
		var err error
		if tx, err = h.db.BeginTx(ctx, nil); err != nil {
			return wire.QuickMatchResult{}, err
		}
		columns.podcastDetails(match, overrideDetails)
	default:
		return wire.QuickMatchResult{}, fmt.Errorf("unsupported media type %q", item.mediaType)
	}

	changed := columns.changed()
	if coverPath != "" {
		changed = append(changed, quickMatchColumn{"coverPath", coverPath})
	}
	if len(columns.set) > 0 && len(columns.changed()) > 0 {
		logging.Debugf(`[Scanner] Updating library item "%s" keys %v`, storedText(item.media.get("title")), columns.changedNames())
		hasUpdated = true
	}
	if hasUpdated {
		if len(changed) > 0 {
			sets := make([]string, 0, len(changed)+1)
			args := make([]any, 0, len(changed)+2)
			for _, c := range changed {
				sets = append(sets, `"`+c.name+`"=?`)
				args = append(args, c.value)
			}
			table := "books"
			if item.mediaType == "podcast" {
				table = "podcasts"
			}
			args = append(args, dbNow(), item.mediaID)
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ",")+`,"updatedAt"=? WHERE id=?`, args...); err != nil {
				return wire.QuickMatchResult{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), item.id); err != nil {
			return wire.QuickMatchResult{}, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return wire.QuickMatchResult{}, err
		}
		tx = nil
	}
	if hasUpdated && item.mediaType == "podcast" && storedText(item.media.get("feedURL")) != "" && h.episodes != nil {
		// Quick match all unmatched podcast episodes (Scanner.js:113-116); the
		// episodes do not depend on the podcast row, so this follows the commit.
		if n, err := h.episodes(ctx, item.id, overrideDetails); err != nil {
			logging.Errorf("[Scanner] quickMatchPodcastEpisodes: %v", err)
		} else if n > 0 {
			logging.Infof(`[Scanner] quickMatchPodcastEpisodes: Updated %d episodes for "%s"`, n, storedText(item.media.get("title")))
		}
	}
	// Events and cleanups follow the order of the source: author_added and
	// series_added while matching, then the emptied authors and series.
	for _, au := range addedAuthors {
		h.filters.AddAuthor(item.libraryID, au.Name, au.ID)
	}
	for _, se := range addedSeries {
		h.filters.AddSeries(item.libraryID, se.Name, se.ID)
	}
	if h.emit != nil {
		for _, event := range events {
			h.emit("*", event.name, event.payload)
		}
	}
	h.checkRemoveAuthorsWithNoBooks(ctx, removedAuthors)
	h.checkRemoveEmptySeries(ctx, removedSeries)
	if hasUpdated {
		if err := h.saveMetadataFiles(ctx, []string{item.id}); err != nil {
			return wire.QuickMatchResult{}, err
		}
		if expanded, err := h.ItemJSON(ctx, item.id, true); err == nil && h.emit != nil {
			h.emit("*", "item_updated", expanded)
		}
	}
	expanded, err := h.ItemJSON(ctx, item.id, true)
	if err != nil {
		return wire.QuickMatchResult{}, err
	}
	raw, err := json.Marshal(expanded)
	if err != nil {
		return wire.QuickMatchResult{}, err
	}
	return wire.QuickMatchResult{Updated: &hasUpdated, LibraryItem: raw}, nil
}

type quickMatchColumn struct {
	name  string
	value any
}

// quickMatchColumns collects the update payload and tells which values really
// differ from the stored row (Sequelize's set()/changed()).
type quickMatchColumns struct {
	current storedRow
	set     []quickMatchColumn
}

func (c *quickMatchColumns) put(name string, value any) {
	c.set = append(c.set, quickMatchColumn{name, value})
}

func (c *quickMatchColumns) changed() []quickMatchColumn {
	var out []quickMatchColumn
	for _, s := range c.set {
		stored := c.current.get(s.name)
		switch v := s.value.(type) {
		case []string: // JSON columns compare by content
			if storedJSONText(stored) == jsonValue(v) {
				continue
			}
			out = append(out, quickMatchColumn{s.name, jsonValue(v)})
		case bool:
			if storedBool(stored) == v {
				continue
			}
			out = append(out, s)
		default:
			if reflect.DeepEqual(storedString(stored), v) {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

func (c *quickMatchColumns) changedNames() []string {
	var names []string
	for _, ch := range c.changed() {
		names = append(names, ch.name)
	}
	return names
}

// bookDetails is the details part of quickMatchBookBuildUpdatePayload
// (Scanner.js:214-253): a key is taken when the book has none yet, or when the
// details are overridden.
func (c *quickMatchColumns) bookDetails(match wire.BookMatch, override bool) {
	stored := func(key string) bool { return jsTruthy(storedValue(c.current.get(key))) }
	text := func(key string, raw json.RawMessage) {
		if jsTruthyRaw(raw) && (!stored(key) || override) {
			c.put(key, matchString(raw))
		}
	}
	text("title", match.Title)
	text("subtitle", match.Subtitle)
	text("description", match.Description)
	if jsTruthyRaw(match.Narrator) {
		if storedJSONText(c.current.get("narrators")) == "[]" || storedJSONText(c.current.get("narrators")) == "null" || override {
			c.put("narrators", orEmpty(matchList(match.Narrator)))
		}
	}
	text("publisher", match.Publisher)
	text("publishedYear", match.PublishedYear)
	if jsTruthyRaw(match.Genres) && (storedJSONText(c.current.get("genres")) == "[]" || override) {
		c.put("genres", orEmpty(matchList(match.Genres)))
	}
	if jsTruthyRaw(match.Tags) && (storedJSONText(c.current.get("tags")) == "[]" || override) {
		c.put("tags", orEmpty(matchList(match.Tags)))
	}
	text("language", match.Language)
	for key, raw := range map[string]json.RawMessage{"explicit": match.Explicit, "abridged": match.Abridged} {
		if jsTruthyRaw(raw) && (!stored(key) || override) {
			c.put(key, true)
		}
	}
	text("asin", match.ASIN)
	text("isbn", match.ISBN)
}

// podcastDetails is quickMatchPodcastBuildUpdatePayload (Scanner.js:145-182).
func (c *quickMatchColumns) podcastDetails(match wire.PodcastMatch, override bool) {
	keys := []struct {
		column string
		value  json.RawMessage
	}{{"title", match.Title}, {"author", match.ArtistName}, {"genres", match.Genres}, {"itunesId", match.ID}, {"itunesPageURL", match.PageURL}, {"itunesArtistId", match.ArtistID}, {"releaseDate", match.ReleaseDate}, {"imageURL", match.Cover}, {"feedURL", match.FeedURL}, {"description", match.DescriptionPlain}}
	for _, k := range keys {
		if !jsTruthyRaw(k.value) {
			continue
		}
		if k.column == "genres" {
			if storedJSONText(c.current.get("genres")) == "[]" || override {
				c.put("genres", orEmpty(matchList(k.value)))
			}
			continue
		}
		value := matchString(k.value)
		if storedText(c.current.get(k.column)) != value && (storedText(c.current.get(k.column)) == "" || override) {
			c.put(k.column, value)
		}
	}
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// storedValue converts a stored column to the JavaScript value used for truthiness.
func storedValue(v any) any {
	switch x := storedString(v).(type) {
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return x
	}
}

// quickMatchAuthors is the author half of quickMatchBookBuildUpdatePayload
// (Scanner.js:255-308): authors of the match that the book lacks are created
// and linked, authors the match does not list are unlinked.
func (h *Handler) quickMatchAuthors(ctx context.Context, tx *sql.Tx, item quickMatchItem, match wire.BookMatch, override bool, events *[]syncEvent, removed *[]string, added *[]filterdata.Named) (bool, error) {
	if !jsTruthyRaw(match.Author) || (item.authorName() != "" && !override) {
		return false, nil
	}
	names := matchList(match.Author)
	current := append([]quickMatchAuthor(nil), item.authors...)
	var clock insertClock
	changed := false
	for _, name := range names {
		known := false
		for _, a := range current {
			if strings.EqualFold(a.name, name) {
				known = true
				break
			}
		}
		if !known {
			var id string
			err := tx.QueryRowContext(ctx, `SELECT id FROM authors WHERE lower(name)=? AND libraryId=? LIMIT 1`, strings.ToLower(name), item.libraryID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				id = makeUUID()
				now := clock.now()
				if _, err = tx.ExecContext(ctx, `INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, nameparse.ToLastFirst(name), item.libraryID, now, now); err != nil {
					return false, err
				}
				payload, e := authorPayload(ctx, tx, id, true, 0)
				if e != nil {
					return false, e
				}
				*events = append(*events, syncEvent{"author_added", payload})
				*added = append(*added, filterdata.Named{ID: id, Name: name})
			} else if err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, makeUUID(), item.mediaID, id, clock.now()); err != nil {
				return false, err
			}
			logging.Infof(`[Scanner] quickMatchBookBuildUpdatePayload: Added author "%s" to "%s"`, name, storedText(item.media.get("title")))
			current = append(current, quickMatchAuthor{id: id, name: name})
			changed = true
		}
		// The removal runs inside the loop upstream; it is idempotent.
		var keep []quickMatchAuthor
		for _, a := range current {
			listed := false
			for _, n := range names {
				if strings.EqualFold(n, a.name) {
					listed = true
					break
				}
			}
			if listed {
				keep = append(keep, a)
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE authorId=? AND bookId=?`, a.id, item.mediaID); err != nil {
				return false, err
			}
			*removed = append(*removed, a.id)
			logging.Infof(`[Scanner] quickMatchBookBuildUpdatePayload: Removed author "%s" from "%s"`, a.name, storedText(item.media.get("title")))
			changed = true
		}
		current = keep
	}
	return changed, nil
}

// matchSeriesEntry is one series of a match: the series name and its sequence
// (a string, a number, or absent).
type matchSeriesEntry struct {
	name     string
	sequence json.RawMessage
}

func matchSeries(match wire.BookMatch) []matchSeriesEntry {
	var list []struct {
		Series   string          `json:"series"`
		Sequence json.RawMessage `json:"sequence"`
	}
	if json.Unmarshal(match.Series, &list) == nil {
		out := make([]matchSeriesEntry, len(list))
		for i, e := range list {
			out[i] = matchSeriesEntry{e.Series, e.Sequence}
		}
		return out
	}
	return []matchSeriesEntry{{matchString(match.Series), match.Sequence}}
}

// quickMatchSeries is the series half of quickMatchBookBuildUpdatePayload
// (Scanner.js:310-364).
func (h *Handler) quickMatchSeries(ctx context.Context, tx *sql.Tx, item quickMatchItem, match wire.BookMatch, override bool, prefixes []string, events *[]syncEvent, removed *[]string, added *[]filterdata.Named) (bool, error) {
	if !jsTruthyRaw(match.Series) || (item.seriesName() != "" && !override) {
		return false, nil
	}
	entries := matchSeries(match)
	current := append([]quickMatchSeries(nil), item.series...)
	var clock insertClock
	changed := false
	for _, entry := range entries {
		sequence := sql.NullString{}
		if jsTruthyRaw(entry.sequence) || string(entry.sequence) == `""` {
			sequence = sql.NullString{String: matchString(entry.sequence), Valid: true}
		}
		var existing *quickMatchSeries
		for i := range current {
			if strings.EqualFold(current[i].name, entry.name) {
				existing = &current[i]
				break
			}
		}
		if existing != nil {
			// existingSeries.bookSeries.sequence !== seriesMatchItem.sequence
			if existing.sequence != sequence {
				if _, err := tx.ExecContext(ctx, `UPDATE bookSeries SET sequence=? WHERE bookId=? AND seriesId=?`, store.NullString(sequence), item.mediaID, existing.id); err != nil {
					return false, err
				}
				existing.sequence = sequence
				logging.Infof(`[Scanner] quickMatchBookBuildUpdatePayload: Updated series sequence for "%s" to %s in "%s"`, existing.name, sequence.String, storedText(item.media.get("title")))
				changed = true
			}
		} else {
			var id string
			err := tx.QueryRowContext(ctx, `SELECT id FROM series WHERE lower(name)=? AND libraryId=? LIMIT 1`, strings.ToLower(entry.name), item.libraryID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				id = makeUUID()
				now := clock.now()
				if _, err = tx.ExecContext(ctx, `INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, entry.name, titleIgnorePrefix(entry.name, prefixes), item.libraryID, now, now); err != nil {
					return false, err
				}
				*events = append(*events, syncEvent{"series_added", wire.SeriesCreated{ID: id, Name: entry.name, NameIgnorePrefix: catalogTitlePrefixAtEnd(entry.name, prefixes), AddedAt: dbTimeMillis(now), UpdatedAt: dbTimeMillis(now), LibraryID: item.libraryID}})
				*added = append(*added, filterdata.Named{ID: id, Name: entry.name})
			} else if err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO bookSeries(id,bookId,seriesId,sequence,createdAt) VALUES(?,?,?,?,?)`, makeUUID(), item.mediaID, id, store.NullString(sequence), clock.now()); err != nil {
				return false, err
			}
			logging.Infof(`[Scanner] quickMatchBookBuildUpdatePayload: Added series "%s" to "%s"`, entry.name, storedText(item.media.get("title")))
			current = append(current, quickMatchSeries{id: id, name: entry.name, sequence: sequence})
			changed = true
		}
		var keep []quickMatchSeries
		for _, s := range current {
			listed := false
			for _, e := range entries {
				if strings.EqualFold(e.name, s.name) {
					listed = true
					break
				}
			}
			if listed {
				keep = append(keep, s)
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=? AND seriesId=?`, item.mediaID, s.id); err != nil {
				return false, err
			}
			*removed = append(*removed, s.id)
			logging.Infof(`[Scanner] quickMatchBookBuildUpdatePayload: Removed series "%s" from "%s"`, s.name, storedText(item.media.get("title")))
			changed = true
		}
		current = keep
	}
	return changed, nil
}

func dbTimeMillis(value string) int64 {
	n, _ := dbtime.OptionalMillis(value)
	if n == nil {
		return 0
	}
	return *n
}

// matchOptionsFromBody reads the string overrides and the two booleans of a
// match request (LibraryItemController.js:526-545).
func matchOptionsFromBody(body map[string]json.RawMessage, keys ...string) quickMatchOptions {
	var options quickMatchOptions
	str := func(key string) string {
		var s string
		if json.Unmarshal(body[key], &s) == nil {
			return s
		}
		return ""
	}
	for _, key := range keys {
		switch key {
		case "provider":
			options.provider = str(key)
		case "title":
			options.title = str(key)
		case "author":
			options.author = str(key)
		case "isbn":
			options.isbn = str(key)
		case "asin":
			options.asin = str(key)
		}
	}
	for key, target := range map[string]**bool{"overrideCover": &options.overrideCover, "overrideDetails": &options.overrideDetails} {
		if raw, present := body[key]; present {
			var v any
			_ = json.Unmarshal(raw, &v)
			*target = optionalBool(jsTruthy(v))
		}
	}
	return options
}

// itemMatch is LibraryItemController.match (LibraryItemController.js:526-548)
// behind LibraryItemController.middleware (:1220-1249).
func (h *Handler) itemMatch(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	id := r.PathValue("id")
	var libraryID, mediaType, mediaID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId FROM libraryItems WHERE id=?`, id).Scan(&libraryID, &mediaType, &mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	allowed, err := h.userCanAccessItems(r.Context(), user, []batchItem{{id: id, libraryID: libraryID, mediaType: mediaType, mediaID: mediaID}})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	if !userAllows(user, "update") {
		logging.Warnf(`[LibraryItemController] User "%s" attempted to update without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	options := matchOptionsFromBody(jsonBodyObject(r), "provider", "title", "author", "isbn", "asin")
	result, err := h.quickMatchLibraryItem(r.Context(), id, options)
	if err != nil {
		return httputil.Internal("internal error", err)
	}
	return writeJSONWithETag(w, r, http.StatusOK, result)
}

// batchQuickMatch is LibraryItemController.batchQuickMatch (:763-812): admins
// only; the request is answered at once and the matching runs on, ending with
// batch_quickmatch_complete to the requesting user.
func (h *Handler) batchQuickMatch(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	if !adminUser(user) {
		logging.Warnf(`Non-admin user "%s" other than admin attempted to batch quick match library items`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	body := jsonBodyObject(r)
	var ids []string
	var rawIDs []json.RawMessage
	if json.Unmarshal(body["libraryItemIds"], &rawIDs) != nil || len(rawIDs) == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	for _, raw := range rawIDs {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			ids = append(ids, s)
		}
	}
	items, err := h.itemsExpandedWhere(r.Context(), ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(items) == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	var optionBody map[string]json.RawMessage
	_ = json.Unmarshal(body["options"], &optionBody)
	options := matchOptionsFromBody(optionBody, "provider")
	h.background(func(ctx context.Context) {
		updated, unmatched := 0, 0
		for _, item := range items {
			result, err := h.quickMatchLibraryItem(ctx, item.id, options)
			if err != nil {
				logging.Errorf("[LibraryItemController] batch quick match %s: %v", item.id, err)
				continue
			}
			if result.Updated != nil && *result.Updated {
				updated++
			} else if result.Warning != nil {
				unmatched++
			}
		}
		if h.emit != nil {
			h.emit(user.ID, "batch_quickmatch_complete", wire.BatchQuickMatchResult{Success: updated > 0, Updates: updated, Unmatched: unmatched})
		}
	})
	return httputil.OK(w, r)
}

// libraryMatchAll is LibraryController.matchAll (LibraryController.js:1245-1254)
// over Scanner.matchLibraryItems (Scanner.js:472-548) and
// matchLibraryItemsChunk (:435-469).
func (h *Handler) libraryMatchAll(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	library, err := h.libraryAccess(r, user)
	if err != nil {
		return err
	}
	if !adminUser(user) {
		logging.Errorf(`[LibraryController] Non-root user "%s" attempted to match library items`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	h.background(func(ctx context.Context) { h.matchLibraryItems(ctx, library) })
	return httputil.OK(w, r)
}

func (h *Handler) matchLibraryItems(ctx context.Context, library libraryRecord) {
	if library.mediaType == "podcast" {
		logging.Errorf("[Scanner] matchLibraryItems: Match all not supported for podcasts yet")
		return
	}
	var settings struct {
		SkipMatchingMediaWithAsin bool `json:"skipMatchingMediaWithAsin"`
		SkipMatchingMediaWithIsbn bool `json:"skipMatchingMediaWithIsbn"`
	}
	_ = json.Unmarshal(library.settings, &settings)
	task := h.startTask(library)
	logging.Infof(`[Scanner] matchLibraryItems: Starting library match scan %s for %s`, task.scanID, library.name)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,b.title,b.asin,b.isbn FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=? ORDER BY li.createdAt`, library.id)
	if err != nil {
		logging.Errorf("[Scanner] matchLibraryItems: %v", err)
		return
	}
	type target struct {
		id, title, asin, isbn string
	}
	var targets []target
	for rows.Next() {
		var t target
		var title, asin, isbn sql.NullString
		if rows.Scan(&t.id, &title, &asin, &isbn) == nil {
			t.title, t.asin, t.isbn = title.String, asin.String, isbn.String
			targets = append(targets, t)
		}
	}
	rows.Close()
	updated := 0
	for i, t := range targets {
		if t.asin != "" && settings.SkipMatchingMediaWithAsin {
			logging.Debugf(`[Scanner] matchLibraryItems: Skipping "%s" because it already has an ASIN (%d of %d)`, t.title, i+1, len(targets))
			continue
		}
		if t.isbn != "" && settings.SkipMatchingMediaWithIsbn {
			logging.Debugf(`[Scanner] matchLibraryItems: Skipping "%s" because it already has an ISBN (%d of %d)`, t.title, i+1, len(targets))
			continue
		}
		logging.Debugf(`[Scanner] matchLibraryItems: Quick matching "%s" (%d of %d)`, t.title, i+1, len(targets))
		result, err := h.quickMatchLibraryItem(ctx, t.id, quickMatchOptions{provider: library.provider})
		if err != nil {
			logging.Errorf("[Scanner] matchLibraryItems: %v", err)
			continue
		}
		if result.Warning != nil {
			logging.Warnf(`[Scanner] matchLibraryItems: Match warning %s for library item "%s"`, *result.Warning, t.title)
		} else if result.Updated != nil && *result.Updated {
			updated++
		}
	}
	h.finishTask(task, len(targets), updated)
}
