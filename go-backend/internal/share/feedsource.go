package share

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/item"
)

// entityKind is the feeds.entityType a feed is attached to.
type entityKind string

const (
	itemFeed       entityKind = "libraryItem"
	collectionFeed entityKind = "collection"
	seriesFeed     entityKind = "series"
)

// errNoEntity reports that the library item, collection or series of a feed
// does not exist.
var errNoEntity = errors.New("feed entity not found")

// source is what a feed is built from: the feed-level fields and the episodes
// of a library item, collection or series (Feed.getFeedObjFor*,
// FeedEpisode.createFrom*). Nullable fields stay nil so a missing value is
// NULL in the feed row.
type source struct {
	libraryID   string
	updatedAt   db.Time // becomes feeds.entityUpdatedAt
	siteURL     string
	title       string
	description *string
	author      *string
	language    *string
	coverPath   *string
	podcastType string
	explicit    bool
	episodes    []episodeSource
	trackCount  int // collections and series: included audio files over all books, to spot a stale feed
}

// episodeSource is an episode before it has an id: a feedEpisodes row without
// its id, enclosure URL, author, link and times.
type episodeSource struct {
	db.FeedEpisode
	ext string // extension of the audio file name; ends the enclosure URL
}

// loadSource reads the source of an entity; errNoEntity when it is gone.
func (h *Handler) loadSource(ctx context.Context, kind entityKind, id string) (*source, error) {
	switch kind {
	case itemFeed:
		return h.itemSource(ctx, id)
	case collectionFeed, seriesFeed:
		return h.aggregateSource(ctx, kind, id)
	}
	return nil, errNoEntity
}

func later(a, b db.Time) db.Time {
	if b.After(a.Time) {
		return b
	}
	return a
}

// itemSource is Feed.getFeedObjForLibraryItem: a book is one episode per
// audio file, a podcast one per episode.
func (h *Handler) itemSource(ctx context.Context, id string) (*source, error) {
	it, err := item.Get(ctx, h.db, id, item.Options{Episodes: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoEntity
	}
	if err != nil {
		return nil, fmt.Errorf("load feed item: %w", err)
	}
	src := &source{libraryID: db.Val(it.LibraryID), siteURL: "/item/" + id, updatedAt: it.UpdatedAt}
	if p := it.Podcast; p != nil {
		podcastSource(p, src)
		return src, nil
	}
	book := it.Book
	src.updatedAt = later(src.updatedAt, book.UpdatedAt)
	src.title, src.description, src.language, src.coverPath = db.Val(book.Title), book.Description, book.Language, book.CoverPath
	src.explicit = bool(book.Explicit)
	src.author = db.Ptr(book.AuthorName())
	src.podcastType = "serial"
	src.episodes = bookEpisodes(*it, it.CreatedAt, 0)
	return src, nil
}

// podcastSource is Feed.getFeedObjForLibraryItem for a podcast and
// FeedEpisode.createFromPodcastEpisodes: episodes by publication date, newest
// first for an episodic podcast and oldest first otherwise.
func podcastSource(p *item.Podcast, src *source) {
	src.title, src.author, src.description, src.coverPath, src.language = db.Val(p.Title), p.Author, p.Description, p.CoverPath, p.Language
	src.podcastType = db.Val(p.PodcastType)
	src.explicit = bool(p.Explicit)
	for _, e := range p.Episodes {
		file := e.AudioFile.V
		src.updatedAt = later(src.updatedAt, e.UpdatedAt)
		src.episodes = append(src.episodes, episodeSource{ext: filepath.Ext(file.Metadata.Filename), FeedEpisode: db.FeedEpisode{
			Title: db.Ptr(db.Val(e.Title)), Description: e.Description, PubDate: e.PubDate, Season: e.Season, Episode: e.Episode,
			EpisodeType: e.EpisodeType, EnclosureType: nilIfEmpty(file.MimeType), EnclosureSize: db.Ptr(file.Metadata.Size),
			Duration: db.Ptr(db.Val(file.Duration)), FilePath: nilIfEmpty(file.Metadata.Path), Explicit: p.Explicit,
		}})
	}
	direction := 1
	if src.podcastType == "episodic" {
		direction = -1
	}
	slices.SortStableFunc(src.episodes, func(a, b episodeSource) int {
		return direction * cmp.Compare(millis(a.PubDate), millis(b.PubDate))
	})
}

func millis(pubDate *string) int64 {
	t, _ := parsePubDate(db.Val(pubDate))
	return t.UnixMilli()
}

// bookEpisodes is FeedEpisode.getFeedEpisodeObjFromAudiobookTrack for every
// included audio file of a book (server/models/FeedEpisode.js:130). Episodes
// are published one minute apart from pubStart, offset episodes late, so
// podcast apps keep the track order.
func bookEpisodes(it item.Item, pubStart db.Time, offset int) []episodeSource {
	book := it.Book
	var files []db.AudioFile
	for _, file := range book.AudioFiles.V {
		if !file.Exclude {
			files = append(files, file)
		}
	}
	// Chapters name the episodes when there is one per file, each starting
	// where its file starts.
	chapters := book.Chapters.V
	useChapters := len(chapters) == len(files)
	var start float64
	starts := make([]float64, len(files))
	for i, file := range files {
		starts[i] = start
		start += db.Val(file.Duration)
		useChapters = useChapters && math.Abs(chapters[i].Start-starts[i]) < 1
	}
	episodes := make([]episodeSource, len(files))
	for i, file := range files {
		title := baseName(file.Metadata.Filename)
		switch {
		case len(files) == 1:
			title = db.Val(book.Title)
		case useChapters && chapters[i].Title != "":
			title = chapters[i].Title
		}
		published := pubStart.Add(time.Duration(offset+i) * time.Minute)
		episodes[i] = episodeSource{ext: filepath.Ext(file.Metadata.Filename), FeedEpisode: db.FeedEpisode{
			Title: &title, Description: db.Ptr(db.Val(book.Description)), PubDate: db.Ptr(published.UTC().Format(gmtLayout)),
			EnclosureType: nilIfEmpty(file.MimeType), EnclosureSize: db.Ptr(file.Metadata.Size), Duration: db.Ptr(db.Val(file.Duration)),
			FilePath: nilIfEmpty(file.Metadata.Path), Explicit: book.Explicit,
		}}
	}
	return episodes
}

// baseName is the file name without its extension (the whole name when that is
// all there is).
func baseName(filename string) string {
	base := filepath.Base(filename)
	if ext := filepath.Ext(base); ext != base {
		return strings.TrimSuffix(base, ext)
	}
	return base
}

// aggregateSource is Feed.getFeedObjForCollection / getFeedObjForSeries: the
// episodes of every book of the collection or series, in its order. The feed
// borrows the cover and explicit flag of its books and lists up to three
// authors.
func (h *Handler) aggregateSource(ctx context.Context, kind entityKind, id string) (*source, error) {
	var head struct {
		Name        *string `db:"name"`
		Description *string `db:"description"`
		UpdatedAt   db.Time `db:"updatedAt"`
	}
	headQuery, booksQuery := `SELECT name, description, updatedAt FROM collections WHERE id = ?`,
		`SELECT li.id FROM collectionBooks cb JOIN books b ON b.id = cb.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
		WHERE cb.collectionId = ? ORDER BY cb."order"`
	if kind == seriesFeed {
		headQuery = `SELECT name, description, updatedAt FROM series WHERE id = ?`
		booksQuery = `SELECT li.id FROM bookSeries bs JOIN books b ON b.id = bs.bookId JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
			WHERE bs.seriesId = ? ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, bs.rowid ASC`
	}
	var itemIDs []string
	err := h.db.Get(ctx, &head, headQuery, id)
	if err == nil {
		err = h.db.Select(ctx, &itemIDs, booksQuery, id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoEntity
	}
	if err != nil {
		return nil, fmt.Errorf("load %s for feed: %w", kind, err)
	}
	items, err := item.Load(ctx, h.db, itemIDs, item.Options{})
	if err != nil {
		return nil, fmt.Errorf("load %s books for feed: %w", kind, err)
	}
	src := &source{
		title: db.Val(head.Name), description: db.Ptr(db.Val(head.Description)), podcastType: "serial",
		updatedAt: head.UpdatedAt,
	}
	var withAudio []item.Item // books that have at least one included audio file
	var authors []string
	for _, it := range items {
		tracks := len(bookEpisodes(it, db.Time{}, 0))
		src.trackCount += tracks
		if tracks == 0 {
			continue
		}
		withAudio = append(withAudio, it)
		src.updatedAt = later(src.updatedAt, later(it.UpdatedAt, it.Book.UpdatedAt))
		if src.coverPath == nil && it.Book.CoverPath != nil && *it.Book.CoverPath != "" {
			src.coverPath = it.Book.CoverPath
		}
		src.explicit = src.explicit || bool(it.Book.Explicit)
		for _, author := range it.Book.Authors {
			if !slices.Contains(authors, author.Name) {
				authors = append(authors, author.Name)
			}
		}
	}
	src.author = db.Ptr(strings.Join(authors[:min(3, len(authors))], ", "))
	if len(authors) > 3 {
		*src.author += " & more"
	}
	if len(withAudio) > 0 {
		earliest := withAudio[0].CreatedAt
		for _, it := range withAudio[1:] {
			if it.CreatedAt.Before(earliest.Time) {
				earliest = it.CreatedAt
			}
		}
		for _, it := range withAudio {
			src.episodes = append(src.episodes, bookEpisodes(it, earliest, len(src.episodes))...)
		}
		src.libraryID = db.Val(withAudio[0].LibraryID)
	}
	src.siteURL = "/collection/" + id
	if kind == seriesFeed {
		src.siteURL = "/library/" + src.libraryID + "/series/" + id
	}
	return src, nil
}
