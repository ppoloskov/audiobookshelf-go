package wire

// MediaProgress is the legacy media progress object
// (server/models/MediaProgress.js:155 getOldMediaProgress). Key order follows
// upstream. EpisodeID is set only for podcast episodes; every other nullable
// scalar marshals as null.
type MediaProgress struct {
	ID                        string   `json:"id"`
	UserID                    string   `json:"userId"`
	LibraryItemID             *string  `json:"libraryItemId"`
	EpisodeID                 *string  `json:"episodeId"`
	MediaItemID               string   `json:"mediaItemId"`
	MediaItemType             string   `json:"mediaItemType"`
	Duration                  *float64 `json:"duration"`
	Progress                  float64  `json:"progress"`
	CurrentTime               *float64 `json:"currentTime"`
	IsFinished                bool     `json:"isFinished"`
	HideFromContinueListening bool     `json:"hideFromContinueListening"`
	EbookLocation             *string  `json:"ebookLocation"`
	EbookProgress             *float64 `json:"ebookProgress"`
	LastUpdate                *int64   `json:"lastUpdate"`
	StartedAt                 *int64   `json:"startedAt"`
	FinishedAt                *int64   `json:"finishedAt"`
}
