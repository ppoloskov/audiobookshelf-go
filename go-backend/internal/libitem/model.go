// Package libitem owns the podcast media model, its projections and shared
// file/audio-file representations. Common item loading lives in internal/items;
// book media lives in internal/books.
package libitem

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
