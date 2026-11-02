package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

// feedSource is the feed-shaped view of a library item, collection or series:
// what Feed.getFeedObjForLibraryItem / getFeedObjForCollection /
// getFeedObjForSeries (server/models/Feed.js:112, 197, 285) read from the
// entity, plus the episodes FeedEpisode.createFrom* derive from it. Columns
// that upstream leaves NULL stay sql.NullString.
type feedSource struct {
	LibraryID       string // libraryItem only, for the access check
	EntityUpdatedAt time.Time
	SiteURL         string
	Title           string
	Description     sql.NullString
	Author          sql.NullString
	PodcastType     string
	Language        sql.NullString
	Explicit        bool
	CoverPath       sql.NullString
	Episodes        []feedEpisodeSource
	// BookTrackCount is the number of included audio files over all books of a
	// collection or series (RssFeedManager.checkFeedRequiresUpdate).
	BookTrackCount int
	// ChildrenUpdatedAt is the newest updatedAt over all books and their
	// library items, tracks or not (same method). OwnUpdatedAt is the
	// collection's or series' own updatedAt.
	ChildrenUpdatedAt time.Time
	OwnUpdatedAt      time.Time
}

type feedEpisodeSource struct {
	Title         string
	Description   sql.NullString
	EnclosureType sql.NullString
	EnclosureSize sql.NullInt64
	PubDate       sql.NullString
	Season        sql.NullString
	Episode       sql.NullString
	EpisodeType   sql.NullString
	Duration      sql.NullFloat64
	Filename      string
	FilePath      sql.NullString
	Explicit      bool
}

type audioFileJSON struct {
	Index    int         `json:"index"`
	Exclude  bool        `json:"exclude"`
	Duration float64     `json:"duration"`
	MimeType *string     `json:"mimeType"`
	Metadata audioFileMD `json:"metadata"`
}

type audioFileMD struct {
	Filename string       `json:"filename"`
	Path     *string      `json:"path"`
	Size     *json.Number `json:"size"`
}

type chapterJSON struct {
	Start float64 `json:"start"`
	Title string  `json:"title"`
}

var errFeedEntityMissing = errors.New("feed entity not found")

// bookData is a book with its library item and authors, the expanded shape
// Collection.getExpandedById and Series.getBooksExpandedWithLibraryItem load.
type bookData struct {
	ID          string
	Title       string
	Description sql.NullString
	CoverPath   sql.NullString
	Language    sql.NullString
	Explicit    bool
	Chapters    []chapterJSON
	Files       []audioFileJSON
	UpdatedAt   time.Time
	ItemID      string
	LibraryID   string
	ItemCreated time.Time
	ItemUpdated time.Time
	Authors     []string
}

func (b bookData) included() []audioFileJSON {
	out := make([]audioFileJSON, 0, len(b.Files))
	for _, f := range b.Files {
		if !f.Exclude {
			out = append(out, f)
		}
	}
	return out
}

func asTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		if t, ok := dbtime.Parse(x); ok {
			return t
		}
	case []byte:
		return asTime(string(x))
	}
	return time.Time{}
}

func sizeValue(n *json.Number) sql.NullInt64 {
	if n == nil {
		return sql.NullInt64{}
	}
	if i, err := n.Int64(); err == nil {
		return sql.NullInt64{Int64: i, Valid: true}
	}
	if f, err := n.Float64(); err == nil {
		return sql.NullInt64{Int64: int64(f), Valid: true}
	}
	return sql.NullInt64{}
}

// nodeExt is path.extname: "" for dotfiles without another dot.
func nodeExt(name string) string {
	base := filepath.Base(name)
	i := strings.LastIndex(base, ".")
	if i <= 0 || base == ".." {
		return ""
	}
	return base[i:]
}

// nodeBasename is path.basename(name, ext): the extension is kept when it is
// the whole name.
func nodeBasename(name, ext string) string {
	base := filepath.Base(name)
	if ext != "" && ext != base && strings.HasSuffix(base, ext) {
		return strings.TrimSuffix(base, ext)
	}
	return base
}

func (h *Handler) bookAuthors(ctx context.Context, bookID string) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId=? ORDER BY ba.createdAt ASC, ba.rowid ASC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name.String)
	}
	return out, rows.Err()
}

type bookRowScan struct {
	bookData
	files, chapters                   []byte
	explicit                          sql.NullInt64
	updated, itemCreated, itemUpdated any
}

func (s *bookRowScan) finish() {
	s.Explicit = s.explicit.Int64 != 0
	s.UpdatedAt = asTime(s.updated)
	s.ItemCreated = asTime(s.itemCreated)
	s.ItemUpdated = asTime(s.itemUpdated)
	_ = json.Unmarshal(s.files, &s.Files)
	_ = json.Unmarshal(s.chapters, &s.Chapters)
}

// bookEpisodes is FeedEpisode.getFeedEpisodeObjFromAudiobookTrack for every
// included track of one book (server/models/FeedEpisode.js:130-176). pubStart
// plus offset*1min is the pubDate, written in the server's local time with a
// literal GMT like upstream's date.format.
func bookEpisodes(b bookData, pubStart time.Time, offset int) []feedEpisodeSource {
	files := b.included()
	starts := make([]float64, len(files))
	total := 0.0
	for i, f := range files {
		starts[i] = total
		total += f.Duration
	}
	useChapterTitles := len(files) == len(b.Chapters)
	for i := 0; useChapterTitles && i < len(files); i++ {
		if math.Abs(b.Chapters[i].Start-starts[i]) >= 1 {
			useChapterTitles = false
		}
	}
	out := make([]feedEpisodeSource, 0, len(files))
	for i, f := range files {
		title := nodeBasename(f.Metadata.Filename, nodeExt(f.Metadata.Filename))
		if len(files) == 1 {
			title = b.Title
		} else if useChapterTitles {
			for _, ch := range b.Chapters {
				if math.Abs(ch.Start-starts[i]) < 1 {
					if ch.Title != "" {
						title = ch.Title
					}
					break
				}
			}
		}
		ep := feedEpisodeSource{
			Title:         title,
			Description:   sql.NullString{String: b.Description.String, Valid: true},
			EnclosureSize: sizeValue(f.Metadata.Size),
			PubDate:       sql.NullString{String: pubStart.Add(time.Duration(offset+i) * time.Minute).Local().Format("Mon, 02 Jan 2006 15:04:05 GMT"), Valid: true},
			Duration:      sql.NullFloat64{Float64: f.Duration, Valid: true},
			Filename:      f.Metadata.Filename,
			Explicit:      b.Explicit,
		}
		if f.MimeType != nil {
			ep.EnclosureType = sql.NullString{String: *f.MimeType, Valid: true}
		}
		if f.Metadata.Path != nil {
			ep.FilePath = sql.NullString{String: *f.Metadata.Path, Valid: true}
		}
		out = append(out, ep)
	}
	return out
}

func joinAuthors(names []string) string { return strings.Join(names, ", ") }

// loadItemSource reads a library item the way LibraryItem.getExpandedById and
// Feed.getFeedObjForLibraryItem do.
func (h *Handler) loadItemSource(ctx context.Context, id string) (*feedSource, error) {
	var libraryID, mediaID, mediaType sql.NullString
	var itemUpdated any
	var itemCreated any
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,mediaId,mediaType,updatedAt,createdAt FROM libraryItems WHERE id=?`, id).Scan(&libraryID, &mediaID, &mediaType, &itemUpdated, &itemCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errFeedEntityMissing
	}
	if err != nil {
		return nil, err
	}
	src := &feedSource{LibraryID: libraryID.String, SiteURL: "/item/" + id, EntityUpdatedAt: asTime(itemUpdated)}
	switch mediaType.String {
	case "book":
		var b bookRowScan
		var title sql.NullString
		err = h.db.QueryRowContext(ctx, `SELECT title,description,coverPath,language,explicit,chapters,audioFiles,updatedAt FROM books WHERE id=?`, mediaID.String).Scan(&title, &b.Description, &b.CoverPath, &b.Language, &b.explicit, &b.chapters, &b.files, &b.updated)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errFeedEntityMissing
		}
		if err != nil {
			return nil, err
		}
		b.Title = title.String
		b.finish()
		if b.UpdatedAt.After(src.EntityUpdatedAt) {
			src.EntityUpdatedAt = b.UpdatedAt
		}
		if b.Authors, err = h.bookAuthors(ctx, mediaID.String); err != nil {
			return nil, err
		}
		src.Title, src.Description, src.Language, src.Explicit, src.CoverPath = b.Title, b.Description, b.Language, b.Explicit, b.CoverPath
		src.Author = sql.NullString{String: joinAuthors(b.Authors), Valid: true}
		src.PodcastType = "serial"
		src.Episodes = bookEpisodes(b.bookData, asTime(itemCreated), 0)
	case "podcast":
		if err = h.loadPodcastSource(ctx, mediaID.String, src); err != nil {
			return nil, err
		}
	default:
		return nil, errFeedEntityMissing
	}
	return src, nil
}

// loadPodcastSource is Feed.getFeedObjForLibraryItem for a podcast plus
// FeedEpisode.createFromPodcastEpisodes (server/models/FeedEpisode.js:87).
func (h *Handler) loadPodcastSource(ctx context.Context, podcastID string, src *feedSource) error {
	var title, podcastType sql.NullString
	var explicit sql.NullInt64
	err := h.db.QueryRowContext(ctx, `SELECT title,author,description,coverPath,language,podcastType,explicit FROM podcasts WHERE id=?`, podcastID).Scan(&title, &src.Author, &src.Description, &src.CoverPath, &src.Language, &podcastType, &explicit)
	if errors.Is(err, sql.ErrNoRows) {
		return errFeedEntityMissing
	}
	if err != nil {
		return err
	}
	src.Title = title.String
	src.PodcastType = podcastType.String
	src.Explicit = explicit.Int64 != 0
	rows, err := h.db.QueryContext(ctx, `SELECT title,description,pubDate,season,episode,episodeType,audioFile,updatedAt FROM podcastEpisodes WHERE podcastId=? ORDER BY rowid`, podcastID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var epTitle sql.NullString
		var ep feedEpisodeSource
		var audio []byte
		var updated any
		if err := rows.Scan(&epTitle, &ep.Description, &ep.PubDate, &ep.Season, &ep.Episode, &ep.EpisodeType, &audio, &updated); err != nil {
			return err
		}
		var af audioFileJSON
		_ = json.Unmarshal(audio, &af)
		ep.Title = epTitle.String
		ep.Filename = af.Metadata.Filename
		ep.EnclosureSize = sizeValue(af.Metadata.Size)
		ep.Duration = sql.NullFloat64{Float64: af.Duration, Valid: true}
		if af.MimeType != nil {
			ep.EnclosureType = sql.NullString{String: *af.MimeType, Valid: true}
		}
		if af.Metadata.Path != nil {
			ep.FilePath = sql.NullString{String: *af.Metadata.Path, Valid: true}
		}
		ep.Explicit = src.Explicit
		if t := asTime(updated); t.After(src.EntityUpdatedAt) {
			src.EntityUpdatedAt = t
		}
		src.Episodes = append(src.Episodes, ep)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Sorted by pubDate: newest first for episodic, oldest first for serial.
	// new Date(invalid) is NaN, which compares as equal (stable sort).
	ms := func(ep feedEpisodeSource) (float64, bool) {
		t, ok := parseJSDate(ep.PubDate.String)
		return float64(t.UnixMilli()), ok && ep.PubDate.Valid
	}
	sort.SliceStable(src.Episodes, func(i, j int) bool {
		a, aok := ms(src.Episodes[i])
		b, bok := ms(src.Episodes[j])
		if !aok || !bok {
			return false
		}
		if src.PodcastType == "episodic" {
			return b-a < 0
		}
		return a-b < 0
	})
	return nil
}

// loadAggregateSource reads a collection or series and its books
// (Feed.getFeedObjForCollection / getFeedObjForSeries).
func (h *Handler) loadAggregateSource(ctx context.Context, entityType, id string) (*feedSource, error) {
	var name, desc sql.NullString
	var entityUpdated any
	table, join, order := "collections", `collectionBooks cb JOIN books b ON b.id=cb.bookId`, `ORDER BY cb."order" ASC`
	where := `cb.collectionId=?`
	if entityType == "series" {
		table, join, order = "series", `bookSeries bs JOIN books b ON b.id=bs.bookId`, `ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, bs.rowid ASC`
		where = `bs.seriesId=?`
	}
	if err := h.db.QueryRowContext(ctx, `SELECT name,description,updatedAt FROM `+table+` WHERE id=?`, id).Scan(&name, &desc, &entityUpdated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errFeedEntityMissing
		}
		return nil, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT b.id,b.title,b.description,b.coverPath,b.language,b.explicit,b.chapters,b.audioFiles,b.updatedAt,li.id,li.libraryId,li.createdAt,li.updatedAt FROM `+join+` JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE `+where+` `+order, id)
	if err != nil {
		return nil, err
	}
	var books []bookData
	for rows.Next() {
		var s bookRowScan
		var title, itemID, libraryID sql.NullString
		if err := rows.Scan(&s.ID, &title, &s.Description, &s.CoverPath, &s.Language, &s.explicit, &s.chapters, &s.files, &s.updated, &itemID, &libraryID, &s.itemCreated, &s.itemUpdated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		s.Title, s.ItemID, s.LibraryID = title.String, itemID.String, libraryID.String
		s.finish()
		books = append(books, s.bookData)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	src := &feedSource{PodcastType: "serial", Title: name.String, Description: sql.NullString{String: desc.String, Valid: true}}
	src.EntityUpdatedAt = asTime(entityUpdated)
	src.OwnUpdatedAt = src.EntityUpdatedAt
	var withTracks []bookData
	for i := range books {
		if books[i].Authors, err = h.bookAuthors(ctx, books[i].ID); err != nil {
			return nil, err
		}
		n := len(books[i].included())
		src.BookTrackCount += n
		if t := newerOf(books[i].ItemUpdated, books[i].UpdatedAt); t.After(src.ChildrenUpdatedAt) {
			src.ChildrenUpdatedAt = t
		}
		if n > 0 {
			withTracks = append(withTracks, books[i])
		}
	}
	for _, b := range withTracks {
		if t := newerOf(b.ItemUpdated, b.UpdatedAt); t.After(src.EntityUpdatedAt) {
			src.EntityUpdatedAt = t
		}
	}
	var names []string
	for _, b := range withTracks {
		if !src.CoverPath.Valid && b.CoverPath.String != "" {
			src.CoverPath = b.CoverPath
		}
		if b.Explicit {
			src.Explicit = true
		}
		for _, a := range b.Authors {
			if !containsString(names, a) {
				names = append(names, a)
			}
		}
	}
	author := strings.Join(names[:min(3, len(names))], ", ")
	if len(names) > 3 {
		author += " & more"
	}
	src.Author = sql.NullString{String: author, Valid: true}
	if len(withTracks) > 0 {
		earliest := withTracks[0].ItemCreated
		for _, b := range withTracks[1:] {
			if b.ItemCreated.Before(earliest) {
				earliest = b.ItemCreated
			}
		}
		offset := 0
		for _, b := range withTracks {
			eps := bookEpisodes(b, earliest, offset)
			offset += len(eps)
			src.Episodes = append(src.Episodes, eps...)
		}
		src.LibraryID = withTracks[0].LibraryID
	}
	if entityType == "series" {
		src.SiteURL = "/library/" + src.LibraryID + "/series/" + id
	} else {
		src.SiteURL = "/collection/" + id
	}
	return src, nil
}

func newerOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (h *Handler) loadFeedSource(ctx context.Context, entityType, id string) (*feedSource, error) {
	if entityType == "libraryItem" {
		return h.loadItemSource(ctx, id)
	}
	return h.loadAggregateSource(ctx, entityType, id)
}

// parseJSDate approximates Date.parse for the pubDate spellings feeds carry:
// RFC 822/1123 dates, ISO 8601 dates and date-times. ok is false where
// JavaScript yields an Invalid Date.
func parseJSDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"Mon, 02 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 MST",
		"Mon, 02 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 02 Jan 2006 15:04 MST", "Mon, 2 Jan 2006 15:04 -0700",
		"02 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 MST",
		time.RFC3339Nano, "2006-01-02T15:04Z07:00",
		"2006-01-02", "2006-01", "2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "Jan 2, 2006", "January 2, 2006", "Mon Jan 02 2006 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
