package books

import (
	"database/sql"

	"audiobookshelf-compat/internal/store"
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

// Book is one persisted book and its ordered author and series associations.
// Its persisted representation stays private so callers use its projections.
type Book struct {
	row       bookRow
	authors   []wire.BookAuthor
	series    []wire.BookSeries
	narrators []string
	genres    []string
	tags      []string
	audio     []map[string]any
	chapters  []map[string]any
	ebook     map[string]any
}

// Title returns the nullable title used by catalog's title-prefix policy.
func (b Book) Title() *string { return store.NullStringPtr(b.row.title) }

func decodeBook(row bookRow, authors []wire.BookAuthor, series []wire.BookSeries) Book {
	audio := store.DecodeJSONSlice[map[string]any](row.audio)
	chapters := store.DecodeJSONSlice[map[string]any](row.chapters)
	ebook := store.DecodeJSONObject(row.ebook)
	if len(ebook) == 0 {
		ebook = nil
	}
	return Book{
		row: row, authors: authors, series: series, narrators: store.DecodeJSONSlice[string](row.narrators), genres: store.DecodeJSONSlice[string](row.genres), tags: store.DecodeJSONSlice[string](row.tags),
		audio: audio, chapters: chapters, ebook: ebook,
	}
}
