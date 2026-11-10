package library

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (h *Handler) libraryFilterData(w http.ResponseWriter, r *http.Request, lib Scope) error {
	data, err := h.filters.Get(r.Context(), h.db, lib.ID, lib.MediaType)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, data)
}

func (h *Handler) libraries(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	includeStats := strings.Contains(","+r.URL.Query().Get("include")+",", ",stats,")
	ids, err := h.queryStrings(r.Context(), `SELECT id FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	perms := access.For(r.Context(), user)
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		if !perms.ListLibrary(id) {
			continue
		}
		lib, err := ReadView(r.Context(), h.db, id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if includeStats {
			if lib.Stats, err = StatsValue(r.Context(), h.db, id, *cmp.Or(lib.MediaType, new(string))); err != nil {
				return httputil.Internal("database read failed", err)
			}
		}
		out = append(out, lib)
	}
	return httputil.JSON(w, r, struct {
		Libraries []View `json:"libraries"`
	}{out})
}

func (h *Handler) library(w http.ResponseWriter, r *http.Request, lib Scope) error {
	user, id := lib.User, lib.ID
	payload, err := ReadView(r.Context(), h.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if strings.Contains(","+r.URL.Query().Get("include")+",", ",filterdata,") {
		filterdata, err := h.filters.Get(r.Context(), h.db, id, lib.MediaType)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		playlists, err := sqlc.New(h.db).CountUserLibraryPlaylists(r.Context(), sqlc.CountUserLibraryPlaylistsParams{UserID: user.ID, LibraryID: id})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		return httputil.JSON(w, r, map[string]any{"filterdata": filterdata, "issues": filterdata.NumIssues, "numUserPlaylists": playlists, "library": payload})
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) libraryStats(w http.ResponseWriter, r *http.Request, lib Scope) error {
	stats, err := StatsValue(r.Context(), h.db, lib.ID, lib.MediaType)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, stats)
}

// StatsValue returns the aggregate stats payload for a library media type.
func StatsValue(ctx context.Context, db *sql.DB, id, mediaType string) (map[string]any, error) {
	var size, audioCount, total int64
	var duration float64
	if mediaType == "book" {
		err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(li.size),0),COALESCE(SUM(b.duration),0),COALESCE(SUM(json_array_length(b.audioFiles)),0),COUNT(*) FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=?`, id).Scan(&size, &duration, &audioCount, &total)
		if err != nil {
			return nil, err
		}
		genres, err := genreCounts(ctx, db, id, "books")
		if err != nil {
			return nil, err
		}
		var authorTotal int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM authors WHERE libraryId=?`, id).Scan(&authorTotal); err != nil {
			return nil, err
		}
		// authorFilters.getAuthorsWithCount (server/utils/queries/authorFilters.js:27):
		// inner join from bookAuthors, grouped by authorId, so ties keep group order.
		authorRows, err := db.QueryContext(ctx, `SELECT ba.authorId,a.name,COUNT(ba.authorId) AS count FROM bookAuthors ba INNER JOIN authors a ON a.id=ba.authorId AND a.libraryId=? GROUP BY ba.authorId, a.id ORDER BY count DESC LIMIT 10`, id)
		if err != nil {
			return nil, err
		}
		authors := make([]map[string]any, 0)
		for authorRows.Next() {
			var aid, name string
			var count int64
			if err := authorRows.Scan(&aid, &name, &count); err != nil {
				authorRows.Close()
				return nil, err
			}
			authors = append(authors, map[string]any{"id": aid, "name": name, "count": count})
		}
		if err := authorRows.Err(); err != nil {
			authorRows.Close()
			return nil, err
		}
		if err := authorRows.Close(); err != nil {
			return nil, err
		}
		longest, err := longestItems(ctx, db, id, "books", "m.duration")
		if err != nil {
			return nil, err
		}
		largest, err := largestItems(ctx, db, id, "books")
		if err != nil {
			return nil, err
		}
		return map[string]any{"totalAuthors": authorTotal, "authorsWithCount": authors, "totalGenres": len(genres), "genresWithCount": genres, "totalItems": total, "longestItems": longest, "largestItems": largest, "totalSize": size, "totalDuration": duration, "numAudioTracks": audioCount}, nil
	}
	if mediaType == "podcast" {
		// libraryItemsPodcastFilters.getPodcastLibraryStats (server/utils/queries/
		// libraryItemsPodcastFilters.js:566-580): the size is summed over the
		// library items alone, not over their joined episodes.
		err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(json_extract(pe.audioFile,'$.duration')),0),COUNT(pe.id),COUNT(DISTINCT li.id) FROM libraryItems li, podcasts p LEFT OUTER JOIN podcastEpisodes pe ON pe.podcastId=p.id WHERE p.id=li.mediaId AND li.libraryId=?`, id).Scan(&duration, &audioCount, &total)
		if err != nil {
			return nil, err
		}
		err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(li.size),0) FROM libraryItems li WHERE li.mediaType='podcast' AND li.libraryId=?`, id).Scan(&size)
		if err != nil {
			return nil, err
		}
		genres, err := genreCounts(ctx, db, id, "podcasts")
		if err != nil {
			return nil, err
		}
		longest, err := longestItems(ctx, db, id, "podcasts", "(SELECT SUM(json_extract(pe.audioFile,'$.duration')) FROM podcastEpisodes pe WHERE pe.podcastId=m.id)")
		if err != nil {
			return nil, err
		}
		largest, err := largestItems(ctx, db, id, "podcasts")
		if err != nil {
			return nil, err
		}
		return map[string]any{"totalGenres": len(genres), "genresWithCount": genres, "totalItems": total, "longestItems": longest, "largestItems": largest, "totalSize": size, "totalDuration": duration, "numAudioTracks": audioCount}, nil
	}
	return nil, fmt.Errorf("unsupported library media type %q", mediaType)
}

func genreCounts(ctx context.Context, db *sql.DB, id, table string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := db.QueryContext(ctx, `SELECT j.value,COUNT(*) AS n FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id, json_each(m.genres) j WHERE li.libraryId=? AND json_valid(m.genres) GROUP BY j.value ORDER BY n DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var genre string
		var n int64
		if err := rows.Scan(&genre, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"genre": genre, "count": n})
	}
	return out, rows.Err()
}

func longestItems(ctx context.Context, db *sql.DB, id, table, expression string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := db.QueryContext(ctx, `SELECT li.id,m.title,`+expression+` AS duration FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id WHERE li.libraryId=? ORDER BY duration DESC LIMIT 10`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var itemID string
		var title sql.NullString
		var duration any
		if err := rows.Scan(&itemID, &title, &duration); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": itemID, "title": store.NullString(title), "duration": duration})
	}
	return out, rows.Err()
}

func largestItems(ctx context.Context, db *sql.DB, id, table string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := db.QueryContext(ctx, `SELECT li.id,m.title,li.size FROM libraryItems li JOIN `+table+` m ON m.id=li.mediaId WHERE li.libraryId=? ORDER BY li.size DESC LIMIT 10`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var itemID string
		var title sql.NullString
		var size any
		if err := rows.Scan(&itemID, &title, &size); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": itemID, "title": store.NullString(title), "size": size})
	}
	return out, rows.Err()
}
