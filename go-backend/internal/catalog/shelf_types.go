package catalog

// shelf is one home-page shelf of GET /api/libraries/:id/personalized
// (LibraryItem.getPersonalizedShelves, server/models/LibraryItem.js:384-491).
// Entities holds the shelf's entries (minified items, series or authors).
// The fields are declared in the alphabetical order encoding/json gave the map
// this replaced, so the response bytes are unchanged.
type shelf struct {
	Entities       any    `json:"entities"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	LabelStringKey string `json:"labelStringKey"`
	Total          int    `json:"total"`
	Type           string `json:"type"`
}

// seriesShelfEntry is one entity of the "recent-series" home shelf:
// seriesView.toOldJSON (server/models/Series.js:168) plus the rssFeed and books
// that libraryFilters.getSeriesMostRecentlyAdded attaches
// (server/utils/queries/libraryFilters.js:286-312), in assignment order.
type seriesShelfEntry struct {
	seriesView
	RSSFeed any `json:"rssFeed,omitempty"`
	// Books are minified library items whose author and series names are
	// blank, because upstream empties book.authors and book.series first.
	Books []any `json:"books"`
}

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

// mediaItemShareView is libraryItem.mediaItemShare, the open share of a
// book as ShareManager.findByMediaItemId returns it
// (server/managers/ShareManager.js:49-60): MediaItemShare.toJSON() without
// pash, userId and extraData, so its dates serialize as ISO strings.
type mediaItemShareView struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  string  `json:"mediaItemType"`
	Slug           string  `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	IsDownloadable bool    `json:"isDownloadable"`
	CreatedAt      *string `json:"createdAt"`
	UpdatedAt      *string `json:"updatedAt"`
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

// itemMatch is one library item hit in a search response
// ({libraryItem} entries of server/utils/queries/libraryItemsPodcastFilters.js search).
type itemMatch struct {
	LibraryItem any `json:"libraryItem"`
}

// batchUpdateResult is the body of POST /api/items/batch/update
// (LibraryItemController.js:730-733).
type batchUpdateResult struct {
	Success bool `json:"success"`
	Updates int  `json:"updates"`
}
