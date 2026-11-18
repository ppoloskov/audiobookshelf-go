package library_data

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	statsRoute = "server/routers/ApiRouter.js:85"
	statsSrc   = "server/controllers/LibraryController.js:978-1012"
)

type statItem struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Size     *int64   `json:"size"`
	Duration *float64 `json:"duration"`
}

type statAuthor struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type statGenre struct {
	Genre string `json:"genre"`
	Count int    `json:"count"`
}

// libraryStats is the document of GET /api/libraries/:id/stats.
type libraryStats struct {
	LargestItems    []statItem   `json:"largestItems"`
	TotalAuthors    *int         `json:"totalAuthors"`
	AuthorsWithCnt  []statAuthor `json:"authorsWithCount"`
	TotalGenres     int          `json:"totalGenres"`
	GenresWithCount []statGenre  `json:"genresWithCount"`
	TotalItems      int          `json:"totalItems"`
	LongestItems    []statItem   `json:"longestItems"`
	TotalSize       int64        `json:"totalSize"`
	TotalDuration   float64      `json:"totalDuration"`
	NumAudioTracks  int          `json:"numAudioTracks"`
}

func getStats(t *testing.T, e *harness.Env, token, libraryID string) (libraryStats, map[string]json.RawMessage) {
	t.Helper()
	resp := e.Get(t, token, libPath(libraryID, "/stats"))
	requireStatus(t, resp, http.StatusOK, "stats")
	var stats libraryStats
	var raw map[string]json.RawMessage
	resp.JSON(t, &stats)
	resp.JSON(t, &raw)
	return stats, raw
}

func requireNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func sizeOf(item statItem) int64 {
	if item.Size == nil {
		return -1
	}
	return *item.Size
}

func durationOf(item statItem) float64 {
	if item.Duration == nil {
		return -1
	}
	return *item.Duration
}

func statsScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r032", Invariant: "INV-ROUTE-R032-CONTRACT", Case: "normal", State: "stateless", Assertion: "stats-book-library-fixture",
			Sources: []string{statsRoute, statsSrc, "server/utils/queries/libraryItemFilters.js:197-223", "server/utils/queries/authorFilters.js:11-51", "server/utils/queries/libraryItemsBookFilters.js:1290-1352"},
			Claim:   "GET /api/libraries/:id/stats of the fixture book library answers the five items by size and by duration with id, title and size or duration, the authors with book counts, the genres with counts, and the totals of items, size, duration and audio tracks.",
			Run: func(t *testing.T, e *harness.Env) {
				stats, raw := getStats(t, e, e.Token(t, "root"), e.IDs[bookLib])
				for _, key := range []string{"largestItems", "totalAuthors", "authorsWithCount", "totalGenres", "genresWithCount", "totalItems", "longestItems", "totalSize", "totalDuration", "numAudioTracks"} {
					if _, ok := raw[key]; !ok {
						t.Fatalf("stats lacks %q", key)
					}
				}
				wantLargest := []struct {
					key, title string
					size       int64
				}{{"saga1", "The Beginning", 244064}, {"saga2", "The Middle", 218657}, {"novel", "Single File Novel", 193740}, {"disposable", "Hard Delete Me", 12530}, {"ebook", "Ebook Only", 1367}}
				if len(stats.LargestItems) != len(wantLargest) {
					t.Fatalf("largestItems: %+v", stats.LargestItems)
				}
				for i, want := range wantLargest {
					got := stats.LargestItems[i]
					if got.ID != e.IDs[want.key] || got.Title != want.title || sizeOf(got) != want.size || got.Duration != nil {
						t.Fatalf("largestItems[%d] = %+v, want %+v", i, got, want)
					}
				}
				wantLongest := []struct {
					key, title string
					duration   float64
				}{{"saga1", "The Beginning", 60.186123}, {"novel", "Single File Novel", 45}, {"saga2", "The Middle", 30}, {"disposable", "Hard Delete Me", 3.056327}, {"ebook", "Ebook Only", 0}}
				if len(stats.LongestItems) != len(wantLongest) {
					t.Fatalf("longestItems: %+v", stats.LongestItems)
				}
				for i, want := range wantLongest {
					got := stats.LongestItems[i]
					if got.ID != e.IDs[want.key] || got.Title != want.title || got.Size != nil {
						t.Fatalf("longestItems[%d] = %+v, want %+v", i, got, want)
					}
					requireNear(t, "longest duration", durationOf(got), want.duration)
				}
				if stats.TotalAuthors == nil || *stats.TotalAuthors != 3 {
					t.Fatalf("totalAuthors: %v", stats.TotalAuthors)
				}
				want := map[string]int{"Ada Example": 2, "Terry Fixture": 2, "Zed Disposable": 1}
				if len(stats.AuthorsWithCnt) != len(want) {
					t.Fatalf("authorsWithCount: %+v", stats.AuthorsWithCnt)
				}
				for i, author := range stats.AuthorsWithCnt {
					if author.Count != want[author.Name] || author.ID == "" {
						t.Fatalf("authorsWithCount[%d] = %+v", i, author)
					}
					rows := e.Rows(t, "SELECT name FROM authors WHERE id = ?", author.ID)
					if len(rows) != 1 || rows[0]["name"] != author.Name {
						t.Fatalf("author id %s does not match name %s", author.ID, author.Name)
					}
				}
				if stats.AuthorsWithCnt[2].Name != "Zed Disposable" {
					t.Fatalf("authors must be ordered by count descending: %+v", stats.AuthorsWithCnt)
				}
				requireEqual(t, "genresWithCount", stats.GenresWithCount, []statGenre{{Genre: "Fiction", Count: 1}})
				if stats.TotalGenres != 1 || stats.TotalItems != 5 || stats.TotalSize != 670358 || stats.NumAudioTracks != 6 {
					t.Fatalf("totals: %+v", stats)
				}
				requireNear(t, "totalDuration", stats.TotalDuration, 138.24245)
			},
		},
		{
			Requirement: "route-r032", Invariant: "INV-ROUTE-R032-PODCAST-001", Case: "edge", State: "stateless", Assertion: "stats-podcast-library-fixture",
			Sources: []string{statsRoute, "server/controllers/LibraryController.js:1000-1011", "server/utils/queries/libraryItemsPodcastFilters.js"},
			Claim:   "GET stats of a podcast library omits totalAuthors and authorsWithCount and reports the podcast's genre, size, duration and episode count as audio tracks.",
			Run: func(t *testing.T, e *harness.Env) {
				stats, raw := getStats(t, e, e.Token(t, "root"), e.IDs[podcastLib])
				for _, key := range []string{"totalAuthors", "authorsWithCount"} {
					if _, ok := raw[key]; ok {
						t.Fatalf("a podcast library has no %q", key)
					}
				}
				if len(stats.LargestItems) != 1 || stats.LargestItems[0].ID != e.IDs["podcast"] || stats.LargestItems[0].Title != "Fixture Cast" || sizeOf(stats.LargestItems[0]) != 219867 {
					t.Fatalf("largestItems: %+v", stats.LargestItems)
				}
				if len(stats.LongestItems) != 1 || stats.LongestItems[0].ID != e.IDs["podcast"] || stats.LongestItems[0].Title != "Fixture Cast" {
					t.Fatalf("longestItems: %+v", stats.LongestItems)
				}
				requireNear(t, "longest duration", durationOf(stats.LongestItems[0]), 54.204082)
				requireEqual(t, "genresWithCount", stats.GenresWithCount, []statGenre{{Genre: "Technology", Count: 1}})
				if stats.TotalGenres != 1 || stats.TotalItems != 1 || stats.TotalSize != 219867 || stats.NumAudioTracks != 3 {
					t.Fatalf("totals: %+v", stats)
				}
				requireNear(t, "totalDuration", stats.TotalDuration, 54.204082)
			},
		},
		{
			Requirement: "route-r032", Invariant: "INV-ROUTE-R032-LIMITS-001", Case: "edge", State: "stateless", Assertion: "stats-top-ten-limits-and-genre-order",
			Sources: []string{statsRoute, statsSrc, "server/utils/queries/libraryItemFilters.js:197-223", "server/utils/queries/authorFilters.js:27-51"},
			Claim:   "In a library of twelve books by twelve authors the largest items, longest items and authors with count are cut to ten, the largest are the ten biggest by size in descending order, totals cover all twelve, and genres are ordered by count.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "stats-many", nil, nil)
				source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3")
				data, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				for i := 1; i <= 12; i++ {
					// trailing bytes after the audio make every size distinct; ffprobe ignores them
					padded := append(slices.Clone(data), make([]byte, i*100)...)
					folder := filepath.Join(lib.Root, fmt.Sprintf("Author %02d", i), fmt.Sprintf("Book %02d", i))
					writeFile(t, filepath.Join(folder, "audio.mp3"), string(padded))
				}
				lib.scan(t, e, false, 12)
				ids := map[int]string{}
				for _, row := range e.Rows(t, "SELECT id, path FROM libraryItems WHERE libraryId = ?", lib.ID) {
					var n int
					if _, err := fmt.Sscanf(filepath.Base(row["path"].(string)), "Book %d", &n); err != nil {
						t.Fatalf("unexpected path %v", row["path"])
					}
					ids[n] = row["id"].(string)
				}
				if len(ids) != 12 {
					t.Fatalf("items by title: %v", ids)
				}
				genres := map[int]string{1: `["G1","G2"]`, 2: `["G1","G2"]`, 3: `["G1"]`}
				for i := 1; i <= 12; i++ {
					// the audio tags name one author and one title for every copy: give each book its own
					body := fmt.Sprintf(`{"metadata":{"title":"Book %02d","authors":[{"name":"Author %02d"}]`, i, i)
					if g, ok := genres[i]; ok {
						body += `,"genres":` + g
					}
					patchMedia(t, e, ids[i], body+"}}")
				}
				stats, _ := getStats(t, e, e.Token(t, "root"), lib.ID)
				if stats.TotalItems != 12 || stats.NumAudioTracks != 12 || stats.TotalAuthors == nil || *stats.TotalAuthors != 12 {
					t.Fatalf("totals must cover all items: %+v", stats)
				}
				if len(stats.LargestItems) != 10 || len(stats.LongestItems) != 10 || len(stats.AuthorsWithCnt) != 10 {
					t.Fatalf("top lists must hold ten entries: %d %d %d", len(stats.LargestItems), len(stats.LongestItems), len(stats.AuthorsWithCnt))
				}
				for i, item := range stats.LargestItems {
					if want := ids[12-i]; item.ID != want || item.Title != fmt.Sprintf("Book %02d", 12-i) {
						t.Fatalf("largestItems[%d] = %+v, want %s", i, item, want)
					}
				}
				var sum int64
				for _, row := range e.Rows(t, "SELECT size FROM libraryItems WHERE libraryId = ?", lib.ID) {
					sum += row["size"].(int64)
				}
				if stats.TotalSize != sum || sum <= 0 {
					t.Fatalf("totalSize %d, rows %d", stats.TotalSize, sum)
				}
				requireEqual(t, "genres", stats.GenresWithCount, []statGenre{{"G1", 3}, {"G2", 2}})
				if stats.TotalGenres != 2 {
					t.Fatalf("totalGenres %d", stats.TotalGenres)
				}
			},
		},
		{
			Requirement: "route-r032", Invariant: "INV-ROUTE-R032-EMPTY-001", Case: "edge", State: "stateless", Assertion: "stats-empty-library-zeros",
			Sources: []string{statsRoute, "server/utils/queries/libraryItemsBookFilters.js:1312-1324"},
			Claim:   "GET stats of a library without items answers empty lists and zero totals for books.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "stats-empty", nil, nil)
				stats, raw := getStats(t, e, e.Token(t, "root"), lib.ID)
				for _, key := range []string{"largestItems", "authorsWithCount", "genresWithCount", "longestItems"} {
					if string(raw[key]) != "[]" {
						t.Fatalf("%s = %s, want []", key, raw[key])
					}
				}
				if stats.TotalAuthors == nil || *stats.TotalAuthors != 0 || stats.TotalItems != 0 || stats.TotalSize != 0 || stats.TotalDuration != 0 || stats.NumAudioTracks != 0 || stats.TotalGenres != 0 {
					t.Fatalf("totals: %+v", stats)
				}
			},
		},
		{
			Requirement: "route-r032", Invariant: "INV-ROUTE-R032-AUTH-001", Case: "error", State: "stateless", Assertion: "stats-denied-and-unknown",
			Sources: []string{statsRoute, middleware},
			Claim:   "GET stats answers 401 without a token, 403 for a user without access to the library (before existence is checked), 404 for an unknown id, and 400 for an invalid limit; any user with library access may read it.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.podcastOnly(t, e)
				path := libPath(e.IDs[bookLib], "/stats")
				requireStatus(t, e.Get(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, limited.Token, path), http.StatusForbidden, "limited user")
				requireStatus(t, e.Get(t, limited.Token, libPath(missingID, "/stats")), http.StatusForbidden, "limited, unknown")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/stats")), http.StatusNotFound, "unknown")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path+"?limit=-5"), http.StatusBadRequest, "negative limit")
				root, _ := getStats(t, e, e.Token(t, "root"), e.IDs[bookLib])
				listener, _ := getStats(t, e, e.Token(t, "listener"), e.IDs[bookLib])
				requireEqual(t, "listener stats", listener.TotalItems, root.TotalItems)
			},
		},
	}
}
