package catalog

import (
	"audiobookshelf-compat/internal/library"
)

// shelf is one home-page shelf of GET /api/libraries/:id/personalized
// (LibraryItem.getPersonalizedShelves, server/models/LibraryItem.js:384-491).
// Entities holds the shelf's entries (minified items, series or authors).
// The fields are in alphabetical key order, which is the response order.
type shelf struct {
	Entities       any    `json:"entities"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	LabelStringKey string `json:"labelStringKey"`
	Total          int    `json:"total"`
	Type           string `json:"type"`
}

// seriesShelfEntry is one entity of the "recent-series" home shelf:
// library.SeriesView.toOldJSON (server/models/Series.js:168) plus the rssFeed and books
// that libraryFilters.getSeriesMostRecentlyAdded attaches
// (server/utils/queries/libraryFilters.js:286-312), in assignment order.
type seriesShelfEntry struct {
	library.SeriesView
	RSSFeed any `json:"rssFeed,omitempty"`
	// Books are minified library items whose author and series names are
	// blank, because upstream empties book.authors and book.series first.
	Books []any `json:"books"`
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
