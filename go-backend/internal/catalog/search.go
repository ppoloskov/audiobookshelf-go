package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/people"
	"audiobookshelf-compat/internal/podcasts/read"
	"audiobookshelf-compat/internal/store"
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

func (h *Handler) search(w http.ResponseWriter, r *http.Request, lib library.Scope) error {
	user, libraryID, mediaType := lib.User, lib.ID, lib.MediaType
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		return httputil.Status(http.StatusBadRequest, `Invalid request. Query param "q" must be a string`)
	}
	limit := cmp.Or(lib.Limit, 12) // `req.query.limit || 12` (LibraryController.js:964)
	table, err := lib.MediaTable()
	if err != nil {
		return err
	}
	permissionSQL, permissionArgs := access.For(r.Context(), user).MediaClause("b")
	querySQL := `SELECT li.id FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId WHERE li.libraryId=? AND (lower(b.title) LIKE lower(?) OR `
	if mediaType == "book" {
		querySQL += `lower(b.subtitle) LIKE lower(?) OR lower(b.asin) LIKE lower(?) OR lower(b.isbn) LIKE lower(?))`
	} else {
		querySQL += `lower(b.author) LIKE lower(?) OR lower(b.itunesId) LIKE lower(?) OR lower(b.itunesArtistId) LIKE lower(?))`
	}
	querySQL += permissionSQL
	pattern := "%" + query + "%"
	args := []any{libraryID, pattern, pattern, pattern, pattern}
	args = append(args, permissionArgs...)
	rows, err := h.db.QueryContext(r.Context(), querySQL, args...)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		ids = append(ids, id)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	expanded, err := h.expandedItems(r.Context(), ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	items := make([]libraryItemHit, 0, len(expanded))
	for _, item := range expanded {
		if mediaType == "podcast" {
			item.Media = withoutEpisodes(item.Media)
		}
		items = append(items, libraryItemHit{item})
	}
	tags, err := h.searchJSONValues(r.Context(), libraryID, table, "tags", query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	genres, err := h.searchJSONValues(r.Context(), libraryID, table, "genres", query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if mediaType == "podcast" {
		episodes, err := h.searchEpisodes(r.Context(), libraryID, pattern, permissionSQL, permissionArgs, limit)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		return httputil.JSON(w, r, map[string]any{"podcast": items, "tags": tags, "genres": genres, "episodes": episodes})
	}
	narrators, err := h.searchJSONValues(r.Context(), libraryID, table, "narrators", query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	authors, err := h.searchAuthors(r.Context(), libraryID, query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	series, err := h.searchSeries(r.Context(), libraryID, query, limit, user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"book": items, "narrators": narrators, "tags": tags, "genres": genres, "series": series, "authors": authors})
}

// searchEpisodes is the episode half of libraryItemsPodcastFilters.search
// (server/utils/queries/libraryItemsPodcastFilters.js:411-440): episodes whose
// title matches, each with its podcast (loaded without episodes) and the
// expanded episode as recentEpisode.
func (h *Handler) searchEpisodes(ctx context.Context, libraryID, pattern, permissionSQL string, permissionArgs []any, limit int) ([]itemMatch, error) {
	query := `SELECT li.id, e.id FROM podcastEpisodes e JOIN podcasts b ON b.id=e.podcastId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='podcast' WHERE li.libraryId=? AND lower(e.title) LIKE lower(?)` + permissionSQL + ` ORDER BY e.rowid`
	args := append([]any{libraryID, pattern}, permissionArgs...)
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type ref struct{ itemID, episodeID string }
	refs := make([]ref, 0)
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.itemID, &x.episodeID); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, x)
		if limit > 0 && len(refs) >= limit {
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(refs))
	for i, x := range refs {
		itemIDs[i] = x.itemID
	}
	expanded, err := h.expandedItems(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	if len(expanded) != len(refs) {
		return nil, sql.ErrNoRows
	}
	out := make([]itemMatch, 0, len(refs))
	for i, x := range refs {
		item := expanded[i]
		item.RecentEpisode = items.JSONNull
		if media, ok := item.Media.(podcastread.PodcastMedia); ok {
			episodes, _ := media.Episodes.([]podcastread.EpisodeExpanded)
			for _, ep := range episodes {
				if ep.ID == x.episodeID {
					item.RecentEpisode = ep
				}
			}
		}
		item.Media = withoutEpisodes(item.Media)
		out = append(out, itemMatch{LibraryItem: item})
	}
	return out, nil
}

type libraryItemHit struct {
	LibraryItem items.Expanded `json:"libraryItem"`
}

// withoutEpisodes empties a podcast media value's episodes, count and size, as
// the search responses send podcasts.
func withoutEpisodes(media any) any {
	if p, ok := media.(podcastread.PodcastMedia); ok {
		zero := int64(0)
		p.Episodes, p.NumEpisodes, p.Size = []podcastread.EpisodeExpanded{}, &zero, &zero
		return p
	}
	return media
}

func (h *Handler) searchJSONValues(ctx context.Context, libraryID, table, column, query string, limit int) ([]map[string]any, error) {
	if (table != "books" && table != "podcasts") || (column != "tags" && column != "genres" && column != "narrators") {
		return nil, fmt.Errorf("unsupported search field")
	}
	if column == "narrators" && table != "books" {
		return []map[string]any{}, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT j.value,COUNT(*) FROM `+table+` b JOIN libraryItems li ON li.mediaId=b.id,json_each(b.`+column+`) j WHERE li.libraryId=? AND lower(CAST(j.value AS TEXT)) LIKE lower(?) GROUP BY j.value ORDER BY COUNT(*) DESC LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var value string
		var n int64
		if err := rows.Scan(&value, &n); err != nil {
			return nil, err
		}
		if column == "narrators" {
			out = append(out, map[string]any{"name": value, "numBooks": n})
		} else {
			out = append(out, map[string]any{"name": value, "numItems": n})
		}
	}
	return out, rows.Err()
}

func (h *Handler) searchAuthors(ctx context.Context, libraryID, query string, limit int) ([]people.AuthorWithBooks, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT `+people.AuthorColumns+`,(SELECT COUNT(*) FROM bookAuthors ba WHERE ba.authorId=authors.id) FROM authors WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]people.AuthorWithBooks, 0)
	for rows.Next() {
		var a people.AuthorRecord
		var count int
		if err := rows.Scan(&a.ID, &a.Name, &a.LastFirst, &a.ASIN, &a.Description, &a.ImagePath, &a.LibraryID, &a.CreatedAt, &a.UpdatedAt, &count); err != nil {
			return nil, err
		}
		view, err := a.ToOldJSON()
		if err != nil {
			return nil, err
		}
		out = append(out, people.AuthorWithBooks{AuthorView: view, NumBooks: count})
	}
	return out, rows.Err()
}

// searchSeries mirrors the series part of libraryItemsBookFilters.search
// (server/utils/queries/libraryItemsBookFilters.js:1227-1270): each match
// carries its user-visible books as full legacy items with authors and series
// emptied, and series without visible books are dropped after the limit.
func (h *Handler) searchSeries(ctx context.Context, libraryID, query string, limit int, user store.User) ([]seriesSearchHit, error) {
	prefixes := h.sortingPrefixes(ctx)
	rows, err := h.db.QueryContext(ctx, `SELECT id,name,description,createdAt,updatedAt,libraryId FROM series WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	var matches []library.SeriesView
	for rows.Next() {
		var rec library.SeriesRecord
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Description, &rec.CreatedAt, &rec.UpdatedAt, &rec.LibraryID); err != nil {
			rows.Close()
			return nil, err
		}
		view, err := library.NewSeriesView(rec, prefixes)
		if err != nil {
			rows.Close()
			return nil, err
		}
		matches = append(matches, view)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	permission, permissionArgs := access.For(ctx, user).MediaClause("b")
	out := make([]seriesSearchHit, 0, len(matches))
	for _, m := range matches {
		itemIDs, err := h.queryStrings(ctx, `SELECT li.id FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE bs.seriesId=?`+permission+` ORDER BY bs.rowid`, append([]any{m.ID}, permissionArgs...)...)
		if err != nil {
			return nil, err
		}
		loaded, err := h.loadItems(ctx, itemIDs, true)
		if err != nil {
			return nil, err
		}
		bookResults := make([]items.Full, 0, len(loaded))
		for _, it := range loaded {
			item := it.Full()
			if media, ok := item.Media.(books.BookFull); ok && item.MediaType == "book" {
				media.Metadata.Authors, media.Metadata.Series = []books.Author{}, []books.SeriesRef{}
				item.Media = media
			}
			bookResults = append(bookResults, item)
		}
		if len(bookResults) == 0 {
			continue
		}
		out = append(out, seriesSearchHit{m, bookResults})
	}
	return out, nil
}

type seriesSearchHit struct {
	Series library.SeriesView `json:"series"`
	Books  []items.Full       `json:"books"`
}
