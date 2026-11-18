package browse

import (
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	srcSeriesList   = "server/controllers/LibraryController.js:742-766"
	srcSeriesDetail = "server/controllers/LibraryController.js:778-806"
	srcSeriesQuery  = "server/utils/queries/seriesFilters.js:28-222"
	srcSeriesCtl    = "server/controllers/SeriesController.js:28-121"
)

// seriesJSON is a series as the series routes return it.
type seriesJSON struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	NameIgnore  string      `json:"nameIgnorePrefix"`
	Description *string     `json:"description"`
	AddedAt     int64       `json:"addedAt"`
	UpdatedAt   int64       `json:"updatedAt"`
	LibraryID   string      `json:"libraryId"`
	Books       []listItem  `json:"books"`
	RssFeed     *feedRef    `json:"rssFeed"`
	Progress    *seriesProg `json:"progress"`
	Duration    *float64    `json:"totalDuration"`
}

// seriesProg is the include=progress object.
type seriesProg struct {
	LibraryItemIDs         []string `json:"libraryItemIds"`
	LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
	IsFinished             bool     `json:"isFinished"`
}

// seriesPage is the payload of GET /api/libraries/:id/series.
type seriesPage struct {
	Results  []seriesJSON `json:"results"`
	Total    *int         `json:"total"`
	Limit    *int         `json:"limit"`
	Page     *int         `json:"page"`
	SortBy   *string      `json:"sortBy"`
	SortDesc bool         `json:"sortDesc"`
	FilterBy *string      `json:"filterBy"`
	Minified bool         `json:"minified"`
	Include  string       `json:"include"`
}

// names lists the series names in response order.
func (p seriesPage) names() []string {
	out := make([]string, 0, len(p.Results))
	for _, series := range p.Results {
		out = append(out, series.Name)
	}
	return out
}

// getSeriesPage requests the series list and decodes it.
func getSeriesPage(t *testing.T, e *harness.Env, token string, values url.Values) seriesPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: libraryPath(e, bookLibrary, "/series"), Token: token, Query: values})
	mustOK(t, "series list "+values.Encode(), resp)
	var page seriesPage
	resp.JSON(t, &page)
	return page
}

// getSeries requests one series route and decodes it, failing unless 200.
func getSeries(t *testing.T, e *harness.Env, token, path string, values url.Values) seriesJSON {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: path, Token: token, Query: values})
	mustOK(t, "GET "+path, resp)
	var series seriesJSON
	resp.JSON(t, &series)
	return series
}

// bookKeys maps the books of a series to fixture keys.
func bookKeys(e *harness.Env, series seriesJSON) []string {
	ids := make([]string, 0, len(series.Books))
	for _, book := range series.Books {
		ids = append(ids, book.ID)
	}
	return keysOf(e, ids)
}

// seriesRows reads the series row by id.
func seriesRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT name, nameIgnorePrefix, description, libraryId, updatedAt FROM series WHERE id = ?", id)
	if len(rows) != 1 {
		return nil
	}
	return rows[0]
}
