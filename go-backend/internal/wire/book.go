package wire

import "encoding/json"

// BookAuthor is the legacy author object embedded in book metadata.
type BookAuthor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BookSeries is the legacy series object embedded in book metadata.
type BookSeries struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// BookMetadataFull is the legacy non-expanded metadata shape. Field order
// matches Book.oldMetadataToJSON in pinned upstream.
type BookMetadataFull struct {
	Title         *string      `json:"title"`
	Subtitle      *string      `json:"subtitle"`
	Authors       []BookAuthor `json:"authors"`
	Narrators     []string     `json:"narrators"`
	Series        []BookSeries `json:"series"`
	Genres        []string     `json:"genres"`
	PublishedYear *string      `json:"publishedYear"`
	PublishedDate *string      `json:"publishedDate"`
	Publisher     *string      `json:"publisher"`
	Description   *string      `json:"description"`
	ISBN          *string      `json:"isbn"`
	ASIN          *string      `json:"asin"`
	Language      *string      `json:"language"`
	Explicit      bool         `json:"explicit"`
	Abridged      bool         `json:"abridged"`
}

// BookMetadataMinified is the shelf/collection metadata shape. Field order
// matches Book.oldMetadataToJSONMinified in pinned upstream.
type BookMetadataMinified struct {
	Title             *string  `json:"title"`
	TitleIgnorePrefix *string  `json:"titleIgnorePrefix"`
	Subtitle          *string  `json:"subtitle"`
	AuthorName        string   `json:"authorName"`
	AuthorNameLF      string   `json:"authorNameLF"`
	NarratorName      string   `json:"narratorName"`
	SeriesName        string   `json:"seriesName"`
	Genres            []string `json:"genres"`
	PublishedYear     *string  `json:"publishedYear"`
	PublishedDate     *string  `json:"publishedDate"`
	Publisher         *string  `json:"publisher"`
	Description       *string  `json:"description"`
	ISBN              *string  `json:"isbn"`
	ASIN              *string  `json:"asin"`
	Language          *string  `json:"language"`
	Explicit          bool     `json:"explicit"`
	Abridged          bool     `json:"abridged"`
}

// BookMetadataExpanded is the strict superset used by item detail and socket
// events. Embedded full metadata keeps upstream's field order, followed by the
// minified-only fields and descriptionPlain.
type BookMetadataExpanded struct {
	BookMetadataFull
	TitleIgnorePrefix *string `json:"titleIgnorePrefix"`
	AuthorName        string  `json:"authorName"`
	AuthorNameLF      string  `json:"authorNameLF"`
	NarratorName      string  `json:"narratorName"`
	SeriesName        string  `json:"seriesName"`
	DescriptionPlain  *string `json:"descriptionPlain"`
}

// BookFull is the legacy, non-expanded book media shape. Nested source JSON
// remains opaque while the envelope and metadata are migrated.
type BookFull struct {
	ID            string           `json:"id"`
	LibraryItemID string           `json:"libraryItemId"`
	Metadata      BookMetadataFull `json:"metadata"`
	CoverPath     *string          `json:"coverPath"`
	Tags          []string         `json:"tags"`
	AudioFiles    json.RawMessage  `json:"audioFiles"`
	Chapters      json.RawMessage  `json:"chapters"`
	EbookFile     json.RawMessage  `json:"ebookFile"`
}

// BookMinified is the book media shape returned in shelf and collection items.
type BookMinified struct {
	ID            string               `json:"id"`
	Metadata      BookMetadataMinified `json:"metadata"`
	CoverPath     *string              `json:"coverPath"`
	Tags          []string             `json:"tags"`
	NumTracks     int                  `json:"numTracks"`
	NumAudioFiles int                  `json:"numAudioFiles"`
	NumChapters   int                  `json:"numChapters"`
	Duration      *float64             `json:"duration"`
	Size          int64                `json:"size"`
	EbookFormat   json.RawMessage      `json:"ebookFormat,omitempty"`
}

// BookExpanded is the expanded book media shape. Track data is opaque here;
// its DTO is intentionally deferred by Architecture decision D5.
type BookExpanded struct {
	ID            string               `json:"id"`
	Metadata      BookMetadataExpanded `json:"metadata"`
	CoverPath     *string              `json:"coverPath"`
	Tags          []string             `json:"tags"`
	NumTracks     int                  `json:"numTracks"`
	NumAudioFiles int                  `json:"numAudioFiles"`
	NumChapters   int                  `json:"numChapters"`
	Duration      *float64             `json:"duration"`
	Size          int64                `json:"size"`
	EbookFormat   json.RawMessage      `json:"ebookFormat,omitempty"`
	LibraryItemID string               `json:"libraryItemId"`
	AudioFiles    json.RawMessage      `json:"audioFiles"`
	Chapters      json.RawMessage      `json:"chapters"`
	EbookFile     json.RawMessage      `json:"ebookFile"`
	Tracks        json.RawMessage      `json:"tracks"`
}
