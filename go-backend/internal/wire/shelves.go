package wire

// SeriesShelfEntry is one entity of the "recent-series" home shelf:
// Series.toOldJSON (server/models/Series.js:168) plus the rssFeed and books
// that libraryFilters.getSeriesMostRecentlyAdded attaches
// (server/utils/queries/libraryFilters.js:286-312), in assignment order.
type SeriesShelfEntry struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Description      *string `json:"description"`
	AddedAt          *int64  `json:"addedAt"`
	UpdatedAt        *int64  `json:"updatedAt"`
	LibraryID        string  `json:"libraryId"`
	RSSFeed          any     `json:"rssFeed,omitempty"`
	// Books are minified library items whose author and series names are
	// blank, because upstream empties book.authors and book.series first.
	Books []any `json:"books"`
}

// AuthorShelfEntry is one entity of the "newest-authors" home shelf:
// Author.toOldJSONExpanded(numBooks) (server/models/Author.js:195).
type AuthorShelfEntry struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     *int64  `json:"addedAt"`
	UpdatedAt   *int64  `json:"updatedAt"`
	NumBooks    int     `json:"numBooks"`
}

// CollapsedSeries is libraryItem.collapsedSeries on collapsed library pages
// (server/utils/queries/libraryItemsBookFilters.js:661-668).
type CollapsedSeries struct {
	ID               string   `json:"id"`
	Name             *string  `json:"name"`
	NameIgnorePrefix *string  `json:"nameIgnorePrefix"`
	Sequence         *string  `json:"sequence"`
	NumBooks         int64    `json:"numBooks"`
	LibraryItemIDs   []string `json:"libraryItemIds"`
}

// CollapsedSubseries is libraryItem.collapsedSeries on a series-filtered
// library page with collapseseries=1
// (server/utils/libraryHelpers.js:233-258). It differs from CollapsedSeries:
// no sequence, the prefix form is recomputed, and seriesSequenceList lists
// the filtered series' sequences the sub-series covers.
type CollapsedSubseries struct {
	ID                 string   `json:"id"`
	Name               *string  `json:"name"`
	NameIgnorePrefix   string   `json:"nameIgnorePrefix"`
	LibraryItemIDs     []string `json:"libraryItemIds"`
	NumBooks           int      `json:"numBooks"`
	SeriesSequenceList string   `json:"seriesSequenceList"`
}

// CollapsedSubseriesPage is GET /api/libraries/:id/items for a series filter
// with collapseseries=1: the payload object of LibraryController.getLibraryItems
// (LibraryController.js:595-635) in assignment order. sortBy is undefined when
// absent or not a valid collapse sort.
type CollapsedSubseriesPage struct {
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

// LibraryCollectionsPage is GET /api/libraries/:id/collections
// (server/controllers/LibraryController.js:820-842). sortBy and filterBy are
// undefined unless supplied.
type LibraryCollectionsPage struct {
	Results  []any   `json:"results"`
	Total    int     `json:"total"`
	Limit    int     `json:"limit"`
	Page     int     `json:"page"`
	SortBy   *string `json:"sortBy,omitempty"`
	SortDesc bool    `json:"sortDesc"`
	FilterBy *string `json:"filterBy,omitempty"`
	Minified bool    `json:"minified"`
	Include  string  `json:"include"`
}

// LibraryPlaylistsPage is GET /api/libraries/:id/playlists
// (server/controllers/LibraryController.js:856-869).
type LibraryPlaylistsPage struct {
	Results []any `json:"results"`
	Total   int   `json:"total"`
	Limit   int   `json:"limit"`
	Page    int   `json:"page"`
}
