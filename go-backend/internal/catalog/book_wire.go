package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"audiobookshelf-compat/internal/wire"
)

type bookRow struct {
	id, title, subtitle, year, publishedDate, publisher sql.NullString
	description, isbn, asin, language, coverPath        sql.NullString
	explicit, abridged                                  int64
	duration                                            any
	narrators, audio, ebook, chapters                   []byte
	tags, genres                                        []byte
}

type bookSource struct {
	row       bookRow
	authors   []wire.BookAuthor
	series    []wire.BookSeries
	narrators []string
	genres    []string
	tags      []string
}

func (h *Handler) loadBookRow(ctx context.Context, bookID string) (bookRow, error) {
	var row bookRow
	err := h.db.QueryRowContext(ctx, `SELECT id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres FROM books WHERE id=?`, bookID).
		Scan(&row.id, &row.title, &row.subtitle, &row.year, &row.publishedDate, &row.publisher, &row.description, &row.isbn, &row.asin, &row.language, &row.explicit, &row.abridged, &row.coverPath, &row.duration, &row.narrators, &row.audio, &row.ebook, &row.chapters, &row.tags, &row.genres)
	return row, err
}

func (h *Handler) loadBookSource(ctx context.Context, bookID string) (bookSource, error) {
	row, err := h.loadBookRow(ctx, bookID)
	if err != nil {
		return bookSource{}, err
	}
	rawAuthors, rawSeries, err := h.bookRelations(ctx, bookID)
	if err != nil {
		return bookSource{}, err
	}
	authors := make([]wire.BookAuthor, 0, len(rawAuthors))
	for _, author := range rawAuthors {
		authors = append(authors, wire.BookAuthor{
			ID:   stringValue(author["id"]),
			Name: stringValue(author["name"]),
		})
	}
	series := make([]wire.BookSeries, 0, len(rawSeries))
	for _, entry := range rawSeries {
		var sequence *string
		if value, ok := entry["sequence"].(string); ok {
			sequence = &value
		}
		series = append(series, wire.BookSeries{
			ID:       stringValue(entry["id"]),
			Name:     stringValue(entry["name"]),
			Sequence: sequence,
		})
	}
	return bookSource{
		row:       row,
		authors:   authors,
		series:    series,
		narrators: decodeStrings(row.narrators),
		genres:    decodeStrings(row.genres),
		tags:      decodeStrings(row.tags),
	}, nil
}

// bookFull builds the legacy non-expanded book media shape.
func (h *Handler) bookFull(ctx context.Context, bookID, itemID string) (wire.BookFull, error) {
	source, err := h.loadBookSource(ctx, bookID)
	if err != nil {
		return wire.BookFull{}, err
	}
	audio, chapters := decodeArray(source.row.audio), decodeArray(source.row.chapters)
	audioJSON, err := encodeJSON(audio)
	if err != nil {
		return wire.BookFull{}, err
	}
	chaptersJSON, err := encodeJSON(chapters)
	if err != nil {
		return wire.BookFull{}, err
	}
	ebookJSON, err := encodeJSON(bookEbook(source.row.ebook))
	if err != nil {
		return wire.BookFull{}, err
	}
	return wire.BookFull{
		ID:            source.row.id.String,
		LibraryItemID: itemID,
		Metadata:      fullMetadata(source),
		CoverPath:     nullableStringPointer(source.row.coverPath),
		Tags:          source.tags,
		AudioFiles:    audioJSON,
		Chapters:      chaptersJSON,
		EbookFile:     ebookJSON,
	}, nil
}

// bookMinified builds the shelf/collection book media shape.
func (h *Handler) bookMinified(ctx context.Context, bookID, itemID string) (wire.BookMinified, error) {
	source, err := h.loadBookSource(ctx, bookID)
	if err != nil {
		return wire.BookMinified{}, err
	}
	audio, chapters := decodeArray(source.row.audio), decodeArray(source.row.chapters)
	ebook := bookEbook(source.row.ebook)
	var ebookFormat json.RawMessage
	if format, ok := ebook["ebookFormat"]; ok {
		ebookFormat, err = encodeJSON(format)
		if err != nil {
			return wire.BookMinified{}, err
		}
	}
	return wire.BookMinified{
		ID:            source.row.id.String,
		Metadata:      minifiedMetadata(source, h.bookTitlePrefix(ctx, source.row.title)),
		CoverPath:     nullableStringPointer(source.row.coverPath),
		Tags:          source.tags,
		NumTracks:     includedAudioCount(audio),
		NumAudioFiles: len(audio),
		NumChapters:   len(chapters),
		Duration:      durationPointer(source.row.duration),
		Size:          audioSize(audio, ebook),
		EbookFormat:   ebookFormat,
	}, nil
}

// bookExpanded builds the item-detail/socket book media shape.
func (h *Handler) bookExpanded(ctx context.Context, bookID, itemID string) (wire.BookExpanded, error) {
	source, err := h.loadBookSource(ctx, bookID)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	audio, chapters := decodeArray(source.row.audio), decodeArray(source.row.chapters)
	ebook := bookEbook(source.row.ebook)
	var ebookFormat json.RawMessage
	if format, ok := ebook["ebookFormat"]; ok {
		ebookFormat, err = encodeJSON(format)
		if err != nil {
			return wire.BookExpanded{}, err
		}
	}
	audioJSON, err := encodeJSON(audio)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	chaptersJSON, err := encodeJSON(chapters)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	ebookJSON, err := encodeJSON(ebook)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	tracksJSON, err := encodeJSON(makeTracks(itemID, audio))
	if err != nil {
		return wire.BookExpanded{}, err
	}
	return wire.BookExpanded{
		ID:            source.row.id.String,
		Metadata:      expandedMetadata(source, h.bookTitlePrefix(ctx, source.row.title)),
		CoverPath:     nullableStringPointer(source.row.coverPath),
		Tags:          source.tags,
		NumTracks:     includedAudioCount(audio),
		NumAudioFiles: len(audio),
		NumChapters:   len(chapters),
		Duration:      durationPointer(source.row.duration),
		Size:          audioSize(audio, ebook),
		EbookFormat:   ebookFormat,
		LibraryItemID: itemID,
		AudioFiles:    audioJSON,
		Chapters:      chaptersJSON,
		EbookFile:     ebookJSON,
		Tracks:        tracksJSON,
	}, nil
}

func fullMetadata(source bookSource) wire.BookMetadataFull {
	return wire.BookMetadataFull{
		Title:         nullableStringPointer(source.row.title),
		Subtitle:      nullableStringPointer(source.row.subtitle),
		Authors:       source.authors,
		Narrators:     source.narrators,
		Series:        source.series,
		Genres:        source.genres,
		PublishedYear: nullableStringPointer(source.row.year),
		PublishedDate: nullableStringPointer(source.row.publishedDate),
		Publisher:     nullableStringPointer(source.row.publisher),
		Description:   nullableStringPointer(source.row.description),
		ISBN:          nullableStringPointer(source.row.isbn),
		ASIN:          nullableStringPointer(source.row.asin),
		Language:      nullableStringPointer(source.row.language),
		Explicit:      source.row.explicit != 0,
		Abridged:      source.row.abridged != 0,
	}
}

func minifiedMetadata(source bookSource, titlePrefix *string) wire.BookMetadataMinified {
	return wire.BookMetadataMinified{
		Title:             nullableStringPointer(source.row.title),
		TitleIgnorePrefix: titlePrefix,
		Subtitle:          nullableStringPointer(source.row.subtitle),
		AuthorName:        authorNames(source.authors, false),
		AuthorNameLF:      authorNames(source.authors, true),
		NarratorName:      strings.Join(source.narrators, ", "),
		SeriesName:        seriesName(source.series),
		Genres:            source.genres,
		PublishedYear:     nullableStringPointer(source.row.year),
		PublishedDate:     nullableStringPointer(source.row.publishedDate),
		Publisher:         nullableStringPointer(source.row.publisher),
		Description:       nullableStringPointer(source.row.description),
		ISBN:              nullableStringPointer(source.row.isbn),
		ASIN:              nullableStringPointer(source.row.asin),
		Language:          nullableStringPointer(source.row.language),
		Explicit:          source.row.explicit != 0,
		Abridged:          source.row.abridged != 0,
	}
}

func expandedMetadata(source bookSource, titlePrefix *string) wire.BookMetadataExpanded {
	return wire.BookMetadataExpanded{
		BookMetadataFull:  fullMetadata(source),
		TitleIgnorePrefix: titlePrefix,
		AuthorName:        authorNames(source.authors, false),
		AuthorNameLF:      authorNames(source.authors, true),
		NarratorName:      strings.Join(source.narrators, ", "),
		SeriesName:        seriesName(source.series),
		DescriptionPlain:  descriptionPlain(source.row.description),
	}
}

func authorNames(authors []wire.BookAuthor, lastFirstOrder bool) string {
	names := make([]string, 0, len(authors))
	for _, author := range authors {
		if lastFirstOrder {
			names = append(names, lastFirst(author.Name))
		} else {
			names = append(names, author.Name)
		}
	}
	return strings.Join(names, ", ")
}

func seriesName(series []wire.BookSeries) string {
	names := make([]string, 0, len(series))
	for _, entry := range series {
		name := entry.Name
		if entry.Sequence != nil && *entry.Sequence != "" {
			name += " #" + *entry.Sequence
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func descriptionPlain(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	plain := stripHTML(value.String)
	return &plain
}

func (h *Handler) bookTitlePrefix(ctx context.Context, title sql.NullString) *string {
	if !title.Valid {
		return nil
	}
	prefix := h.titlePrefix(ctx, title.String)
	return &prefix
}

func durationPointer(value any) *float64 {
	switch number := value.(type) {
	case float64:
		return &number
	case int64:
		converted := float64(number)
		return &converted
	case int:
		converted := float64(number)
		return &converted
	default:
		return nil
	}
}

func bookEbook(raw []byte) map[string]any {
	ebook := decodeObject(raw)
	if len(ebook) == 0 || string(raw) == "null" {
		return nil
	}
	return ebook
}

func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableStringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
