// Package books owns the book media model and its stored-row/API projections.
package books

import (
	"encoding/json"
	"strings"

	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
)

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
	AudioFiles    []libitem.File
	Chapters      []map[string]any
	Ebook         libitem.File // nil when the book has no ebook file

	Authors []Author
	Series  []SeriesRef
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
	AudioFiles    []libitem.File   `json:"audioFiles"`
	Chapters      []map[string]any `json:"chapters"`
	EbookFile     libitem.File     `json:"ebookFile"`
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
	AudioFiles    []libitem.File       `json:"audioFiles"`
	Chapters      []map[string]any     `json:"chapters"`
	EbookFile     libitem.File         `json:"ebookFile"`
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
