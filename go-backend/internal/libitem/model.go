// Package libitem owns the typed book and podcast media models plus their
// stored-row and API projections. Common library-item identity and loading
// live in internal/items.
package libitem

// Author and Series are the ordered associations of a book.
type Author struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SeriesRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// Book is a books row with its ordered authors and series. AudioFiles,
// Chapters and Ebook are scanner-owned JSON passed through to clients; the
// accessors below read the few keys the model needs.
type Book struct {
	ID            string
	Title         *string
	Subtitle      *string
	PublishedYear *string
	PublishedDate *string
	Publisher     *string
	Description   *string
	ISBN          *string
	ASIN          *string
	Language      *string
	Explicit      bool
	Abridged      bool
	CoverPath     *string
	Duration      *float64
	UpdatedAt     *int64
	Narrators     []string
	Genres        []string
	Tags          []string
	AudioFiles    []File
	Chapters      []map[string]any
	Ebook         File // nil when the book has no ebook file

	Authors []Author
	Series  []SeriesRef
}

// Podcast is a podcasts row. Episodes is nil unless the loader requested them;
// NumEpisodes is the stored column.
type Podcast struct {
	ID                       string
	Title                    *string
	Author                   *string
	Description              *string
	ReleaseDate              *string
	FeedURL                  *string
	ImageURL                 *string
	ITunesPageURL            *string
	ITunesID                 *string
	ITunesArtistID           *string
	Language                 *string
	Type                     *string
	Explicit                 bool
	AutoDownloadEpisodes     bool
	AutoDownloadSchedule     *string
	LastEpisodeCheck         *int64 // nil when NULL or 0
	MaxEpisodesToKeep        int64
	MaxNewEpisodesToDownload int64
	CoverPath                *string
	Tags                     []string
	Genres                   []string
	NumEpisodes              int64

	Episodes []Episode
}

// Episode is a podcastEpisodes row.
type Episode struct {
	ID           string
	PodcastID    string
	OldEpisodeID string
	GUID         string
	Index        *int64
	Season       *string
	Episode      *string
	EpisodeType  *string
	Title        *string
	Subtitle     *string
	Description  *string
	PubDate      *string
	Enclosure    *Enclosure
	PublishedAt  *int64 // nil when NULL or 0
	AddedAt      *int64
	UpdatedAt    *int64
	Chapters     []map[string]any
	AudioFile    File
}

// Enclosure is the feed enclosure of an episode; Length is the decimal size.
type Enclosure struct {
	URL    string  `json:"url"`
	Type   *string `json:"type"`
	Length *string `json:"length"`
}
