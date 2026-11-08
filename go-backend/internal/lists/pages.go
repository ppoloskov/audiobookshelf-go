package lists

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
