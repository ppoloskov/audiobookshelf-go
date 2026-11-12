package item

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/media"
)

// chunkSize bounds the length of an IN list (SQLite limits bound variables).
const chunkSize = 500

// Options selects the optional associations Load reads.
type Options struct {
	// Episodes loads Podcast.Episodes (createdAt ascending). List pages leave
	// it unset.
	Episodes bool
	// EpisodeIDs, when set together with Episodes, limits the episodes loaded
	// to these ids (a shelf needs only the episodes it shows).
	EpisodeIDs []string
}

// Load reads items with their media in a fixed number of queries (items, books,
// authors, series, podcasts and, with opt.Episodes, episodes; each IN list
// chunked at 500). The result follows ids, duplicates included. Unknown ids
// and items whose media row is missing are omitted.
func Load(ctx context.Context, d *db.DB, ids []string, opt Options) ([]Item, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ids)))
	rows, err := selectIn[db.LibraryItem](ctx, d, `SELECT * FROM libraryItems WHERE id IN (?)`, "", unique)
	if err != nil {
		return nil, fmt.Errorf("load library items: %w", err)
	}
	byID := make(map[string]*Item, len(rows))
	var bookIDs, podcastIDs []string
	for _, row := range rows {
		normalizeItem(&row)
		byID[row.ID] = &Item{LibraryItem: row}
		switch db.Val(row.MediaType) {
		case "book":
			bookIDs = append(bookIDs, db.Val(row.MediaID))
		case "podcast":
			podcastIDs = append(podcastIDs, db.Val(row.MediaID))
		}
	}
	books, err := loadBooks(ctx, d, bookIDs)
	if err != nil {
		return nil, err
	}
	podcasts, err := loadPodcasts(ctx, d, podcastIDs, opt)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		found, ok := byID[id]
		if !ok {
			continue
		}
		it := *found
		switch db.Val(it.MediaType) {
		case "book":
			it.Book = books[db.Val(it.MediaID)]
		case "podcast":
			it.Podcast = podcasts[db.Val(it.MediaID)]
		}
		if it.Book != nil || it.Podcast != nil {
			out = append(out, it)
		}
	}
	return out, nil
}

// Get loads one item; it returns sql.ErrNoRows when the item or its media row
// is missing.
func Get(ctx context.Context, d *db.DB, id string, opt Options) (*Item, error) {
	items, err := Load(ctx, d, []string{id}, opt)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

// selectIn runs query (whose single placeholder is `IN (?)`) over ids in
// chunks of 500 and appends the rows; suffix is appended to the statement.
func selectIn[T any](ctx context.Context, d *db.DB, query, suffix string, ids []string) ([]T, error) {
	var out []T
	for chunk := range slices.Chunk(ids, chunkSize) {
		var rows []T
		if err := d.Select(ctx, &rows, query+suffix, chunk); err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// normalizeItem makes the item row's slices non-nil and derives each library
// file's fileType from its extension (LibraryFile.js:42-49).
func normalizeItem(row *db.LibraryItem) {
	if row.LibraryFiles.V == nil {
		row.LibraryFiles.V = []db.LibraryFile{}
	}
	for i := range row.LibraryFiles.V {
		file := &row.LibraryFiles.V[i]
		file.FileType = media.KindOf("." + strings.TrimPrefix(file.Metadata.Ext, "."))
	}
}

func loadBooks(ctx context.Context, d *db.DB, ids []string) (map[string]*Book, error) {
	books := make(map[string]*Book, len(ids))
	rows, err := selectIn[db.Book](ctx, d, `SELECT * FROM books WHERE id IN (?)`, "", ids)
	if err != nil {
		return nil, fmt.Errorf("load books: %w", err)
	}
	for _, row := range rows {
		normalizeBook(&row)
		books[row.ID] = &Book{Book: row, Authors: []Author{}, Series: []SeriesRef{}}
	}
	authors, err := selectIn[struct {
		BookID string  `db:"bookId"`
		ID     string  `db:"id"`
		Name   *string `db:"name"`
	}](ctx, d, `SELECT ba.bookId AS bookId, a.id AS id, a.name AS name FROM authors a JOIN bookAuthors ba ON ba.authorId = a.id WHERE ba.bookId IN (?)`,
		` ORDER BY ba.bookId, ba.createdAt ASC, ba.rowid`, slices.Collect(maps.Keys(books)))
	if err != nil {
		return nil, fmt.Errorf("load book authors: %w", err)
	}
	for _, row := range authors {
		books[row.BookID].Authors = append(books[row.BookID].Authors, Author{ID: row.ID, Name: db.Val(row.Name)})
	}
	series, err := selectIn[struct {
		BookID   string  `db:"bookId"`
		ID       string  `db:"id"`
		Name     *string `db:"name"`
		Sequence *string `db:"sequence"`
	}](ctx, d, `SELECT bs.bookId AS bookId, s.id AS id, s.name AS name, bs.sequence AS sequence FROM series s JOIN bookSeries bs ON bs.seriesId = s.id WHERE bs.bookId IN (?)`,
		` ORDER BY bs.bookId, bs.createdAt ASC, bs.rowid`, slices.Collect(maps.Keys(books)))
	if err != nil {
		return nil, fmt.Errorf("load book series: %w", err)
	}
	for _, row := range series {
		books[row.BookID].Series = append(books[row.BookID].Series, SeriesRef{ID: row.ID, Name: db.Val(row.Name), Sequence: row.Sequence})
	}
	return books, nil
}

func normalizeBook(row *db.Book) {
	if row.Narrators.V == nil {
		row.Narrators.V = []string{}
	}
	if row.Tags.V == nil {
		row.Tags.V = []string{}
	}
	if row.Genres.V == nil {
		row.Genres.V = []string{}
	}
	if row.Chapters.V == nil {
		row.Chapters.V = []db.Chapter{}
	}
	if row.AudioFiles.V == nil {
		row.AudioFiles.V = []db.AudioFile{}
	}
	for i := range row.AudioFiles.V {
		normalizeAudio(&row.AudioFiles.V[i])
	}
}

func normalizeAudio(file *db.AudioFile) {
	if file.Chapters == nil {
		file.Chapters = []db.Chapter{}
	}
}

func loadPodcasts(ctx context.Context, d *db.DB, ids []string, opt Options) (map[string]*Podcast, error) {
	podcasts := make(map[string]*Podcast, len(ids))
	rows, err := selectIn[db.Podcast](ctx, d, `SELECT * FROM podcasts WHERE id IN (?)`, "", ids)
	if err != nil {
		return nil, fmt.Errorf("load podcasts: %w", err)
	}
	for _, row := range rows {
		if row.Tags.V == nil {
			row.Tags.V = []string{}
		}
		if row.Genres.V == nil {
			row.Genres.V = []string{}
		}
		podcasts[row.ID] = &Podcast{Podcast: row}
		if opt.Episodes {
			podcasts[row.ID].Episodes = []Episode{}
		}
	}
	if !opt.Episodes || len(podcasts) == 0 {
		return podcasts, nil
	}
	where, keyList := `podcastId`, slices.Collect(maps.Keys(podcasts))
	if opt.EpisodeIDs != nil {
		where, keyList = `id`, opt.EpisodeIDs
	}
	episodes, err := selectIn[db.PodcastEpisode](ctx, d, `SELECT * FROM podcastEpisodes WHERE `+where+` IN (?)`, ` ORDER BY podcastId, createdAt ASC, rowid`, keyList)
	if err != nil {
		return nil, fmt.Errorf("load podcast episodes: %w", err)
	}
	for _, row := range episodes {
		if row.Chapters.V == nil {
			row.Chapters.V = []db.Chapter{}
		}
		normalizeAudio(&row.AudioFile.V)
		if p := podcasts[db.Val(row.PodcastID)]; p != nil {
			p.Episodes = append(p.Episodes, Episode{row})
		}
	}
	return podcasts, nil
}
