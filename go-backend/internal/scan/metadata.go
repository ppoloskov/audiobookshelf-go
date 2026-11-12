package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/item"
)

// podcastJSON is the podcast metadata.json document. Its key spellings are
// the ones v2.37.2 writes.
type podcastJSON struct {
	Tags           []string `json:"tags"`
	Title          *string  `json:"title"`
	Author         *string  `json:"author"`
	Description    *string  `json:"description"`
	ReleaseDate    *string  `json:"releaseDate"`
	Genres         []string `json:"genres"`
	FeedURL        *string  `json:"feedURL"`
	ImageURL       *string  `json:"imageURL"`
	ItunesPageURL  *string  `json:"itunesPageURL"`
	ItunesID       *string  `json:"itunesId"`
	ItunesArtistID *string  `json:"itunesArtistId"`
	Language       *string  `json:"language"`
	Explicit       bool     `json:"explicit"`
	PodcastType    *string  `json:"podcastType"`
}

func bookJSON(b *item.Book) MetadataJSON {
	doc := MetadataJSON{Tags: orEmpty(b.Tags.V), Chapters: b.Chapters.V, Title: b.Title, Subtitle: b.Subtitle, Authors: []string{}, Narrators: orEmpty(b.Narrators.V),
		Series: []SeriesEntry{}, Genres: orEmpty(b.Genres.V), PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher,
		Description: b.Description, ISBN: b.ISBN, ASIN: b.ASIN, Language: b.Language, Explicit: bool(b.Explicit), Abridged: bool(b.Abridged)}
	for _, a := range b.Authors {
		if a.Name != "" {
			doc.Authors = append(doc.Authors, a.Name)
		}
	}
	for _, s := range b.Series {
		if s.Name != "" {
			doc.Series = append(doc.Series, SeriesEntry{s.Name, db.Val(s.Sequence)})
		}
	}
	return doc
}

func podcastDoc(p *item.Podcast) podcastJSON {
	return podcastJSON{Tags: orEmpty(p.Tags.V), Title: p.Title, Author: p.Author, Description: p.Description, ReleaseDate: p.ReleaseDate, Genres: orEmpty(p.Genres.V),
		FeedURL: p.FeedURL, ImageURL: p.ImageURL, ItunesPageURL: p.ItunesPageURL, ItunesID: p.ItunesID, ItunesArtistID: p.ItunesArtistID, Language: p.Language,
		Explicit: bool(p.Explicit), PodcastType: p.PodcastType}
}

// WriteMetadata writes the item's metadata.json: beside the item when
// storeMetadataWithItem is on (a podcast or a book folder), otherwise under
// the metadata directory. v2.37.2 reads the file back.
func (s *Scanner) WriteMetadata(ctx context.Context, itemID string) error {
	it, err := item.Get(ctx, s.db, itemID, item.Options{})
	if err != nil {
		return fmt.Errorf("metadata item %q: %w", itemID, err)
	}
	withItem := s.settings.Server().StoreMetadataWithItem && (it.Podcast != nil || !bool(it.IsFile))
	dir := filepath.Join(s.cfg.Dirs().Items, itemID)
	if withItem {
		dir = db.Val(it.Path)
	} else if s.cfg.MetadataPath == "" {
		return errors.New("metadata path is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	path := filepath.Join(dir, "metadata.json")
	var doc any
	if it.Podcast != nil {
		doc = podcastDoc(it.Podcast)
	} else {
		doc = bookJSON(it.Book)
	}
	if err := writeJSONFile(path, doc); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}
	if withItem {
		return s.recordSidecar(ctx, itemID, path)
	}
	return nil
}

// recordSidecar lists the metadata.json written into the item folder among the
// item's library files.
func (s *Scanner) recordSidecar(ctx context.Context, itemID, path string) error {
	var row db.LibraryItem
	if err := s.db.Get(ctx, &row, `SELECT * FROM libraryItems WHERE id = ?`, itemID); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir, err := os.Stat(db.Val(row.Path))
	if err != nil {
		return err
	}
	entry := newLibraryFile(fileEntry{path: path, rel: "metadata.json", info: info})
	entry.FileType = filekind.Metadata
	if i := slices.IndexFunc(row.LibraryFiles.V, func(f db.LibraryFile) bool { return f.Metadata.Path == path }); i >= 0 {
		row.LibraryFiles.V[i] = entry
	} else {
		row.LibraryFiles.V = append(row.LibraryFiles.V, entry)
	}
	var size int64
	for _, f := range row.LibraryFiles.V {
		size += f.Metadata.Size
	}
	mtime, ctime, birthtime := statTimes(db.Val(row.Path), dir)
	row.Size, row.Mtime, row.Ctime, row.Birthtime, row.UpdatedAt = &size, db.Time{Time: mtime.UTC()}, db.Time{Time: ctime.UTC()}, db.Time{Time: birthtime.UTC()}, db.Now()
	return s.db.Update(ctx, "libraryItems", &row, "libraryFiles", "size", "mtime", "ctime", "birthtime", "updatedAt")
}
