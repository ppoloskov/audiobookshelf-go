package library

// collapsedSeriesView is libraryItem.collapsedSeries on collapsed library pages
// (server/utils/queries/libraryItemsBookFilters.js:661-668).
type collapsedSeriesView struct {
	ID               string   `json:"id"`
	Name             *string  `json:"name"`
	NameIgnorePrefix *string  `json:"nameIgnorePrefix"`
	Sequence         *string  `json:"sequence"`
	NumBooks         int64    `json:"numBooks"`
	LibraryItemIDs   []string `json:"libraryItemIds"`
}

// collapsedSubseries is libraryItem.collapsedSeries on a series-filtered
// library page with collapseseries=1
// (server/utils/libraryHelpers.js:233-258). It differs from CollapsedSeries:
// no sequence, the prefix form is recomputed, and seriesSequenceList lists
// the filtered series' sequences the sub-series covers.
type collapsedSubseries struct {
	ID                 string   `json:"id"`
	Name               *string  `json:"name"`
	NameIgnorePrefix   string   `json:"nameIgnorePrefix"`
	LibraryItemIDs     []string `json:"libraryItemIds"`
	NumBooks           int      `json:"numBooks"`
	SeriesSequenceList string   `json:"seriesSequenceList"`
}

// collapsedSubseriesPage is GET /api/libraries/:id/items for a series filter
// with collapseseries=1: the payload object of LibraryController.getLibraryItems
// (LibraryController.js:595-635) in assignment order. sortBy is undefined when
// absent or not a valid collapse sort.
type collapsedSubseriesPage struct {
	Results        []any   `json:"results"`
	Total          int     `json:"total"`
	Limit          int     `json:"limit"`
	Page           int     `json:"page"`
	SortBy         *string `json:"sortBy,omitempty"`
	SortDesc       bool    `json:"sortDesc"`
	FilterBy       string  `json:"filterBy"`
	MediaType      string  `json:"mediaType"`
	Minified       bool    `json:"minified"`
	CollapseSeries bool    `json:"collapseseries"`
	Include        string  `json:"include"`
	Offset         int     `json:"offset"`
}
