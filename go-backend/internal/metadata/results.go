package metadata

import (
	"encoding/json"

	"audiobookshelf-compat/internal/textclean"
)

// Series is one series a book belongs to.
type Series struct {
	Series   string `json:"series"`
	Sequence string `json:"sequence,omitempty"`
}

// BookResult is one book found by any provider. A provider fills what it
// knows; an empty field is left out of the JSON. The web client's match tab
// reads these keys.
type BookResult struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
	// Publisher and the fields below follow the order of the match tab.
	Publisher        string   `json:"publisher,omitempty"`
	PublishedYear    string   `json:"publishedYear,omitempty"`
	Description      string   `json:"description,omitempty"`
	DescriptionPlain string   `json:"descriptionPlain,omitempty"`
	Cover            string   `json:"cover,omitempty"`
	ISBN             string   `json:"isbn,omitempty"`
	ASIN             string   `json:"asin,omitempty"`
	Genres           []string `json:"genres,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Series           []Series `json:"series,omitempty"`
	Language         string   `json:"language,omitempty"`
	Duration         float64  `json:"duration,omitzero"` // minutes
	// ID and ArtistID are the provider's own identifiers: a string or a number
	// (the match tab reads the iTunes id as sent).
	ID               json.RawMessage `json:"id,omitempty"`
	ArtistID         json.RawMessage `json:"artistId,omitempty"`
	Key              string          `json:"key,omitempty"`
	Edition          string          `json:"edition,omitempty"`
	Covers           []string        `json:"covers,omitempty"`
	FirstPublishDate string          `json:"first_publish_date,omitempty"`
	Region           string          `json:"region,omitempty"`
	Rating           string          `json:"rating,omitempty"`
	Abridged         *bool           `json:"abridged,omitempty"`
	Explicit         *bool           `json:"explicit,omitempty"`
}

// sanitize reduces Description to the allowed markup and fills
// DescriptionPlain; the web client renders the former with v-html.
func (b *BookResult) sanitize() {
	if b.Description != "" {
		clean := textclean.Sanitize(b.Description)
		b.Description, b.DescriptionPlain = clean, textclean.Strip(clean)
	}
}

// PodcastResult is one iTunes podcast, as the new-podcast form and the quick
// match read it.
type PodcastResult struct {
	ID               json.RawMessage `json:"id"`
	ArtistID         json.RawMessage `json:"artistId"`
	Title            *string         `json:"title"`
	ArtistName       *string         `json:"artistName"`
	Description      string          `json:"description"`
	DescriptionPlain string          `json:"descriptionPlain"`
	ReleaseDate      *string         `json:"releaseDate"`
	Genres           []string        `json:"genres"`
	Cover            *string         `json:"cover"`
	TrackCount       json.RawMessage `json:"trackCount"`
	FeedURL          *string         `json:"feedUrl"`
	PageURL          *string         `json:"pageUrl"`
	Explicit         bool            `json:"explicit"`
}

// AuthorResult is an Audnexus author record.
type AuthorResult struct {
	ASIN        string  `json:"asin"`
	Description string  `json:"description"`
	Image       *string `json:"image"`
	Name        string  `json:"name"`
}

// BookQuery is a book search. Provider is a provider value ("google",
// "audible.uk", "custom-<id>"); Duration (seconds) orders Audible results by
// closeness to a library item; MaxFuzzy bounds the cleaned-title retries of
// SearchBooks.
type BookQuery struct {
	Provider string
	Title    string
	Author   string
	ISBN     string
	ASIN     string
	Region   string
	Duration float64
	MaxFuzzy int
}
