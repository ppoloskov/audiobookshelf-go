// Package item is the read model of library items: one loader for an item with
// its book or podcast media, and the typed minified, full and expanded views
// that clients receive. Writes (media edits, links, deletion, covers) live
// elsewhere and build on these types.
package item

import (
	"audiobookshelf-compat/internal/db"
)

// Item is a libraryItems row with its media. Exactly one of Book and Podcast
// is non-nil. The embedded row's JSON columns are normalised by Load: library
// files carry their derived fileType and no slice is nil.
type Item struct {
	db.LibraryItem
	Book    *Book
	Podcast *Podcast
}

// Book is a books row with its authors and series in stored order
// (link createdAt, then rowid). Both slices are non-nil.
type Book struct {
	db.Book
	Authors []Author
	Series  []SeriesRef
}

// Podcast is a podcasts row. Episodes is nil unless Options.Episodes was set
// when loading ("not loaded"), otherwise non-nil in createdAt order.
type Podcast struct {
	db.Podcast
	Episodes []Episode
}

// Episode is a podcastEpisodes row.
type Episode struct {
	db.PodcastEpisode
}

// Author is the author reference of a book.
type Author struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SeriesRef is the series reference of a book; Sequence is null when unset.
type SeriesRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// Access is what a permission check needs of an item: its library and the
// media's explicit flag and tags (a podcast's explicit flag is its own).
type Access struct {
	LibraryID string
	Explicit  bool
	Tags      []string
}

// Access reports the permission inputs of the item.
func (it Item) Access() Access {
	a := Access{LibraryID: db.Val(it.LibraryID)}
	a.Explicit, a.Tags = it.mediaFlags()
	return a
}

func (it Item) mediaFlags() (explicit bool, tags []string) {
	switch {
	case it.Book != nil:
		return bool(it.Book.Explicit), it.Book.Tags.V
	case it.Podcast != nil:
		return bool(it.Podcast.Explicit), it.Podcast.Tags.V
	}
	return false, nil
}

// millis is the epoch milliseconds of t, nil for the zero Time.
func millis(t db.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	return db.Ptr(t.Millis())
}

// millisNonZero is millis that also reads a stored 0 as absent (the scanner
// writes 0 for "never"): lastEpisodeCheck and publishedAt.
func millisNonZero(t db.Time) *int64 {
	if m := millis(t); m != nil && *m != 0 {
		return m
	}
	return nil
}

// nilIfEmpty is the pointer to s, nil when s is empty.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MediaRowID is the id of the item's books or podcasts row, "" when neither is
// loaded.
func (it Item) MediaRowID() string {
	switch {
	case it.Book != nil:
		return it.Book.ID
	case it.Podcast != nil:
		return it.Podcast.ID
	}
	return ""
}
