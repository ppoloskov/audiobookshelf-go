package libitem

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/textclean"
)

// JSONNull sets a decorator to an explicit JSON null (the zero value omits it).
var JSONNull = json.RawMessage("null")

// Head is the library-item envelope every shape shares.
type Head struct {
	ID               string  `json:"id"`
	Ino              *string `json:"ino"`
	OldLibraryItemID *string `json:"oldLibraryItemId"`
	LibraryID        string  `json:"libraryId"`
	FolderID         *string `json:"folderId"`
	Path             string  `json:"path"`
	RelPath          string  `json:"relPath"`
	IsFile           bool    `json:"isFile"`
	MTimeMS          *int64  `json:"mtimeMs,omitempty"`
	CTimeMS          *int64  `json:"ctimeMs,omitempty"`
	BirthtimeMS      *int64  `json:"birthtimeMs,omitempty"`
	AddedAt          *int64  `json:"addedAt"`
	UpdatedAt        *int64  `json:"updatedAt"`
	IsMissing        bool    `json:"isMissing"`
	IsInvalid        bool    `json:"isInvalid"`
	MediaType        string  `json:"mediaType"`
}

// Extras are the keys callers add per request. Zero values are omitted; set
// JSONNull where the client expects an explicit null (userMediaProgress,
// rssFeed and mediaItemShare on an expanded item).
type Extras struct {
	UserMediaProgress      any   `json:"userMediaProgress,omitempty"`
	RSSFeed                any   `json:"rssFeed,omitempty"`
	MediaItemShare         any   `json:"mediaItemShare,omitempty"`
	EpisodeDownloadsQueued any   `json:"episodeDownloadsQueued,omitempty"`
	EpisodesDownloading    any   `json:"episodesDownloading,omitempty"`
	RecentEpisode          any   `json:"recentEpisode,omitempty"`
	CollapsedSeries        any   `json:"collapsedSeries,omitempty"`
	NumEpisodesIncomplete  int64 `json:"numEpisodesIncomplete,omitempty"`
	ProgressLastUpdate     int64 `json:"progressLastUpdate,omitempty"`
}

// Full is GET /api/items/:id without expanded (LibraryItem.toOldJSON).
type Full struct {
	Head
	LastScan     *int64           `json:"lastScan,omitempty"`
	ScanVersion  *string          `json:"scanVersion"`
	LibraryFiles []map[string]any `json:"libraryFiles"`
	Media        any              `json:"media"` // BookFull or PodcastMedia
	Extras
}

// Expanded is toOldJSONExpanded: item detail, socket events, playlists.
type Expanded struct {
	Head
	LastScan     *int64           `json:"lastScan"`
	ScanVersion  *string          `json:"scanVersion"`
	LibraryFiles []map[string]any `json:"libraryFiles"`
	NumFiles     int              `json:"numFiles"`
	Size         *int64           `json:"size,omitempty"`
	Media        any              `json:"media"` // BookExpanded or PodcastMedia
	Extras
}

// Minified is toOldJSONMinified: shelves, collections and list rows.
type Minified struct {
	Head
	NumFiles int    `json:"numFiles"`
	Size     *int64 `json:"size,omitempty"`
	Media    any    `json:"media"` // BookMinified or PodcastMedia
	Extras
}

func (it Item) head() Head {
	var old *string
	if it.OldLibraryItemID != "" {
		old = &it.OldLibraryItemID
	}
	return Head{ID: it.ID, Ino: it.Ino, OldLibraryItemID: old, LibraryID: it.LibraryID, FolderID: it.FolderID, Path: it.Path, RelPath: it.RelPath,
		IsFile: it.IsFile, MTimeMS: it.MTimeMS, CTimeMS: it.CTimeMS, BirthtimeMS: it.BirthtimeMS, AddedAt: it.AddedAt, UpdatedAt: it.UpdatedAt,
		IsMissing: it.IsMissing, IsInvalid: it.IsInvalid, MediaType: it.MediaType}
}

// Full serializes the item for GET /api/items/:id.
func (it Item) Full() Full {
	f := Full{Head: it.head(), LastScan: it.LastScan, ScanVersion: it.ScanVersion, LibraryFiles: it.LibraryFiles}
	if it.Book != nil {
		f.Media = it.Book.full(it.ID)
	} else if it.Podcast != nil {
		f.Media = it.Podcast.media(it, nil, true, false)
	}
	return f
}

// Expanded serializes the item with expanded media. A podcast's episodes must
// have been loaded (Options.Episodes) for numEpisodes, size and episodes to
// reflect them.
func (it Item) Expanded(prefixes []string) Expanded {
	e := Expanded{Head: it.head(), LastScan: it.LastScan, ScanVersion: it.ScanVersion, LibraryFiles: it.LibraryFiles, NumFiles: len(it.LibraryFiles), Size: it.Size}
	if it.Book != nil {
		e.Media = it.Book.expanded(it.ID, prefixes)
	} else if it.Podcast != nil {
		e.Media = it.Podcast.media(it, prefixes, false, true)
	}
	return e
}

// Minified serializes the shelf and collection shape.
func (it Item) Minified(prefixes []string) Minified {
	m := Minified{Head: it.head(), NumFiles: len(it.LibraryFiles), Size: it.Size}
	if it.Book != nil {
		m.Media = it.Book.minified(prefixes)
	} else if it.Podcast != nil {
		m.Media = it.Podcast.media(it, prefixes, false, false)
	}
	return m
}

// ListRow is the library items page row: Minified, except that a podcast
// reports numEpisodes from its column and the item's size
// (LibraryItem.getByFilterAndSort, server/models/LibraryItem.js:310-312).
func (it Item) ListRow(prefixes []string) Minified {
	m := it.Minified(prefixes)
	if p, ok := m.Media.(PodcastMedia); ok {
		n := it.Podcast.NumEpisodes
		size := int64(0)
		if it.Size != nil {
			size = *it.Size
		}
		p.NumEpisodes, p.Size = &n, &size
		m.Media = p
	}
	return m
}

// SortingPrefixes reads sortingPrefixes from the server-settings JSON; non-string
// entries are skipped and the default is ["the","a"].
func SortingPrefixes(settings []byte) []string {
	var parsed struct {
		Prefixes []any `json:"sortingPrefixes"`
	}
	if json.Unmarshal(settings, &parsed) != nil || parsed.Prefixes == nil {
		return []string{"the", "a"}
	}
	out := make([]string, 0, len(parsed.Prefixes))
	for _, v := range parsed.Prefixes {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// LoadSortingPrefixes reads the server's sortingPrefixes once for a request.
func LoadSortingPrefixes(ctx context.Context, db *sql.DB) []string {
	var raw []byte
	_ = db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw)
	return SortingPrefixes(raw)
}

// TitleIgnorePrefix moves a leading sorting prefix to the end ("The Hobbit"
// becomes "Hobbit, The"), as getTitlePrefixAtEnd does.
func TitleIgnorePrefix(title string, prefixes []string) string {
	for _, prefix := range prefixes {
		if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) && title[len(prefix)] == ' ' {
			capPrefix := prefix
			if capPrefix != "" {
				capPrefix = strings.ToUpper(capPrefix[:1]) + capPrefix[1:]
			}
			return title[len(prefix)+1:] + ", " + capPrefix
		}
	}
	return title
}

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
	AudioFiles    []map[string]any `json:"audioFiles"`
	Chapters      []map[string]any `json:"chapters"`
	EbookFile     map[string]any   `json:"ebookFile"`
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
	AudioFiles    []map[string]any     `json:"audioFiles"`
	Chapters      []map[string]any     `json:"chapters"`
	EbookFile     map[string]any       `json:"ebookFile"`
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
	v := TitleIgnorePrefix(*b.Title, prefixes)
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

func (b *Book) full(itemID string) BookFull {
	return BookFull{ID: b.ID, LibraryItemID: itemID, Metadata: b.metadataFull(), CoverPath: b.CoverPath, Tags: b.Tags,
		AudioFiles: b.AudioFiles, Chapters: b.Chapters, EbookFile: b.Ebook}
}

func (b *Book) minified(prefixes []string) BookMinified {
	var format json.RawMessage
	if v, ok := b.Ebook["ebookFormat"]; ok {
		format, _ = json.Marshal(v)
	}
	var size int64
	tracks := 0
	for _, f := range b.AudioFiles {
		size += fileSize(f)
		if f["exclude"] != true {
			tracks++
		}
	}
	size += fileSize(b.Ebook)
	return BookMinified{ID: b.ID, CoverPath: b.CoverPath, Tags: b.Tags, NumTracks: tracks, NumAudioFiles: len(b.AudioFiles), NumChapters: len(b.Chapters),
		Duration: b.Duration, Size: size, EbookFormat: format,
		Metadata: BookMetadataMinified{Title: b.Title, TitleIgnorePrefix: b.titlePrefix(prefixes), Subtitle: b.Subtitle, AuthorName: b.authorNames(false),
			AuthorNameLF: b.authorNames(true), NarratorName: strings.Join(b.Narrators, ", "), SeriesName: b.seriesName(), Genres: b.Genres,
			PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher, Description: b.Description, ISBN: b.ISBN, ASIN: b.ASIN,
			Language: b.Language, Explicit: b.Explicit, Abridged: b.Abridged}}
}

func (b *Book) expanded(itemID string, prefixes []string) BookExpanded {
	min, full := b.minified(prefixes), b.full(itemID)
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
		if file["exclude"] == true {
			continue
		}
		track := make(map[string]any, len(file)+3)
		for k, v := range file {
			track[k] = v
		}
		meta, _ := file["metadata"].(map[string]any)
		track["title"] = meta["filename"]
		track["startOffset"] = offset
		track["contentUrl"] = contentURL(itemID, file)
		offset += num(file["duration"])
		tracks = append(tracks, track)
	}
	return tracks
}

func contentURL(itemID string, file map[string]any) string {
	ino, _ := file["ino"].(string)
	return "/api/items/" + itemID + "/file/" + url.PathEscape(ino)
}

func num(v any) float64 { n, _ := v.(float64); return n }

func fileSize(file map[string]any) int64 {
	meta, _ := file["metadata"].(map[string]any)
	return int64(num(meta["size"]))
}

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
	AudioFile     map[string]any   `json:"audioFile"`
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
	meta, _ := e.AudioFile["metadata"].(map[string]any)
	track := make(map[string]any, len(e.AudioFile)+4)
	for k, v := range e.AudioFile {
		track[k] = v
	}
	track["startOffset"], track["title"], track["index"], track["contentUrl"] = 0, meta["filename"], 1, contentURL(itemID, e.AudioFile)
	return EpisodeExpanded{EpisodeJSON: e.JSON(itemID), AudioTrack: track, Size: int64(num(meta["size"])), Duration: e.AudioFile["duration"]}
}

func emptyNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// media builds the podcast media value. full is Podcast.toOldJSON (episodes,
// no counts); otherwise the minified keys are used, with expanded adding the
// loaded episodes, their count and total size (Podcast.toOldJSONExpanded).
// A minified podcast reports numEpisodes and size 0, as upstream loads it
// without episodes.
func (p *Podcast) media(it Item, prefixes []string, full, expanded bool) PodcastMedia {
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
		prefix := TitleIgnorePrefix(title, prefixes)
		m.Metadata.TitleIgnorePrefix = &prefix
		var n, size int64
		m.NumEpisodes, m.Size = &n, &size
	}
	if !full && !expanded {
		return m
	}
	m.LibraryItemID = it.ID
	if full {
		eps := make([]EpisodeJSON, len(p.Episodes))
		for i, e := range p.Episodes {
			eps[i] = e.JSON(it.ID)
		}
		m.Episodes = eps
		return m
	}
	eps := make([]EpisodeExpanded, len(p.Episodes))
	var size int64
	for i, e := range p.Episodes {
		eps[i] = e.Expanded(it.ID)
		size += fileSize(e.AudioFile)
	}
	n := int64(len(eps))
	m.NumEpisodes, m.Size, m.Episodes = &n, &size, eps
	return m
}
