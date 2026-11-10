package libitem

import (
	"encoding/json"
	"strings"

	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
)

// ---- books ----

type BookMetadataFull struct {
	Title         *string     `json:"title"`
	Subtitle      *string     `json:"subtitle"`
	Authors       []Author    `json:"authors"`
	Narrators     []string    `json:"narrators"`
	Series        []SeriesRef `json:"series"`
	Genres        []string    `json:"genres"`
	PublishedYear *string     `json:"publishedYear"`
	PublishedDate *string     `json:"publishedDate"`
	Publisher     *string     `json:"publisher"`
	Description   *string     `json:"description"`
	ISBN          *string     `json:"isbn"`
	ASIN          *string     `json:"asin"`
	Language      *string     `json:"language"`
	Explicit      bool        `json:"explicit"`
	Abridged      bool        `json:"abridged"`
}

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
	// Series is added by callers on continue-series shelves, series-filtered
	// pages and author series groups.
	Series any `json:"series,omitempty"`
}

type BookMetadataExpanded struct {
	BookMetadataFull
	TitleIgnorePrefix *string `json:"titleIgnorePrefix"`
	AuthorName        string  `json:"authorName"`
	AuthorNameLF      string  `json:"authorNameLF"`
	NarratorName      string  `json:"narratorName"`
	SeriesName        string  `json:"seriesName"`
	DescriptionPlain  *string `json:"descriptionPlain"`
}

type BookFull struct {
	ID            string           `json:"id"`
	LibraryItemID string           `json:"libraryItemId"`
	Metadata      BookMetadataFull `json:"metadata"`
	CoverPath     *string          `json:"coverPath"`
	Tags          []string         `json:"tags"`
	AudioFiles    []File           `json:"audioFiles"`
	Chapters      []map[string]any `json:"chapters"`
	EbookFile     File             `json:"ebookFile"`
}

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
	AudioFiles    []File               `json:"audioFiles"`
	Chapters      []map[string]any     `json:"chapters"`
	EbookFile     File                 `json:"ebookFile"`
	Tracks        []map[string]any     `json:"tracks"`
}

func (b *Book) metadataFull() BookMetadataFull {
	return BookMetadataFull{Title: b.Title, Subtitle: b.Subtitle, Authors: b.Authors, Narrators: b.Narrators, Series: b.Series, Genres: b.Genres,
		PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher, Description: b.Description, ISBN: b.ISBN, ASIN: b.ASIN,
		Language: b.Language, Explicit: b.Explicit, Abridged: b.Abridged}
}

func (b *Book) titlePrefix(prefixes []string) *string {
	if b.Title == nil {
		return nil
	}
	v := textsort.PrefixAtEnd(*b.Title, prefixes)
	return &v
}

// AuthorName is the authors joined with ", " in book order.
func (b *Book) AuthorName() string { return b.authorNames(false) }

func (b *Book) authorNames(lastFirst bool) string {
	names := make([]string, len(b.Authors))
	for i, a := range b.Authors {
		names[i] = a.Name
		if lastFirst {
			names[i] = nameparse.ToLastFirst(a.Name)
		}
	}
	return strings.Join(names, ", ")
}

func (b *Book) seriesName() string {
	names := make([]string, len(b.Series))
	for i, s := range b.Series {
		names[i] = s.Name
		if s.Sequence != nil && *s.Sequence != "" {
			names[i] += " #" + *s.Sequence
		}
	}
	return strings.Join(names, ", ")
}

// Full is the full book media projection (Book.toOldJSON).
func (b *Book) Full(itemID string) BookFull {
	return BookFull{ID: b.ID, LibraryItemID: itemID, Metadata: b.metadataFull(), CoverPath: b.CoverPath, Tags: b.Tags,
		AudioFiles: b.AudioFiles, Chapters: b.Chapters, EbookFile: b.Ebook}
}

// Minified is the shelf/list book media projection (Book.toOldJSONMinified).
func (b *Book) Minified(prefixes []string) BookMinified {
	var format json.RawMessage
	if v, ok := b.Ebook["ebookFormat"]; ok {
		format, _ = json.Marshal(v)
	}
	var size int64
	tracks := 0
	for _, f := range b.AudioFiles {
		size += f.Size()
		if !f.Excluded() {
			tracks++
		}
	}
	size += b.Ebook.Size()
	return BookMinified{ID: b.ID, CoverPath: b.CoverPath, Tags: b.Tags, NumTracks: tracks, NumAudioFiles: len(b.AudioFiles), NumChapters: len(b.Chapters),
		Duration: b.Duration, Size: size, EbookFormat: format,
		Metadata: BookMetadataMinified{Title: b.Title, TitleIgnorePrefix: b.titlePrefix(prefixes), Subtitle: b.Subtitle, AuthorName: b.authorNames(false),
			AuthorNameLF: b.authorNames(true), NarratorName: strings.Join(b.Narrators, ", "), SeriesName: b.seriesName(), Genres: b.Genres,
			PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher, Description: b.Description, ISBN: b.ISBN, ASIN: b.ASIN,
			Language: b.Language, Explicit: b.Explicit, Abridged: b.Abridged}}
}

// Expanded is the expanded book media projection (Book.toOldJSONExpanded).
func (b *Book) Expanded(itemID string, prefixes []string) BookExpanded {
	min, full := b.Minified(prefixes), b.Full(itemID)
	var plain *string
	if b.Description != nil && *b.Description != "" {
		v := textclean.Strip(*b.Description)
		plain = &v
	}
	return BookExpanded{ID: b.ID, CoverPath: b.CoverPath, Tags: b.Tags, NumTracks: min.NumTracks, NumAudioFiles: min.NumAudioFiles, NumChapters: min.NumChapters,
		Duration: min.Duration, Size: min.Size, EbookFormat: min.EbookFormat, LibraryItemID: itemID, AudioFiles: full.AudioFiles, Chapters: full.Chapters,
		EbookFile: full.EbookFile, Tracks: b.tracks(itemID),
		Metadata: BookMetadataExpanded{BookMetadataFull: full.Metadata, TitleIgnorePrefix: min.Metadata.TitleIgnorePrefix, AuthorName: min.Metadata.AuthorName,
			AuthorNameLF: min.Metadata.AuthorNameLF, NarratorName: min.Metadata.NarratorName, SeriesName: min.Metadata.SeriesName, DescriptionPlain: plain}}
}

// tracks are the playable (non-excluded) audio files with running offsets.
func (b *Book) tracks(itemID string) []map[string]any {
	tracks := make([]map[string]any, 0, len(b.AudioFiles))
	var offset float64
	for _, file := range b.AudioFiles {
		if file.Excluded() {
			continue
		}
		track := make(map[string]any, len(file)+3)
		for k, v := range file {
			track[k] = v
		}
		track["title"] = file.Metadata()["filename"]
		track["startOffset"] = offset
		track["contentUrl"] = file.ContentURL(itemID)
		offset += num(file["duration"])
		tracks = append(tracks, track)
	}
	return tracks
}

func num(v any) float64 { n, _ := v.(float64); return n }

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
