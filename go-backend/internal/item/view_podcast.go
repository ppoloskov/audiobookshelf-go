package item

import (
	"strconv"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/text"
)

// PodcastMetadataFull is the metadata of the full podcast view.
type PodcastMetadataFull struct {
	Title          *string  `json:"title"`
	Author         *string  `json:"author"`
	Description    *string  `json:"description"`
	ReleaseDate    *string  `json:"releaseDate"`
	Genres         []string `json:"genres"`
	FeedURL        *string  `json:"feedUrl"`
	ImageURL       *string  `json:"imageUrl"`
	ITunesPageURL  *string  `json:"itunesPageUrl"`
	ITunesID       *string  `json:"itunesId"`
	ITunesArtistID *string  `json:"itunesArtistId"`
	Language       *string  `json:"language"`
	Explicit       bool     `json:"explicit"`
	Type           *string  `json:"type"`
}

// PodcastMetadataMinified is the metadata of the minified and expanded views.
type PodcastMetadataMinified struct {
	PodcastMetadataFull
	TitleIgnorePrefix *string `json:"titleIgnorePrefix"`
}

// PodcastSettings are the fields every podcast view shares.
type PodcastSettings struct {
	ID                       string   `json:"id"`
	CoverPath                *string  `json:"coverPath"`
	Tags                     []string `json:"tags"`
	AutoDownloadEpisodes     bool     `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string  `json:"autoDownloadSchedule"`
	LastEpisodeCheck         *int64   `json:"lastEpisodeCheck"` // null when NULL or 0
	MaxEpisodesToKeep        int64    `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload int64    `json:"maxNewEpisodesToDownload"`
}

// PodcastFull is Podcast.toOldJSON.
type PodcastFull struct {
	PodcastSettings
	Metadata      PodcastMetadataFull `json:"metadata"`
	LibraryItemID string              `json:"libraryItemId"`
	Episodes      []EpisodeFull       `json:"episodes"`
}

// PodcastMinified is the minified podcast media. NumEpisodes and Size are 0
// unless a list row fills them (Item.ListRow).
type PodcastMinified struct {
	PodcastSettings
	Metadata    PodcastMetadataMinified `json:"metadata"`
	NumEpisodes int64                   `json:"numEpisodes"`
	Size        int64                   `json:"size"`
}

// PodcastExpanded is Podcast.toOldJSONExpanded: NumEpisodes and Size come from
// the loaded episodes.
type PodcastExpanded struct {
	PodcastSettings
	Metadata      PodcastMetadataMinified `json:"metadata"`
	NumEpisodes   int64                   `json:"numEpisodes"`
	Size          int64                   `json:"size"`
	LibraryItemID string                  `json:"libraryItemId"`
	Episodes      []EpisodeExpanded       `json:"episodes"`
}

// Enclosure is the feed enclosure of an episode; Length is the decimal size.
type Enclosure struct {
	URL    string  `json:"url"`
	Type   *string `json:"type"`
	Length *string `json:"length"`
}

// EpisodeFull is PodcastEpisode.toOldJSON.
type EpisodeFull struct {
	LibraryItemID string       `json:"libraryItemId"`
	PodcastID     string       `json:"podcastId"`
	ID            string       `json:"id"`
	OldEpisodeID  *string      `json:"oldEpisodeId"`
	Index         *int64       `json:"index"`
	Season        *string      `json:"season"`
	Episode       *string      `json:"episode"`
	EpisodeType   *string      `json:"episodeType"`
	Title         *string      `json:"title"`
	Subtitle      *string      `json:"subtitle"`
	Description   *string      `json:"description"`
	Enclosure     *Enclosure   `json:"enclosure"`
	GUID          *string      `json:"guid"`
	PubDate       *string      `json:"pubDate"`
	Chapters      []db.Chapter `json:"chapters"`
	AudioFile     db.AudioFile `json:"audioFile"`
	PublishedAt   *int64       `json:"publishedAt"` // null when NULL or 0
	AddedAt       *int64       `json:"addedAt"`
	UpdatedAt     *int64       `json:"updatedAt"`
}

// EpisodeExpanded adds the playable track, size and duration of the audio file.
type EpisodeExpanded struct {
	EpisodeFull
	AudioTrack AudioTrack `json:"audioTrack"`
	Size       int64      `json:"size"`
	Duration   *float64   `json:"duration"`
}

// MediaID implements Media.
func (p PodcastFull) MediaID() string { return p.ID }

// Flags implements Media.
func (p PodcastFull) Flags() (bool, []string) { return p.Metadata.Explicit, p.Tags }

// MediaID implements Media.
func (p PodcastMinified) MediaID() string { return p.ID }

// Flags implements Media.
func (p PodcastMinified) Flags() (bool, []string) { return p.Metadata.Explicit, p.Tags }

// MediaID implements Media.
func (p PodcastExpanded) MediaID() string { return p.ID }

// Flags implements Media.
func (p PodcastExpanded) Flags() (bool, []string) { return p.Metadata.Explicit, p.Tags }

func (p *Podcast) settings() PodcastSettings {
	return PodcastSettings{ID: p.ID, CoverPath: p.CoverPath, Tags: p.Tags.V, AutoDownloadEpisodes: bool(p.AutoDownloadEpisodes),
		AutoDownloadSchedule: p.AutoDownloadSchedule, LastEpisodeCheck: millisNonZero(p.LastEpisodeCheck),
		MaxEpisodesToKeep: db.Val(p.MaxEpisodesToKeep), MaxNewEpisodesToDownload: db.Val(p.MaxNewEpisodesToDownload)}
}

func (p *Podcast) metadata() PodcastMetadataFull {
	return PodcastMetadataFull{Title: p.Title, Author: p.Author, Description: p.Description, ReleaseDate: p.ReleaseDate, Genres: p.Genres.V,
		FeedURL: p.FeedURL, ImageURL: p.ImageURL, ITunesPageURL: p.ItunesPageURL, ITunesID: p.ItunesID, ITunesArtistID: p.ItunesArtistID,
		Language: p.Language, Explicit: bool(p.Explicit), Type: p.PodcastType}
}

func (p *Podcast) metadataMinified(prefixes []string) PodcastMetadataMinified {
	// Unlike a book, a podcast without a title still gets a prefix value ("").
	return PodcastMetadataMinified{PodcastMetadataFull: p.metadata(), TitleIgnorePrefix: db.Ptr(text.PrefixAtEnd(db.Val(p.Title), prefixes))}
}

// Full is the full media view (Podcast.toOldJSON) with the loaded episodes.
func (p *Podcast) Full(itemID string) PodcastFull {
	episodes := make([]EpisodeFull, len(p.Episodes))
	for i, episode := range p.Episodes {
		episodes[i] = episode.Full(itemID)
	}
	return PodcastFull{PodcastSettings: p.settings(), Metadata: p.metadata(), LibraryItemID: itemID, Episodes: episodes}
}

// Minified is the minified media view; numEpisodes and size are 0, as upstream
// loads the podcast without episodes.
func (p *Podcast) Minified(prefixes []string) PodcastMinified {
	return PodcastMinified{PodcastSettings: p.settings(), Metadata: p.metadataMinified(prefixes)}
}

// Expanded is the expanded media view (Podcast.toOldJSONExpanded); the
// episodes must have been loaded for the count, size and list to be real.
func (p *Podcast) Expanded(itemID string, prefixes []string) PodcastExpanded {
	view := PodcastExpanded{PodcastSettings: p.settings(), Metadata: p.metadataMinified(prefixes), LibraryItemID: itemID,
		NumEpisodes: int64(len(p.Episodes)), Episodes: make([]EpisodeExpanded, len(p.Episodes))}
	for i, episode := range p.Episodes {
		view.Episodes[i] = episode.Expanded(itemID)
		view.Size += view.Episodes[i].Size
	}
	return view
}

// Full is the episode view (PodcastEpisode.toOldJSON) of the podcast of item
// itemID.
func (e Episode) Full(itemID string) EpisodeFull {
	view := EpisodeFull{LibraryItemID: itemID, PodcastID: db.Val(e.PodcastID), ID: e.ID, Index: e.Index, Season: e.Season, Episode: e.Episode,
		EpisodeType: e.EpisodeType, Title: e.Title, Subtitle: e.Subtitle, Description: e.Description, PubDate: e.PubDate,
		Chapters: e.Chapters.V, AudioFile: e.AudioFile.V, PublishedAt: millisNonZero(e.PublishedAt),
		AddedAt: millis(e.CreatedAt), UpdatedAt: millis(e.UpdatedAt)}
	if extra := e.ExtraData.V; extra != nil {
		view.OldEpisodeID, view.GUID = nilIfEmpty(db.Val(extra.OldEpisodeID)), nilIfEmpty(db.Val(extra.GUID))
	}
	if url := db.Val(e.EnclosureURL); url != "" {
		view.Enclosure = &Enclosure{URL: url, Type: e.EnclosureType}
		if e.EnclosureSize != nil {
			view.Enclosure.Length = db.Ptr(strconv.FormatInt(*e.EnclosureSize, 10))
		}
	}
	return view
}

// Expanded is the episode with its playable audio track.
func (e Episode) Expanded(itemID string) EpisodeExpanded {
	file := e.AudioFile.V
	return EpisodeExpanded{EpisodeFull: e.Full(itemID), AudioTrack: newTrack(itemID, file, 1, 0), Size: file.Metadata.Size, Duration: file.Duration}
}
