// Package libitem is the one typed model of a library item: a batched loader
// over the libraryItems, books, podcasts, author, series and episode tables,
// and the serializers for every item JSON shape the API serves. Callers set
// per-request decorators (progress, feeds, shares, ...) on the envelope
// structs instead of editing maps.
package libitem

import (
	"database/sql"
	"slices"

	"audiobookshelf-compat/internal/jsonvalue"
)

// Item is a library item row with its media. Exactly one of Book and Podcast
// is non-nil.
type Item struct {
	ID               string
	MediaID          string
	Ino              *string
	LibraryID        string
	FolderID         *string
	Path             string
	RelPath          string
	IsFile           bool
	MTimeMS          *int64
	CTimeMS          *int64
	BirthtimeMS      *int64
	AddedAt          *int64
	UpdatedAt        *int64
	LastScan         *int64
	ScanVersion      *string
	IsMissing        bool
	IsInvalid        bool
	MediaType        string
	Size             *int64
	OldLibraryItemID string
	// LibraryFiles is passed through opaquely; each entry's fileType is derived
	// from metadata.ext at load time.
	LibraryFiles []map[string]any

	Book    *Book
	Podcast *Podcast
}

// Author and Series are the ordered associations of a book.
type Author struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SeriesRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// Book is a books row with its ordered authors and series. AudioFiles,
// Chapters and Ebook are scanner-owned JSON passed through to clients; the
// accessors below read the few keys the model needs.
type Book struct {
	ID            string
	Title         *string
	Subtitle      *string
	PublishedYear *string
	PublishedDate *string
	Publisher     *string
	Description   *string
	ISBN          *string
	ASIN          *string
	Language      *string
	Explicit      bool
	Abridged      bool
	CoverPath     *string
	Duration      *float64
	UpdatedAt     *int64
	Narrators     []string
	Genres        []string
	Tags          []string
	AudioFiles    []map[string]any
	Chapters      []map[string]any
	Ebook         map[string]any // nil when the book has no ebook file

	Authors []Author
	Series  []SeriesRef
}

// Podcast is a podcasts row. Episodes is nil unless Options.Episodes was set;
// NumEpisodes is the stored column.
type Podcast struct {
	ID                       string
	Title                    *string
	Author                   *string
	Description              *string
	ReleaseDate              *string
	FeedURL                  *string
	ImageURL                 *string
	ITunesPageURL            *string
	ITunesID                 *string
	ITunesArtistID           *string
	Language                 *string
	Type                     *string
	Explicit                 bool
	AutoDownloadEpisodes     bool
	AutoDownloadSchedule     *string
	LastEpisodeCheck         *int64 // nil when NULL or 0
	MaxEpisodesToKeep        int64
	MaxNewEpisodesToDownload int64
	CoverPath                *string
	Tags                     []string
	Genres                   []string
	NumEpisodes              int64

	Episodes []Episode
}

// Episode is a podcastEpisodes row.
type Episode struct {
	ID           string
	PodcastID    string
	OldEpisodeID string
	GUID         string
	Index        *int64
	Season       *string
	Episode      *string
	EpisodeType  *string
	Title        *string
	Subtitle     *string
	Description  *string
	PubDate      *string
	Enclosure    *Enclosure
	PublishedAt  *int64 // nil when NULL or 0
	AddedAt      *int64
	UpdatedAt    *int64
	Chapters     []map[string]any
	AudioFile    map[string]any
}

// Enclosure is the feed enclosure of an episode; Length is the decimal size.
type Enclosure struct {
	URL    string  `json:"url"`
	Type   *string `json:"type"`
	Length *string `json:"length"`
}

// Options selects the optional associations Load reads.
type Options struct {
	// Episodes loads Podcast.Episodes (createdAt ascending). List pages must
	// leave it unset.
	Episodes bool
}

// Access is what every permission check reads of an item.
type Access struct {
	LibraryID string
	Explicit  bool
	Tags      []string
}

// Access exposes the permission inputs: the item's library and the media's
// explicit flag and tags (a podcast's explicit flag is its metadata.explicit).
func (it Item) Access() Access {
	a := Access{LibraryID: it.LibraryID}
	switch {
	case it.Book != nil:
		a.Explicit, a.Tags = it.Book.Explicit, it.Book.Tags
	case it.Podcast != nil:
		a.Explicit, a.Tags = it.Podcast.Explicit, it.Podcast.Tags
	}
	return a
}

// Access of a serialized item: the envelope's library and its media's explicit
// flag and tags, so event filters need not re-load the row.
func (h Head) access(media any) Access {
	a := Access{LibraryID: h.LibraryID}
	switch m := media.(type) {
	case BookFull:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case BookMinified:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case BookExpanded:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case PodcastMedia:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	}
	return a
}

// mediaID is the id of a serialized media value ("" for an unknown one).
func mediaID(media any) string {
	switch m := media.(type) {
	case BookFull:
		return m.ID
	case BookMinified:
		return m.ID
	case BookExpanded:
		return m.ID
	case PodcastMedia:
		return m.ID
	}
	return ""
}

// MediaID is the id of the item's book or podcast.
func (f Full) MediaID() string     { return mediaID(f.Media) }
func (e Expanded) MediaID() string { return mediaID(e.Media) }
func (m Minified) MediaID() string { return mediaID(m.Media) }

func (f Full) Access() Access     { return f.Head.access(f.Media) }
func (e Expanded) Access() Access { return e.Head.access(e.Media) }
func (m Minified) Access() Access { return m.Head.access(m.Media) }

// AllowedBy is User.checkCanAccessLibraryItem for a user's permissions object:
// library, explicit-content and tag rules (admins are the caller's concern).
func (a Access) AllowedBy(perms map[string]any) bool {
	if perms["accessAllLibraries"] != true && !slices.Contains(jsonvalue.Strings(perms["librariesAccessible"]), a.LibraryID) {
		return false
	}
	if a.Explicit && perms["accessExplicitContent"] != true {
		return false
	}
	if perms["accessAllTags"] == true {
		return true
	}
	inverted := perms["selectedTagsNotAccessible"] == true
	if len(a.Tags) == 0 {
		return inverted
	}
	selected := jsonvalue.Strings(perms["itemTagsSelected"])
	return slices.ContainsFunc(a.Tags, func(t string) bool { return slices.Contains(selected, t) }) != inverted
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
