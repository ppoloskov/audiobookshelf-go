package books

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

const maxBookIDsPerBatch = 250

const bookColumns = `id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres`

// Load loads a book and its ordered associations, closing rows between queries.
func Load(ctx context.Context, db *sql.DB, bookID string) (Book, error) {
	books, err := LoadMany(ctx, db, []string{bookID})
	if err != nil {
		return Book{}, err
	}
	book, ok := books[bookID]
	if !ok {
		return Book{}, sql.ErrNoRows
	}
	return book, nil
}

// LoadMany loads books and ordered associations in chunks of at most 250 IDs.
// Rows are closed between the three queries for each chunk.
func LoadMany(ctx context.Context, db *sql.DB, bookIDs []string) (map[string]Book, error) {
	uniqueIDs := make([]string, 0, len(bookIDs))
	seen := make(map[string]struct{}, len(bookIDs))
	for _, id := range bookIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	result := make(map[string]Book)
	for ids := range slices.Chunk(uniqueIDs, maxBookIDsPerBatch) {
		books, err := loadBookRows(ctx, db, ids)
		if err != nil {
			return nil, err
		}
		authors, err := loadBookAuthors(ctx, db, ids)
		if err != nil {
			return nil, err
		}
		series, err := loadBookSeries(ctx, db, ids)
		if err != nil {
			return nil, err
		}
		for id, row := range books {
			result[id] = decodeBook(row, authors[id], series[id])
		}
	}
	return result, nil
}

func loadBookRows(ctx context.Context, db *sql.DB, ids []string) (map[string]bookRow, error) {
	query := `SELECT ` + bookColumns + ` FROM books WHERE id IN (` + placeholders(len(ids)) + `)`
	rows, err := db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	books := make(map[string]bookRow, len(ids))
	for rows.Next() {
		var row bookRow
		if err := rows.Scan(&row.id, &row.title, &row.subtitle, &row.year, &row.publishedDate, &row.publisher, &row.description, &row.isbn, &row.asin, &row.language, &row.explicit, &row.abridged, &row.coverPath, &row.duration, &row.narrators, &row.audio, &row.ebook, &row.chapters, &row.tags, &row.genres); err != nil {
			_ = rows.Close()
			return nil, err
		}
		books[row.id.String] = row
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return books, nil
}

func loadBookAuthors(ctx context.Context, db *sql.DB, ids []string) (map[string][]wire.BookAuthor, error) {
	query := `SELECT ba.bookId,a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId IN (` + placeholders(len(ids)) + `) ORDER BY ba.bookId,ba.createdAt ASC`
	rows, err := db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	authors := make(map[string][]wire.BookAuthor, len(ids))
	for _, id := range ids {
		authors[id] = make([]wire.BookAuthor, 0)
	}
	for rows.Next() {
		var bookID string
		var author wire.BookAuthor
		if err := rows.Scan(&bookID, &author.ID, &author.Name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		authors[bookID] = append(authors[bookID], author)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return authors, nil
}

func loadBookSeries(ctx context.Context, db *sql.DB, ids []string) (map[string][]wire.BookSeries, error) {
	query := `SELECT bs.bookId,s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId IN (` + placeholders(len(ids)) + `) ORDER BY bs.bookId,bs.createdAt ASC`
	rows, err := db.QueryContext(ctx, query, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	series := make(map[string][]wire.BookSeries, len(ids))
	for _, id := range ids {
		series[id] = make([]wire.BookSeries, 0)
	}
	for rows.Next() {
		var bookID string
		var entry wire.BookSeries
		var sequence sql.NullString
		if err := rows.Scan(&bookID, &entry.ID, &entry.Name, &sequence); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entry.Sequence = store.NullStringPtr(sequence)
		series[bookID] = append(series[bookID], entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return series, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
