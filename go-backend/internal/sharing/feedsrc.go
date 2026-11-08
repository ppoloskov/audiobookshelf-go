package sharing

import (
	"audiobookshelf-compat/internal/db/sqlc"
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
	"audiobookshelf-compat/internal/libitem"
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

// bookFromItem is the feed's view of a loaded book item.
func bookFromItem(it libitem.Item) bookData {
	b := it.Book
	bd := bookData{ID: b.ID, Title: deref(b.Title), Description: nullable(b.Description), CoverPath: nullable(b.CoverPath), Language: nullable(b.Language),
		Explicit: b.Explicit, UpdatedAt: msTime(b.UpdatedAt), ItemID: it.ID, LibraryID: it.LibraryID, ItemCreated: msTime(it.AddedAt), ItemUpdated: msTime(it.UpdatedAt)}
	remarshal(b.AudioFiles, &bd.Files)
	remarshal(b.Chapters, &bd.Chapters)
	for _, a := range b.Authors {
		bd.Authors = append(bd.Authors, a.Name)
	}
	return bd
}

func remarshal(from, to any) {
	raw, _ := json.Marshal(from)
	_ = json.Unmarshal(raw, to)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s *string) sql.NullString { return sql.NullString{String: deref(s), Valid: s != nil} }

func msTime(ms *int64) time.Time {
	if ms == nil {
		return time.Time{}
	}
	return time.UnixMilli(*ms)
}

// loadItemSource reads a library item the way LibraryItem.getExpandedById and
// Feed.getFeedObjForLibraryItem do.
func (h *Handler) loadItemSource(ctx context.Context, id string) (*feedSource, error) {
	it, err := libitem.LoadOne(ctx, h.db, id, libitem.Options{Episodes: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errFeedEntityMissing
	}
	if err != nil {
		return nil, err
	}
	src := &feedSource{LibraryID: it.LibraryID, SiteURL: "/item/" + id, EntityUpdatedAt: msTime(it.UpdatedAt)}
	if it.Book == nil {
		podcastSource(it.Podcast, src)
		return src, nil
	}
	b := bookFromItem(it)
	if b.UpdatedAt.After(src.EntityUpdatedAt) {
		src.EntityUpdatedAt = b.UpdatedAt
	}
	src.Title, src.Description, src.Language, src.Explicit, src.CoverPath = b.Title, b.Description, b.Language, b.Explicit, b.CoverPath
	src.Author = sql.NullString{String: strings.Join(b.Authors, ", "), Valid: true}
	src.PodcastType = "serial"
	src.Episodes = bookEpisodes(b, b.ItemCreated, 0)
	return src, nil
}

// podcastSource is Feed.getFeedObjForLibraryItem for a podcast plus
// FeedEpisode.createFromPodcastEpisodes (server/models/FeedEpisode.js:87).
func podcastSource(p *libitem.Podcast, src *feedSource) {
	src.Title, src.Author, src.Description, src.CoverPath, src.Language = deref(p.Title), nullable(p.Author), nullable(p.Description), nullable(p.CoverPath), nullable(p.Language)
	src.PodcastType = deref(p.Type)
	src.Explicit = p.Explicit
	for _, e := range p.Episodes {
		var af audioFileJSON
		remarshal(e.AudioFile, &af)
		ep := feedEpisodeSource{Title: deref(e.Title), Description: nullable(e.Description), PubDate: nullable(e.PubDate), Season: nullable(e.Season),
			Episode: nullable(e.Episode), EpisodeType: nullable(e.EpisodeType), Filename: af.Metadata.Filename, EnclosureSize: sizeValue(af.Metadata.Size),
			Duration: sql.NullFloat64{Float64: af.Duration, Valid: true}, EnclosureType: nullable(af.MimeType), FilePath: nullable(af.Metadata.Path), Explicit: src.Explicit}
		if t := msTime(e.UpdatedAt); t.After(src.EntityUpdatedAt) {
			src.EntityUpdatedAt = t
		}
		src.Episodes = append(src.Episodes, ep)
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
}

// aggregateEntity is a collection or series with its books: the entity row and
// the books its join yields, in the entity's order.
type aggregateEntity struct {
	name, desc sql.NullString
	updated    any
	books      []bookData
}

func (h *Handler) loadCollectionEntity(ctx context.Context, id string) (aggregateEntity, error) {
	c, err := sqlc.New(h.db).GetCollection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return aggregateEntity{}, errFeedEntityMissing
	}
	if err != nil {
		return aggregateEntity{}, err
	}
	rows, err := sqlc.New(h.db).ListCollectionBookSources(ctx, id)
	if err != nil {
		return aggregateEntity{}, err
	}
	itemIDs := make([]string, len(rows))
	for i, r := range rows {
		itemIDs[i] = r.ItemID
	}
	agg := aggregateEntity{name: c.Name, desc: c.Description, updated: c.Updatedat}
	agg.books, err = h.loadBooks(ctx, itemIDs)
	return agg, err
}

// loadBooks loads the book items in one batch and returns them in the order of
// itemIDs.
func (h *Handler) loadBooks(ctx context.Context, itemIDs []string) ([]bookData, error) {
	items, err := libitem.Load(ctx, h.db, itemIDs, libitem.Options{})
	if err != nil {
		return nil, err
	}
	var books []bookData
	for _, it := range items {
		if it.Book != nil {
			books = append(books, bookFromItem(it))
		}
	}
	return books, nil
}

// loadSeriesEntity reads a series and its books in sequence order.
func (h *Handler) loadSeriesEntity(ctx context.Context, id string) (aggregateEntity, error) {
	var agg aggregateEntity
	err := h.db.QueryRowContext(ctx, `SELECT name,description,updatedAt FROM series WHERE id=?`, id).Scan(&agg.name, &agg.desc, &agg.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return agg, errFeedEntityMissing
	}
	if err != nil {
		return agg, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT li.id FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE bs.seriesId=? ORDER BY CAST(bs.sequence AS FLOAT) ASC NULLS LAST, bs.rowid ASC`, id)
	if err != nil {
		return agg, err
	}
	defer rows.Close()
	var itemIDs []string
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			return aggregateEntity{}, err
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := rows.Err(); err != nil {
		return aggregateEntity{}, err
	}
	agg.books, err = h.loadBooks(ctx, itemIDs)
	return agg, err
}

// loadAggregateSource reads a collection or series and its books
// (Feed.getFeedObjForCollection / getFeedObjForSeries).
func (h *Handler) loadAggregateSource(ctx context.Context, entityType, id string) (*feedSource, error) {
	var agg aggregateEntity
	var err error
	if entityType == "series" {
		agg, err = h.loadSeriesEntity(ctx, id)
	} else {
		agg, err = h.loadCollectionEntity(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	books := agg.books
	src := &feedSource{PodcastType: "serial", Title: agg.name.String, Description: sql.NullString{String: agg.desc.String, Valid: true}}
	src.EntityUpdatedAt = asTime(agg.updated)
	src.OwnUpdatedAt = src.EntityUpdatedAt
	var withTracks []bookData
	for i := range books {
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
