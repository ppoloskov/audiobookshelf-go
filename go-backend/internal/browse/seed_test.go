package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

// The ids of the users of the upstream fixture install; progress rows need
// real users.
const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	finisherID = "7c538a8f-9711-47bd-89e4-8d0139473dcf"
)

const (
	old = "2024-01-01T00:00:00Z"
	rcn = "2099-01-01T00:00:00Z" // newer than any cutoff: "recent"

	ebookFile = `{"ino":"eb","metadata":{"filename":"a.epub","ext":".epub","path":"/media/books/a.epub","relPath":"a.epub","size":5,"mtimeMs":1,"ctimeMs":1,"birthtimeMs":1},"ebookFormat":"epub","addedAt":1,"updatedAt":1}`
)

// seeder inserts rows with named columns.
type seeder struct {
	t *testing.T
	d *db.DB
}

func (s seeder) ins(table string, kv ...any) {
	s.t.Helper()
	var cols, marks []string
	var args []any
	for i := 0; i < len(kv); i += 2 {
		cols, marks, args = append(cols, "`"+kv[i].(string)+"`"), append(marks, "?"), append(args, kv[i+1])
	}
	verb := "INSERT"
	if table == "users" {
		verb = "INSERT OR IGNORE" // the fixture install has them
	}
	q := verb + " INTO " + table + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(marks, ",") + ")"
	if _, err := s.d.Exec(context.Background(), q, args...); err != nil {
		s.t.Fatalf("%v: %s", err, q)
	}
}

func (s seeder) exec(q string, args ...any) {
	s.t.Helper()
	if _, err := s.d.Exec(context.Background(), q, args...); err != nil {
		s.t.Fatalf("%v: %s", err, q)
	}
}

type bookRow struct {
	id, title, titleIgnore, year, publisher, lang string
	explicit, abridged                            int
	duration                                      float64
	narrators, audio, ebook, tags, genres         string
	asin                                          any
	size, missing                                 int
	files                                         string
	created                                       string
	lib, folder, itemID                           string
	mtimeDay                                      int
}

func (s seeder) book(b bookRow) {
	s.ins("books", "id", b.id, "title", b.title, "titleIgnorePrefix", b.titleIgnore, "publishedYear", nullIfEmpty(b.year), "publisher", nullIfEmpty(b.publisher),
		"language", nullIfEmpty(b.lang), "explicit", b.explicit, "abridged", b.abridged, "duration", b.duration, "narrators", b.narrators, "audioFiles", b.audio,
		"ebookFile", b.ebook, "chapters", "[]", "tags", b.tags, "genres", b.genres, "asin", b.asin, "createdAt", b.created, "updatedAt", b.created)
	s.ins("libraryItems", "id", b.itemID, "ino", "ino-"+b.itemID, "path", "/media/books/"+b.id, "relPath", b.id, "mediaId", b.id, "mediaType", "book",
		"isFile", 0, "isMissing", b.missing, "isInvalid", 0, "mtime", fmt.Sprintf("2024-02-%02dT00:00:00Z", b.mtimeDay), "ctime", "2024-02-01T00:00:00Z",
		"birthtime", fmt.Sprintf("2024-03-%02dT00:00:00Z", 11-b.mtimeDay), "size", b.size, "libraryFiles", b.files, "extraData", "{}", "title", b.title,
		"titleIgnorePrefix", b.titleIgnore, "authorNamesFirstLast", "", "authorNamesLastFirst", "", "createdAt", b.created, "updatedAt", b.created,
		"libraryId", b.lib, "libraryFolderId", b.folder)
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s seeder) author(id, name, lastFirst, lib, created string) {
	s.ins("authors", "id", id, "name", name, "lastFirst", lastFirst, "libraryId", lib, "createdAt", created, "updatedAt", created)
}

func (s seeder) series(id, name, prefixFree, lib, created string) {
	s.ins("series", "id", id, "name", name, "nameIgnorePrefix", prefixFree, "libraryId", lib, "createdAt", created, "updatedAt", created)
}

// link adds a bookAuthors row, then bookSeries rows through linkSeries; the
// link times are one millisecond apart so read order is insert order.
var linkClock int

func (s seeder) linkAuthor(bookID, authorID string) {
	linkClock++
	s.ins("bookAuthors", "id", fmt.Sprintf("ba-%d", linkClock), "bookId", bookID, "authorId", authorID, "createdAt", fmt.Sprintf("2024-01-01 00:00:00.%03d +00:00", linkClock%1000))
}

func (s seeder) linkSeries(bookID, seriesID string, sequence any) {
	linkClock++
	s.ins("bookSeries", "id", fmt.Sprintf("bs-%d", linkClock), "sequence", sequence, "bookId", bookID, "seriesId", seriesID, "createdAt", fmt.Sprintf("2024-01-01 00:00:00.%03d +00:00", linkClock%1000))
}

func (s seeder) progress(user, media, podcast string, current, ebook float64, finished, hidden int, updated string) {
	var podcastID any
	if podcast != "" {
		podcastID = podcast
	}
	s.ins("mediaProgresses", "id", user[:4]+media, "userId", user, "mediaItemId", media, "podcastId", podcastID, "currentTime", current, "ebookProgress", ebook,
		"isFinished", finished, "hideFromContinueListening", hidden, "updatedAt", updated, "createdAt", old)
}

func (s seeder) podcast(id, itemID, lib, folder, title, tags string, explicit int, created string, episodes int) {
	s.ins("podcasts", "id", id, "title", title, "titleIgnorePrefix", title, "author", "Host", "language", "en", "explicit", explicit, "tags", tags, "genres", "[]",
		"numEpisodes", episodes, "createdAt", created, "updatedAt", created)
	s.ins("libraryItems", "id", itemID, "ino", "ino-"+itemID, "path", "/media/podcasts/"+id, "relPath", id, "mediaId", id, "mediaType", "podcast",
		"isFile", 0, "isMissing", 0, "isInvalid", 0, "size", 500, "libraryFiles", "[]", "extraData", "{}", "title", title, "titleIgnorePrefix", title,
		"createdAt", created, "updatedAt", created, "libraryId", lib, "libraryFolderId", folder)
}

func (s seeder) episode(id, podcastID, title, created string, n int) {
	s.ins("podcastEpisodes", "id", id, "index", n, "season", "1", "episode", fmt.Sprint(n), "episodeType", "full", "title", title, "subtitle", "Sub", "description", "Desc",
		"pubDate", "2024-01-01", "enclosureURL", "https://example.test/e.mp3", "enclosureSize", 123, "enclosureType", "audio/mpeg", "publishedAt", created,
		"audioFile", fullAudio("ep"), "chapters", "[]", "extraData", `{"guid":"g"}`,
		"podcastId", podcastID, "createdAt", created, "updatedAt", created)
}

// seed adds three book libraries and two podcast libraries to d.
//
// l1 (book): b1..b6 / items i1..i6 with every filter group represented, two
// series, progress of the root user (and one row of the finisher), an open feed
// on i2 and a share on b3. l2 (podcast): p1..p3. l3 (book): g1..g10 for the
// shelves, with books in two series and a sub-series, finished, in-progress and
// ebook-only progress, feeds, shares and series that are new. l4 (podcast): gp1..gp3
// with episodes and progress.
func seed(t *testing.T, d *db.DB) {
	t.Helper()
	s := seeder{t, d}
	for _, u := range []struct{ id, name string }{{rootID, "trial-root"}, {listenerID, "trial-listener"}, {finisherID, "trial-finisher"}} {
		s.ins("users", "id", u.id, "username", u.name, "type", "user", "isActive", 1, "permissions", "{}", "createdAt", old, "updatedAt", old)
	}
	for i, lib := range []struct{ id, mediaType, settings string }{{"l1", "book", "{}"}, {"l2", "podcast", "{}"}, {"l3", "book", "{}"}, {"l4", "podcast", "{}"}} {
		s.ins("libraries", "id", lib.id, "name", "Lib "+lib.id, "displayOrder", 10+i, "mediaType", lib.mediaType, "provider", "google", "settings", lib.settings,
			"extraData", "{}", "createdAt", old, "updatedAt", old)
		s.ins("libraryFolders", "id", "f"+lib.id, "path", "/media/"+lib.id, "libraryId", lib.id, "createdAt", old, "updatedAt", old)
	}

	// l1
	s.author("a1", "Ada Author", "Author, Ada", "l1", old)
	s.author("a2", "Bob Writer", "Writer, Bob", "l1", old)
	s.series("s1", "Alpha Saga", "Alpha Saga", "l1", old)
	s.series("s2", "The Beta Cycle", "Beta Cycle", "l1", old)
	for i, b := range []bookRow{
		{id: "b1", title: "The Alpha", titleIgnore: "Alpha", year: "1995", publisher: "Pub A", lang: "en", duration: 10, narrators: `["Nora"]`, audio: audioFile, ebook: `null`, tags: `["red"]`, genres: `["fiction"]`, asin: "A1", size: 100, files: `[]`},
		{id: "b2", title: "Beta", titleIgnore: "Beta", year: "1999", publisher: "Pub B", lang: "fr", explicit: 1, abridged: 1, duration: 20, narrators: `["Nora","Omar"]`, audio: "[" + fullAudio("a1") + "," + fullAudio("a2") + "]", ebook: ebookFile, tags: `["blue","red"]`, genres: `["mystery"]`, size: 300, files: supplementaryFile},
		{id: "b3", title: "A Gamma", titleIgnore: "Gamma", year: "2003", publisher: "Pub A", lang: "en", duration: 5, narrators: `[]`, audio: `[]`, ebook: ebookFile, tags: `["blue"]`, genres: `["fiction","mystery"]`, asin: "A3", size: 200, missing: 1, files: `[]`},
		{id: "b4", title: "delta", titleIgnore: "delta", year: "2010", publisher: "Pub C", lang: "en", duration: 30, narrators: `["Omar"]`, audio: audioFile, ebook: `null`, tags: `[]`, genres: `[]`, size: 50, files: `[]`},
		{id: "b5", title: "Epsilon", titleIgnore: "Epsilon", year: "2011", publisher: "Pub B", lang: "de", narrators: `null`, audio: `[]`, ebook: `null`, tags: `["green"]`, genres: `["horror"]`, asin: "A5", size: 400, files: `[]`},
		{id: "b6", title: "Alpha Two", titleIgnore: "Alpha Two", duration: 15, narrators: `[]`, audio: audioFile, ebook: `null`, tags: `[]`, genres: `["fiction"]`, size: 10, files: `[]`},
	} {
		b.created, b.lib, b.folder, b.itemID, b.mtimeDay = fmt.Sprintf("2024-01-%02dT00:00:00Z", i+1), "l1", "fl1", fmt.Sprintf("i%d", i+1), 7-(i+1)
		s.book(b)
	}
	for _, l := range [][2]string{{"b1", "a1"}, {"b2", "a2"}, {"b3", "a1"}, {"b4", "a2"}, {"b5", "a1"}} {
		s.linkAuthor(l[0], l[1])
	}
	for _, l := range [][3]string{{"b1", "s1", "1"}, {"b2", "s1", "2"}, {"b3", "s2", "3"}, {"b4", "s2", "1"}, {"b5", "s2", "2.5"}} {
		s.linkSeries(l[0], l[1], l[2])
	}
	s.progress(rootID, "b1", "", 5, 0, 0, 0, "2024-01-05T00:00:00Z")
	s.progress(rootID, "b2", "", 20, 0, 1, 0, "2024-01-04T00:00:00Z")
	s.progress(rootID, "b3", "", 0, 0.5, 0, 0, "2024-01-06T00:00:00Z")
	s.progress(rootID, "b4", "", 0, 0, 0, 0, "2024-01-07T00:00:00Z")
	s.progress(finisherID, "b5", "", 9, 0, 0, 0, "2024-01-08T00:00:00Z")
	s.progress(rootID, "b5", "", 0, 0.4, 1, 0, "2024-01-09T00:00:00Z")
	s.ins("feeds", "id", "feed-i2", "entityType", "libraryItem", "entityId", "i2", "feedURL", "/feed/i2", "title", "Beta", "preventIndexing", 1, "ownerName", "Owner", "createdAt", old, "updatedAt", old)
	s.ins("mediaItemShares", "id", "share-b3", "mediaItemType", "book", "mediaItemId", "b3", "slug", "b3-share", "isDownloadable", 1, "createdAt", old, "updatedAt", old)

	// l2
	for i, p := range []struct {
		id, title, tags string
		explicit, eps   int
	}{{"p1", "Science Now", `["sci"]`, 0, 3}, {"p2", "The Daily", `["news"]`, 1, 10}, {"p3", "Chatter", `[]`, 0, 0}} {
		s.podcast(p.id, "pi"+p.id, "l2", "fl2", p.title, p.tags, p.explicit, fmt.Sprintf("2024-01-%02dT00:00:00Z", i+1), p.eps)
	}
	s.exec(`UPDATE libraryItems SET isMissing=1 WHERE id='pip3'`)
	s.exec(`UPDATE podcasts SET author='amy', language='fr', genres='["news","science"]' WHERE id='p2'`)
	s.exec(`UPDATE podcasts SET author='Zed', genres='["science"]' WHERE id='p1'`)
	s.ins("feeds", "id", "feed-pip2", "entityType", "libraryItem", "entityId", "pip2", "createdAt", old, "updatedAt", old)

	// l3: shelves
	for _, a := range [][3]string{{"ga1", rcn, "1"}, {"ga2", rcn, "2"}, {"ga3", rcn, "3"}, {"ga4", old, "4"}} {
		s.author(a[0], "Author "+a[0], a[0]+", Author", "l3", a[1])
	}
	for _, sr := range [][2]string{{"gs1", old}, {"gs2", rcn}, {"gs3", rcn}, {"gs4", rcn}, {"gs5", old}} {
		s.series(sr[0], "Series "+sr[0], "Series "+sr[0], "l3", sr[1])
	}
	type g struct {
		n        int
		audio    bool
		ebook    bool
		explicit int
		tags     string
		created  string
	}
	for _, b := range []g{{1, true, false, 0, `["tag"]`, old}, {2, true, false, 0, `[]`, rcn}, {3, true, false, 0, `[]`, rcn}, {4, false, true, 0, `[]`, rcn},
		{5, true, false, 0, `["x"]`, rcn}, {6, true, false, 1, `[]`, rcn}, {7, true, false, 0, `["secret"]`, old}, {8, true, false, 0, `[]`, rcn},
		{9, false, true, 0, `[]`, old}, {10, true, false, 0, `[]`, old}} {
		audio, ebook := `[]`, `null`
		if b.audio {
			audio = audioFile
		}
		if b.ebook {
			ebook = ebookFile
		}
		title := fmt.Sprintf("Golden Book %d", b.n)
		s.book(bookRow{id: fmt.Sprintf("g%d", b.n), itemID: fmt.Sprintf("gi%d", b.n), title: title, titleIgnore: title, year: "2024", lang: "en", explicit: b.explicit,
			duration: 12.5, narrators: `[]`, audio: audio, ebook: ebook, tags: b.tags, genres: `["fiction"]`, size: 100, files: `[]`, created: b.created,
			lib: "l3", folder: "fl3", mtimeDay: b.n})
	}
	for _, l := range [][2]string{{"g2", "ga2"}, {"g5", "ga2"}, {"g6", "ga3"}, {"g7", "ga4"}, {"g3", "ga1"}, {"g1", "ga1"}} {
		s.linkAuthor(l[0], l[1])
	}
	for _, l := range [][3]any{{"g1", "gs1", "1"}, {"g2", "gs1", "2"}, {"g3", "gs1", "10"}, {"g2", "gs2", "3"}, {"g5", "gs2", "1"}, {"g6", "gs2", "2"}, {"g8", "gs3", nil},
		{"g10", "gs4", "1"}, {"g9", "gs5", "1.5"}} {
		s.linkSeries(l[0].(string), l[1].(string), l[2])
	}
	s.progress(listenerID, "g1", "", 12.5, 0, 1, 0, "2024-02-01T00:00:00Z")
	s.progress(listenerID, "g4", "", 0, 0.5, 0, 0, "2024-02-02T00:00:00Z")
	s.progress(listenerID, "g5", "", 10, 0, 0, 0, "2024-02-03T00:00:00Z")
	s.progress(listenerID, "g7", "", 12.5, 0, 1, 0, "2024-02-04T00:00:00Z")
	s.progress(listenerID, "g9", "", 0, 1, 1, 0, "2024-02-05T00:00:00Z")
	s.progress(listenerID, "g10", "", 3, 0, 0, 0, "2024-02-06T00:00:00Z")
	s.progress(finisherID, "g1", "", 12.5, 0, 1, 0, "2024-03-01T00:00:00Z")
	s.progress(finisherID, "g2", "", 12.5, 0, 1, 0, "2024-03-02T00:00:00Z")
	s.progress(rootID, "g5", "", 10, 0, 0, 1, "2024-03-03T00:00:00Z")
	s.progress(rootID, "g3", "", 4, 0, 0, 0, "2024-03-04T00:00:00Z")
	s.progress(rootID, "g1", "", 12.5, 0, 1, 0, "2024-03-05T00:00:00Z")
	s.ins("feeds", "id", "feed-gi2", "entityType", "libraryItem", "entityId", "gi2", "feedURL", "/feed/gi2", "title", "G2", "preventIndexing", 0, "createdAt", old, "updatedAt", old)
	s.ins("feeds", "id", "feed-gs2", "entityType", "series", "entityId", "gs2", "feedURL", "/feed/gs2", "title", "S2", "preventIndexing", 1, "createdAt", old, "updatedAt", old)
	s.ins("mediaItemShares", "id", "share-g3", "mediaItemType", "book", "mediaItemId", "g3", "slug", "g3-share", "isDownloadable", 1, "createdAt", old, "updatedAt", old)
	s.ins("mediaItemShares", "id", "share-g2", "mediaItemType", "book", "mediaItemId", "g2", "slug", "g2-share", "isDownloadable", 0, "expiresAt", old, "createdAt", old, "updatedAt", old)

	// l4: podcast shelves
	for _, p := range []struct{ id, created, tags, explicit string }{{"gp1", rcn, `[]`, "0"}, {"gp2", old, `[]`, "1"}, {"gp3", rcn, `["secret"]`, "0"}} {
		s.podcast(p.id, "i"+p.id, "l4", "fl4", "Cast "+p.id, p.tags, int(p.explicit[0]-'0'), p.created, 3)
		for n := 1; n <= 3; n++ {
			created := fmt.Sprintf("2099-01-0%dT00:00:00Z", n)
			if n == 3 {
				created = old
			}
			s.episode(fmt.Sprintf("ge%s%d", p.id, n), p.id, fmt.Sprintf("Episode %s-%d", p.id, n), created, n)
		}
	}
	s.progress(listenerID, "gegp11", "gp1", 5, 0, 0, 0, "2024-04-01T00:00:00Z")
	s.progress(listenerID, "gegp12", "gp1", 30, 0, 1, 0, "2024-04-02T00:00:00Z")
	s.progress(listenerID, "gegp31", "gp3", 30, 0, 1, 0, "2024-04-03T00:00:00Z")
	s.progress(listenerID, "gegp21", "gp2", 30, 0, 1, 0, "2024-04-04T00:00:00Z")
	s.progress(rootID, "gegp13", "gp1", 7, 0, 0, 0, "2024-04-05T00:00:00Z")
	s.progress(rootID, "gegp22", "gp2", 7, 0, 0, 1, "2024-04-06T00:00:00Z")
}

// seeded returns an empty schema database holding seed.
func seeded(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.New(t)
	seed(t, d)
	return d
}

// fullAudio is a complete audio file document, as the scanner writes it.
func fullAudio(ino string) string {
	duration := 12.5
	raw, err := json.Marshal(db.AudioFile{Index: 1, Ino: ino, AddedAt: 1, UpdatedAt: 2, Format: "MP3", Duration: &duration, MimeType: "audio/mpeg", Chapters: []db.Chapter{},
		Metadata: db.FileMetadata{Filename: "01.mp3", Ext: ".mp3", Path: "/media/books/01.mp3", RelPath: "01.mp3", Size: 100}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

var audioFile = "[" + fullAudio("audio-1") + "]"

// supplementaryFile is a library file flagged as supplementary.
var supplementaryFile = func() string {
	raw, err := json.Marshal([]db.LibraryFile{{Ino: "f1", AddedAt: 1, UpdatedAt: 2, IsSupplementary: db.Ptr(true),
		Metadata: db.FileMetadata{Filename: "notes.pdf", Ext: ".pdf", Path: "/media/books/b2/notes.pdf", RelPath: "notes.pdf", Size: 9}}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}()
