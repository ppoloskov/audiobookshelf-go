// TEMPORARY: parity with internal/items, books, podcasts/read; delete with them.

package item

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/podcasts/read"
)

var prefixVariants = [][]string{nil, {"the", "a"}, {"an", "le"}}

// compare fails the test with a path-level diff when old and new differ.
func compare(t *testing.T, label string, old, now any) {
	t.Helper()
	if diffs := diffJSON("$", roundTrip(t, old), roundTrip(t, now)); len(diffs) > 0 {
		if len(diffs) > 12 {
			diffs = append(diffs[:12], fmt.Sprintf("... and %d more", len(diffs)-12))
		}
		t.Errorf("%s differs:\n  %s", label, strings.Join(diffs, "\n  "))
	}
}

// parityCase is a named mutation of the fixture database.
type parityCase struct {
	name string
	sql  []string
}

const (
	bookDelete  = "da2e4ecd-d9cc-44be-bac7-6b30992d1c0e" // Hard Delete Me: one author, no series
	bookNovel   = "91bb63f7-97c9-4230-8fd0-1dfd994977ea" // Single File Novel
	bookEbook   = "bf4f5bac-080b-465e-adf0-1a3ea5018ac6" // Ebook Only
	bookMiddle  = "7baea19e-3334-416f-b862-0cb7499e5006" // The Middle: series 2
	bookBegin   = "b3c61dfc-dd4b-47ee-8998-0340c1228f52" // The Beginning: series 1
	podcastItem = "eeb6224f-1316-40c8-b8bf-00804cc90436"
)

// byItem addresses a books row through its item.
func byItem(id string) string {
	return `(SELECT mediaId FROM libraryItems WHERE id='` + id + `')`
}

var parityCases = []parityCase{
	{"fixture as is", nil},
	{"book without authors", []string{`DELETE FROM bookAuthors WHERE bookId=` + byItem(bookNovel)}},
	{"book with two series and sequences", []string{
		`INSERT INTO series (id,name,createdAt,updatedAt) VALUES ('s-extra','Side Saga','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00'),('s-three','Third','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO bookSeries (id,sequence,createdAt,bookId,seriesId) VALUES ('bs1','1.5','2030-01-01 00:00:00.000 +00:00',` + byItem(bookMiddle) + `,'s-extra'),('bs2',NULL,'2030-01-01 00:00:00.001 +00:00',` + byItem(bookMiddle) + `,'s-three')`,
		`UPDATE bookSeries SET sequence='' WHERE id='bs2'`,
	}},
	{"book with two authors added out of order", []string{
		`INSERT INTO authors (id,name,createdAt,updatedAt) VALUES ('a-x','Zoe Zed','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00'),('a-y','Madonna','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO bookAuthors (id,createdAt,bookId,authorId) VALUES ('ba1','2000-01-01 00:00:00.000 +00:00',` + byItem(bookBegin) + `,'a-y'),('ba2','2000-01-01 00:00:00.000 +00:00',` + byItem(bookBegin) + `,'a-x')`,
	}},
	{"NULL subtitle, description, cover, duration", []string{
		`UPDATE books SET subtitle=NULL, description=NULL, coverPath=NULL, duration=NULL, publishedYear=NULL, isbn=NULL WHERE id=` + byItem(bookNovel),
	}},
	{"description with html, subtitle and metadata", []string{
		`UPDATE books SET title='The Hobbit', subtitle='There and Back', description='<p>Hello <b>big</b> world &amp; more</p>', narrators='["Rob Inglis","Andy Serkis"]', genres='["Fantasy","Adventure"]', tags='["t1","t2"]', publishedYear='1937', publishedDate='1937-09-21', publisher='Allen', isbn='123', asin='B000', language='English', explicit=1, abridged=1 WHERE id=` + byItem(bookNovel),
	}},
	{"empty description", []string{`UPDATE books SET description='' WHERE id=` + byItem(bookNovel)}},
	{"NULL title", []string{`UPDATE books SET title=NULL WHERE id=` + byItem(bookNovel)}},
	{"audiobook with ebook file", []string{
		`UPDATE books SET ebookFile=(SELECT ebookFile FROM books WHERE id=` + byItem(bookEbook) + `) WHERE id=` + byItem(bookNovel),
	}},
	{"ebookFile NULL on ebook-only book", []string{`UPDATE books SET ebookFile=NULL WHERE id=` + byItem(bookEbook)}},
	{"ebookFile is the string null", []string{`UPDATE books SET ebookFile='null' WHERE id=` + byItem(bookEbook)}},
	{"ebookFile second shape without fileType", []string{
		`UPDATE books SET ebookFile=json_remove(ebookFile,'$.fileType','$.isSupplementary') WHERE id=` + byItem(bookEbook),
	}},
	{"ebookFile with isSupplementary false", []string{
		`UPDATE books SET ebookFile=json_set(ebookFile,'$.isSupplementary',json('false')) WHERE id=` + byItem(bookEbook),
	}},
	{"isMissing and isInvalid", []string{`UPDATE libraryItems SET isMissing=1, isInvalid=1 WHERE id IN ('` + bookNovel + `','` + podcastItem + `')`}},
	{"NULL item columns", []string{`UPDATE libraryItems SET ino=NULL, libraryFolderId=NULL, lastScan=NULL, lastScanVersion=NULL, mtime=NULL, ctime=NULL, birthtime=NULL, size=NULL, isFile=NULL, isMissing=NULL, isInvalid=NULL WHERE id='` + bookBegin + `'`}},
	{"item with old id and scan version", []string{`UPDATE libraryItems SET extraData='{"oldLibraryItemId":"li_old"}', lastScanVersion='2.0.0', isFile=1 WHERE id='` + bookBegin + `'`}},
	{"item with empty old id and NULL extraData", []string{
		`UPDATE libraryItems SET extraData='{"oldLibraryItemId":""}' WHERE id='` + bookBegin + `'`,
		`UPDATE libraryItems SET extraData=NULL WHERE id='` + bookNovel + `'`,
	}},
	{"library files of every kind", []string{
		`UPDATE libraryItems SET libraryFiles=json_array(
			json_set(json_extract(libraryFiles,'$[0]'),'$.metadata.ext','.epub','$.fileType','audio'),
			json_set(json_extract(libraryFiles,'$[0]'),'$.metadata.ext','.JPG'),
			json_set(json_extract(libraryFiles,'$[0]'),'$.metadata.ext','.xyz','$.isSupplementary',json('true')),
			json_set(json_extract(libraryFiles,'$[0]'),'$.metadata.ext','nfo'),
			json_set(json_extract(libraryFiles,'$[0]'),'$.metadata.ext','','$.isSupplementary',json('false'))) WHERE id='` + bookNovel + `'`,
	}},
	{"empty and NULL libraryFiles", []string{
		`UPDATE libraryItems SET libraryFiles='[]' WHERE id='` + bookNovel + `'`,
		`UPDATE libraryItems SET libraryFiles=NULL WHERE id='` + bookBegin + `'`,
	}},
	{"NULL json columns of a book", []string{`UPDATE books SET tags=NULL, narrators=NULL, genres=NULL, chapters=NULL, audioFiles=NULL WHERE id=` + byItem(bookMiddle)}},
	{"audio files with excluded track and fractional durations", []string{
		`UPDATE books SET audioFiles=json_array(
			json_extract(audioFiles,'$[0]'),
			json_set(json_extract(audioFiles,'$[0]'),'$.index',2,'$.ino','1000','$.duration',10.25,'$.exclude',json('true'),'$.metadata.filename','b side.mp3'),
			json_set(json_extract(audioFiles,'$[0]'),'$.index',3,'$.ino','1001/x y','$.duration',2.5,'$.metadata.filename','c#.mp3','$.metadata.size',777),
			json_set(json_extract(audioFiles,'$[0]'),'$.index',4,'$.ino','1002','$.duration',json('null'),'$.error','bad','$.metaTags',json('{}'))
		), chapters='[{"id":0,"start":0,"end":1.5,"title":"One"},{"id":1,"start":1.5,"end":3,"title":"Two"}]', duration=18.5 WHERE id=` + byItem(bookNovel),
	}},
	{"audio file with chapters, tags and unusual values", []string{
		`UPDATE books SET audioFiles=json_set(audioFiles,'$[0].chapters',json('[{"id":0,"start":0,"end":1,"title":"x"}]'),'$[0].trackNumFromMeta',3,'$[0].language','eng','$[0].embeddedCoverArt','mjpeg','$[0].manuallyVerified',json('true')) WHERE id=` + byItem(bookDelete),
	}},
	{"podcast without episodes", []string{`DELETE FROM podcastEpisodes`, `UPDATE podcasts SET numEpisodes=0`}},
	{"podcast metadata, prefix and settings", []string{
		`UPDATE podcasts SET title='The Fixture Cast', author='Host', description='<b>d</b>', releaseDate='2020', feedURL='https://f/x', imageURL='https://i/x', itunesPageURL='https://p', itunesId='42', itunesArtistId='43', language='en', podcastType='serial', explicit=1, autoDownloadEpisodes=1, autoDownloadSchedule='0 * * * *', lastEpisodeCheck='2024-05-06 07:08:09.123 +00:00', maxEpisodesToKeep=5, maxNewEpisodesToDownload=3, coverPath='/c.jpg', tags='["a","b"]', genres='["Tech"]'`,
	}},
	{"podcast with NULL columns", []string{`UPDATE podcasts SET title=NULL, author=NULL, tags=NULL, genres=NULL, maxEpisodesToKeep=NULL, maxNewEpisodesToDownload=NULL, numEpisodes=NULL, lastEpisodeCheck=NULL, explicit=NULL, autoDownloadEpisodes=NULL`}},
	{"episode with NULL publishedAt", []string{`UPDATE podcastEpisodes SET publishedAt=NULL WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`}},
	{"episode with epoch publishedAt", []string{`UPDATE podcastEpisodes SET publishedAt='1970-01-01 00:00:00.000 +00:00' WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`}},
	{"episodes with enclosure, guid and old id", []string{
		`UPDATE podcastEpisodes SET enclosureURL='https://e/1.mp3', enclosureSize=123456, enclosureType='audio/mpeg', extraData='{"guid":"g-1","oldEpisodeId":"ep_old"}', "index"=7, season='2', episode='5', episodeType='full', subtitle='sub', description='<p>x</p>', pubDate='Sat, 01 Mar 2025 00:00:00 GMT' WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`,
		`UPDATE podcastEpisodes SET enclosureURL='https://e/2.mp3', enclosureType=NULL, enclosureSize=NULL, extraData='{"guid":""}' WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)+1`,
		`UPDATE podcastEpisodes SET enclosureURL='', extraData=NULL, chapters='[{"id":0,"start":0,"end":2,"title":"c"}]' WHERE rowid=(SELECT max(rowid) FROM podcastEpisodes)`,
	}},
	{"episodes created out of insertion order", []string{
		`UPDATE podcastEpisodes SET createdAt='2031-01-01 00:00:00.000 +00:00' WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`,
	}},
	{"episode audio file with excluded flag and spaces in ino", []string{
		`UPDATE podcastEpisodes SET audioFile=json_set(audioFile,'$.ino','12 34','$.exclude',json('true'),'$.duration',json('null')) WHERE rowid=(SELECT min(rowid) FROM podcastEpisodes)`,
	}},
	{"orphan and unknown media items", []string{
		`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-orphan','/o','o','nope','book',0,0,0,'[]','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00',(SELECT id FROM libraries LIMIT 1)),('li-weird','/w','w','x','music',0,0,0,'[]','2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00',(SELECT id FROM libraries LIMIT 1))`,
	}},
}

func TestParityWithOldItems(t *testing.T) {
	for _, tc := range parityCases {
		t.Run(tc.name, func(t *testing.T) {
			d := dbtest.Fixture(t)
			ctx := context.Background()
			for _, stmt := range tc.sql {
				if _, err := d.Exec(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			runParity(t, d)
		})
	}
}

func runParity(t *testing.T, d *db.DB) {
	ctx := context.Background()
	var ids []string
	if err := d.Select(ctx, &ids, `SELECT id FROM libraryItems ORDER BY rowid`); err != nil {
		t.Fatal(err)
	}
	var firstEpisode string
	if err := d.Select(ctx, &[]string{}, `SELECT id FROM podcastEpisodes LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	var episodeIDs []string
	if err := d.Select(ctx, &episodeIDs, `SELECT id FROM podcastEpisodes ORDER BY rowid DESC LIMIT 2`); err != nil {
		t.Fatal(err)
	}
	if len(episodeIDs) > 0 {
		firstEpisode = episodeIDs[0]
	}
	// Requested order: reversed, with a duplicate and an unknown id.
	request := []string{"missing-id"}
	for i := len(ids) - 1; i >= 0; i-- {
		request = append(request, ids[i])
	}
	request = append(request, ids...)
	options := []struct {
		name string
		old  items.Options
		now  Options
	}{
		{"no episodes", items.Options{}, Options{}},
		{"episodes", items.Options{Episodes: true}, Options{Episodes: true}},
		{"selected episodes", items.Options{Episodes: true, EpisodeIDs: []string{firstEpisode}}, Options{Episodes: true, EpisodeIDs: []string{firstEpisode}}},
		{"episode ids without episodes", items.Options{EpisodeIDs: []string{firstEpisode}}, Options{EpisodeIDs: []string{firstEpisode}}},
	}
	for _, opt := range options {
		oldItems, err := items.Load(ctx, d.SQL(), request, opt.old)
		if err != nil {
			t.Fatalf("old Load: %v", err)
		}
		newItems, err := Load(ctx, d, request, opt.now)
		if err != nil {
			t.Fatalf("new Load: %v", err)
		}
		if len(oldItems) != len(newItems) {
			t.Fatalf("%s: old loaded %d items, new %d", opt.name, len(oldItems), len(newItems))
		}
		if len(newItems) < len(ids)-2 {
			t.Fatalf("%s: only %d items loaded of %d", opt.name, len(newItems), len(ids))
		}
		for i := range oldItems {
			label := opt.name + " " + oldItems[i].ID
			if oldItems[i].ID != newItems[i].ID {
				t.Fatalf("%s: order differs: old %s, new %s", opt.name, oldItems[i].ID, newItems[i].ID)
			}
			compareItem(t, label, oldItems[i], newItems[i])
		}
	}
}

func compareItem(t *testing.T, label string, old items.Item, now Item) {
	t.Helper()
	if old.MediaID != now.MediaRowID() {
		t.Errorf("%s: media id old %q, new %q", label, old.MediaID, now.MediaRowID())
	}
	for _, prefixes := range prefixVariants {
		l := fmt.Sprintf("%s prefixes=%v", label, prefixes)
		compare(t, l+" minified", old.Minified(prefixes), now.Minified(prefixes))
		compare(t, l+" list row", old.ListRow(prefixes), now.ListRow(prefixes))
		compare(t, l+" expanded", old.Expanded(prefixes), now.Expanded(prefixes))
		if prefixes == nil {
			compare(t, l+" full", old.Full(), now.Full())
		}
		compareMedia(t, l, old, now, prefixes)
	}
	oldAccess, newAccess := old.Access(), now.Access()
	if oldAccess.LibraryID != newAccess.LibraryID || oldAccess.Explicit != newAccess.Explicit || fmt.Sprint(oldAccess.Tags) != fmt.Sprint(newAccess.Tags) {
		t.Errorf("%s: access old %+v, new %+v", label, oldAccess, newAccess)
	}
	// The serialized views report the same access and media id as the item.
	for _, view := range []struct {
		access Access
		id     string
	}{{now.Minified(nil).Access(), now.Minified(nil).MediaID()}, {now.Expanded(nil).Access(), now.Expanded(nil).MediaID()}, {now.Full().Access(), now.Full().MediaID()}} {
		if fmt.Sprint(view.access) != fmt.Sprint(newAccess) || view.id != old.MediaID {
			t.Errorf("%s: view access %+v id %q, want %+v %q", label, view.access, view.id, newAccess, old.MediaID)
		}
	}
}

// compareMedia checks the media and episode views that callers use on their own.
func compareMedia(t *testing.T, label string, old items.Item, now Item, prefixes []string) {
	t.Helper()
	switch {
	case old.Book != nil:
		compare(t, label+" book full", old.Book.Full(old.ID), now.Book.Full(now.ID))
		compare(t, label+" book minified", old.Book.Minified(prefixes), now.Book.Minified(prefixes))
		compare(t, label+" book expanded", old.Book.Expanded(old.ID, prefixes), now.Book.Expanded(now.ID, prefixes))
		if old.Book.AuthorName() != now.Book.AuthorName() {
			t.Errorf("%s: author name old %q, new %q", label, old.Book.AuthorName(), now.Book.AuthorName())
		}
	case old.Podcast != nil:
		compare(t, label+" podcast full", old.Podcast.Media(old.ID, nil, true, false), now.Podcast.Full(now.ID))
		compare(t, label+" podcast minified", old.Podcast.Media(old.ID, prefixes, false, false), now.Podcast.Minified(prefixes))
		compare(t, label+" podcast expanded", old.Podcast.Media(old.ID, prefixes, false, true), now.Podcast.Expanded(now.ID, prefixes))
		if (old.Podcast.Episodes == nil) != (now.Podcast.Episodes == nil) || len(old.Podcast.Episodes) != len(now.Podcast.Episodes) {
			t.Fatalf("%s: episodes old %d (nil %v), new %d (nil %v)", label, len(old.Podcast.Episodes), old.Podcast.Episodes == nil, len(now.Podcast.Episodes), now.Podcast.Episodes == nil)
		}
		for i := range old.Podcast.Episodes {
			compare(t, fmt.Sprintf("%s episode %d full", label, i), old.Podcast.Episodes[i].JSON(old.ID), now.Podcast.Episodes[i].Full(now.ID))
			compare(t, fmt.Sprintf("%s episode %d expanded", label, i), old.Podcast.Episodes[i].Expanded(old.ID), now.Podcast.Episodes[i].Expanded(now.ID))
		}
	}
}

var (
	_ = books.Book{}
	_ = podcastread.Podcast{}
)
