package podcast_feed

import (
	"encoding/json"
	"encoding/xml"
)

// rssChapter is a parsed podlove chapter.
type rssChapter struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// rssMetadata is the metadata object server/utils/podcastUtils.js returns.
type rssMetadata struct {
	Image            *string  `json:"image"`
	Categories       []string `json:"categories"`
	FeedURL          *string  `json:"feedUrl"`
	Description      *string  `json:"description"`
	DescriptionPlain *string  `json:"descriptionPlain"`
	Type             *string  `json:"type"`
	Title            *string  `json:"title"`
	Language         *string  `json:"language"`
	Explicit         *string  `json:"explicit"`
	Author           *string  `json:"author"`
	PubDate          *string  `json:"pubDate"`
	Link             *string  `json:"link"`
}

// rssEnclosure is an episode enclosure.
type rssEnclosure struct {
	URL    string  `json:"url"`
	Type   *string `json:"type"`
	Length *string `json:"length"`
}

// rssEpisode is one parsed feed episode.
type rssEpisode struct {
	Title            string       `json:"title"`
	Subtitle         string       `json:"subtitle"`
	Description      string       `json:"description"`
	DescriptionPlain string       `json:"descriptionPlain"`
	PubDate          string       `json:"pubDate"`
	EpisodeType      string       `json:"episodeType"`
	Season           string       `json:"season"`
	Episode          string       `json:"episode"`
	Author           string       `json:"author"`
	Duration         string       `json:"duration"`
	DurationSeconds  *int         `json:"durationSeconds"`
	Explicit         string       `json:"explicit"`
	PublishedAt      *int64       `json:"publishedAt"`
	Enclosure        rssEnclosure `json:"enclosure"`
	GUID             *string      `json:"guid"`
	ChaptersURL      *string      `json:"chaptersUrl"`
	ChaptersType     *string      `json:"chaptersType"`
	Chapters         []rssChapter `json:"chapters"`
}

// rssPodcast is the podcast object of POST /api/podcasts/feed.
type rssPodcast struct {
	Metadata rssMetadata  `json:"metadata"`
	Episodes []rssEpisode `json:"episodes"`
}

// podcastPayload is the body of POST /api/podcasts/feed.
type podcastPayload struct {
	Podcast rssPodcast `json:"podcast"`
}

// podcastMetadata is the podcast metadata object of the library item JSON.
type podcastMetadata struct {
	Title             *string  `json:"title"`
	Author            *string  `json:"author"`
	Description       *string  `json:"description"`
	ReleaseDate       *string  `json:"releaseDate"`
	Genres            []string `json:"genres"`
	FeedURL           *string  `json:"feedUrl"`
	ImageURL          *string  `json:"imageUrl"`
	ItunesPageURL     *string  `json:"itunesPageUrl"`
	ItunesID          *string  `json:"itunesId"`
	ItunesArtistID    *string  `json:"itunesArtistId"`
	Explicit          bool     `json:"explicit"`
	Language          *string  `json:"language"`
	Type              *string  `json:"type"`
	TitleIgnorePrefix *string  `json:"titleIgnorePrefix"`
}

// episodeJSON is one episode of the library item JSON.
type episodeJSON struct {
	ID            string          `json:"id"`
	LibraryItemID string          `json:"libraryItemId"`
	PodcastID     string          `json:"podcastId"`
	Index         *int            `json:"index"`
	Season        *string         `json:"season"`
	Episode       *string         `json:"episode"`
	EpisodeType   *string         `json:"episodeType"`
	Title         *string         `json:"title"`
	Subtitle      *string         `json:"subtitle"`
	Description   *string         `json:"description"`
	Enclosure     *rssEnclosure   `json:"enclosure"`
	GUID          *string         `json:"guid"`
	PubDate       *string         `json:"pubDate"`
	Chapters      []rssChapter    `json:"chapters"`
	PublishedAt   *int64          `json:"publishedAt"`
	AddedAt       int64           `json:"addedAt"`
	UpdatedAt     int64           `json:"updatedAt"`
	AudioFile     json.RawMessage `json:"audioFile"`
}

// podcastMedia is the podcast media object of the library item JSON.
type podcastMedia struct {
	ID                       string          `json:"id"`
	LibraryItemID            string          `json:"libraryItemId"`
	Metadata                 podcastMetadata `json:"metadata"`
	CoverPath                *string         `json:"coverPath"`
	Tags                     []string        `json:"tags"`
	NumEpisodes              int             `json:"numEpisodes"`
	AutoDownloadEpisodes     bool            `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string         `json:"autoDownloadSchedule"`
	LastEpisodeCheck         *int64          `json:"lastEpisodeCheck"`
	MaxEpisodesToKeep        int             `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload int             `json:"maxNewEpisodesToDownload"`
	Size                     int64           `json:"size"`
	Episodes                 []episodeJSON   `json:"episodes"`
}

// libraryFile is one library file entry of the library item JSON.
type libraryFile struct {
	Ino      string `json:"ino"`
	FileType string `json:"fileType"`
	Metadata struct {
		Filename string `json:"filename"`
		Ext      string `json:"ext"`
		Path     string `json:"path"`
		RelPath  string `json:"relPath"`
		Size     int64  `json:"size"`
	} `json:"metadata"`
}

// libraryItemJSON is the library item object of the create and item routes.
type libraryItemJSON struct {
	ID           string        `json:"id"`
	LibraryID    string        `json:"libraryId"`
	FolderID     string        `json:"folderId"`
	Path         string        `json:"path"`
	RelPath      string        `json:"relPath"`
	IsFile       bool          `json:"isFile"`
	IsMissing    bool          `json:"isMissing"`
	IsInvalid    bool          `json:"isInvalid"`
	MediaType    string        `json:"mediaType"`
	AddedAt      int64         `json:"addedAt"`
	UpdatedAt    int64         `json:"updatedAt"`
	Media        podcastMedia  `json:"media"`
	LibraryFiles []libraryFile `json:"libraryFiles"`
}

// opmlDocument is the parsed OPML response.
type opmlDocument struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    struct {
		Title string `xml:"title"`
	} `xml:"head"`
	Body struct {
		Outlines []opmlOutline `xml:"outline"`
	} `xml:"body"`
}

// opmlOutline is one OPML subscription outline.
type opmlOutline struct {
	Type        string `xml:"type,attr"`
	Text        string `xml:"text,attr"`
	Title       string `xml:"title,attr"`
	XMLURL      string `xml:"xmlUrl,attr"`
	Description string `xml:"description,attr"`
	HTMLURL     string `xml:"htmlUrl,attr"`
	Language    string `xml:"language,attr"`
}

// titlesResponse is the body of GET /api/libraries/:id/podcast-titles.
type titlesResponse struct {
	Podcasts []struct {
		Title         string  `json:"title"`
		ItunesID      *string `json:"itunesId"`
		LibraryItemID string  `json:"libraryItemId"`
		LibraryID     string  `json:"libraryId"`
	} `json:"podcasts"`
}
