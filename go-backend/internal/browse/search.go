package browse

import (
	"cmp"
	"context"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/author"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
)

// itemHit and episodeHit are the library items of a search response.
type itemHit struct {
	LibraryItem item.Expanded `json:"libraryItem"`
}

type seriesHit struct {
	Series seriesView  `json:"series"`
	Books  []item.Full `json:"books"`
}

// tagCount, genreCount and narratorCount are the counted values of a search;
// the first two count items, narrators count books.
type valueCount struct {
	Name     string `json:"name" db:"name"`
	NumItems int64  `json:"numItems" db:"numItems"`
}

type narratorCount struct {
	Name     string `json:"name"`
	NumBooks int64  `json:"numBooks"`
}

type bookSearch struct {
	Book      []itemHit       `json:"book"`
	Narrators []narratorCount `json:"narrators"`
	Tags      []valueCount    `json:"tags"`
	Genres    []valueCount    `json:"genres"`
	Series    []seriesHit     `json:"series"`
	Authors   []author.View   `json:"authors"`
}

type podcastSearch struct {
	Podcast  []itemHit    `json:"podcast"`
	Tags     []valueCount `json:"tags"`
	Genres   []valueCount `json:"genres"`
	Episodes []itemHit    `json:"episodes"`
}

// search is GET /api/libraries/{id}/search (libraryItemsBookFilters.search,
// libraryItemsPodcastFilters.search).
func (h *Handler) search(w http.ResponseWriter, r *http.Request, s scope) error {
	ctx := r.Context()
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		return httpx.BadRequest(`Invalid request. Query param "q" must be a string`)
	}
	limit := cmp.Or(s.limit, 12) // `req.query.limit || 12` (LibraryController.js:964)
	table, err := s.mediaTable()
	if err != nil {
		return err
	}
	pattern := "%" + query + "%"
	perm, permArgs := s.user.MediaClause("b")
	match := `lower(b.title) LIKE lower(?) OR lower(b.subtitle) LIKE lower(?) OR lower(b.asin) LIKE lower(?) OR lower(b.isbn) LIKE lower(?)`
	if s.podcast() {
		match = `lower(b.title) LIKE lower(?) OR lower(b.author) LIKE lower(?) OR lower(b.itunesId) LIKE lower(?) OR lower(b.itunesArtistId) LIKE lower(?)`
	}
	var ids []string
	err = h.db.Select(ctx, &ids, `SELECT li.id FROM libraryItems li JOIN `+table+` b ON b.id=li.mediaId WHERE li.libraryId=? AND (`+match+`)`+perm+` LIMIT ?`,
		append(append([]any{s.lib.ID, pattern, pattern, pattern, pattern}, permArgs...), limit)...)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	// Podcasts are sent without their episodes: Expanded of an item loaded
	// without them has none, and numEpisodes and size 0.
	loaded, err := item.Load(ctx, h.db, ids, item.Options{})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	prefixes := h.settings.Server().SortingPrefixes
	hits := make([]itemHit, len(loaded))
	for i, it := range loaded {
		hits[i] = itemHit{it.Expanded(prefixes)}
	}
	tags, err := h.valueCounts(ctx, s.lib.ID, table, "tags", pattern, limit)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	genres, err := h.valueCounts(ctx, s.lib.ID, table, "genres", pattern, limit)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if s.podcast() {
		episodes, err := h.searchEpisodes(ctx, s, pattern, perm, permArgs, limit, prefixes)
		if err != nil {
			return httpx.Internal("database read failed", err)
		}
		return httpx.JSON(w, r, podcastSearch{hits, tags, genres, episodes})
	}
	out := bookSearch{Book: hits, Tags: tags, Genres: genres, Narrators: []narratorCount{}}
	counts, err := h.valueCounts(ctx, s.lib.ID, table, "narrators", pattern, limit)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	for _, c := range counts {
		out.Narrators = append(out.Narrators, narratorCount{c.Name, c.NumItems})
	}
	if out.Authors, err = h.searchAuthors(ctx, s.lib.ID, pattern, limit); err != nil {
		return httpx.Internal("database read failed", err)
	}
	if out.Series, err = h.searchSeries(ctx, s, pattern, limit, prefixes); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, out)
}

// valueCounts counts the values of a JSON array column (tags, genres,
// narrators) of the library's media that contain the pattern, most common
// first. Like upstream it ignores the user's content permissions.
func (h *Handler) valueCounts(ctx context.Context, libraryID, table, column, pattern string, limit int) ([]valueCount, error) {
	if column == "narrators" && table != "books" {
		return []valueCount{}, nil
	}
	rows := []valueCount{}
	err := h.db.Select(ctx, &rows, `SELECT j.value AS name, COUNT(*) AS numItems FROM `+table+` b JOIN libraryItems li ON li.mediaId=b.id, json_each(b.`+column+`) j
		WHERE li.libraryId=? AND lower(CAST(j.value AS TEXT)) LIKE lower(?) GROUP BY j.value ORDER BY COUNT(*) DESC LIMIT ?`, libraryID, pattern, limit)
	return rows, err
}

func (h *Handler) searchAuthors(ctx context.Context, libraryID, pattern string, limit int) ([]author.View, error) {
	var rows []struct {
		db.Author
		NumBooks int `db:"numBooks"`
	}
	err := h.db.Select(ctx, &rows, `SELECT authors.*, (SELECT COUNT(*) FROM bookAuthors ba WHERE ba.authorId=authors.id) AS numBooks
		FROM authors WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, pattern, limit)
	out := make([]author.View, len(rows))
	for i, row := range rows {
		out[i] = authorView(row.Author, row.NumBooks)
	}
	return out, err
}

// searchSeries finds series by name; each carries its visible books as full
// items with authors and series emptied. Series without visible books are
// dropped after the limit.
func (h *Handler) searchSeries(ctx context.Context, s scope, pattern string, limit int, prefixes []string) ([]seriesHit, error) {
	var found []db.Series
	if err := h.db.Select(ctx, &found, `SELECT * FROM series WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, s.lib.ID, pattern, limit); err != nil {
		return nil, err
	}
	out := []seriesHit{}
	if len(found) == 0 {
		return out, nil
	}
	ids := make([]string, len(found))
	for i, series := range found {
		ids[i] = series.ID
	}
	linked, err := h.seriesBooks(ctx, s.user, orderLinked, ids)
	if err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(linked))
	for i, b := range linked {
		itemIDs[i] = b.ItemID
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{})
	if err != nil {
		return nil, err
	}
	books := make(map[string]item.Full, len(loaded))
	for _, it := range loaded {
		full := it.Full()
		media := full.Media.(item.BookFull)
		media.Metadata.Authors, media.Metadata.Series = []item.Author{}, []item.SeriesRef{}
		full.Media = media
		books[it.ID] = full
	}
	for _, series := range found {
		hit := seriesHit{Series: newSeriesView(series, prefixes), Books: []item.Full{}}
		for _, b := range linked {
			if b.SeriesID == series.ID {
				hit.Books = append(hit.Books, books[b.ItemID])
			}
		}
		if len(hit.Books) > 0 {
			out = append(out, hit)
		}
	}
	return out, nil
}

// searchEpisodes finds episodes by title: each hit is its podcast (without
// episodes) with the expanded episode as recentEpisode.
func (h *Handler) searchEpisodes(ctx context.Context, s scope, pattern, perm string, permArgs []any, limit int, prefixes []string) ([]itemHit, error) {
	var found []struct {
		ItemID    string `db:"itemId"`
		EpisodeID string `db:"episodeId"`
	}
	err := h.db.Select(ctx, &found, `SELECT li.id AS itemId, e.id AS episodeId FROM podcastEpisodes e JOIN podcasts b ON b.id=e.podcastId
		JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='podcast' WHERE li.libraryId=? AND lower(e.title) LIKE lower(?)`+perm+` ORDER BY e.rowid LIMIT ?`,
		append(append([]any{s.lib.ID, pattern}, permArgs...), limit)...)
	if err != nil {
		return nil, err
	}
	itemIDs, episodeIDs := make([]string, len(found)), make([]string, len(found))
	for i, f := range found {
		itemIDs[i], episodeIDs[i] = f.ItemID, f.EpisodeID
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{Episodes: true, EpisodeIDs: episodeIDs})
	if err != nil {
		return nil, err
	}
	out := make([]itemHit, len(loaded))
	for i, it := range loaded {
		hit := it.Expanded(prefixes)
		hit.RecentEpisode = item.JSONNull
		for _, episode := range it.Podcast.Episodes {
			if episode.ID == episodeIDs[i] {
				hit.RecentEpisode = episode.Expanded(it.ID)
			}
		}
		media := hit.Media.(item.PodcastExpanded)
		media.Episodes, media.NumEpisodes, media.Size = []item.EpisodeExpanded{}, 0, 0
		hit.Media = media
		out[i] = itemHit{hit}
	}
	return out, nil
}
