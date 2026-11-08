// Quick match mirroring server/scanner/Scanner.js (pinned v2.37.2)
// quickMatchLibraryItem and matchLibraryItems, and the routes that drive it:
// POST /api/items/:id/match, POST /api/items/batch/quickmatch and
// GET /api/libraries/:id/matchall. The provider searches are injected
// (BookFinder), the podcast episode matching is the scanner's.
package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/wire"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

type quickMatchItem struct {
	itemScope
	media   storedRow
	authors []quickMatchAuthor
	series  []quickMatchSeries
}

type quickMatchAuthor struct{ id, name string }

type quickMatchSeries struct {
	id, name string
	sequence sql.NullString
}

func (h *Handler) loadQuickMatchItem(ctx context.Context, itemID string) (quickMatchItem, error) {
	var item quickMatchItem
	var err error
	if item.itemScope, err = scanItemScope(h.db.QueryRowContext(ctx, `SELECT `+itemScopeColumns+` FROM libraryItems li WHERE li.id=?`, itemID)); err != nil {
		return item, err
	}
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
func (h *Handler) quickMatchLibraryItem(ctx context.Context, itemID string, options quickMatchOptions) (quickMatchResult, error) {
	item, err := h.loadQuickMatchItem(ctx, itemID)
	if err != nil {
		return quickMatchResult{}, err
	}
	settings := serversettings.Load(ctx, h.db).Doc()
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
	if options.overrideCover == nil && options.overrideDetails == nil && settings["scannerPreferMatchedMetadata"] == true {
		options.overrideCover, options.overrideDetails = optionalBool(true), optionalBool(true)
	}
	overrideCover := options.overrideCover != nil && *options.overrideCover
	overrideDetails := options.overrideDetails != nil && *options.overrideDetails
	withItem := settings["storeCoverWithItem"] == true

	columns := newColumnSet(item.media)
	hasUpdated := false
	coverPath := ""
	var tx *sql.Tx
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	// Read before the transaction: the database may be limited to one connection.
	prefixes := h.sortingPrefixes(ctx)
	var changes itemwrite.Changes
	table := "books"

	switch item.mediaType {
	case "book":
		isbn, asin := options.isbn, options.asin
		if isbn == "" {
			isbn = storedText(item.media.get("isbn"))
		}
		if asin == "" {
			asin = storedText(item.media.get("asin"))
		}
		results := h.deps.Books.SearchBooks(ctx, wire.BookSearch{Provider: provider, Title: searchTitle, Author: searchAuthor, ISBN: isbn, ASIN: asin, Duration: jsonvalue.Float(item.media.get("duration")), MaxFuzzySearches: 2})
		if len(results) == 0 {
			warning := fmt.Sprintf("No %s match found", provider)
			return quickMatchResult{Warning: &warning}, nil
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
			return quickMatchResult{}, err
		}
		columns.bookDetails(match, overrideDetails)
		// Authors and series of the match replace the book's own only when it
		// has none or the details are overridden (Scanner.js:255-364).
		rules := itemwrite.Rules{Replace: true, SeriesAdded: true}
		if jsTruthyRaw(match.Author) && (item.authorName() == "" || overrideDetails) {
			changed, e := changes.LinkAuthors(ctx, tx, item.libraryID, item.mediaID, matchList(match.Author), rules)
			if e != nil {
				return quickMatchResult{}, e
			}
			hasUpdated = hasUpdated || changed
		}
		if jsTruthyRaw(match.Series) && (item.seriesName() == "" || overrideDetails) {
			changed, e := changes.LinkSeries(ctx, tx, item.libraryID, item.mediaID, matchSeries(match), prefixes, rules)
			if e != nil {
				return quickMatchResult{}, e
			}
			hasUpdated = hasUpdated || changed
		}
	case "podcast":
		table = "podcasts"
		results := h.deps.Books.SearchPodcasts(ctx, searchTitle)
		if len(results) == 0 {
			warning := fmt.Sprintf("No %s match found", provider)
			return quickMatchResult{Warning: &warning}, nil
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
			return quickMatchResult{}, err
		}
		columns.podcastDetails(match, overrideDetails)
	default:
		return quickMatchResult{}, fmt.Errorf("unsupported media type %q", item.mediaType)
	}

	if len(columns.set) > 0 {
		logging.Debugf(`[Scanner] Updating library item "%s" keys %v`, storedText(item.media.get("title")), columns.names())
		hasUpdated = true
	}
	if coverPath != "" {
		columns.set["coverPath"] = coverPath
	}
	if hasUpdated {
		if len(columns.set) > 0 {
			if err := columns.update(ctx, tx, table, item.mediaID); err != nil {
				return quickMatchResult{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbtime.Now(), item.id); err != nil {
			return quickMatchResult{}, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return quickMatchResult{}, err
		}
		tx = nil
	}
	if hasUpdated && item.mediaType == "podcast" && storedText(item.media.get("feedURL")) != "" && h.deps.PodcastEpisodes != nil {
		// Quick match all unmatched podcast episodes (Scanner.js:113-116); the
		// episodes do not depend on the podcast row, so this follows the commit.
		if n, err := h.deps.PodcastEpisodes(ctx, item.id, overrideDetails); err != nil {
			logging.Errorf("[Scanner] quickMatchPodcastEpisodes: %v", err)
		} else if n > 0 {
			logging.Infof(`[Scanner] quickMatchPodcastEpisodes: Updated %d episodes for "%s"`, n, storedText(item.media.get("title")))
		}
	}
	if err := h.finishItemWrites(ctx, itemWrite{itemID: item.id, libraryID: item.libraryID, changes: changes, updated: hasUpdated}); err != nil {
		return quickMatchResult{}, err
	}
	expanded, err := h.expandedItem(ctx, item.id)
	if err != nil {
		return quickMatchResult{}, err
	}
	raw, err := json.Marshal(expanded)
	if err != nil {
		return quickMatchResult{}, err
	}
	return quickMatchResult{Updated: &hasUpdated, LibraryItem: raw}, nil
}

// bookDetails is the details part of quickMatchBookBuildUpdatePayload
// (Scanner.js:214-253): a key is taken when the book has none yet, or when the
// details are overridden.
func (c *columnSet) bookDetails(match wire.BookMatch, override bool) {
	stored := func(key string) bool { return jsonvalue.Truthy(storedValue(c.current.get(key))) }
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
func (c *columnSet) podcastDetails(match wire.PodcastMatch, override bool) {
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

func matchSeries(match wire.BookMatch) []itemwrite.Series {
	var list []struct {
		Series   string          `json:"series"`
		Sequence json.RawMessage `json:"sequence"`
	}
	if json.Unmarshal(match.Series, &list) != nil {
		list = []struct {
			Series   string          `json:"series"`
			Sequence json.RawMessage `json:"sequence"`
		}{{matchString(match.Series), match.Sequence}}
	}
	out := make([]itemwrite.Series, len(list))
	for i, e := range list {
		out[i] = itemwrite.Series{Name: e.Series}
		if jsTruthyRaw(e.Sequence) {
			out[i].Sequence = matchString(e.Sequence)
		}
	}
	return out
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
			*target = optionalBool(jsonvalue.Truthy(v))
		}
	}
	return options
}

// itemMatch is LibraryItemController.match (LibraryItemController.js:526-548)
// behind LibraryItemController.middleware (:1220-1249).
func (h *Handler) itemMatch(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	id := r.PathValue("id")
	scope, err := h.loadItem(r.Context(), id)
	if err != nil {
		return err
	}
	allowed, err := h.userCanAccessItems(r.Context(), user, []itemScope{scope})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	if !access.For(r.Context(), user).Update() {
		logging.Warnf(`[LibraryItemController] User "%s" attempted to update without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	options := matchOptionsFromBody(jsonBodyObject(r), "provider", "title", "author", "isbn", "asin")
	result, err := h.quickMatchLibraryItem(r.Context(), id, options)
	if err != nil {
		return httputil.Internal("internal error", err)
	}
	return httputil.JSON(w, r, result)
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
	// The work continues after the response is written, the way the source
	// lets a request handler's promise continue (batchQuickMatch).
	go func() {
		ctx := context.Background()
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
		h.emit.ToUser(user.ID, "batch_quickmatch_complete", batchQuickMatchResult{Success: updated > 0, Updates: updated, Unmatched: unmatched})
	}()
	return httputil.OK(w, r)
}
