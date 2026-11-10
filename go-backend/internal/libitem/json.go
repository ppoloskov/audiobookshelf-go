package libitem

import (
	"audiobookshelf-compat/internal/textsort"
)

// ---- podcasts ----

type PodcastMetadata struct {
	Title             *string  `json:"title"`
	TitleIgnorePrefix *string  `json:"titleIgnorePrefix,omitempty"` // minified and expanded only
	Author            *string  `json:"author"`
	Description       *string  `json:"description"`
	ReleaseDate       *string  `json:"releaseDate"`
	Genres            []string `json:"genres"`
	FeedURL           *string  `json:"feedUrl"`
	ImageURL          *string  `json:"imageUrl"`
	ITunesPageURL     *string  `json:"itunesPageUrl"`
	ITunesID          *string  `json:"itunesId"`
	ITunesArtistID    *string  `json:"itunesArtistId"`
	Language          *string  `json:"language"`
	Explicit          bool     `json:"explicit"`
	Type              *string  `json:"type"`
}

// PodcastMedia is the media of a podcast item in every shape. NumEpisodes and
// Size are present in the minified and expanded shapes; LibraryItemID and
// Episodes in the full and expanded ones (Episodes holds []EpisodeJSON or
// []EpisodeExpanded).
type PodcastMedia struct {
	ID                       string          `json:"id"`
	Metadata                 PodcastMetadata `json:"metadata"`
	CoverPath                *string         `json:"coverPath"`
	Tags                     []string        `json:"tags"`
	AutoDownloadEpisodes     bool            `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string         `json:"autoDownloadSchedule"`
	LastEpisodeCheck         *int64          `json:"lastEpisodeCheck"`
	MaxEpisodesToKeep        int64           `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload int64           `json:"maxNewEpisodesToDownload"`
	NumEpisodes              *int64          `json:"numEpisodes,omitempty"`
	Size                     *int64          `json:"size,omitempty"`
	LibraryItemID            string          `json:"libraryItemId,omitempty"`
	Episodes                 any             `json:"episodes,omitempty"`
}

// EpisodeJSON is a podcast episode (PodcastEpisode.toOldJSON).
type EpisodeJSON struct {
	LibraryItemID string           `json:"libraryItemId"`
	PodcastID     string           `json:"podcastId"`
	ID            string           `json:"id"`
	OldEpisodeID  *string          `json:"oldEpisodeId"`
	Index         *int64           `json:"index"`
	Season        *string          `json:"season"`
	Episode       *string          `json:"episode"`
	EpisodeType   *string          `json:"episodeType"`
	Title         *string          `json:"title"`
	Subtitle      *string          `json:"subtitle"`
	Description   *string          `json:"description"`
	Enclosure     *Enclosure       `json:"enclosure"`
	GUID          *string          `json:"guid"`
	PubDate       *string          `json:"pubDate"`
	Chapters      []map[string]any `json:"chapters"`
	AudioFile     File             `json:"audioFile"`
	PublishedAt   *int64           `json:"publishedAt"`
	AddedAt       *int64           `json:"addedAt"`
	UpdatedAt     *int64           `json:"updatedAt"`
}

// EpisodeExpanded adds the playable track, size and duration of the audio file.
type EpisodeExpanded struct {
	EpisodeJSON
	AudioTrack map[string]any `json:"audioTrack"`
	Size       int64          `json:"size"`
	Duration   any            `json:"duration"`
}

func (e Episode) JSON(itemID string) EpisodeJSON {
	return EpisodeJSON{LibraryItemID: itemID, PodcastID: e.PodcastID, ID: e.ID, OldEpisodeID: emptyNil(e.OldEpisodeID), Index: e.Index, Season: e.Season,
		Episode: e.Episode, EpisodeType: e.EpisodeType, Title: e.Title, Subtitle: e.Subtitle, Description: e.Description, Enclosure: e.Enclosure,
		GUID: emptyNil(e.GUID), PubDate: e.PubDate, Chapters: e.Chapters, AudioFile: e.AudioFile, PublishedAt: e.PublishedAt, AddedAt: e.AddedAt, UpdatedAt: e.UpdatedAt}
}

func (e Episode) Expanded(itemID string) EpisodeExpanded {
	meta := e.AudioFile.Metadata()
	track := make(map[string]any, len(e.AudioFile)+4)
	for k, v := range e.AudioFile {
		track[k] = v
	}
	track["startOffset"], track["title"], track["index"], track["contentUrl"] = 0, meta["filename"], 1, e.AudioFile.ContentURL(itemID)
	return EpisodeExpanded{EpisodeJSON: e.JSON(itemID), AudioTrack: track, Size: e.AudioFile.Size(), Duration: e.AudioFile["duration"]}
}

func emptyNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Media builds the podcast media value. full is Podcast.toOldJSON (episodes,
// no counts); otherwise the minified keys are used, with expanded adding the
// loaded episodes, their count and total size (Podcast.toOldJSONExpanded).
// A minified podcast reports numEpisodes and size 0, as upstream loads it
// without episodes.
func (p *Podcast) Media(itemID string, prefixes []string, full, expanded bool) PodcastMedia {
	m := PodcastMedia{ID: p.ID, CoverPath: p.CoverPath, Tags: p.Tags, AutoDownloadEpisodes: p.AutoDownloadEpisodes, AutoDownloadSchedule: p.AutoDownloadSchedule,
		LastEpisodeCheck: p.LastEpisodeCheck, MaxEpisodesToKeep: p.MaxEpisodesToKeep, MaxNewEpisodesToDownload: p.MaxNewEpisodesToDownload,
		Metadata: PodcastMetadata{Title: p.Title, Author: p.Author, Description: p.Description, ReleaseDate: p.ReleaseDate, Genres: p.Genres, FeedURL: p.FeedURL,
			ImageURL: p.ImageURL, ITunesPageURL: p.ITunesPageURL, ITunesID: p.ITunesID, ITunesArtistID: p.ITunesArtistID, Language: p.Language,
			Explicit: p.Explicit, Type: p.Type}}
	if !full {
		title := ""
		if p.Title != nil {
			title = *p.Title
		}
		prefix := textsort.PrefixAtEnd(title, prefixes)
		m.Metadata.TitleIgnorePrefix = &prefix
		var n, size int64
		m.NumEpisodes, m.Size = &n, &size
	}
	if !full && !expanded {
		return m
	}
	m.LibraryItemID = itemID
	if full {
		eps := make([]EpisodeJSON, len(p.Episodes))
		for i, e := range p.Episodes {
			eps[i] = e.JSON(itemID)
		}
		m.Episodes = eps
		return m
	}
	eps := make([]EpisodeExpanded, len(p.Episodes))
	var size int64
	for i, e := range p.Episodes {
		eps[i] = e.Expanded(itemID)
		size += e.AudioFile.Size()
	}
	n := int64(len(eps))
	m.NumEpisodes, m.Size, m.Episodes = &n, &size, eps
	return m
}
