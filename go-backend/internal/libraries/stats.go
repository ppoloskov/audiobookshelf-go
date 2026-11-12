package libraries

import (
	"context"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// Stats is the stats of a podcast library; BookStats adds the authors.
type Stats struct {
	TotalGenres     int           `json:"totalGenres"`
	GenresWithCount []genreCount  `json:"genresWithCount"`
	TotalItems      int64         `json:"totalItems"`
	LongestItems    []longestItem `json:"longestItems"`
	LargestItems    []largestItem `json:"largestItems"`
	TotalSize       int64         `json:"totalSize"`
	TotalDuration   float64       `json:"totalDuration"`
	NumAudioTracks  int64         `json:"numAudioTracks"`
}

// BookStats is the stats of a book library.
type BookStats struct {
	TotalAuthors     int64         `json:"totalAuthors"`
	AuthorsWithCount []authorCount `json:"authorsWithCount"`
	Stats
}

type genreCount struct {
	Genre string `json:"genre" db:"genre"`
	Count int64  `json:"count" db:"count"`
}

type authorCount struct {
	ID    string `json:"id" db:"id"`
	Name  string `json:"name" db:"name"`
	Count int64  `json:"count" db:"count"`
}

type longestItem struct {
	ID       string   `json:"id" db:"id"`
	Title    *string  `json:"title" db:"title"`
	Duration *float64 `json:"duration" db:"duration"`
}

type largestItem struct {
	ID    string  `json:"id" db:"id"`
	Title *string `json:"title" db:"title"`
	Size  *int64  `json:"size" db:"size"`
}

// stats is GET /api/libraries/{id}/stats.
func (h *Handler) stats(w http.ResponseWriter, r *http.Request, scope Scope) error {
	stats, err := h.statsOf(r.Context(), scope.ID, db.Val(scope.MediaType))
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, stats)
}

// statsOf is the aggregate payload of a library of the given media type
// (libraryItemsBookFilters.getBookLibraryStats and the podcast counterpart).
// The size of a podcast library sums the library items alone, not the items
// joined to their episodes (libraryItemsPodcastFilters.js:566-580).
func (h *Handler) statsOf(ctx context.Context, id, mediaType string) (any, error) {
	var total struct {
		Size     int64   `db:"size"`
		Duration float64 `db:"duration"`
		Tracks   int64   `db:"tracks"`
		Items    int64   `db:"items"`
	}
	var table, durationExpr string
	switch mediaType {
	case "book":
		table, durationExpr = "books", "m.duration"
		if err := h.db.Get(ctx, &total, `SELECT COALESCE(SUM(li.size),0) AS size, COALESCE(SUM(b.duration),0) AS duration, COALESCE(SUM(json_array_length(b.audioFiles)),0) AS tracks, COUNT(*) AS items FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId = ?`, id); err != nil {
			return nil, err
		}
	case "podcast":
		table, durationExpr = "podcasts", "(SELECT SUM(json_extract(pe.audioFile,'$.duration')) FROM podcastEpisodes pe WHERE pe.podcastId=m.id)"
		if err := h.db.Get(ctx, &total, `SELECT COALESCE(SUM(json_extract(pe.audioFile,'$.duration')),0) AS duration, COUNT(pe.id) AS tracks, COUNT(DISTINCT li.id) AS items, (SELECT COALESCE(SUM(size),0) FROM libraryItems WHERE mediaType='podcast' AND libraryId = ?) AS size FROM libraryItems li, podcasts p LEFT OUTER JOIN podcastEpisodes pe ON pe.podcastId=p.id WHERE p.id=li.mediaId AND li.libraryId = ?`, id, id); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported library media type %q", mediaType)
	}
	stats := Stats{TotalItems: total.Items, TotalSize: total.Size, TotalDuration: total.Duration, NumAudioTracks: total.Tracks}
	var err error
	// table and durationExpr are constants chosen above.
	if stats.GenresWithCount, err = selectAll[genreCount](ctx, h.db, `SELECT j.value AS genre, COUNT(*) AS count FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id, json_each(m.genres) j WHERE li.libraryId = ? AND json_valid(m.genres) GROUP BY j.value ORDER BY count DESC`, id); err != nil {
		return nil, err
	}
	stats.TotalGenres = len(stats.GenresWithCount)
	if stats.LongestItems, err = selectAll[longestItem](ctx, h.db, `SELECT li.id AS id, m.title AS title, `+durationExpr+` AS duration FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id WHERE li.libraryId = ? ORDER BY duration DESC LIMIT 10`, id); err != nil {
		return nil, err
	}
	if stats.LargestItems, err = selectAll[largestItem](ctx, h.db, `SELECT li.id AS id, m.title AS title, li.size AS size FROM libraryItems li JOIN `+table+` m ON m.id=li.mediaId WHERE li.libraryId = ? ORDER BY li.size DESC LIMIT 10`, id); err != nil {
		return nil, err
	}
	if mediaType == "podcast" {
		return stats, nil
	}
	books := BookStats{Stats: stats}
	if err = h.db.Get(ctx, &books.TotalAuthors, `SELECT COUNT(*) FROM authors WHERE libraryId = ?`, id); err != nil {
		return nil, err
	}
	// authorFilters.getAuthorsWithCount: grouped by author, so ties keep group order.
	books.AuthorsWithCount, err = selectAll[authorCount](ctx, h.db, `SELECT ba.authorId AS id, a.name AS name, COUNT(ba.authorId) AS count FROM bookAuthors ba INNER JOIN authors a ON a.id=ba.authorId AND a.libraryId = ? GROUP BY ba.authorId, a.id ORDER BY count DESC LIMIT 10`, id)
	return books, err
}
