package item

import (
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
)

// BookMetadataFull is the metadata of the full and expanded book views.
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

// BookMetadataMinified is the metadata of the minified book view.
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
	// pages and author series groups; each shape is the caller's own, so it is
	// open (any) and omitted when unset.
	Series any `json:"series,omitempty"`
}

// BookMetadataExpanded is the metadata of the expanded book view.
type BookMetadataExpanded struct {
	BookMetadataFull
	TitleIgnorePrefix *string `json:"titleIgnorePrefix"`
	AuthorName        string  `json:"authorName"`
	AuthorNameLF      string  `json:"authorNameLF"`
	NarratorName      string  `json:"narratorName"`
	SeriesName        string  `json:"seriesName"`
	DescriptionPlain  *string `json:"descriptionPlain"`
}

// BookFull is Book.toOldJSON. EbookFile is null without an ebook.
type BookFull struct {
	ID            string           `json:"id"`
	LibraryItemID string           `json:"libraryItemId"`
	Metadata      BookMetadataFull `json:"metadata"`
	CoverPath     *string          `json:"coverPath"`
	Tags          []string         `json:"tags"`
	AudioFiles    []db.AudioFile   `json:"audioFiles"`
	Chapters      []db.Chapter     `json:"chapters"`
	EbookFile     *db.EbookFile    `json:"ebookFile"`
}

// BookCounts are the derived numbers of the minified and expanded views.
type BookCounts struct {
	NumTracks     int      `json:"numTracks"`
	NumAudioFiles int      `json:"numAudioFiles"`
	NumChapters   int      `json:"numChapters"`
	Duration      *float64 `json:"duration"`
	Size          int64    `json:"size"`
	// EbookFormat is present exactly when the book has an ebook file.
	EbookFormat *string `json:"ebookFormat,omitempty"`
}

// BookMinified is Book.toOldJSONMinified.
type BookMinified struct {
	ID        string               `json:"id"`
	Metadata  BookMetadataMinified `json:"metadata"`
	CoverPath *string              `json:"coverPath"`
	Tags      []string             `json:"tags"`
	BookCounts
}

// BookExpanded is Book.toOldJSONExpanded.
type BookExpanded struct {
	ID        string               `json:"id"`
	Metadata  BookMetadataExpanded `json:"metadata"`
	CoverPath *string              `json:"coverPath"`
	Tags      []string             `json:"tags"`
	BookCounts
	LibraryItemID string         `json:"libraryItemId"`
	AudioFiles    []db.AudioFile `json:"audioFiles"`
	Chapters      []db.Chapter   `json:"chapters"`
	EbookFile     *db.EbookFile  `json:"ebookFile"`
	Tracks        []AudioTrack   `json:"tracks"`
}

// AudioTrack is a playable audio file: the stored file plus its title (the
// file name), running offset and content URL. Index shadows the file's own
// index so an episode's track can report 1.
type AudioTrack struct {
	db.AudioFile
	Index       int64   `json:"index"`
	Title       string  `json:"title"`
	StartOffset float64 `json:"startOffset"`
	ContentURL  string  `json:"contentUrl"`
}

// MediaID implements Media.
func (b BookFull) MediaID() string { return b.ID }

// Flags implements Media.
func (b BookFull) Flags() (bool, []string) { return b.Metadata.Explicit, b.Tags }

// MediaID implements Media.
func (b BookMinified) MediaID() string { return b.ID }

// Flags implements Media.
func (b BookMinified) Flags() (bool, []string) { return b.Metadata.Explicit, b.Tags }

// MediaID implements Media.
func (b BookExpanded) MediaID() string { return b.ID }

// Flags implements Media.
func (b BookExpanded) Flags() (bool, []string) { return b.Metadata.Explicit, b.Tags }

// AuthorName is the authors joined with ", " in book order.
func (b *Book) AuthorName() string { return b.authorNames(false) }

func (b *Book) authorNames(lastFirst bool) string {
	names := make([]string, len(b.Authors))
	for i, author := range b.Authors {
		names[i] = author.Name
		if lastFirst {
			names[i] = nameparse.ToLastFirst(author.Name)
		}
	}
	return strings.Join(names, ", ")
}

func (b *Book) seriesName() string {
	names := make([]string, len(b.Series))
	for i, series := range b.Series {
		names[i] = series.Name
		if series.Sequence != nil && *series.Sequence != "" {
			names[i] += " #" + *series.Sequence
		}
	}
	return strings.Join(names, ", ")
}

func (b *Book) metadataFull() BookMetadataFull {
	return BookMetadataFull{Title: b.Title, Subtitle: b.Subtitle, Authors: b.Authors, Narrators: b.Narrators.V, Series: b.Series, Genres: b.Genres.V,
		PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher, Description: b.Description, ISBN: b.ISBN, ASIN: b.ASIN,
		Language: b.Language, Explicit: bool(b.Explicit), Abridged: bool(b.Abridged)}
}

func (b *Book) titleIgnorePrefix(prefixes []string) *string {
	if b.Title == nil {
		return nil
	}
	return db.Ptr(textsort.PrefixAtEnd(*b.Title, prefixes))
}

// counts is the one place the derived numbers are computed: tracks are the
// non-excluded audio files, size adds audio and ebook sizes.
func (b *Book) counts() BookCounts {
	c := BookCounts{NumAudioFiles: len(b.AudioFiles.V), NumChapters: len(b.Chapters.V), Duration: b.Duration}
	for _, file := range b.AudioFiles.V {
		c.Size += file.Metadata.Size
		if !file.Exclude {
			c.NumTracks++
		}
	}
	if ebook := b.EbookFile.V; ebook != nil {
		c.Size += ebook.Metadata.Size
		c.EbookFormat = &ebook.EbookFormat
	}
	return c
}

// Full is the full media view (Book.toOldJSON) of the book of item itemID.
func (b *Book) Full(itemID string) BookFull {
	return BookFull{ID: b.ID, LibraryItemID: itemID, Metadata: b.metadataFull(), CoverPath: b.CoverPath, Tags: b.Tags.V,
		AudioFiles: b.AudioFiles.V, Chapters: b.Chapters.V, EbookFile: b.EbookFile.V}
}

// Minified is the shelf and list media view (Book.toOldJSONMinified).
func (b *Book) Minified(prefixes []string) BookMinified {
	return BookMinified{ID: b.ID, CoverPath: b.CoverPath, Tags: b.Tags.V, BookCounts: b.counts(),
		Metadata: BookMetadataMinified{Title: b.Title, TitleIgnorePrefix: b.titleIgnorePrefix(prefixes), Subtitle: b.Subtitle,
			AuthorName: b.authorNames(false), AuthorNameLF: b.authorNames(true), NarratorName: strings.Join(b.Narrators.V, ", "), SeriesName: b.seriesName(),
			Genres: b.Genres.V, PublishedYear: b.PublishedYear, PublishedDate: b.PublishedDate, Publisher: b.Publisher, Description: b.Description,
			ISBN: b.ISBN, ASIN: b.ASIN, Language: b.Language, Explicit: bool(b.Explicit), Abridged: bool(b.Abridged)}}
}

// Expanded is the expanded media view (Book.toOldJSONExpanded).
func (b *Book) Expanded(itemID string, prefixes []string) BookExpanded {
	var plain *string
	if b.Description != nil && *b.Description != "" {
		plain = db.Ptr(textclean.Strip(*b.Description))
	}
	return BookExpanded{ID: b.ID, CoverPath: b.CoverPath, Tags: b.Tags.V, BookCounts: b.counts(), LibraryItemID: itemID,
		AudioFiles: b.AudioFiles.V, Chapters: b.Chapters.V, EbookFile: b.EbookFile.V, Tracks: b.tracks(itemID),
		Metadata: BookMetadataExpanded{BookMetadataFull: b.metadataFull(), TitleIgnorePrefix: b.titleIgnorePrefix(prefixes),
			AuthorName: b.authorNames(false), AuthorNameLF: b.authorNames(true), NarratorName: strings.Join(b.Narrators.V, ", "),
			SeriesName: b.seriesName(), DescriptionPlain: plain}}
}

// tracks are the non-excluded audio files with running start offsets.
func (b *Book) tracks(itemID string) []AudioTrack {
	tracks := make([]AudioTrack, 0, len(b.AudioFiles.V))
	var offset float64
	for _, file := range b.AudioFiles.V {
		if file.Exclude {
			continue
		}
		tracks = append(tracks, newTrack(itemID, file, file.Index, offset))
		offset += db.Val(file.Duration)
	}
	return tracks
}

func newTrack(itemID string, file db.AudioFile, index int64, offset float64) AudioTrack {
	return AudioTrack{AudioFile: file, Index: index, Title: file.Metadata.Filename, StartOffset: offset,
		ContentURL: "/api/items/" + itemID + "/file/" + url.PathEscape(file.Ino)}
}
