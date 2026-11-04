package books

import (
	"context"
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

// Load loads a book and its ordered associations, closing rows between queries.
func Load(ctx context.Context, db *sql.DB, bookID string) (Book, error) {
	var row bookRow
	err := db.QueryRowContext(ctx, `SELECT id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres FROM books WHERE id=?`, bookID).
		Scan(&row.id, &row.title, &row.subtitle, &row.year, &row.publishedDate, &row.publisher, &row.description, &row.isbn, &row.asin, &row.language, &row.explicit, &row.abridged, &row.coverPath, &row.duration, &row.narrators, &row.audio, &row.ebook, &row.chapters, &row.tags, &row.genres)
	if err != nil {
		return Book{}, err
	}
	authors, series, err := loadRelations(ctx, db, bookID)
	if err != nil {
		return Book{}, err
	}
	audio := store.DecodeJSONSlice[map[string]any](row.audio)
	chapters := store.DecodeJSONSlice[map[string]any](row.chapters)
	ebook := store.DecodeJSONObject(row.ebook)
	if len(ebook) == 0 {
		ebook = nil
	}
	return Book{
		row: row, authors: authors, series: series, narrators: store.DecodeJSONSlice[string](row.narrators), genres: store.DecodeJSONSlice[string](row.genres), tags: store.DecodeJSONSlice[string](row.tags),
		audio: audio, chapters: chapters, ebook: ebook,
	}, nil
}

func loadRelations(ctx context.Context, db *sql.DB, bookID string) ([]wire.BookAuthor, []wire.BookSeries, error) {
	aRows, err := db.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt ASC`, bookID)
	if err != nil {
		return nil, nil, err
	}
	authors := make([]wire.BookAuthor, 0)
	for aRows.Next() {
		var author wire.BookAuthor
		if err := aRows.Scan(&author.ID, &author.Name); err != nil {
			aRows.Close()
			return nil, nil, err
		}
		authors = append(authors, author)
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return nil, nil, err
	}
	if err := aRows.Close(); err != nil {
		return nil, nil, err
	}

	sRows, err := db.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt ASC`, bookID)
	if err != nil {
		return nil, nil, err
	}
	series := make([]wire.BookSeries, 0)
	for sRows.Next() {
		var entry wire.BookSeries
		var sequence sql.NullString
		if err := sRows.Scan(&entry.ID, &entry.Name, &sequence); err != nil {
			sRows.Close()
			return nil, nil, err
		}
		entry.Sequence = store.NullStringPtr(sequence)
		series = append(series, entry)
	}
	if err := sRows.Err(); err != nil {
		sRows.Close()
		return nil, nil, err
	}
	if err := sRows.Close(); err != nil {
		return nil, nil, err
	}
	return authors, series, nil
}
