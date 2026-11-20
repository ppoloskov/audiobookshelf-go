package db

import "encoding/json"

// This file holds one struct per JSON-column document, with the JSON keys
// upstream v2.37.2 writes. A decode then encode round trip keeps every key,
// including keys whose value is null.
//
// Conventions, so a row that Go rewrites is unchanged by value:
//   - a key upstream always writes has no omitempty; when its value can be null
//     the field is a pointer, which writes null when nil;
//   - a key upstream writes only sometimes (older rows, other media type) is a
//     pointer with omitempty or a slice with omitzero, and is absent when nil
//     (omitempty would also drop an empty slice, omitzero keeps it);
//   - sizes, counts and Date.now() timestamps are int64; file-system times in
//     milliseconds, durations and chapter times are float64, because they are
//     fractional in practice.

// FileMetadata is the metadata object of a library file, audio file or ebook
// file (server/objects/metadata/FileMetadata.js).
type FileMetadata struct {
	Filename    string  `json:"filename"`
	Ext         string  `json:"ext"`
	Path        string  `json:"path"`
	RelPath     string  `json:"relPath"`
	Size        int64   `json:"size"`
	MtimeMs     float64 `json:"mtimeMs"`
	CtimeMs     float64 `json:"ctimeMs"`
	BirthtimeMs float64 `json:"birthtimeMs"`
}

// Chapter is one chapter of an audio file, book or podcast episode (ffprobe
// chapters sorted by start, id is the index; server/utils/prober.js).
type Chapter struct {
	ID    int64   `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// AudioMetaTags is the tags scraped from an audio file
// (server/objects/metadata/AudioMetaTags.js). Upstream writes only the tags that
// are set, so every key is omitted when empty.
type AudioMetaTags struct {
	Album                  string `json:"tagAlbum,omitempty"`
	AlbumSort              string `json:"tagAlbumSort,omitempty"`
	Artist                 string `json:"tagArtist,omitempty"`
	ArtistSort             string `json:"tagArtistSort,omitempty"`
	Genre                  string `json:"tagGenre,omitempty"`
	Title                  string `json:"tagTitle,omitempty"`
	TitleSort              string `json:"tagTitleSort,omitempty"`
	Series                 string `json:"tagSeries,omitempty"`
	SeriesPart             string `json:"tagSeriesPart,omitempty"`
	Grouping               string `json:"tagGrouping,omitempty"`
	Track                  string `json:"tagTrack,omitempty"`
	Disc                   string `json:"tagDisc,omitempty"`
	Subtitle               string `json:"tagSubtitle,omitempty"`
	AlbumArtist            string `json:"tagAlbumArtist,omitempty"`
	Date                   string `json:"tagDate,omitempty"`
	Composer               string `json:"tagComposer,omitempty"`
	Publisher              string `json:"tagPublisher,omitempty"`
	Comment                string `json:"tagComment,omitempty"`
	Description            string `json:"tagDescription,omitempty"`
	Encoder                string `json:"tagEncoder,omitempty"`
	EncodedBy              string `json:"tagEncodedBy,omitempty"`
	ISBN                   string `json:"tagIsbn,omitempty"`
	Language               string `json:"tagLanguage,omitempty"`
	ASIN                   string `json:"tagASIN,omitempty"`
	ItunesID               string `json:"tagItunesId,omitempty"`
	PodcastType            string `json:"tagPodcastType,omitempty"`
	EpisodeType            string `json:"tagEpisodeType,omitempty"`
	OverdriveMediaMarker   string `json:"tagOverdriveMediaMarker,omitempty"`
	OriginalYear           string `json:"tagOriginalYear,omitempty"`
	ReleaseCountry         string `json:"tagReleaseCountry,omitempty"`
	ReleaseType            string `json:"tagReleaseType,omitempty"`
	ReleaseStatus          string `json:"tagReleaseStatus,omitempty"`
	ISRC                   string `json:"tagISRC,omitempty"`
	MusicBrainzTrackID     string `json:"tagMusicBrainzTrackId,omitempty"`
	MusicBrainzAlbumID     string `json:"tagMusicBrainzAlbumId,omitempty"`
	MusicBrainzAlbumArtist string `json:"tagMusicBrainzAlbumArtistId,omitempty"`
	MusicBrainzArtistID    string `json:"tagMusicBrainzArtistId,omitempty"`
}

// AudioFile is one audio file of a book (books.audioFiles) or the audio file of
// a podcast episode (podcastEpisodes.audioFile); server/objects/files/AudioFile.js
// toJSON.
type AudioFile struct {
	Index                int64         `json:"index"`
	Ino                  string        `json:"ino"`
	Metadata             FileMetadata  `json:"metadata"`
	AddedAt              int64         `json:"addedAt"`
	UpdatedAt            int64         `json:"updatedAt"`
	TrackNumFromMeta     *int64        `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int64        `json:"discNumFromMeta"`
	TrackNumFromFilename *int64        `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int64        `json:"discNumFromFilename"`
	ManuallyVerified     bool          `json:"manuallyVerified"`
	Exclude              bool          `json:"exclude"`
	Error                *string       `json:"error"`
	Format               string        `json:"format"`
	Duration             *float64      `json:"duration"`
	BitRate              *int64        `json:"bitRate"`
	Language             *string       `json:"language"`
	Codec                *string       `json:"codec"`
	TimeBase             *string       `json:"timeBase"`
	Channels             *int64        `json:"channels"`
	ChannelLayout        *string       `json:"channelLayout"`
	Chapters             []Chapter     `json:"chapters"`
	EmbeddedCoverArt     *string       `json:"embeddedCoverArt"`
	MetaTags             AudioMetaTags `json:"metaTags"`
	MimeType             string        `json:"mimeType"`
}

// LibraryFile is one entry of libraryItems.libraryFiles
// (server/objects/files/LibraryFile.js toJSON).
type LibraryFile struct {
	Ino             string       `json:"ino"`
	Metadata        FileMetadata `json:"metadata"`
	IsSupplementary *bool        `json:"isSupplementary"`
	AddedAt         int64        `json:"addedAt"`
	UpdatedAt       int64        `json:"updatedAt"`
	FileType        string       `json:"fileType"`
}

// EbookFile is books.ebookFile. The scanner writes it in two shapes
// (server/scanner/BookScanner.js lines 187-205): a LibraryFile plus ebookFormat
// when it first finds an ebook, and EBookFile.toJSON (no isSupplementary or
// fileType) when it updates one. FileType is empty and IsSupplementary absent in
// the second shape.
type EbookFile struct {
	Ino         string       `json:"ino"`
	Metadata    FileMetadata `json:"metadata"`
	EbookFormat string       `json:"ebookFormat"`
	AddedAt     int64        `json:"addedAt"`
	UpdatedAt   int64        `json:"updatedAt"`
	FileType    string       `json:"fileType,omitempty"`
	// IsSupplementary is raw so that an absent key (second shape) stays distinct
	// from null, true and false (first shape).
	IsSupplementary json.RawMessage `json:"isSupplementary,omitempty"`
}

// LibraryItemExtra is libraryItems.extraData, {} for items created by the
// current scanner. OldLibraryItemID is set by the pre-2.3 database migration.
type LibraryItemExtra struct {
	OldLibraryItemID *string `json:"oldLibraryItemId,omitempty"`
}

// EpisodeExtra is podcastEpisodes.extraData: the RSS guid of an episode that was
// downloaded from a feed, and the id the pre-2.3 migration replaced.
type EpisodeExtra struct {
	GUID         *string `json:"guid,omitempty"`
	OldEpisodeID *string `json:"oldEpisodeId,omitempty"`
}

// Permissions is users.permissions and apiKeys.permissions
// (server/models/User.js getDefaultPermissionsForUserType, ApiKey.js).
type Permissions struct {
	Download                  bool     `json:"download"`
	Update                    bool     `json:"update"`
	Delete                    bool     `json:"delete"`
	Upload                    bool     `json:"upload"`
	CreateEreader             bool     `json:"createEreader"`
	AccessAllLibraries        bool     `json:"accessAllLibraries"`
	AccessAllTags             bool     `json:"accessAllTags"`
	AccessExplicitContent     bool     `json:"accessExplicitContent"`
	SelectedTagsNotAccessible bool     `json:"selectedTagsNotAccessible"`
	LibrariesAccessible       []string `json:"librariesAccessible"`
	ItemTagsSelected          []string `json:"itemTagsSelected"`
}

// Bookmark is one entry of users.bookmarks (server/models/User.js createBookmark).
type Bookmark struct {
	LibraryItemID string  `json:"libraryItemId"`
	Time          float64 `json:"time"`
	Title         string  `json:"title"`
	CreatedAt     int64   `json:"createdAt"`
}

// UserExtra is users.extraData. SeriesHideFromContinueListening is written for
// every user created since 2.3; AuthOpenIDSub is set by OpenID logins and
// OldUserID by the pre-2.3 migration (it keeps old tokens working).
type UserExtra struct {
	SeriesHideFromContinueListening []string `json:"seriesHideFromContinueListening,omitzero"`
	AuthOpenIDSub                   *string  `json:"authOpenIDSub,omitempty"`
	OldUserID                       *string  `json:"oldUserId,omitempty"`
}

// LibrarySettings is libraries.settings (server/models/Library.js
// getDefaultLibrarySettingsForMediaType). Keys that exist for only one media
// type, or that older libraries lack, are pointers or omitted when empty.
type LibrarySettings struct {
	CoverAspectRatio       float64 `json:"coverAspectRatio"`
	DisableWatcher         bool    `json:"disableWatcher"`
	AutoScanCronExpression *string `json:"autoScanCronExpression"`
	// Book libraries only.
	SkipMatchingMediaWithAsin          *bool    `json:"skipMatchingMediaWithAsin,omitempty"`
	SkipMatchingMediaWithIsbn          *bool    `json:"skipMatchingMediaWithIsbn,omitempty"`
	AudiobooksOnly                     *bool    `json:"audiobooksOnly,omitempty"`
	EpubsAllowScriptedContent          *bool    `json:"epubsAllowScriptedContent,omitempty"`
	HideSingleBookSeries               *bool    `json:"hideSingleBookSeries,omitempty"`
	OnlyShowLaterBooksInContinueSeries *bool    `json:"onlyShowLaterBooksInContinueSeries,omitempty"`
	MetadataPrecedence                 []string `json:"metadataPrecedence,omitzero"`
	// Podcast libraries only.
	PodcastSearchRegion *string `json:"podcastSearchRegion,omitempty"`
	// Both. Percent is 0-100 and wins over the seconds remaining when set.
	MarkAsFinishedPercentComplete *float64 `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining   *float64 `json:"markAsFinishedTimeRemaining"`
}

// LibraryExtra is libraries.extraData (NULL for podcast libraries that were
// never scanned). OldLibraryID is set by the pre-2.3 migration.
type LibraryExtra struct {
	LastScanMetadataPrecedence []string `json:"lastScanMetadataPrecedence,omitzero"`
	OldLibraryID               *string  `json:"oldLibraryId,omitempty"`
}

// ProgressExtra is mediaProgresses.extraData: the owning library item and the
// 0-1 progress the old model stored on the progress object.
type ProgressExtra struct {
	LibraryItemID *string  `json:"libraryItemId,omitempty"`
	Progress      *float64 `json:"progress,omitempty"`
}

// SessionExtra is playbackSessions.extraData.
type SessionExtra struct {
	LibraryItemID *string `json:"libraryItemId,omitempty"`
}

// DeviceExtra is devices.extraData: the device details upstream stores only when
// they are non-empty (server/models/Device.js getFromOld).
type DeviceExtra struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	OSName       string `json:"osName,omitempty"`
	OSVersion    string `json:"osVersion,omitempty"`
	BrowserName  string `json:"browserName,omitempty"`
}

// SessionPerson is an author in a book session's metadata.
type SessionPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionSeries is a series in a book session's metadata.
type SessionSeries struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// SessionBookMetadata is the mediaMetadata a new book playback session writes
// (server/models/Book.js oldMetadataToJSON). The column itself is raw
// (Go playbackSessions.mediaMetadata is JSON[json.RawMessage]): it differs for
// book and podcast sessions, and rows migrated from the pre-2.3 server miss
// keys or hold {}, which a typed struct would fill in with nulls.
type SessionBookMetadata struct {
	Title         *string         `json:"title"`
	Subtitle      *string         `json:"subtitle"`
	Authors       []SessionPerson `json:"authors"`
	Narrators     []string        `json:"narrators"`
	Series        []SessionSeries `json:"series"`
	Genres        []string        `json:"genres"`
	PublishedYear *string         `json:"publishedYear"`
	PublishedDate *string         `json:"publishedDate"`
	Publisher     *string         `json:"publisher"`
	Description   *string         `json:"description"`
	ISBN          *string         `json:"isbn"`
	ASIN          *string         `json:"asin"`
	Language      *string         `json:"language"`
	Explicit      *bool           `json:"explicit"`
	Abridged      *bool           `json:"abridged"`
}

// SessionPodcastMetadata is the mediaMetadata a new podcast playback session
// writes (server/models/Podcast.js oldMetadataToJSON); see SessionBookMetadata. Note the key spellings feedUrl,
// imageUrl and itunesPageUrl.
type SessionPodcastMetadata struct {
	Title         *string  `json:"title"`
	Author        *string  `json:"author"`
	Description   *string  `json:"description"`
	ReleaseDate   *string  `json:"releaseDate"`
	Genres        []string `json:"genres"`
	FeedURL       *string  `json:"feedUrl"`
	ImageURL      *string  `json:"imageUrl"`
	ItunesPageURL *string  `json:"itunesPageUrl"`
	ItunesID      *string  `json:"itunesId"`
	ItunesArtist  *string  `json:"itunesArtistId"`
	Explicit      *bool    `json:"explicit"`
	Language      *string  `json:"language"`
	Type          *string  `json:"type"`
}
