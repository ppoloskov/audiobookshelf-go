package books

import (
	"database/sql"
	"strings"

	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/wire"
)

func (b Book) fullMetadata() wire.BookMetadataFull {
	return wire.BookMetadataFull{
		Title:         store.NullStringPtr(b.row.title),
		Subtitle:      store.NullStringPtr(b.row.subtitle),
		Authors:       b.authors,
		Narrators:     b.narrators,
		Series:        b.series,
		Genres:        b.genres,
		PublishedYear: store.NullStringPtr(b.row.year),
		PublishedDate: store.NullStringPtr(b.row.publishedDate),
		Publisher:     store.NullStringPtr(b.row.publisher),
		Description:   store.NullStringPtr(b.row.description),
		ISBN:          store.NullStringPtr(b.row.isbn),
		ASIN:          store.NullStringPtr(b.row.asin),
		Language:      store.NullStringPtr(b.row.language),
		Explicit:      b.row.explicit != 0,
		Abridged:      b.row.abridged != 0,
	}
}

func (b Book) minifiedMetadata(titlePrefix *string) wire.BookMetadataMinified {
	return wire.BookMetadataMinified{
		Title:             store.NullStringPtr(b.row.title),
		TitleIgnorePrefix: titlePrefix,
		Subtitle:          store.NullStringPtr(b.row.subtitle),
		AuthorName:        b.authorNames(false),
		AuthorNameLF:      b.authorNames(true),
		NarratorName:      strings.Join(b.narrators, ", "),
		SeriesName:        b.seriesName(),
		Genres:            b.genres,
		PublishedYear:     store.NullStringPtr(b.row.year),
		PublishedDate:     store.NullStringPtr(b.row.publishedDate),
		Publisher:         store.NullStringPtr(b.row.publisher),
		Description:       store.NullStringPtr(b.row.description),
		ISBN:              store.NullStringPtr(b.row.isbn),
		ASIN:              store.NullStringPtr(b.row.asin),
		Language:          store.NullStringPtr(b.row.language),
		Explicit:          b.row.explicit != 0,
		Abridged:          b.row.abridged != 0,
	}
}

func expandedMetadata(full wire.BookMetadataFull, minified wire.BookMetadataMinified, description sql.NullString) wire.BookMetadataExpanded {
	return wire.BookMetadataExpanded{
		BookMetadataFull:  full,
		TitleIgnorePrefix: minified.TitleIgnorePrefix,
		AuthorName:        minified.AuthorName,
		AuthorNameLF:      minified.AuthorNameLF,
		NarratorName:      minified.NarratorName,
		SeriesName:        minified.SeriesName,
		DescriptionPlain:  descriptionPlain(description),
	}
}

func (b Book) seriesName() string {
	names := make([]string, 0, len(b.series))
	for _, entry := range b.series {
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
	plain := textclean.Strip(value.String)
	return &plain
}

func (b Book) authorNames(lastFirst bool) string {
	names := make([]string, 0, len(b.authors))
	for _, author := range b.authors {
		name := author.Name
		if lastFirst {
			name = nameparse.ToLastFirst(name)
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
