package userapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) getYearStats(w http.ResponseWriter, r *http.Request, user store.User) error {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 2000 || year > 9999 {
		return httputil.Status(400, "Invalid year")
	}
	start, end := strconv.Itoa(year)+"-01-01", strconv.Itoa(year+1)+"-01-01"
	// userStats.getStatsForYear sums raw float seconds (`ls.timeListening
	// || 0`) and Math.rounds only the totals (server/utils/queries/userStats.js:114-190).
	var sessions int
	var total, bookTime, podcastTime float64
	sessionRows, err := sqlc.New(h.db).ListPlaybackSessionsCreatedBetween(r.Context(), sqlc.ListPlaybackSessionsCreatedBetweenParams{UserID: store.Text(user.ID), StartAt: start, EndAt: end})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	months := map[int]float64{}
	books := map[string]float64{}
	authors := map[string]float64{}
	genres := map[string]float64{}
	narrators := map[string]float64{}
	for _, row := range sessionRows {
		typ, title := row.Mediaitemtype, row.Displaytitle
		metaRaw, created := []byte(row.Mediametadata.String), row.Createdat
		secs := row.Timelistening.Float64
		sessions++
		total += secs
		// userStats.getStatsForYear (server/utils/queries/userStats.js:108-150):
		// the month is ls.createdAt.getMonth() in server-local time; book
		// sessions count toward a title only when displayTitle is non-empty,
		// and authors, narrators and genres are tallied for book sessions
		// only (podcast sessions add to totalPodcastListeningTime alone).
		if t := dbtime.Time(created); !t.IsZero() {
			months[int(t.In(time.Local).Month())-1] += secs
		}
		if typ.String != "book" {
			podcastTime += secs
			continue
		}
		bookTime += secs
		if title.String != "" {
			books[title.String] += secs
		}
		var m map[string]any
		_ = json.Unmarshal(metaRaw, &m)
		for _, x := range jsonvalue.Array(m["authors"]) {
			a, _ := x.(map[string]any)
			authors[jsonvalue.String(a["name"])] += secs
		}
		for _, x := range jsonvalue.Array(m["narrators"]) {
			n, _ := x.(string)
			narrators[n] += secs
		}
		for _, x := range jsonvalue.Array(m["genres"]) {
			g, _ := x.(string)
			if g != "" && !strings.Contains(strings.ToLower(g), "audiobook") && !strings.Contains(strings.ToLower(g), "audio book") {
				genres[g] += secs
			}
		}
	}
	finishedRows, err := sqlc.New(h.db).ListFinishedBookProgressBetween(r.Context(), sqlc.ListFinishedBookProgressBetweenParams{UserID: store.Text(user.ID), StartAt: start, EndAt: end})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	finished := 0
	finishedCovers := make([]any, 0, 5)
	longest := map[string]any(nil)
	longestDuration := float64(0)
	for _, row := range finishedRows {
		finished++
		if row.Duration.Valid && row.Duration.Float64 > longestDuration {
			longestDuration = row.Duration.Float64
			longest = map[string]any{"id": row.Mediaitemid.String, "title": row.Title.String, "duration": int(math.Round(row.Duration.Float64)), "finishedAt": dbtime.Millis(row.Finishedat)}
		}
		if row.Coverpath.Valid && len(finishedCovers) < 5 {
			if _, err := os.Stat(row.Coverpath.String); err == nil {
				finishedCovers = append(finishedCovers, row.Libraryitemid)
			}
		}
	}
	finishedSet := map[string]bool{}
	for _, v := range finishedCovers {
		finishedSet[v.(string)] = true
	}
	booksWithCovers := make([]any, 0, 25)
	booksWithCoversSet := map[string]bool{}
	yearSessions, err := h.listeningSessions(r, user.ID, "")
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	for _, session := range yearSessions {
		created := dbtime.Time(session.CreatedAt)
		if created.IsZero() || created.Before(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)) || !created.Before(time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)) {
			continue
		}
		// Upstream joins the session's book and uses its current coverPath
		// and library item id (userStats.js:8-30, 108-111), not the cover
		// path stored on the session. Its session order is RANDOM(), so the
		// order of booksWithCovers is unspecified upstream.
		if session.MediaItemType.String != "book" || len(booksWithCovers) >= 25 {
			continue
		}
		var itemID string
		var coverPath sql.NullString
		if err := h.db.QueryRowContext(r.Context(), `SELECT li.id, b.coverPath FROM books b JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book' WHERE b.id = ?`, session.MediaItemID.String).Scan(&itemID, &coverPath); err != nil {
			continue
		}
		if finishedSet[itemID] || booksWithCoversSet[itemID] || coverPath.String == "" {
			continue
		}
		if _, err := os.Stat(coverPath.String); err == nil {
			booksWithCovers = append(booksWithCovers, itemID)
			booksWithCoversSet[itemID] = true
		}
	}
	return httputil.JSON(w, r, map[string]any{"totalListeningSessions": sessions, "totalListeningTime": int(math.Round(total)), "totalBookListeningTime": int(math.Round(bookTime)), "totalPodcastListeningTime": int(math.Round(podcastTime)), "topAuthors": topEntries(authors, "name", 3), "topGenres": topEntries(genres, "genre", 3), "mostListenedNarrator": topOne(narrators, "name"), "mostListenedMonth": topMonth(months), "numBooksFinished": finished, "numBooksListened": len(books), "longestAudiobookFinished": longest, "booksWithCovers": booksWithCovers, "finishedBooksWithCovers": finishedCovers})
}

// Rank by the upstream per-entry Math.round of summed seconds.
func topEntries(m map[string]float64, key string, n int) []any {
	type kv struct {
		k string
		v int
	}
	a := []kv{}
	for k, v := range m {
		if k != "" {
			a = append(a, kv{k, int(math.Round(v))})
		}
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].v == a[j].v {
			return a[i].k < a[j].k
		}
		return a[i].v > a[j].v
	})
	if len(a) > n {
		a = a[:n]
	}
	out := make([]any, 0, len(a))
	for _, x := range a {
		out = append(out, map[string]any{key: x.k, "time": x.v})
	}
	return out
}
func topOne(m map[string]float64, key string) any {
	a := topEntries(m, key, 1)
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

// Round each month before choosing the highest total.
func topMonth(m map[int]float64) any {
	bestMonth, best := 0, 0
	for k, seconds := range m {
		v := int(math.Round(seconds))
		if v > best {
			bestMonth, best = k, v
		}
	}
	if best == 0 {
		return nil
	}
	return map[string]any{"month": bestMonth, "time": best}
}
