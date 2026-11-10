package library

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"encoding/json"
	"errors"
)

// SeriesView is Series.toOldJSON (server/models/Series.js:168-178).
type SeriesView struct {
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
	SeriesView
	Progress *SeriesProgress `json:"progress,omitempty"`
	RSSFeed  json.RawMessage `json:"rssFeed,omitempty"`
}

// NewSeriesView is Series.toOldJSON (server/models/Series.js:168-178); the
// prefix-free name is computed from the name, not read from the column, which
// a rename (PATCH /api/series/:id) leaves unchanged (Series.js:172).
func NewSeriesView(s SeriesRecord, prefixes []string) (SeriesView, error) {
	added, err := dbtime.OptionalMillis(s.CreatedAt)
	if err != nil || added == nil {
		return SeriesView{}, errors.New("series createdAt")
	}
	updated, err := dbtime.OptionalMillis(s.UpdatedAt)
	if err != nil || updated == nil {
		return SeriesView{}, errors.New("series updatedAt")
	}
	var prefixFree *string
	if s.Name.Valid {
		value := textsort.PrefixAtEnd(s.Name.String, prefixes)
		prefixFree = &value
	}
	return SeriesView{ID: s.ID, Name: store.NullStringPtr(s.Name), NameIgnorePrefix: prefixFree, Description: store.NullStringPtr(s.Description), AddedAt: *added, UpdatedAt: *updated, LibraryID: s.LibraryID}, nil
}

// SeriesListEntry is one result of GET /api/libraries/:id/series: the series
// with its visible books, the total duration when sorted by it, and its feed.
type SeriesListEntry struct {
	SeriesView
	TotalDuration float64          `json:"totalDuration,omitempty"`
	Books         []items.Minified `json:"books"`
	RSSFeed       *feeds.Minified  `json:"rssFeed,omitempty"`
}
