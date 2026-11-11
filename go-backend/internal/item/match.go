package item

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/wire"
)

// MatchOptions are the overrides of a quick match (Scanner.QuickMatchOptions,
// Scanner.js:14-22). Nil override flags fall back to the server setting
// scannerPreferMatchedMetadata.
type MatchOptions struct {
	Provider        string `json:"provider"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	ISBN            string `json:"isbn"`
	ASIN            string `json:"asin"`
	OverrideCover   *bool  `json:"overrideCover"`
	OverrideDetails *bool  `json:"overrideDetails"`
}

// MatchResult is the answer of Scanner.quickMatchLibraryItem (Scanner.js:37-137):
// {warning} when nothing matched, else {updated, libraryItem}.
type MatchResult struct {
	Warning     *string   `json:"warning,omitempty"`
	Updated     *bool     `json:"updated,omitempty"`
	LibraryItem *Expanded `json:"libraryItem,omitempty"`
}

// QuickMatch searches the metadata providers for the item and applies the best
// result to it (Scanner.quickMatchLibraryItem). Without override a field is
// taken only when the item has none yet. The provider result becomes a typed
// patch here and goes through the same write path as PATCH media.
func (h *Handler) QuickMatch(ctx context.Context, itemID string, opts MatchOptions) (MatchResult, error) {
	it, err := Get(ctx, h.db, itemID, Options{})
	if err != nil {
		return MatchResult{}, err
	}
	server := h.settings.Server()
	if opts.Provider == "" {
		opts.Provider = "google"
	}
	if opts.OverrideCover == nil && opts.OverrideDetails == nil && server.ScannerPreferMatchedData {
		opts.OverrideCover, opts.OverrideDetails = db.Ptr(true), db.Ptr(true)
	}
	overrideCover, overrideDetails := db.Val(opts.OverrideCover), db.Val(opts.OverrideDetails)
	ref := itemRef{ID: it.ID, LibraryID: db.Val(it.LibraryID), MediaType: db.Val(it.MediaType), MediaID: db.Val(it.MediaID), Path: db.Val(it.Path), IsFile: it.IsFile}
	noMatch := func() MatchResult {
		return MatchResult{Warning: db.Ptr(fmt.Sprintf("No %s match found", opts.Provider))}
	}

	var p patch
	var cover json.RawMessage
	var storedCover *string
	switch {
	case it.Book != nil:
		b := it.Book
		search := wire.BookSearch{Provider: opts.Provider, Title: firstNonEmpty(opts.Title, db.Val(b.Title)), Author: firstNonEmpty(opts.Author, bookAuthors(b)),
			ISBN: firstNonEmpty(opts.ISBN, db.Val(b.ISBN)), ASIN: firstNonEmpty(opts.ASIN, db.Val(b.ASIN)), Duration: db.Val(b.Duration), MaxFuzzySearches: 2}
		results := h.deps.Books.SearchBooks(ctx, search)
		if len(results) == 0 {
			return noMatch(), nil
		}
		bp := bookMatchPatch(b, results[0], overrideDetails)
		p, cover, storedCover = patch{book: &bp}, results[0].Cover, b.CoverPath
		if it.IsFile { // a single-file item has no folder to hold the cover
			ref.Path = ""
		}
	case it.Podcast != nil:
		results := h.deps.Books.SearchPodcasts(ctx, firstNonEmpty(opts.Title, db.Val(it.Podcast.Title)))
		if len(results) == 0 {
			return noMatch(), nil
		}
		pp := podcastMatchPatch(&it.Podcast.Podcast, results[0], overrideDetails)
		p, cover, storedCover = patch{podcast: &pp}, results[0].Cover, it.Podcast.CoverPath
	default:
		return MatchResult{}, fmt.Errorf("unsupported media type %q", ref.MediaType)
	}

	coverPath := h.matchCover(ctx, ref, server.StoreCoverWithItem, cover, storedCover, overrideCover)
	switch {
	case p.book != nil:
		p.book.coverPath = coverPath
	default:
		p.podcast.coverPath = coverPath
	}
	out, err := h.update(ctx, ref, p, fromMatch)
	if err != nil {
		return MatchResult{}, err
	}
	if out.updated && it.Podcast != nil && db.Val(it.Podcast.FeedURL) != "" && h.deps.PodcastEpisodes != nil {
		// Quick match all unmatched podcast episodes (Scanner.js:113-116).
		if n, err := h.deps.PodcastEpisodes(ctx, ref.ID, overrideDetails); err != nil {
			slog.Error("quick match podcast episodes", "item", ref.ID, "error", err)
		} else if n > 0 {
			slog.Info("quick matched podcast episodes", "item", ref.ID, "updated", n)
		}
	}
	if err := h.finish(ctx, out); err != nil {
		return MatchResult{}, err
	}
	fresh, err := Get(ctx, h.db, ref.ID, Options{Episodes: true})
	if err != nil {
		return MatchResult{}, err
	}
	expanded := fresh.Expanded(h.prefixes())
	return MatchResult{Updated: &out.updated, LibraryItem: &expanded}, nil
}

// matchCover downloads the match's cover when the item has none or the cover
// is overridden. A failure is logged and does not fail the match.
func (h *Handler) matchCover(ctx context.Context, ref itemRef, withItem bool, cover json.RawMessage, stored *string, override bool) *string {
	if !matchTruthy(cover) || (db.Val(stored) != "" && !override) || h.deps.SaveCover == nil {
		return nil
	}
	url := matchText(cover)
	path, err := h.deps.SaveCover(ctx, ref.ID, ref.Path, withItem, url)
	if err != nil {
		slog.Warn("match cover failed to use", "cover", url, "error", err)
		return nil
	}
	return &path
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func bookAuthors(b *Book) string {
	names := make([]string, len(b.Authors))
	for i, a := range b.Authors {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// bookMatchPatch turns a provider result into the patch that fills the book
// (quickMatchBookBuildUpdatePayload, Scanner.js:214-364): a field is taken
// when the book has none yet, or when the details are overridden.
func bookMatchPatch(b *Book, m wire.BookMatch, override bool) BookPatch {
	md := &BookMetadataPatch{}
	p := BookPatch{Metadata: md}
	take := func(raw json.RawMessage, empty bool) bool { return matchTruthy(raw) && (empty || override) }
	text := func(raw json.RawMessage, stored *string, dst *Text) {
		if take(raw, db.Val(stored) == "") {
			*dst = Text{true, matchText(raw)}
		}
	}
	text(m.Title, b.Title, &md.Title)
	text(m.Subtitle, b.Subtitle, &md.Subtitle)
	text(m.Description, b.Description, &md.Description)
	text(m.Publisher, b.Publisher, &md.Publisher)
	text(m.PublishedYear, b.PublishedYear, &md.PublishedYear)
	text(m.Language, b.Language, &md.Language)
	text(m.ASIN, b.ASIN, &md.ASIN)
	text(m.ISBN, b.ISBN, &md.ISBN)
	list := func(raw json.RawMessage, stored []string, dst **[]string) {
		if take(raw, len(stored) == 0) {
			*dst = db.Ptr(matchList(raw))
		}
	}
	list(m.Narrator, b.Narrators.V, &md.Narrators)
	list(m.Genres, b.Genres.V, &md.Genres)
	list(m.Tags, b.Tags.V, &p.Tags)
	if take(m.Explicit, !bool(b.Explicit)) {
		md.Explicit = db.Ptr(true)
	}
	if take(m.Abridged, !bool(b.Abridged)) {
		md.Abridged = db.Ptr(true)
	}
	if take(m.Author, len(b.Authors) == 0) {
		if names := matchList(m.Author); len(names) > 0 {
			authors := make([]AuthorPatch, len(names))
			for i, name := range names {
				authors[i] = AuthorPatch{name}
			}
			md.Authors = &authors
		}
	}
	if take(m.Series, len(b.Series) == 0) {
		if series := matchSeries(m); len(series) > 0 {
			md.Series = &series
		}
	}
	return p
}

// podcastMatchPatch is quickMatchPodcastBuildUpdatePayload (Scanner.js:145-182).
func podcastMatchPatch(pod *db.Podcast, m wire.PodcastMatch, override bool) PodcastPatch {
	md := &PodcastMetadataPatch{}
	text := func(raw json.RawMessage, stored *string, dst *Text) {
		if matchTruthy(raw) && (db.Val(stored) == "" || override) {
			*dst = Text{true, matchText(raw)}
		}
	}
	text(m.Title, pod.Title, &md.Title)
	text(m.ArtistName, pod.Author, &md.Author)
	text(m.ID, pod.ItunesID, &md.ItunesID)
	text(m.PageURL, pod.ItunesPageURL, &md.ItunesPageURL)
	text(m.ArtistID, pod.ItunesArtistID, &md.ItunesArtistID)
	text(m.ReleaseDate, pod.ReleaseDate, &md.ReleaseDate)
	text(m.Cover, pod.ImageURL, &md.ImageURL)
	text(m.FeedURL, pod.FeedURL, &md.FeedURL)
	text(m.DescriptionPlain, pod.Description, &md.Description)
	if matchTruthy(m.Genres) && (len(pod.Genres.V) == 0 || override) {
		md.Genres = db.Ptr(matchList(m.Genres))
	}
	return PodcastPatch{Metadata: md}
}

// matchSeries reads a result's series: a list of {series, sequence}, or one
// series name with the result's sequence.
func matchSeries(m wire.BookMatch) []SeriesPatch {
	var entries []struct {
		Series   string          `json:"series"`
		Sequence json.RawMessage `json:"sequence"`
	}
	if json.Unmarshal(m.Series, &entries) != nil {
		entries = []struct {
			Series   string          `json:"series"`
			Sequence json.RawMessage `json:"sequence"`
		}{{matchText(m.Series), m.Sequence}}
	}
	out := make([]SeriesPatch, len(entries))
	for i, e := range entries {
		out[i] = SeriesPatch{Name: e.Series, keepSequence: len(e.Sequence) == 0}
		if v := matchValue(e.Sequence); v != nil {
			out[i].Sequence = db.Ptr(matchText(e.Sequence))
		}
	}
	return out
}

// The provider boundary: results keep each field as the provider's JSON, which
// may be a string, a number or a list, and are read like the JavaScript does.

func matchValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var v any
	if decoder.Decode(&v) != nil {
		return nil
	}
	return v
}

// matchTruthy is JavaScript truthiness: absent, null, false, 0 and "" are false;
// lists and objects are true.
func matchTruthy(raw json.RawMessage) bool {
	switch v := matchValue(raw).(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	}
	return true
}

// matchText is a value as JavaScript prints it: strings as they are, numbers
// and booleans as their literal, lists joined by commas, anything else empty.
func matchText(raw json.RawMessage) string {
	switch v := matchValue(raw).(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			encoded, _ := json.Marshal(item) // items of a decoded list always encode
			parts[i] = matchText(encoded)
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// matchList is `Array.isArray(v) ? [...v] : v.split(',').map(trim).filter(Boolean)`.
func matchList(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil && list != nil {
		return list
	}
	out := []string{}
	for _, part := range strings.Split(matchText(raw), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// decodeOptional decodes a JSON body, treating an empty one as the zero value.
func decodeOptional[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	if r.ContentLength == 0 {
		var zero T
		return zero, nil
	}
	return httpx.Decode[T](w, r)
}

// match is LibraryItemController.match (js:526-548) behind its middleware
// (js:1220-1249). Order: 404, 403 for item access, 403 for the update permission.
func (h *Handler) match(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.loadRef(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	allowed, err := h.canAccessAll(ctx, user, []itemRef{ref})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if !allowed || !user.CanUpdate() {
		return httpx.Forbidden()
	}
	opts, err := decodeOptional[MatchOptions](w, r)
	if err != nil {
		return err
	}
	result, err := h.QuickMatch(ctx, ref.ID, opts)
	if err != nil {
		return httpx.Internal("internal error", err)
	}
	return httpx.JSON(w, r, result)
}

// batchQuickMatchRequest is the body of POST /api/items/batch/quickmatch.
type batchQuickMatchRequest struct {
	batchIDs
	Options MatchOptions `json:"options"`
}

// batchQuickMatchResult is the batch_quickmatch_complete payload (js:803-810).
type batchQuickMatchResult struct {
	Success   bool `json:"success"`
	Updates   int  `json:"updates"`
	Unmatched int  `json:"unmatched"`
}

// batchQuickMatch is LibraryItemController.batchQuickMatch (js:763-812): admins
// only; the request is answered at once and the matching runs on, ending with
// batch_quickmatch_complete to the requesting user.
func (h *Handler) batchQuickMatch(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.IsAdmin() {
		slog.Warn("non-admin user attempted to batch quick match", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	body, err := httpx.Decode[batchQuickMatchRequest](w, r)
	if err != nil || len(body.IDs) == 0 {
		return httpx.BadRequest("")
	}
	refs, err := h.refsInQueryOrder(r.Context(), body.IDs)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if len(refs) == 0 {
		return httpx.BadRequest("")
	}
	// A batch takes only the provider and the override flags.
	opts := MatchOptions{Provider: body.Options.Provider, OverrideCover: body.Options.OverrideCover, OverrideDetails: body.Options.OverrideDetails}
	// The work continues after the response, as the source lets a handler's
	// promise continue.
	go func() {
		ctx := context.Background()
		var result batchQuickMatchResult
		for _, ref := range refs {
			res, err := h.QuickMatch(ctx, ref.ID, opts)
			switch {
			case err != nil:
				slog.Error("batch quick match", "item", ref.ID, "error", err)
			case res.Updated != nil && *res.Updated:
				result.Updates++
			case res.Warning != nil:
				result.Unmatched++
			}
		}
		result.Success = result.Updates > 0
		h.emit.ToUser(user.ID, "batch_quickmatch_complete", result)
	}()
	return httpx.Status(w, http.StatusOK)
}
