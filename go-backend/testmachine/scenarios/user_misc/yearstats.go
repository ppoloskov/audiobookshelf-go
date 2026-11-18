package user_misc

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	yearStatsSource = "server/controllers/MeController.js:641-655"
	userStatsSource = "server/utils/queries/userStats.js:71-209"
	yearClient      = "client/components/stats/YearInReview.vue:85,159-238"
)

// yearStats is GET /api/me/stats/year/:year as the YearInReview clients read it.
type yearStats struct {
	TotalListeningSessions    int           `json:"totalListeningSessions"`
	TotalListeningTime        float64       `json:"totalListeningTime"`
	TotalBookListeningTime    float64       `json:"totalBookListeningTime"`
	TotalPodcastListeningTime float64       `json:"totalPodcastListeningTime"`
	TopAuthors                []authorTime  `json:"topAuthors"`
	TopGenres                 []genreTime   `json:"topGenres"`
	MostListenedNarrator      *narratorTime `json:"mostListenedNarrator"`
	MostListenedMonth         *monthTime    `json:"mostListenedMonth"`
	NumBooksFinished          int           `json:"numBooksFinished"`
	NumBooksListened          int           `json:"numBooksListened"`
	LongestAudiobookFinished  *longestBook  `json:"longestAudiobookFinished"`
	BooksWithCovers           []string      `json:"booksWithCovers"`
	FinishedBooksWithCovers   []string      `json:"finishedBooksWithCovers"`
}

type authorTime struct {
	Name string  `json:"name"`
	Time float64 `json:"time"`
}

type genreTime struct {
	Genre string  `json:"genre"`
	Time  float64 `json:"time"`
}

type narratorTime struct {
	Time float64 `json:"time"`
	Name string  `json:"name"`
}

type monthTime struct {
	Month int     `json:"month"`
	Time  float64 `json:"time"`
}

type longestBook struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Duration   float64  `json:"duration"`
	FinishedAt *float64 `json:"finishedAt"`
}

// emptyYear is the all-zero answer for a user or year without history; arrays
// are empty (never null) and single-value picks are null.
func emptyYear() yearStats {
	return yearStats{
		TopAuthors: []authorTime{}, TopGenres: []genreTime{},
		BooksWithCovers: []string{}, FinishedBooksWithCovers: []string{},
	}
}

func yearStatsScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r120", Invariant: "INV-R120-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "year-stats-listener",
			Sources: []string{"server/routers/ApiRouter.js:194", yearStatsSource, userStatsSource, yearClient},
			Claim:   "GET /api/me/stats/year/2026 returns the listener's seven 2026 sessions as whole-second totals (65 overall, 49 books, 17 podcasts), one author, one genre, the most-listened narrator and month, two books listened, one book finished (null longest book because its duration is zero) and the cover lists of the finished and listened books.",
			Run: func(t *testing.T, e *harness.Env) {
				want := yearStats{
					TotalListeningSessions: 7, TotalListeningTime: 65, TotalBookListeningTime: 49, TotalPodcastListeningTime: 17,
					TopAuthors:           []authorTime{{Name: "Terry Fixture", Time: 49}},
					TopGenres:            []genreTime{{Genre: "Fiction", Time: 12}},
					MostListenedNarrator: &narratorTime{Time: 9, Name: "Nora Narrator"},
					MostListenedMonth:    &monthTime{Month: 10, Time: 53},
					NumBooksFinished:     1, NumBooksListened: 2,
					BooksWithCovers:         []string{e.IDs["saga1"]},
					FinishedBooksWithCovers: []string{e.IDs["novel"]},
				}
				var got yearStats
				getJSON(t, e, e.Token(t, "listener"), "/api/me/stats/year/2026", &got)
				equalJSON(t, "2026 stats", got, want)
			},
		},
		{
			Requirement: "route-r120", Invariant: "INV-R120-YEAR-001", Case: "edge", State: "stateless", Assertion: "year-stats-year-filter-and-empty",
			Sources: []string{yearStatsSource, userStatsSource},
			Claim:   "The year in the path selects only that year's sessions: 2025 is all zeros with empty arrays and null picks for the listener, root has no history at all, and the finisher shows one finished book but no listening time and no longest book.",
			Run: func(t *testing.T, e *harness.Env) {
				var past yearStats
				getJSON(t, e, e.Token(t, "listener"), "/api/me/stats/year/2025", &past)
				equalJSON(t, "listener 2025", past, emptyYear())

				var root yearStats
				getJSON(t, e, e.Token(t, "root"), "/api/me/stats/year/2026", &root)
				equalJSON(t, "root 2026", root, emptyYear())

				finisher := emptyYear()
				finisher.NumBooksFinished = 1
				finisher.FinishedBooksWithCovers = []string{e.IDs["saga1"]}
				var got yearStats
				getJSON(t, e, e.Token(t, "finisher"), "/api/me/stats/year/2026", &got)
				equalJSON(t, "finisher 2026", got, finisher)
			},
		},
		{
			Requirement: "route-r120", Invariant: "INV-R120-ERRORS-001", Case: "error", State: "stateless", Assertion: "year-stats-invalid-year",
			Sources: []string{yearStatsSource, "server/routers/ApiRouter.js:194"},
			Claim:   "GET /api/me/stats/year/:year answers 400 for a non-numeric year and for years outside 2000..9999, and 401 without a token.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				for _, year := range []string{"abc", "1999", "10000", "0000"} {
					wantStatus(t, e.Get(t, token, "/api/me/stats/year/"+year), http.StatusBadRequest, "invalid year "+year)
				}
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/stats/year/2026")
			},
		},
	}
}
