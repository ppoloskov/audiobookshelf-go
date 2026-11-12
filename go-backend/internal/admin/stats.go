package admin

import (
	"cmp"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// mediaStats is one kind of media in the server statistics.
type mediaStats struct {
	TotalSize     int64 `json:"totalSize" db:"totalSize"`
	NumItems      int64 `json:"numItems" db:"numItems"`
	NumAudioFiles int64 `json:"numAudioFiles" db:"-"`
}

func (h *Handler) serverStats(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var books, podcasts mediaStats
	for mediaType, stats := range map[string]*mediaStats{"book": &books, "podcast": &podcasts} {
		if err := h.db.Get(ctx, stats, "SELECT COALESCE(SUM(size), 0) AS totalSize, COUNT(*) AS numItems FROM libraryItems WHERE mediaType = ?", mediaType); err != nil {
			return httpx.Internal("", err)
		}
	}
	if err := h.db.Get(ctx, &books.NumAudioFiles, "SELECT COALESCE(SUM(json_array_length(audioFiles)), 0) FROM books"); err != nil {
		return httpx.Internal("", err)
	}
	if err := h.db.Get(ctx, &podcasts.NumAudioFiles, "SELECT COUNT(*) FROM podcastEpisodes"); err != nil {
		return httpx.Internal("", err)
	}
	total := mediaStats{books.TotalSize + podcasts.TotalSize, books.NumItems + podcasts.NumItems, books.NumAudioFiles + podcasts.NumAudioFiles}
	return httpx.JSON(w, r, struct {
		Books    mediaStats `json:"books"`
		Podcasts mediaStats `json:"podcasts"`
		Total    mediaStats `json:"total"`
	}{books, podcasts, total})
}

// tally sums listening time per name; names keep first-insertion order, which
// the stable sort of top keeps for ties (adminStats.js object key order).
type tally struct {
	index map[string]int
	list  []topEntry
}

func (t *tally) add(name string, seconds float64) {
	i, seen := t.index[name]
	if !seen {
		if t.index == nil {
			t.index = map[string]int{}
		}
		i = len(t.list)
		t.index[name] = i
		t.list = append(t.list, topEntry{Name: name})
	}
	t.list[i].Time += seconds
}

// top is the first three names by rounded time, descending
// (adminStats.getStatsForYear, server/utils/queries/adminStats.js:120-146).
func (t *tally) top() []topEntry {
	for i := range t.list {
		t.list[i].Time = math.Round(t.list[i].Time)
	}
	slices.SortStableFunc(t.list, func(a, b topEntry) int { return cmp.Compare(b.Time, a.Time) })
	return append([]topEntry{}, t.list[:min(3, len(t.list))]...)
}

// topEntry is one row of a top list. Authors and narrators fill Name, genres
// fill Genre instead (the client reads "name" or "genre").
type topEntry struct {
	Name  string  `json:"name,omitempty"`
	Genre string  `json:"genre,omitempty"`
	Time  float64 `json:"time"`
}

// sessionMeta is the part of a playback session's media metadata that the
// yearly statistics count.
type sessionMeta struct {
	Authors   []struct{ Name string } `json:"authors"`
	Narrators []string                `json:"narrators"`
	Genres    []string                `json:"genres"`
}

type yearReply struct {
	NumListeningSessions    int64      `json:"numListeningSessions"`
	NumBooksAdded           int        `json:"numBooksAdded"`
	NumAuthorsAdded         int64      `json:"numAuthorsAdded"`
	TotalBooksAddedSize     float64    `json:"totalBooksAddedSize"`
	TotalBooksAddedDuration float64    `json:"totalBooksAddedDuration"`
	BooksAddedWithCovers    []string   `json:"booksAddedWithCovers"`
	TotalBooksSize          float64    `json:"totalBooksSize" db:"size"`
	TotalBooksDuration      float64    `json:"totalBooksDuration" db:"duration"`
	TotalListeningTime      float64    `json:"totalListeningTime"`
	NumBooks                int64      `json:"numBooks" db:"n"`
	TopAuthors              []topEntry `json:"topAuthors"`
	TopNarrators            []topEntry `json:"topNarrators"`
	TopGenres               []topEntry `json:"topGenres"`
}

// yearStats is the server-wide yearly statistics (adminStats.getStatsForYear).
func (h *Handler) yearStats(w http.ResponseWriter, r *http.Request) error {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 2000 || year > 9999 {
		return httpx.BadRequest("Invalid year")
	}
	ctx := r.Context()
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	var reply yearReply
	var added []struct {
		ID       string   `db:"id"`
		Cover    *string  `db:"coverPath"`
		Duration *float64 `db:"duration"`
		Size     *float64 `db:"size"`
	}
	var played []struct {
		Listen *float64                 `db:"timeListening"`
		Meta   db.JSON[json.RawMessage] `db:"mediaMetadata"`
	}
	err = h.db.Get(ctx, &reply.NumAuthorsAdded, "SELECT COUNT(*) FROM authors WHERE createdAt >= ? AND createdAt < ?", start, end)
	if err == nil {
		err = h.db.Get(ctx, &reply, "SELECT COUNT(*) AS n, COALESCE(SUM(li.size), 0) AS size, COALESCE(SUM(b.duration), 0) AS duration FROM libraryItems li JOIN books b ON b.id = li.mediaId WHERE li.mediaType = 'book' AND li.createdAt < ?", end)
	}
	if err == nil {
		err = h.db.Select(ctx, &added, "SELECT li.id AS id, b.coverPath AS coverPath, b.duration AS duration, li.size AS size FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE b.createdAt >= ? AND b.createdAt < ? ORDER BY RANDOM()", start, end)
	}
	if err == nil {
		err = h.db.Select(ctx, &played, "SELECT timeListening, mediaMetadata FROM playbackSessions WHERE createdAt >= ? AND createdAt < ? ORDER BY rowid", start, end)
	}
	if err != nil {
		return httpx.Internal("database error", err)
	}
	reply.NumListeningSessions = int64(len(played))
	reply.NumBooksAdded = len(added)
	reply.BooksAddedWithCovers = []string{}
	var durations float64
	for _, book := range added {
		durations += db.Val(book.Duration)
		reply.TotalBooksAddedSize += db.Val(book.Size)
		// The id is the library item's, which the web client loads covers by (adminStats.js:76-80).
		if book.Cover != nil && len(reply.BooksAddedWithCovers) < 25 {
			if _, err := os.Stat(*book.Cover); err == nil {
				reply.BooksAddedWithCovers = append(reply.BooksAddedWithCovers, book.ID)
			}
		}
	}
	reply.TotalBooksAddedDuration = math.Round(durations)
	var authors, narrators, genres tally
	for _, session := range played {
		var meta sessionMeta
		_ = json.Unmarshal(session.Meta.V, &meta) // a session without readable metadata counts for nobody; a mistyped key for its list only
		seconds := db.Val(session.Listen)
		reply.TotalListeningTime += seconds
		for _, author := range meta.Authors {
			if author.Name != "" {
				authors.add(author.Name, seconds)
			}
		}
		for _, name := range meta.Narrators {
			if name != "" {
				narrators.add(name, seconds)
			}
		}
		for _, name := range meta.Genres {
			lower := strings.ToLower(name)
			if name != "" && !strings.Contains(lower, "audiobook") && !strings.Contains(lower, "audio book") {
				genres.add(name, seconds)
			}
		}
	}
	reply.TopAuthors, reply.TopNarrators, reply.TopGenres = authors.top(), narrators.top(), genres.top()
	for i := range reply.TopGenres {
		reply.TopGenres[i].Genre, reply.TopGenres[i].Name = reply.TopGenres[i].Name, ""
	}
	return httpx.JSON(w, r, reply)
}
