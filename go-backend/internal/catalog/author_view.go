package catalog

import (
	"encoding/json"
)

// authorView is Author.toOldJSON (server/models/Author.js:177-188).
type authorView struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// authorWithBooks is Author.toOldJSONExpanded(numBooks) (Author.js:195-199), the
// payload of author_updated.
type authorWithBooks struct {
	authorView
	NumBooks int `json:"numBooks"`
}

type authorListEntry struct {
	authorWithBooks
	LastFirst *string `json:"lastFirst"`
}

// authorSeriesGroup is one entry of GET /api/authors/:id?include=items,series
// "series": the author's items grouped by series
// (server/controllers/AuthorController.js:63-72). Items are minified library
// items whose media.metadata.series is the series they were grouped under.
type authorSeriesGroup struct {
	ID    string            `json:"id"`
	Name  *string           `json:"name"`
	Items []json.RawMessage `json:"items"`
}

// authorDetail is the body of GET /api/authors/:id (AuthorController.js:40-88).
// seriesView is present only for include=items,series and LibraryItems only for
// include=items, assigned in that order.
type authorDetail struct {
	authorView
	Series       *[]authorSeriesGroup `json:"series,omitempty"`
	LibraryItems *[]json.RawMessage   `json:"libraryItems,omitempty"`
}

// authorUpdateResult is the body of PATCH /api/authors/:id
// (AuthorController.js:170-173 for a merge, 220-223 otherwise) and of
// POST /api/authors/:id/match (AuthorController.js:379-382). A merge answers
// {author, merged}, a regular update {author, updated}; the match answer is
// {updated, author}.
type authorUpdateResult struct {
	Author  authorView `json:"author"`
	Updated *bool      `json:"updated,omitempty"`
	Merged  *bool      `json:"merged,omitempty"`
}

// authorMatchResult is the body of POST /api/authors/:id/match, which lists
// updated before author.
type authorMatchResult struct {
	Updated bool       `json:"updated"`
	Author  authorView `json:"author"`
}

type authorImageResult struct {
	Author authorView `json:"author"`
}

// bookSeries is the legacy series object embedded in book metadata.
type bookSeries struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// bookSeriesRef is the metadata.series object of an item in an author's series
// group: the series with its prefix-free name and the book's sequence.
type bookSeriesRef struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Sequence         *string `json:"sequence"`
}
