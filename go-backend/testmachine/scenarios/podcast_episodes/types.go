package podcast_episodes

// enclosure is the episode enclosure object of the old JSON shapes.
type enclosure struct {
	URL    string  `json:"url"`
	Type   *string `json:"type"`
	Length *string `json:"length"`
}

// chapter is one stored episode chapter.
type chapter struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// audioFileJSON is the subset of the stored AudioFile JSON the clients read.
type audioFileJSON struct {
	Index       int          `json:"index"`
	Ino         string       `json:"ino"`
	Duration    float64      `json:"duration"`
	Codec       *string      `json:"codec"`
	MimeType    *string      `json:"mimeType"`
	Metadata    fileMetadata `json:"metadata"`
	Chapters    []chapter    `json:"chapters"`
	TrackTitle  *string      `json:"title"`
	StartOffset int          `json:"startOffset"`
	ContentURL  string       `json:"contentUrl"`
}

// fileMetadata is the file metadata object of a library file.
type fileMetadata struct {
	Filename string `json:"filename"`
	Ext      string `json:"ext"`
	Path     string `json:"path"`
	RelPath  string `json:"relPath"`
	Size     int64  `json:"size"`
}

// episodeJSON is one episode of the old JSON shapes (GET episode, item media,
// recent episodes and the episode_added socket).
type episodeJSON struct {
	LibraryItemID string           `json:"libraryItemId"`
	PodcastID     string           `json:"podcastId"`
	ID            string           `json:"id"`
	OldEpisodeID  *string          `json:"oldEpisodeId"`
	Index         *int             `json:"index"`
	Season        *string          `json:"season"`
	Episode       *string          `json:"episode"`
	EpisodeType   *string          `json:"episodeType"`
	Title         *string          `json:"title"`
	Subtitle      *string          `json:"subtitle"`
	Description   *string          `json:"description"`
	Enclosure     *enclosure       `json:"enclosure"`
	GUID          *string          `json:"guid"`
	PubDate       *string          `json:"pubDate"`
	Chapters      []chapter        `json:"chapters"`
	AudioFile     *audioFileJSON   `json:"audioFile"`
	PublishedAt   *int64           `json:"publishedAt"`
	AddedAt       int64            `json:"addedAt"`
	UpdatedAt     int64            `json:"updatedAt"`
	AudioTrack    *audioFileJSON   `json:"audioTrack"`
	Size          int64            `json:"size"`
	Duration      float64          `json:"duration"`
	LibraryID     string           `json:"libraryId"`
	Podcast       *podcastJSON     `json:"podcast"`
	LibraryItem   *libraryItemJSON `json:"libraryItem"`
}

// podcastMetadata is the old podcast metadata object.
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

// podcastJSON is the old podcast object embedded in a library item or a recent
// episode entry.
type podcastJSON struct {
	ID                       string          `json:"id"`
	LibraryItemID            string          `json:"libraryItemId"`
	Metadata                 podcastMetadata `json:"metadata"`
	CoverPath                *string         `json:"coverPath"`
	Tags                     []string        `json:"tags"`
	Episodes                 []episodeJSON   `json:"episodes"`
	NumEpisodes              int             `json:"numEpisodes"`
	AutoDownloadEpisodes     bool            `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string         `json:"autoDownloadSchedule"`
	LastEpisodeCheck         *int64          `json:"lastEpisodeCheck"`
	MaxEpisodesToKeep        int             `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload int             `json:"maxNewEpisodesToDownload"`
	Size                     int64           `json:"size"`
}

// libraryFile is one library file entry of a library item.
type libraryFile struct {
	Ino      string       `json:"ino"`
	FileType string       `json:"fileType"`
	Metadata fileMetadata `json:"metadata"`
}

// libraryItemJSON is the old library item object.
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
	Media        podcastJSON   `json:"media"`
	LibraryFiles []libraryFile `json:"libraryFiles"`
}

// downloadItem is PodcastEpisodeDownload.toJSONForClient().
type downloadItem struct {
	ID                  string  `json:"id"`
	EpisodeDisplayTitle *string `json:"episodeDisplayTitle"`
	URL                 string  `json:"url"`
	LibraryItemID       *string `json:"libraryItemId"`
	LibraryID           *string `json:"libraryId"`
	IsFinished          bool    `json:"isFinished"`
	Failed              bool    `json:"failed"`
	AppendRandomID      bool    `json:"appendRandomId"`
	StartedAt           *int64  `json:"startedAt"`
	CreatedAt           *int64  `json:"createdAt"`
	FinishedAt          *int64  `json:"finishedAt"`
	PodcastTitle        *string `json:"podcastTitle"`
	PodcastExplicit     bool    `json:"podcastExplicit"`
	Season              *string `json:"season"`
	Episode             *string `json:"episode"`
	EpisodeType         *string `json:"episodeType"`
	PublishedAt         *int64  `json:"publishedAt"`
	GUID                *string `json:"guid"`
}

// queueDetails is the body of GET /api/libraries/:id/episode-downloads.
type queueDetails struct {
	CurrentDownload *downloadItem  `json:"currentDownload"`
	Queue           []downloadItem `json:"queue"`
}

// downloadsPayload is the body of GET /api/podcasts/:id/downloads.
type downloadsPayload struct {
	Downloads []downloadItem `json:"downloads"`
}

// recentPayload is the body of GET /api/libraries/:id/recent-episodes.
type recentPayload struct {
	Episodes []episodeJSON `json:"episodes"`
	Limit    int           `json:"limit"`
	Page     int           `json:"page"`
}

// checkNewPayload is the body of GET /api/podcasts/:id/checknew.
type checkNewPayload struct {
	Episodes []rssEpisode `json:"episodes"`
}

// rssEpisode is one episode object of a feed and of the download request body.
type rssEpisode struct {
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle"`
	Description string    `json:"description"`
	PubDate     string    `json:"pubDate"`
	EpisodeType string    `json:"episodeType"`
	Season      string    `json:"season"`
	Episode     string    `json:"episode"`
	Author      string    `json:"author"`
	PublishedAt int64     `json:"publishedAt"`
	Enclosure   enclosure `json:"enclosure"`
	GUID        *string   `json:"guid"`
}
