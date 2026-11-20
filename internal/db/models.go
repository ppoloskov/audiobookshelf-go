package db

import (
	"encoding/json"
	"fmt"
)

// One struct per table of the pinned schema (schema.sql), with a db tag for
// every column. Typing rules: id and NOT NULL text are string; nullable text and
// UUID are *string; nullable INTEGER and BIGINT are *int64; nullable FLOAT is
// *float64; DATETIME is Time; TINYINT(1) is Bool; JSON is JSON[T] over a
// document from documents.go.
//
// A JSON column is JSON[*T] where NULL is a real state (the cell reads nil and
// writes back NULL) and JSON[T] where upstream always writes a document.
//
// Columns that triggers maintain carry dbwrite:"insert": Insert writes them
// (no trigger fires for a new row) and Update never does.

// ApiKey is a row of apiKeys.
type ApiKey struct {
	ID              string            `db:"id"`
	Name            string            `db:"name"`
	Description     *string           `db:"description"`
	ExpiresAt       Time              `db:"expiresAt"`
	LastUsedAt      Time              `db:"lastUsedAt"`
	IsActive        Bool              `db:"isActive"`
	Permissions     JSON[Permissions] `db:"permissions"`
	CreatedAt       Time              `db:"createdAt"`
	UpdatedAt       Time              `db:"updatedAt"`
	UserID          *string           `db:"userId"`
	CreatedByUserID *string           `db:"createdByUserId"`
}

// Author is a row of authors.
type Author struct {
	ID          string  `db:"id"`
	Name        *string `db:"name"`
	LastFirst   *string `db:"lastFirst"`
	ASIN        *string `db:"asin"`
	Description *string `db:"description"`
	ImagePath   *string `db:"imagePath"`
	CreatedAt   Time    `db:"createdAt"`
	UpdatedAt   Time    `db:"updatedAt"`
	LibraryID   *string `db:"libraryId"`
}

// BookAuthor is a row of bookAuthors.
type BookAuthor struct {
	ID        string  `db:"id"`
	CreatedAt Time    `db:"createdAt"`
	BookID    *string `db:"bookId"`
	AuthorID  *string `db:"authorId"`
}

// BookSeries is a row of bookSeries.
type BookSeries struct {
	ID        string  `db:"id"`
	Sequence  *string `db:"sequence"`
	CreatedAt Time    `db:"createdAt"`
	BookID    *string `db:"bookId"`
	SeriesID  *string `db:"seriesId"`
}

// Book is a row of books. Its title and titleIgnorePrefix are copied into
// libraryItems by triggers.
type Book struct {
	ID                string            `db:"id"`
	Title             *string           `db:"title"`
	TitleIgnorePrefix *string           `db:"titleIgnorePrefix"`
	Subtitle          *string           `db:"subtitle"`
	PublishedYear     *string           `db:"publishedYear"`
	PublishedDate     *string           `db:"publishedDate"`
	Publisher         *string           `db:"publisher"`
	Description       *string           `db:"description"`
	ISBN              *string           `db:"isbn"`
	ASIN              *string           `db:"asin"`
	Language          *string           `db:"language"`
	Explicit          Bool              `db:"explicit"`
	Abridged          Bool              `db:"abridged"`
	CoverPath         *string           `db:"coverPath"`
	Duration          *float64          `db:"duration"`
	Narrators         JSON[[]string]    `db:"narrators"`
	AudioFiles        JSON[[]AudioFile] `db:"audioFiles"`
	EbookFile         JSON[*EbookFile]  `db:"ebookFile"`
	Chapters          JSON[[]Chapter]   `db:"chapters"`
	Tags              JSON[[]string]    `db:"tags"`
	Genres            JSON[[]string]    `db:"genres"`
	CreatedAt         Time              `db:"createdAt"`
	UpdatedAt         Time              `db:"updatedAt"`
}

// CollectionBook is a row of collectionBooks.
type CollectionBook struct {
	ID           string  `db:"id"`
	Order        *int64  `db:"order"`
	CreatedAt    Time    `db:"createdAt"`
	BookID       *string `db:"bookId"`
	CollectionID *string `db:"collectionId"`
}

// Collection is a row of collections.
type Collection struct {
	ID          string  `db:"id"`
	Name        *string `db:"name"`
	Description *string `db:"description"`
	CreatedAt   Time    `db:"createdAt"`
	UpdatedAt   Time    `db:"updatedAt"`
	LibraryID   *string `db:"libraryId"`
}

// CustomMetadataProvider is a row of customMetadataProviders. Its
// authHeaderValue is a secret and is left out of String.
type CustomMetadataProvider struct {
	ID              string  `db:"id"`
	Name            *string `db:"name"`
	MediaType       *string `db:"mediaType"`
	URL             *string `db:"url"`
	AuthHeaderValue *string `db:"authHeaderValue"`
	// ExtraData is open: upstream defines the column but never writes it.
	ExtraData JSON[json.RawMessage] `db:"extraData"`
	CreatedAt Time                  `db:"createdAt"`
	UpdatedAt Time                  `db:"updatedAt"`
}

// String redacts the secret so %v and %+v cannot leak it.
func (p CustomMetadataProvider) String() string {
	return fmt.Sprintf("CustomMetadataProvider{ID:%s}", p.ID)
}

// Device is a row of devices.
type Device struct {
	ID            string            `db:"id"`
	DeviceID      *string           `db:"deviceId"`
	ClientName    *string           `db:"clientName"`
	ClientVersion *string           `db:"clientVersion"`
	IPAddress     *string           `db:"ipAddress"`
	DeviceName    *string           `db:"deviceName"`
	DeviceVersion *string           `db:"deviceVersion"`
	ExtraData     JSON[DeviceExtra] `db:"extraData"`
	CreatedAt     Time              `db:"createdAt"`
	UpdatedAt     Time              `db:"updatedAt"`
	UserID        *string           `db:"userId"`
}

// FeedEpisode is a row of feedEpisodes.
type FeedEpisode struct {
	ID            string   `db:"id"`
	Title         *string  `db:"title"`
	Author        *string  `db:"author"`
	Description   *string  `db:"description"`
	SiteURL       *string  `db:"siteURL"`
	EnclosureURL  *string  `db:"enclosureURL"`
	EnclosureType *string  `db:"enclosureType"`
	EnclosureSize *int64   `db:"enclosureSize"`
	PubDate       *string  `db:"pubDate"`
	Season        *string  `db:"season"`
	Episode       *string  `db:"episode"`
	EpisodeType   *string  `db:"episodeType"`
	Duration      *float64 `db:"duration"`
	FilePath      *string  `db:"filePath"`
	Explicit      Bool     `db:"explicit"`
	CreatedAt     Time     `db:"createdAt"`
	UpdatedAt     Time     `db:"updatedAt"`
	FeedID        *string  `db:"feedId"`
}

// Feed is a row of feeds.
type Feed struct {
	ID              string  `db:"id"`
	Slug            *string `db:"slug"`
	EntityType      *string `db:"entityType"`
	EntityID        *string `db:"entityId"`
	EntityUpdatedAt Time    `db:"entityUpdatedAt"`
	ServerAddress   *string `db:"serverAddress"`
	FeedURL         *string `db:"feedURL"`
	ImageURL        *string `db:"imageURL"`
	SiteURL         *string `db:"siteURL"`
	Title           *string `db:"title"`
	Description     *string `db:"description"`
	Author          *string `db:"author"`
	PodcastType     *string `db:"podcastType"`
	Language        *string `db:"language"`
	OwnerName       *string `db:"ownerName"`
	OwnerEmail      *string `db:"ownerEmail"`
	Explicit        Bool    `db:"explicit"`
	PreventIndexing Bool    `db:"preventIndexing"`
	CoverPath       *string `db:"coverPath"`
	CreatedAt       Time    `db:"createdAt"`
	UpdatedAt       Time    `db:"updatedAt"`
	UserID          *string `db:"userId"`
}

// Library is a row of libraries.
type Library struct {
	ID              string                `db:"id"`
	Name            *string               `db:"name"`
	DisplayOrder    *int64                `db:"displayOrder"`
	Icon            *string               `db:"icon"`
	MediaType       *string               `db:"mediaType"`
	Provider        *string               `db:"provider"`
	LastScan        Time                  `db:"lastScan"`
	LastScanVersion *string               `db:"lastScanVersion"`
	Settings        JSON[LibrarySettings] `db:"settings"`
	ExtraData       JSON[*LibraryExtra]   `db:"extraData"`
	CreatedAt       Time                  `db:"createdAt"`
	UpdatedAt       Time                  `db:"updatedAt"`
}

// LibraryFolder is a row of libraryFolders.
type LibraryFolder struct {
	ID        string  `db:"id"`
	Path      *string `db:"path"`
	CreatedAt Time    `db:"createdAt"`
	UpdatedAt Time    `db:"updatedAt"`
	LibraryID *string `db:"libraryId"`
}

// LibraryItem is a row of libraryItems. Title, TitleIgnorePrefix,
// AuthorNamesFirstLast and AuthorNamesLastFirst are copied from books,
// podcasts and authors by update triggers (see schema.sql). Set them when
// inserting the item, as upstream does; Update never writes them.
type LibraryItem struct {
	ID                   string                  `db:"id"`
	Ino                  *string                 `db:"ino"`
	Path                 *string                 `db:"path"`
	RelPath              *string                 `db:"relPath"`
	MediaID              *string                 `db:"mediaId"`
	MediaType            *string                 `db:"mediaType"`
	IsFile               Bool                    `db:"isFile"`
	IsMissing            Bool                    `db:"isMissing"`
	IsInvalid            Bool                    `db:"isInvalid"`
	Mtime                Time                    `db:"mtime"`
	Ctime                Time                    `db:"ctime"`
	Birthtime            Time                    `db:"birthtime"`
	Size                 *int64                  `db:"size"`
	LastScan             Time                    `db:"lastScan"`
	LastScanVersion      *string                 `db:"lastScanVersion"`
	LibraryFiles         JSON[[]LibraryFile]     `db:"libraryFiles"`
	ExtraData            JSON[*LibraryItemExtra] `db:"extraData"`
	Title                *string                 `db:"title" dbwrite:"insert"`
	TitleIgnorePrefix    *string                 `db:"titleIgnorePrefix" dbwrite:"insert"`
	AuthorNamesFirstLast *string                 `db:"authorNamesFirstLast" dbwrite:"insert"`
	AuthorNamesLastFirst *string                 `db:"authorNamesLastFirst" dbwrite:"insert"`
	CreatedAt            Time                    `db:"createdAt"`
	UpdatedAt            Time                    `db:"updatedAt"`
	LibraryID            *string                 `db:"libraryId"`
	LibraryFolderID      *string                 `db:"libraryFolderId"`
}

// MediaItemShare is a row of mediaItemShares. Its pash is a secret and is left
// out of String.
type MediaItemShare struct {
	ID            string  `db:"id"`
	MediaItemID   *string `db:"mediaItemId"`
	MediaItemType *string `db:"mediaItemType"`
	Slug          *string `db:"slug"`
	Pash          *string `db:"pash"`
	ExpiresAt     Time    `db:"expiresAt"`
	// ExtraData is open: upstream defines the column but never writes it.
	ExtraData      JSON[json.RawMessage] `db:"extraData"`
	IsDownloadable Bool                  `db:"isDownloadable"`
	CreatedAt      Time                  `db:"createdAt"`
	UpdatedAt      Time                  `db:"updatedAt"`
	UserID         *string               `db:"userId"`
}

// String redacts the secret so %v and %+v cannot leak it.
func (s MediaItemShare) String() string { return fmt.Sprintf("MediaItemShare{ID:%s}", s.ID) }

// MediaProgress is a row of mediaProgresses.
type MediaProgress struct {
	ID                        string               `db:"id"`
	MediaItemID               *string              `db:"mediaItemId"`
	MediaItemType             *string              `db:"mediaItemType"`
	Duration                  *float64             `db:"duration"`
	CurrentTime               *float64             `db:"currentTime"`
	IsFinished                Bool                 `db:"isFinished"`
	HideFromContinueListening Bool                 `db:"hideFromContinueListening"`
	EbookLocation             *string              `db:"ebookLocation"`
	EbookProgress             *float64             `db:"ebookProgress"`
	FinishedAt                Time                 `db:"finishedAt"`
	ExtraData                 JSON[*ProgressExtra] `db:"extraData"`
	PodcastID                 *string              `db:"podcastId"`
	CreatedAt                 Time                 `db:"createdAt"`
	UpdatedAt                 Time                 `db:"updatedAt"`
	UserID                    *string              `db:"userId"`
}

// MigrationsMeta is a row of migrationsMeta (it has no id).
type MigrationsMeta struct {
	Key   string `db:"key"`
	Value string `db:"value"`
}

// PlaybackSession is a row of playbackSessions. PlayMethod and TimeListening are
// INTEGER columns that also hold fractional values (the schema's affinity keeps
// whole numbers as integers), so they are float64.
type PlaybackSession struct {
	ID            string   `db:"id"`
	MediaItemID   *string  `db:"mediaItemId"`
	MediaItemType *string  `db:"mediaItemType"`
	DisplayTitle  *string  `db:"displayTitle"`
	DisplayAuthor *string  `db:"displayAuthor"`
	Duration      *float64 `db:"duration"`
	PlayMethod    *float64 `db:"playMethod"`
	MediaPlayer   *string  `db:"mediaPlayer"`
	StartTime     *float64 `db:"startTime"`
	CurrentTime   *float64 `db:"currentTime"`
	ServerVersion *string  `db:"serverVersion"`
	CoverPath     *string  `db:"coverPath"`
	TimeListening *float64 `db:"timeListening"`
	// MediaMetadata is open, see SessionBookMetadata.
	MediaMetadata JSON[json.RawMessage] `db:"mediaMetadata"`
	Date          *string               `db:"date"`
	DayOfWeek     *string               `db:"dayOfWeek"`
	ExtraData     JSON[*SessionExtra]   `db:"extraData"`
	CreatedAt     Time                  `db:"createdAt"`
	UpdatedAt     Time                  `db:"updatedAt"`
	UserID        *string               `db:"userId"`
	DeviceID      *string               `db:"deviceId"`
	LibraryID     *string               `db:"libraryId"`
}

// PlaylistMediaItem is a row of playlistMediaItems.
type PlaylistMediaItem struct {
	ID            string  `db:"id"`
	MediaItemID   *string `db:"mediaItemId"`
	MediaItemType *string `db:"mediaItemType"`
	Order         *int64  `db:"order"`
	CreatedAt     Time    `db:"createdAt"`
	PlaylistID    *string `db:"playlistId"`
}

// Playlist is a row of playlists.
type Playlist struct {
	ID          string  `db:"id"`
	Name        *string `db:"name"`
	Description *string `db:"description"`
	CreatedAt   Time    `db:"createdAt"`
	UpdatedAt   Time    `db:"updatedAt"`
	LibraryID   *string `db:"libraryId"`
	UserID      *string `db:"userId"`
}

// PodcastEpisode is a row of podcastEpisodes.
type PodcastEpisode struct {
	ID            string              `db:"id"`
	Index         *int64              `db:"index"`
	Season        *string             `db:"season"`
	Episode       *string             `db:"episode"`
	EpisodeType   *string             `db:"episodeType"`
	Title         *string             `db:"title"`
	Subtitle      *string             `db:"subtitle"`
	Description   *string             `db:"description"`
	PubDate       *string             `db:"pubDate"`
	EnclosureURL  *string             `db:"enclosureURL"`
	EnclosureSize *int64              `db:"enclosureSize"`
	EnclosureType *string             `db:"enclosureType"`
	PublishedAt   Time                `db:"publishedAt"`
	AudioFile     JSON[AudioFile]     `db:"audioFile"`
	Chapters      JSON[[]Chapter]     `db:"chapters"`
	ExtraData     JSON[*EpisodeExtra] `db:"extraData"`
	CreatedAt     Time                `db:"createdAt"`
	UpdatedAt     Time                `db:"updatedAt"`
	PodcastID     *string             `db:"podcastId"`
}

// Podcast is a row of podcasts. Its title and titleIgnorePrefix are copied into
// libraryItems by triggers.
type Podcast struct {
	ID                       string         `db:"id"`
	Title                    *string        `db:"title"`
	TitleIgnorePrefix        *string        `db:"titleIgnorePrefix"`
	Author                   *string        `db:"author"`
	ReleaseDate              *string        `db:"releaseDate"`
	FeedURL                  *string        `db:"feedURL"`
	ImageURL                 *string        `db:"imageURL"`
	Description              *string        `db:"description"`
	ItunesPageURL            *string        `db:"itunesPageURL"`
	ItunesID                 *string        `db:"itunesId"`
	ItunesArtistID           *string        `db:"itunesArtistId"`
	Language                 *string        `db:"language"`
	PodcastType              *string        `db:"podcastType"`
	Explicit                 Bool           `db:"explicit"`
	AutoDownloadEpisodes     Bool           `db:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string        `db:"autoDownloadSchedule"`
	LastEpisodeCheck         Time           `db:"lastEpisodeCheck"`
	MaxEpisodesToKeep        *int64         `db:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload *int64         `db:"maxNewEpisodesToDownload"`
	CoverPath                *string        `db:"coverPath"`
	Tags                     JSON[[]string] `db:"tags"`
	Genres                   JSON[[]string] `db:"genres"`
	NumEpisodes              *int64         `db:"numEpisodes"`
	CreatedAt                Time           `db:"createdAt"`
	UpdatedAt                Time           `db:"updatedAt"`
}

// Series is a row of series.
type Series struct {
	ID               string  `db:"id"`
	Name             *string `db:"name"`
	NameIgnorePrefix *string `db:"nameIgnorePrefix"`
	Description      *string `db:"description"`
	CreatedAt        Time    `db:"createdAt"`
	UpdatedAt        Time    `db:"updatedAt"`
	LibraryID        *string `db:"libraryId"`
}

// Session is a row of sessions (login sessions). Its refresh tokens are secrets
// and are left out of String.
type Session struct {
	ID                        string  `db:"id"`
	IPAddress                 *string `db:"ipAddress"`
	UserAgent                 *string `db:"userAgent"`
	RefreshToken              string  `db:"refreshToken"`
	ExpiresAt                 Time    `db:"expiresAt"`
	LastRefreshToken          *string `db:"lastRefreshToken"`
	LastRefreshTokenExpiresAt Time    `db:"lastRefreshTokenExpiresAt"`
	CreatedAt                 Time    `db:"createdAt"`
	UpdatedAt                 Time    `db:"updatedAt"`
	UserID                    string  `db:"userId"`
}

// String redacts the secrets so %v and %+v cannot leak them.
func (s Session) String() string { return fmt.Sprintf("Session{ID:%s UserID:%s}", s.ID, s.UserID) }

// Setting is a row of settings (it has no id; the key is the primary key). The
// server settings document holds token secrets, so String leaves Value out. Value
// stays raw here: the settings package types the documents.
type Setting struct {
	Key       string                `db:"key"`
	Value     JSON[json.RawMessage] `db:"value"`
	CreatedAt Time                  `db:"createdAt"`
	UpdatedAt Time                  `db:"updatedAt"`
}

// String redacts the value so %v and %+v cannot leak secrets.
func (s Setting) String() string { return fmt.Sprintf("Setting{Key:%s}", s.Key) }

// User is a row of users. Its pash (password hash) and token are secrets and are
// left out of String.
type User struct {
	ID          string            `db:"id"`
	Username    *string           `db:"username"`
	Email       *string           `db:"email"`
	Pash        *string           `db:"pash"`
	Type        *string           `db:"type"`
	Token       *string           `db:"token"`
	IsActive    Bool              `db:"isActive"`
	IsLocked    Bool              `db:"isLocked"`
	LastSeen    Time              `db:"lastSeen"`
	Permissions JSON[Permissions] `db:"permissions"`
	Bookmarks   JSON[[]Bookmark]  `db:"bookmarks"`
	ExtraData   JSON[*UserExtra]  `db:"extraData"`
	CreatedAt   Time              `db:"createdAt"`
	UpdatedAt   Time              `db:"updatedAt"`
}

// String redacts the secrets so %v and %+v cannot leak them.
func (u User) String() string { return fmt.Sprintf("User{ID:%s Username:%s}", u.ID, Val(u.Username)) }
