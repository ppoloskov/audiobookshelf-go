package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"encoding/json"
	"errors"
)

// seriesView is Series.toOldJSON (server/models/Series.js:168-178).
type seriesView struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Description      *string `json:"description"`
	AddedAt          int64   `json:"addedAt"`
	UpdatedAt        int64   `json:"updatedAt"`
	LibraryID        string  `json:"libraryId"`
}

// seriesProgress is the include=progress object of GET /api/series/:id
// (SeriesController.js:44-54).
type seriesProgress struct {
	LibraryItemIDs         []string `json:"libraryItemIds"`
	LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
	IsFinished             bool     `json:"isFinished"`
}

// seriesDetail is the body of GET /api/series/:id: the series, then progress
// (include=progress) and rssFeed (include=rssfeed; null when the series has no
// open feed), assigned in that order (SeriesController.js:41-61).
type seriesDetail struct {
	seriesView
	Progress *seriesProgress `json:"progress,omitempty"`
	RSSFeed  json.RawMessage `json:"rssFeed,omitempty"`
}

// newSeriesView is Series.toOldJSON (server/models/Series.js:168-178); the
// prefix-free name is computed from the name, not read from the column, which
// a rename (PATCH /api/series/:id) leaves unchanged (Series.js:172).
func newSeriesView(s seriesRecord, prefixes []string) (seriesView, error) {
	added, err := dbtime.OptionalMillis(s.createdAt)
	if err != nil || added == nil {
		return seriesView{}, errors.New("series createdAt")
	}
	updated, err := dbtime.OptionalMillis(s.updatedAt)
	if err != nil || updated == nil {
		return seriesView{}, errors.New("series updatedAt")
	}
	var prefixFree *string
	if s.name.Valid {
		value := textsort.PrefixAtEnd(s.name.String, prefixes)
		prefixFree = &value
	}
	return seriesView{ID: s.id, Name: store.NullStringPtr(s.name), NameIgnorePrefix: prefixFree, Description: store.NullStringPtr(s.description), AddedAt: *added, UpdatedAt: *updated, LibraryID: s.libraryID}, nil
}

// seriesListEntry is one result of GET /api/libraries/:id/series: the series
// with its visible books, the total duration when sorted by it, and its feed.
type seriesListEntry struct {
	seriesView
	TotalDuration float64            `json:"totalDuration,omitempty"`
	Books         []libitem.Minified `json:"books"`
	RSSFeed       *feeds.Minified    `json:"rssFeed,omitempty"`
}
