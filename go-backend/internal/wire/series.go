package wire

import "encoding/json"

// Series is Series.toOldJSON (server/models/Series.js:168-178).
type Series struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Description      *string `json:"description"`
	AddedAt          int64   `json:"addedAt"`
	UpdatedAt        int64   `json:"updatedAt"`
	LibraryID        string  `json:"libraryId"`
}

// SeriesProgress is the include=progress object of GET /api/series/:id
// (SeriesController.js:44-54).
type SeriesProgress struct {
	LibraryItemIDs         []string `json:"libraryItemIds"`
	LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
	IsFinished             bool     `json:"isFinished"`
}

// SeriesDetail is the body of GET /api/series/:id: the series, then progress
// (include=progress) and rssFeed (include=rssfeed; null when the series has no
// open feed), assigned in that order (SeriesController.js:41-61).
type SeriesDetail struct {
	Series
	Progress *SeriesProgress `json:"progress,omitempty"`
	RSSFeed  json.RawMessage `json:"rssFeed,omitempty"`
}
