// Package items owns library-item identity, the batched item loader and every
// common item response envelope. Book/podcast media projections still live in
// libitem until the media domains are extracted.
package items

import (
	"encoding/json"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/libitem"
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
	LibraryFiles []libitem.File

	Book    *books.Book
	Podcast *libitem.Podcast
}

// Options selects the optional associations Load reads.
type Options struct {
	// Episodes loads Podcast.Episodes (createdAt ascending). List pages must
	// leave it unset.
	Episodes bool
	// EpisodeIDs, when set with Episodes, limits the episodes loaded to these
	// ids (a shelf needs only the episodes it shows).
	EpisodeIDs []string
}

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
	LastScan     *int64         `json:"lastScan,omitempty"`
	ScanVersion  *string        `json:"scanVersion"`
	LibraryFiles []libitem.File `json:"libraryFiles"`
	Media        any            `json:"media"` // books.BookFull or libitem.PodcastMedia
	Extras
}

// Expanded is toOldJSONExpanded: item detail, socket events, playlists.
type Expanded struct {
	Head
	LastScan     *int64         `json:"lastScan"`
	ScanVersion  *string        `json:"scanVersion"`
	LibraryFiles []libitem.File `json:"libraryFiles"`
	NumFiles     int            `json:"numFiles"`
	Size         *int64         `json:"size,omitempty"`
	Media        any            `json:"media"` // books.BookExpanded or libitem.PodcastMedia
	Extras
}

// Minified is toOldJSONMinified: shelves, collections and list rows.
type Minified struct {
	Head
	NumFiles int    `json:"numFiles"`
	Size     *int64 `json:"size,omitempty"`
	Media    any    `json:"media"` // books.BookMinified or libitem.PodcastMedia
	Extras
}

// Access exposes the permission inputs: the item's library and the media's
// explicit flag and tags (a podcast's explicit flag is its metadata.explicit).
func (it Item) Access() access.Access {
	a := access.Access{LibraryID: it.LibraryID}
	switch {
	case it.Book != nil:
		a.Explicit, a.Tags = it.Book.Explicit, it.Book.Tags
	case it.Podcast != nil:
		a.Explicit, a.Tags = it.Podcast.Explicit, it.Podcast.Tags
	}
	return a
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
		f.Media = it.Book.Full(it.ID)
	} else if it.Podcast != nil {
		f.Media = it.Podcast.Media(it.ID, nil, true, false)
	}
	return f
}

// Expanded serializes the item with expanded media. A podcast's episodes must
// have been loaded (Options.Episodes) for numEpisodes, size and episodes to
// reflect them.
func (it Item) Expanded(prefixes []string) Expanded {
	e := Expanded{Head: it.head(), LastScan: it.LastScan, ScanVersion: it.ScanVersion, LibraryFiles: it.LibraryFiles, NumFiles: len(it.LibraryFiles), Size: it.Size}
	if it.Book != nil {
		e.Media = it.Book.Expanded(it.ID, prefixes)
	} else if it.Podcast != nil {
		e.Media = it.Podcast.Media(it.ID, prefixes, false, true)
	}
	return e
}

// Minified serializes the shelf and collection shape.
func (it Item) Minified(prefixes []string) Minified {
	m := Minified{Head: it.head(), NumFiles: len(it.LibraryFiles), Size: it.Size}
	if it.Book != nil {
		m.Media = it.Book.Minified(prefixes)
	} else if it.Podcast != nil {
		m.Media = it.Podcast.Media(it.ID, prefixes, false, false)
	}
	return m
}

// ListRow is the library items page row: Minified, except that a podcast
// reports numEpisodes from its column and the item's size
// (LibraryItem.getByFilterAndSort, server/models/LibraryItem.js:310-312).
func (it Item) ListRow(prefixes []string) Minified {
	m := it.Minified(prefixes)
	if p, ok := m.Media.(libitem.PodcastMedia); ok {
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

// Access of a serialized item: the envelope's library and its media's explicit
// flag and tags, so event filters need not re-load the row.
func (h Head) access(media any) access.Access {
	a := access.Access{LibraryID: h.LibraryID}
	switch m := media.(type) {
	case books.BookFull:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case books.BookMinified:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case books.BookExpanded:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	case libitem.PodcastMedia:
		a.Explicit, a.Tags = m.Metadata.Explicit, m.Tags
	}
	return a
}

// mediaID is the id of a serialized media value ("" for an unknown one).
func mediaID(media any) string {
	switch m := media.(type) {
	case books.BookFull:
		return m.ID
	case books.BookMinified:
		return m.ID
	case books.BookExpanded:
		return m.ID
	case libitem.PodcastMedia:
		return m.ID
	}
	return ""
}

// MediaID is the id of the item's book or podcast.
func (f Full) MediaID() string     { return mediaID(f.Media) }
func (e Expanded) MediaID() string { return mediaID(e.Media) }
func (m Minified) MediaID() string { return mediaID(m.Media) }

func (f Full) Access() access.Access     { return f.Head.access(f.Media) }
func (e Expanded) Access() access.Access { return e.Head.access(e.Media) }
func (m Minified) Access() access.Access { return m.Head.access(m.Media) }
