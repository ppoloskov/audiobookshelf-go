package wire

// EpisodeDownload is a podcast episode download as clients see it
// (server/objects/PodcastEpisodeDownload.js:30 toJSONForClient). Key order
// follows upstream; a nullable scalar marshals as null.
type EpisodeDownload struct {
	ID                  string  `json:"id"`
	EpisodeDisplayTitle *string `json:"episodeDisplayTitle"`
	URL                 string  `json:"url"`
	LibraryItemID       string  `json:"libraryItemId"`
	LibraryID           *string `json:"libraryId"`
	IsFinished          bool    `json:"isFinished"`
	Failed              bool    `json:"failed"`
	AppendRandomID      bool    `json:"appendRandomId"`
	StartedAt           *int64  `json:"startedAt"`
	CreatedAt           int64   `json:"createdAt"`
	FinishedAt          *int64  `json:"finishedAt"`
	PodcastTitle        *string `json:"podcastTitle"`
	PodcastExplicit     bool    `json:"podcastExplicit"`
	Season              any     `json:"season"`
	Episode             any     `json:"episode"`
	EpisodeType         any     `json:"episodeType"`
	PublishedAt         any     `json:"publishedAt"`
	GUID                any     `json:"guid"`
}
