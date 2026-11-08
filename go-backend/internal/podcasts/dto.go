package podcasts

import "audiobookshelf-compat/internal/wire"

// EpisodeDownloads is the body of GET /api/podcasts/:id/downloads.
type EpisodeDownloads struct {
	Downloads []wire.EpisodeDownload `json:"downloads"`
}

// EpisodeDownloadQueue is the body of GET /api/libraries/:id/episode-downloads
// (PodcastManager.getDownloadQueueDetails, server/managers/PodcastManager.js:509).
// CurrentDownload is omitted when no download of the library is running.
type EpisodeDownloadQueue struct {
	CurrentDownload *wire.EpisodeDownload  `json:"currentDownload,omitempty"`
	Queue           []wire.EpisodeDownload `json:"queue"`
}
