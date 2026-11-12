package user

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

type totals[K cmp.Ordered] map[K]float64

func (t totals[K]) add(key K, seconds float64) { t[key] += seconds }

func addNamed(t totals[string], seconds float64, names ...string) {
	for _, name := range names {
		if name != "" {
			t[name] += seconds
		}
	}
}

// top is the n keys with the most time. Each sum is rounded to whole seconds
// before ranking (userStats.js); ties go to the lower key (as text). Never nil.
func top[K cmp.Ordered](t totals[K], n int, keyName string) []ranked {
	all := make([]ranked, 0, len(t))
	for key, seconds := range t {
		all = append(all, ranked{keyName, key, int(math.Round(seconds))})
	}
	slices.SortFunc(all, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(b.time, a.time), cmp.Compare(fmt.Sprint(a.key), fmt.Sprint(b.key)))
	})
	return all[:min(n, len(all))]
}

type ranked struct {
	keyName string
	key     any
	time    int
}

func (r ranked) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{r.keyName: r.key, "time": r.time})
}

type finishedBook struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Duration   int    `json:"duration"`
	FinishedAt int64  `json:"finishedAt"`
}

type yearStats struct {
	TotalListeningSessions    int           `json:"totalListeningSessions"`
	TotalListeningTime        int           `json:"totalListeningTime"`
	TotalBookListeningTime    int           `json:"totalBookListeningTime"`
	TotalPodcastListeningTime int           `json:"totalPodcastListeningTime"`
	TopAuthors                []ranked      `json:"topAuthors"`
	TopGenres                 []ranked      `json:"topGenres"`
	MostListenedNarrator      *ranked       `json:"mostListenedNarrator"`
	MostListenedMonth         *ranked       `json:"mostListenedMonth"`
	NumBooksFinished          int           `json:"numBooksFinished"`
	NumBooksListened          int           `json:"numBooksListened"`
	LongestAudiobookFinished  *finishedBook `json:"longestAudiobookFinished"`
	BooksWithCovers           []string      `json:"booksWithCovers"`
	FinishedBooksWithCovers   []string      `json:"finishedBooksWithCovers"`
}

type sessionMetadata struct {
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Narrators []string `json:"narrators"`
	Genres    []string `json:"genres"`
}

func hasFile(path string) bool { _, err := os.Stat(path); return err == nil }

// yearStats is GET /api/me/stats/year/{year} (userStats.getStatsForYear,
// server/utils/queries/userStats.js:114-190): the user's listening in a
// calendar year. Seconds are summed unrounded and rounded only for output.
func (h *Handler) yearStats(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 2000 || year > 9999 {
		return httpx.BadRequest("Invalid year")
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	var sessions []db.PlaybackSession
	if err := h.db.Select(ctx, &sessions, `SELECT * FROM playbackSessions WHERE userId = ? AND createdAt >= ? AND createdAt < ? ORDER BY updatedAt DESC`, me.ID, start, end); err != nil {
		return err
	}
	stats := yearStats{TotalListeningSessions: len(sessions), BooksWithCovers: []string{}, FinishedBooksWithCovers: []string{}}
	var total, bookTime, podcastTime float64
	months, books, authors, genres, narrators := totals[int]{}, totals[string]{}, totals[string]{}, totals[string]{}, totals[string]{}
	for _, s := range sessions {
		seconds := db.Val(s.TimeListening)
		total += seconds
		// The month is createdAt.getMonth() in server-local time.
		months.add(int(s.CreatedAt.In(time.Local).Month())-1, seconds)
		if db.Val(s.MediaItemType) != "book" {
			podcastTime += seconds // podcast sessions add to nothing else
			continue
		}
		bookTime += seconds
		addNamed(books, seconds, db.Val(s.DisplayTitle))
		var meta sessionMetadata
		_ = json.Unmarshal(s.MediaMetadata.V, &meta) // missing or malformed metadata counts for nothing
		for _, a := range meta.Authors {
			addNamed(authors, seconds, a.Name)
		}
		addNamed(narrators, seconds, meta.Narrators...)
		for _, g := range meta.Genres {
			if lower := strings.ToLower(g); !strings.Contains(lower, "audiobook") && !strings.Contains(lower, "audio book") {
				addNamed(genres, seconds, g)
			}
		}
	}
	stats.TotalListeningTime, stats.TotalBookListeningTime = int(math.Round(total)), int(math.Round(bookTime))
	stats.TotalPodcastListeningTime = int(math.Round(podcastTime))
	stats.NumBooksListened = len(books)
	stats.TopAuthors, stats.TopGenres = top(authors, 3, "name"), top(genres, 3, "genre")
	if best := top(narrators, 1, "name"); len(best) > 0 {
		stats.MostListenedNarrator = &best[0]
	}
	if best := top(months, 1, "month"); len(best) > 0 && best[0].time > 0 { // 0 is January
		stats.MostListenedMonth = &best[0]
	}
	if err := h.finishedBooks(ctx, me.ID, start, end, &stats); err != nil {
		return err
	}
	if err := h.coveredBooks(ctx, sessions, &stats); err != nil {
		return err
	}
	return httpx.JSON(w, r, stats)
}

// finishedBooks fills the finished-book statistics of [start, end).
func (h *Handler) finishedBooks(ctx context.Context, userID, start, end string, stats *yearStats) error {
	var finished []struct {
		MediaItemID   string   `db:"mediaItemId"`
		Title         *string  `db:"title"`
		Duration      *float64 `db:"duration"`
		FinishedAt    db.Time  `db:"finishedAt"`
		LibraryItemID string   `db:"libraryItemId"`
		CoverPath     *string  `db:"coverPath"`
	}
	err := h.db.Select(ctx, &finished, `SELECT mp.mediaItemId AS mediaItemId, b.title AS title, mp.duration AS duration, mp.finishedAt AS finishedAt, li.id AS libraryItemId, b.coverPath AS coverPath
FROM mediaProgresses mp
JOIN books b ON b.id = mp.mediaItemId
JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
WHERE mp.userId = ? AND mp.mediaItemType = 'book' AND mp.finishedAt >= ? AND mp.finishedAt < ?`, userID, start, end)
	if err != nil {
		return err
	}
	stats.NumBooksFinished = len(finished)
	longest := 0.0
	for _, book := range finished {
		if duration := db.Val(book.Duration); duration > longest {
			longest = duration
			stats.LongestAudiobookFinished = &finishedBook{book.MediaItemID, db.Val(book.Title), int(math.Round(duration)), book.FinishedAt.Millis()}
		}
		if len(stats.FinishedBooksWithCovers) < 5 && hasFile(db.Val(book.CoverPath)) {
			stats.FinishedBooksWithCovers = append(stats.FinishedBooksWithCovers, book.LibraryItemID)
		}
	}
	return nil
}

// coveredBooks fills up to 25 books with a cover on disk that the user listened
// to in the year, most recent first and not among the finished ones. Upstream
// joins the book for its current cover and item id, and picks the sessions in
// random order (userStats.js:8-30).
func (h *Handler) coveredBooks(ctx context.Context, sessions []db.PlaybackSession, stats *yearStats) error {
	seen := map[string]bool{}
	for _, s := range sessions {
		bookID := db.Val(s.MediaItemID)
		if len(stats.BooksWithCovers) >= 25 {
			break
		}
		if db.Val(s.MediaItemType) != "book" || seen[bookID] {
			continue
		}
		seen[bookID] = true
		var book struct {
			ItemID    string  `db:"itemId"`
			CoverPath *string `db:"coverPath"`
		}
		err := h.db.Get(ctx, &book, `SELECT li.id AS itemId, b.coverPath AS coverPath FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE b.id = ?`, bookID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !slices.Contains(stats.FinishedBooksWithCovers, book.ItemID) && hasFile(db.Val(book.CoverPath)) {
			stats.BooksWithCovers = append(stats.BooksWithCovers, book.ItemID)
		}
	}
	return nil
}
