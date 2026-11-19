package admin_files

import (
	"math"
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	statsYearPath   = "/api/stats/year/"
	statsServerPath = "/api/stats/server"

	statsYearRoute   = "server/routers/ApiRouter.js:332"
	statsServerRoute = "server/routers/ApiRouter.js:333"
	statsAdminSrc    = "server/controllers/StatsController.js:66-73"
	statsYearSrc     = "server/controllers/StatsController.js:50-64"
	statsServerSrc   = "server/controllers/StatsController.js:23-48"
	adminStatsSrc    = "server/utils/queries/adminStats.js:69-216"
)

// listeningStat is one topAuthors/topNarrators entry of the year report.
type listeningStat struct {
	Name string `json:"name"`
	Time int    `json:"time"`
}

// genreStat is one topGenres entry of the year report.
type genreStat struct {
	Genre string `json:"genre"`
	Time  int    `json:"time"`
}

// yearStats is the year-in-review payload the admin client renders.
type yearStats struct {
	NumListeningSessions    int             `json:"numListeningSessions"`
	NumBooksAdded           int             `json:"numBooksAdded"`
	NumAuthorsAdded         int             `json:"numAuthorsAdded"`
	TotalBooksAddedSize     float64         `json:"totalBooksAddedSize"`
	TotalBooksAddedDuration float64         `json:"totalBooksAddedDuration"`
	BooksAddedWithCovers    []string        `json:"booksAddedWithCovers"`
	TotalBooksSize          float64         `json:"totalBooksSize"`
	TotalBooksDuration      float64         `json:"totalBooksDuration"`
	TotalListeningTime      float64         `json:"totalListeningTime"`
	NumBooks                int             `json:"numBooks"`
	TopAuthors              []listeningStat `json:"topAuthors"`
	TopNarrators            []listeningStat `json:"topNarrators"`
	TopGenres               []genreStat     `json:"topGenres"`
}

// sizeStat is one media type of the server statistics payload.
type sizeStat struct {
	TotalSize     int `json:"totalSize"`
	NumItems      int `json:"numItems"`
	NumAudioFiles int `json:"numAudioFiles"`
}

// serverStats is the server statistics payload.
type serverStats struct {
	Books    sizeStat `json:"books"`
	Podcasts sizeStat `json:"podcasts"`
	Total    sizeStat `json:"total"`
}

// wantFloat asserts a JSON number against a fixture-derived value. SQLite sums
// and JavaScript additions are doubles; the tolerance keeps the check about the
// value rather than the last bit of the sum order.
func wantFloat(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// wantInt asserts an exact integer, so a default value cannot pass.
func wantInt(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

// assertJSONEqual compares two decoded JSON values by value.
func assertJSONEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %+v, want %+v", what, got, want)
	}
}

func statsScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r197", "INV-ROUTE-R197-STATS", "normal", "stateless", "stats-year-reports-the-fixture-year",
			"GET /api/stats/year/2026 as an admin answers 200 with the year report of the fixture: 5 books and 3 authors added, the two items that have a cover, the stored sizes and durations, 7 listening sessions, 65.4 seconds listened, Terry Fixture (49), Nora Narrator (9), Technology (17) and Fiction (12) as the top lists.",
			[]string{statsYearRoute, statsYearSrc, statsAdminSrc, adminStatsSrc}, func(t *testing.T, e *harness.Env) {
				want := yearStats{
					NumListeningSessions:    7,
					NumBooksAdded:           5,
					NumAuthorsAdded:         3,
					TotalBooksAddedSize:     670358,
					TotalBooksAddedDuration: 138,
					BooksAddedWithCovers:    []string{e.IDs["novel"], e.IDs["saga1"]},
					TotalBooksSize:          670358,
					TotalBooksDuration:      138.24245,
					TotalListeningTime:      65.4,
					NumBooks:                5,
					TopAuthors:              []listeningStat{{Name: "Terry Fixture", Time: 49}},
					TopNarrators:            []listeningStat{{Name: "Nora Narrator", Time: 9}},
					TopGenres:               []genreStat{{Genre: "Technology", Time: 17}, {Genre: "Fiction", Time: 12}},
				}
				resp := e.Get(t, e.Token(t, "root"), statsYearPath+"2026")
				wantStatus(t, "stats year 2026", resp, http.StatusOK)
				var got yearStats
				resp.JSON(t, &got)
				exactKeys(t, decode(resp), "numListeningSessions", "numBooksAdded", "numAuthorsAdded", "totalBooksAddedSize",
					"totalBooksAddedDuration", "booksAddedWithCovers", "totalBooksSize", "totalBooksDuration", "totalListeningTime",
					"numBooks", "topAuthors", "topNarrators", "topGenres")
				wantInt(t, "numListeningSessions", got.NumListeningSessions, want.NumListeningSessions)
				wantInt(t, "numBooksAdded", got.NumBooksAdded, want.NumBooksAdded)
				wantInt(t, "numAuthorsAdded", got.NumAuthorsAdded, want.NumAuthorsAdded)
				wantFloat(t, "totalBooksAddedSize", got.TotalBooksAddedSize, want.TotalBooksAddedSize)
				wantFloat(t, "totalBooksAddedDuration", got.TotalBooksAddedDuration, want.TotalBooksAddedDuration)
				// The source orders booksAddedWithCovers randomly: compare the set.
				sameList(t, "booksAddedWithCovers", sortedStrings(got.BooksAddedWithCovers), sortedStrings(want.BooksAddedWithCovers))
				wantFloat(t, "totalBooksSize", got.TotalBooksSize, want.TotalBooksSize)
				wantFloat(t, "totalBooksDuration", got.TotalBooksDuration, want.TotalBooksDuration)
				wantFloat(t, "totalListeningTime", got.TotalListeningTime, want.TotalListeningTime)
				wantInt(t, "numBooks", got.NumBooks, want.NumBooks)
				assertJSONEqual(t, "topAuthors", got.TopAuthors, want.TopAuthors)
				assertJSONEqual(t, "topNarrators", got.TopNarrators, want.TopNarrators)
				assertJSONEqual(t, "topGenres", got.TopGenres, want.TopGenres)
			}),
		scenario("route-r197", "INV-ROUTE-R197-STATS", "edge", "stateless", "stats-year-empty-year-and-bounds",
			"GET /api/stats/year/2025 answers 200 with no sessions, books or authors added and empty top lists, while the whole-library totals stay 670358 bytes, 138.24245 seconds and 5 books because the source's year filter never changes; the accepted bounds 2000 and 9999 answer the same report.",
			[]string{statsYearRoute, statsYearSrc, adminStatsSrc}, func(t *testing.T, e *harness.Env) {
				for _, year := range []string{"2025", "2000", "9999"} {
					resp := e.Get(t, e.Token(t, "root"), statsYearPath+year)
					wantStatus(t, "stats year "+year, resp, http.StatusOK)
					var got yearStats
					resp.JSON(t, &got)
					empty := yearStats{BooksAddedWithCovers: []string{}, TopAuthors: []listeningStat{}, TopNarrators: []listeningStat{}, TopGenres: []genreStat{},
						TotalBooksSize: 670358, TotalBooksDuration: 138.24245, NumBooks: 5}
					assertJSONEqual(t, "stats year "+year, got, empty)
				}
			}),
		scenario("route-r197", "INV-ROUTE-R197-ROUTE", "error", "stateless", "stats-year-rejects-callers-and-years",
			"GET /api/stats/year/:year answers 401 without a token and 403 to a non-admin (also with an invalid year), and 400 for a year that is not a number or outside 2000-9999.",
			[]string{statsYearRoute, statsYearSrc, statsAdminSrc}, func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				anonymousAndListener(t, e, "GET", statsYearPath+"2026", nil)
				wantStatus(t, "listener with an invalid year", e.Get(t, e.Token(t, "listener"), statsYearPath+"nope"), http.StatusForbidden)
				for _, bad := range []string{"nope", "0", "1999", "10000"} {
					wantStatus(t, "year "+bad, e.Get(t, root, statsYearPath+bad), http.StatusBadRequest)
				}
			}),
		scenario("route-r198", "INV-ROUTE-R198-STATS", "normal", "stateless", "stats-server-reports-the-fixture-totals",
			"GET /api/stats/server as an admin answers 200 with books (670358 bytes, 5 items, 6 audio files), podcasts (219867 bytes, 1 item, 3 audio files) and the total (890225 bytes, 6 items, 9 audio files).",
			[]string{statsServerRoute, statsServerSrc, adminStatsSrc}, func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), statsServerPath)
				wantStatus(t, "stats server", resp, http.StatusOK)
				var got serverStats
				resp.JSON(t, &got)
				object := exactKeys(t, decode(resp), "books", "podcasts", "total")
				for _, key := range []string{"books", "podcasts", "total"} {
					exactKeys(t, object[key], "totalSize", "numItems", "numAudioFiles")
				}
				want := serverStats{
					Books:    sizeStat{TotalSize: 670358, NumItems: 5, NumAudioFiles: 6},
					Podcasts: sizeStat{TotalSize: 219867, NumItems: 1, NumAudioFiles: 3},
					Total:    sizeStat{TotalSize: 890225, NumItems: 6, NumAudioFiles: 9},
				}
				assertJSONEqual(t, "stats server", got, want)
			}),
		scenario("route-r198", "INV-ROUTE-R198-STATS", "edge", "stateless", "stats-server-repeated-reads-agree",
			"GET /api/stats/server answers the same totals on a repeat read and ignores query parameters.",
			[]string{statsServerRoute, statsServerSrc}, func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				first := e.Get(t, root, statsServerPath)
				wantStatus(t, "stats server", first, http.StatusOK)
				again := e.Do(t, harness.Request{Path: statsServerPath, Token: root, Query: map[string][]string{"year": {"2020"}}})
				wantStatus(t, "stats server with a query", again, http.StatusOK)
				var a, b serverStats
				first.JSON(t, &a)
				again.JSON(t, &b)
				assertJSONEqual(t, "repeated stats server", b, a)
			}),
		scenario("route-r198", "INV-ROUTE-R198-ROUTE", "error", "stateless", "stats-server-rejects-non-admins",
			"GET /api/stats/server answers 401 without a token and 403 to the non-admin fixture user.",
			[]string{statsServerRoute, statsAdminSrc}, func(t *testing.T, e *harness.Env) {
				anonymousAndListener(t, e, "GET", statsServerPath, nil)
			}),
	}
}
