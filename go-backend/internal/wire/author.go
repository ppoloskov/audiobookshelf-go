package wire

import "encoding/json"

// Author is Author.toOldJSON (server/models/Author.js:177-188).
type Author struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// AuthorExpanded is Author.toOldJSONExpanded(numBooks) (Author.js:195-199), the
// payload of author_updated.
type AuthorExpanded struct {
	Author
	NumBooks int `json:"numBooks"`
}

// AuthorSeriesGroup is one entry of GET /api/authors/:id?include=items,series
// "series": the author's items grouped by series
// (server/controllers/AuthorController.js:63-72). Items are minified library
// items whose media.metadata.series is the series they were grouped under.
type AuthorSeriesGroup struct {
	ID    string            `json:"id"`
	Name  *string           `json:"name"`
	Items []json.RawMessage `json:"items"`
}

// AuthorDetail is the body of GET /api/authors/:id (AuthorController.js:40-88).
// Series is present only for include=items,series and LibraryItems only for
// include=items, assigned in that order.
type AuthorDetail struct {
	Author
	Series       *[]AuthorSeriesGroup `json:"series,omitempty"`
	LibraryItems *[]json.RawMessage   `json:"libraryItems,omitempty"`
}

// AuthorUpdateResult is the body of PATCH /api/authors/:id
// (AuthorController.js:170-173 for a merge, 220-223 otherwise) and of
// POST /api/authors/:id/match (AuthorController.js:379-382). A merge answers
// {author, merged}, a regular update {author, updated}; the match answer is
// {updated, author}.
type AuthorUpdateResult struct {
	Author  Author `json:"author"`
	Updated *bool  `json:"updated,omitempty"`
	Merged  *bool  `json:"merged,omitempty"`
}

// AuthorMatchResult is the body of POST /api/authors/:id/match, which lists
// updated before author.
type AuthorMatchResult struct {
	Updated bool   `json:"updated"`
	Author  Author `json:"author"`
}

// AuthorImageResult is the body of POST and DELETE /api/authors/:id/image.
type AuthorImageResult struct {
	Author Author `json:"author"`
}

// AuthorMatch is the Audnexus author lookup result
// (server/providers/Audnexus.js AuthorSearchObj): asin, description, image
// (null when absent) and name.
type AuthorMatch struct {
	ASIN        string  `json:"asin"`
	Description string  `json:"description"`
	Image       *string `json:"image"`
	Name        string  `json:"name"`
}
