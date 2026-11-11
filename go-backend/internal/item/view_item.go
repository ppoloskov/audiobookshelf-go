package item

import (
	"encoding/json"

	"audiobookshelf-compat/internal/db"
)

// JSONNull sets an Extras field to an explicit JSON null (the zero value omits
// the key), for keys the client expects as null on an expanded item.
var JSONNull = json.RawMessage("null")

// Media is the typed media view inside an item envelope: BookMinified,
// BookFull, BookExpanded, PodcastMinified, PodcastFull or PodcastExpanded.
type Media interface {
	// MediaID is the id of the book or podcast.
	MediaID() string
	// Flags are the media's explicit flag and tags, the inputs of access checks.
	Flags() (explicit bool, tags []string)
}

// Head is the part of the item envelope every view shares.
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
// JSONNull where the client expects an explicit null. The fields are any
// because each is a different open shape owned by the caller's domain
// (progress, feeds, shares, playlists, series collapsing).
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

// Full is the item without expanded media (LibraryItem.toOldJSON).
type Full struct {
	Head
	LastScan     *int64           `json:"lastScan,omitempty"`
	ScanVersion  *string          `json:"scanVersion"`
	LibraryFiles []db.LibraryFile `json:"libraryFiles"`
	Media        Media            `json:"media"` // BookFull or PodcastFull; polymorphic by mediaType
	Extras
}

// Expanded is toOldJSONExpanded: item detail, socket events, playlists.
type Expanded struct {
	Head
	LastScan     *int64           `json:"lastScan"`
	ScanVersion  *string          `json:"scanVersion"`
	LibraryFiles []db.LibraryFile `json:"libraryFiles"`
	NumFiles     int              `json:"numFiles"`
	Size         *int64           `json:"size,omitempty"`
	Media        Media            `json:"media"` // BookExpanded or PodcastExpanded; polymorphic by mediaType
	Extras
}

// Minified is toOldJSONMinified: shelves, collections and list rows.
type Minified struct {
	Head
	NumFiles int    `json:"numFiles"`
	Size     *int64 `json:"size,omitempty"`
	Media    Media  `json:"media"` // BookMinified or PodcastMinified; polymorphic by mediaType
	Extras
}

func (it Item) head() Head {
	var oldID *string
	if extra := it.ExtraData.V; extra != nil {
		oldID = extra.OldLibraryItemID
		if oldID != nil && *oldID == "" {
			oldID = nil
		}
	}
	return Head{ID: it.ID, Ino: it.Ino, OldLibraryItemID: oldID, LibraryID: db.Val(it.LibraryID), FolderID: it.LibraryFolderID,
		Path: db.Val(it.Path), RelPath: db.Val(it.RelPath), IsFile: bool(it.IsFile),
		MTimeMS: millis(it.Mtime), CTimeMS: millis(it.Ctime), BirthtimeMS: millis(it.Birthtime),
		AddedAt: millis(it.CreatedAt), UpdatedAt: millis(it.UpdatedAt),
		IsMissing: bool(it.IsMissing), IsInvalid: bool(it.IsInvalid), MediaType: db.Val(it.MediaType)}
}

// Full is the item for GET /api/items/:id without expanded.
func (it Item) Full() Full {
	f := Full{Head: it.head(), LastScan: millis(it.LastScan), ScanVersion: it.LastScanVersion, LibraryFiles: it.LibraryFiles.V}
	switch {
	case it.Book != nil:
		f.Media = it.Book.Full(it.ID)
	case it.Podcast != nil:
		f.Media = it.Podcast.Full(it.ID)
	}
	return f
}

// Expanded is the item with expanded media. A podcast's episodes must have
// been loaded (Options.Episodes) for numEpisodes, size and episodes to
// reflect them. prefixes are the server's sorting prefixes.
func (it Item) Expanded(prefixes []string) Expanded {
	e := Expanded{Head: it.head(), LastScan: millis(it.LastScan), ScanVersion: it.LastScanVersion, LibraryFiles: it.LibraryFiles.V,
		NumFiles: len(it.LibraryFiles.V), Size: it.Size}
	switch {
	case it.Book != nil:
		e.Media = it.Book.Expanded(it.ID, prefixes)
	case it.Podcast != nil:
		e.Media = it.Podcast.Expanded(it.ID, prefixes)
	}
	return e
}

// Minified is the shelf and collection shape. A podcast reports numEpisodes
// and size 0, as upstream loads it without episodes.
func (it Item) Minified(prefixes []string) Minified {
	m := Minified{Head: it.head(), NumFiles: len(it.LibraryFiles.V), Size: it.Size}
	switch {
	case it.Book != nil:
		m.Media = it.Book.Minified(prefixes)
	case it.Podcast != nil:
		m.Media = it.Podcast.Minified(prefixes)
	}
	return m
}

// ListRow is the library items page row: Minified, except that a podcast
// reports numEpisodes from its column and the item's size
// (LibraryItem.getByFilterAndSort, server/models/LibraryItem.js:310-312).
func (it Item) ListRow(prefixes []string) Minified {
	m := it.Minified(prefixes)
	if p, ok := m.Media.(PodcastMinified); ok {
		p.NumEpisodes, p.Size = db.Val(it.Podcast.NumEpisodes), db.Val(it.Size)
		m.Media = p
	}
	return m
}

// MediaID is the id of the serialized item's book or podcast.
func (f Full) MediaID() string { return mediaID(f.Media) }

// MediaID is the id of the serialized item's book or podcast.
func (e Expanded) MediaID() string { return mediaID(e.Media) }

// MediaID is the id of the serialized item's book or podcast.
func (m Minified) MediaID() string { return mediaID(m.Media) }

// Access reports the permission inputs of the serialized item, so event
// filters need not reload the row.
func (f Full) Access() Access { return f.Head.access(f.Media) }

// Access reports the permission inputs of the serialized item.
func (e Expanded) Access() Access { return e.Head.access(e.Media) }

// Access reports the permission inputs of the serialized item.
func (m Minified) Access() Access { return m.Head.access(m.Media) }

func (h Head) access(media Media) Access {
	a := Access{LibraryID: h.LibraryID}
	if media != nil {
		a.Explicit, a.Tags = media.Flags()
	}
	return a
}

func mediaID(media Media) string {
	if media == nil {
		return ""
	}
	return media.MediaID()
}
