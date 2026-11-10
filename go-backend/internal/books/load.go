package books

import (
	"context"
	"database/sql"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/store"
)

// LoadMany reads books and their ordered authors/series in a fixed number of
// queries, chunked by id. Missing ids are omitted.
func LoadMany(ctx context.Context, db *sql.DB, ids []string) (map[string]*Book, error) {
	result := make(map[string]*Book, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	err := store.QueryIn(ctx, db, `SELECT id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,updatedAt FROM books WHERE id IN `, ``, ids, func(rows *sql.Rows) error {
		var b Book
		var id, title, subtitle, year, date, publisher, description, isbn, asin, language, cover sql.NullString
		var explicit, abridged sql.NullInt64
		var duration, updated any
		var narrators, audio, ebook, chapters, tags, genres []byte
		if err := rows.Scan(&id, &title, &subtitle, &year, &date, &publisher, &description, &isbn, &asin, &language, &explicit, &abridged, &cover, &duration, &narrators, &audio, &ebook, &chapters, &tags, &genres, &updated); err != nil {
			return err
		}
		var err error
		if b.UpdatedAt, err = dbtime.OptionalMillis(updated); err != nil {
			return err
		}
		b.ID, b.Title, b.Subtitle, b.PublishedYear, b.PublishedDate, b.Publisher = id.String, store.NullStringPtr(title), store.NullStringPtr(subtitle), store.NullStringPtr(year), store.NullStringPtr(date), store.NullStringPtr(publisher)
		b.Description, b.ISBN, b.ASIN, b.Language, b.CoverPath = store.NullStringPtr(description), store.NullStringPtr(isbn), store.NullStringPtr(asin), store.NullStringPtr(language), store.NullStringPtr(cover)
		b.Explicit, b.Abridged = explicit.Int64 != 0, abridged.Int64 != 0
		if n, ok := jsonvalue.Number(duration); ok {
			b.Duration = &n
		}
		b.Narrators, b.Genres, b.Tags = store.DecodeJSONSlice[string](narrators), store.DecodeJSONSlice[string](genres), store.DecodeJSONSlice[string](tags)
		b.AudioFiles, b.Chapters = store.DecodeJSONSlice[mediafiles.File](audio), store.DecodeJSONSlice[map[string]any](chapters)
		if b.Ebook = mediafiles.File(store.DecodeJSONObject(ebook)); len(b.Ebook) == 0 {
			b.Ebook = nil
		}
		b.Authors, b.Series = []Author{}, []SeriesRef{}
		result[b.ID] = &b
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = store.QueryIn(ctx, db, `SELECT ba.bookId,a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId IN `, ` ORDER BY ba.bookId,ba.createdAt ASC,ba.rowid`, ids, func(rows *sql.Rows) error {
		var bookID string
		var a Author
		var name sql.NullString
		if err := rows.Scan(&bookID, &a.ID, &name); err != nil {
			return err
		}
		a.Name = name.String
		if b := result[bookID]; b != nil {
			b.Authors = append(b.Authors, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = store.QueryIn(ctx, db, `SELECT bs.bookId,s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId IN `, ` ORDER BY bs.bookId,bs.createdAt ASC,bs.rowid`, ids, func(rows *sql.Rows) error {
		var bookID string
		var s SeriesRef
		var name, sequence sql.NullString
		if err := rows.Scan(&bookID, &s.ID, &name, &sequence); err != nil {
			return err
		}
		s.Name, s.Sequence = name.String, store.NullStringPtr(sequence)
		if b := result[bookID]; b != nil {
			b.Series = append(b.Series, s)
		}
		return nil
	})
	return result, err
}
