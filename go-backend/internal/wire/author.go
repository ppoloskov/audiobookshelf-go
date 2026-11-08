package wire

// AuthorMatch is the Audnexus author lookup result
// (server/providers/Audnexus.js AuthorSearchObj): asin, description, image
// (null when absent) and name.
type AuthorMatch struct {
	ASIN        string  `json:"asin"`
	Description string  `json:"description"`
	Image       *string `json:"image"`
	Name        string  `json:"name"`
}
