package item

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

func exec(t testing.TB, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// addBook inserts a book item "li-b<n>" with two audio files (the second
// excluded), a ".mp3" library file and an old library item id.
func addBook(t testing.TB, d *db.DB, n int, title string) string {
	t.Helper()
	id, bookID := fmt.Sprintf("li-b%d", n), fmt.Sprintf("book%d", n)
	exec(t, d, `INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES (?,?,0,0,10,'["t1"]','[]','[]','[{"ino":"a1","duration":4,"metadata":{"filename":"a.mp3","size":100}},{"ino":"a2","exclude":true,"duration":6,"metadata":{"size":50}}]','[]',?,?)`, bookID, title, stamp, stamp)
	exec(t, d, `INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt) VALUES (?,?,?,?,?,'book',0,0,0,150,'[{"ino":"a1","metadata":{"ext":".mp3"}}]','{"oldLibraryItemId":"li_old"}',?,?)`, id, "ino"+id, "/p/"+id, id, bookID, stamp, stamp)
	return id
}

func ids(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestLoadOrderMissingAndEmpty(t *testing.T) {
	d := dbtest.New(t)
	a, b := addBook(t, d, 1, "A"), addBook(t, d, 2, "B")
	exec(t, d, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,libraryFiles,createdAt,updatedAt) VALUES ('li-orphan','/o','o','nope','book','[]',?,?)`, stamp, stamp)
	ctx := context.Background()
	items, err := Load(ctx, d, []string{b, "missing", "li-orphan", a, b}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(items), []string{b, a, b}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for _, id := range []string{"missing", "li-orphan"} {
		if _, err := Get(ctx, d, id, Options{}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("Get(%s) = %v, want sql.ErrNoRows", id, err)
		}
	}
	if items, err := Load(ctx, d, nil, Options{}); err != nil || len(items) != 0 {
		t.Fatalf("Load(nil) = %v, %v", items, err)
	}
	if it, err := Get(ctx, d, a, Options{}); err != nil || it.ID != a || it.Book == nil {
		t.Fatalf("Get(%s) = %+v, %v", a, it, err)
	}
}

func TestLoadChunksLongIDLists(t *testing.T) {
	d := dbtest.New(t)
	var want []string
	for i := range 1100 {
		want = append(want, addBook(t, d, i, "T"))
	}
	items, err := Load(context.Background(), d, want, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(items), want) {
		t.Fatalf("loaded %d of %d items, or out of order", len(items), len(want))
	}
}

func TestRowDecodingAndAccess(t *testing.T) {
	d := dbtest.New(t)
	it, err := Get(context.Background(), d, addBook(t, d, 1, "The Hobbit"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	head := it.Minified(nil).Head
	if head.OldLibraryItemID == nil || *head.OldLibraryItemID != "li_old" || it.LibraryFiles.V[0].FileType != "audio" {
		t.Fatalf("row decode: head %+v, files %+v", head, it.LibraryFiles.V)
	}
	if a := it.Access(); a.Explicit || len(a.Tags) != 1 || a.Tags[0] != "t1" {
		t.Fatalf("access = %+v", a)
	}
}

func TestBookWithoutAuthorsOrSeries(t *testing.T) {
	d := dbtest.New(t)
	it, err := Get(context.Background(), d, addBook(t, d, 1, "The Hobbit"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	min := it.Book.Minified([]string{"the"})
	if min.Metadata.AuthorName != "" || min.Metadata.SeriesName != "" || *min.Metadata.TitleIgnorePrefix != "Hobbit, The" || min.NumTracks != 1 || min.NumAudioFiles != 2 || min.Size != 150 {
		t.Fatalf("minified book = %+v", min)
	}
	raw, err := json.Marshal(it.Book.Full(it.ID))
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatal(err)
	}
	metadata := full["metadata"].(map[string]any)
	if authors, ok := metadata["authors"].([]any); !ok || len(authors) != 0 {
		t.Fatalf("authors = %v, want []", metadata["authors"])
	}
	if series, ok := metadata["series"].([]any); !ok || len(series) != 0 {
		t.Fatalf("series = %v, want []", metadata["series"])
	}
	if v, ok := full["ebookFile"]; !ok || v != nil {
		t.Fatalf("ebookFile = %v (present %v), want null", v, ok)
	}
	tracks := it.Book.Expanded(it.ID, nil).Tracks
	if len(tracks) != 1 || tracks[0].ContentURL != "/api/items/li-b1/file/a1" || tracks[0].Title != "a.mp3" || tracks[0].StartOffset != 0 {
		t.Fatalf("tracks = %+v", tracks)
	}
}

func TestBookAuthorsAndSeriesKeepStoredOrder(t *testing.T) {
	d := dbtest.New(t)
	id := addBook(t, d, 1, "Saga")
	for _, stmt := range []string{
		`INSERT INTO authors (id,name,createdAt,updatedAt) VALUES ('a1','Jane Doe','` + stamp + `','` + stamp + `'),('a2','John Roe','` + stamp + `','` + stamp + `')`,
		`INSERT INTO bookAuthors (id,createdAt,bookId,authorId) VALUES ('x1','2024-01-02 03:04:06.000 +00:00','book1','a2'),('x2','` + stamp + `','book1','a1')`,
		`INSERT INTO series (id,name,createdAt,updatedAt) VALUES ('s1','Saga','` + stamp + `','` + stamp + `'),('s2','Prequel','` + stamp + `','` + stamp + `')`,
		`INSERT INTO bookSeries (id,sequence,createdAt,bookId,seriesId) VALUES ('y1','2','` + stamp + `','book1','s1'),('y2',NULL,'` + stamp + `','book1','s2')`,
	} {
		exec(t, d, stmt)
	}
	it, err := Get(context.Background(), d, id, Options{})
	if err != nil {
		t.Fatal(err)
	}
	md := it.Book.Minified(nil).Metadata
	// Authors follow the link's createdAt; series with equal createdAt follow rowid.
	if md.AuthorName != "Jane Doe, John Roe" || md.AuthorNameLF != "Doe, Jane, Roe, John" || md.SeriesName != "Saga #2, Prequel" {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestPodcastWithoutEpisodesAndListRow(t *testing.T) {
	d := dbtest.New(t)
	exec(t, d, `INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('p1','A Show',1,0,'[]','[]',7,?,?)`, stamp, stamp)
	exec(t, d, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt) VALUES ('li-p1','/p','p','p1','podcast',0,0,0,99,'[]',?,?)`, stamp, stamp)
	it, err := Get(context.Background(), d, "li-p1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if it.Podcast.Episodes != nil {
		t.Fatal("episodes loaded without Options.Episodes")
	}
	if a := it.Access(); !a.Explicit {
		t.Fatalf("podcast access = %+v", a)
	}
	min := it.Minified([]string{"a"}).Media.(PodcastMinified)
	if min.NumEpisodes != 0 || min.Size != 0 || *min.Metadata.TitleIgnorePrefix != "Show, A" {
		t.Fatalf("minified = %+v", min)
	}
	row := it.ListRow([]string{"a"}).Media.(PodcastMinified)
	if row.NumEpisodes != 7 || row.Size != 99 {
		t.Fatalf("list row = %+v", row)
	}
	raw, err := json.Marshal(it.Expanded(nil).Media)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if eps, ok := got["episodes"].([]any); !ok || len(eps) != 0 || got["numEpisodes"] != float64(0) {
		t.Fatalf("expanded podcast without episodes: %s", raw)
	}
}

func TestPodcastEpisodesLoadAndProject(t *testing.T) {
	d := dbtest.New(t)
	exec(t, d, `INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('p1','A Show',0,0,'[]','[]',2,?,?)`, stamp, stamp)
	exec(t, d, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,createdAt,updatedAt) VALUES ('li-p1','/p','p','p1','podcast',0,0,0,'[]',?,?)`, stamp, stamp)
	for _, e := range []string{"e1", "e2"} {
		exec(t, d, `INSERT INTO podcastEpisodes (id,podcastId,title,enclosureURL,enclosureSize,audioFile,chapters,extraData,createdAt,updatedAt) VALUES (?,'p1',?,'https://e/x.mp3',42,'{"ino":"a1","duration":12,"metadata":{"filename":"e.mp3","size":100}}','[]','{"guid":"g"}',?,?)`, e, "Episode "+e, stamp, stamp)
	}
	it, err := Get(context.Background(), d, "li-p1", Options{Episodes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Podcast.Episodes) != 2 {
		t.Fatalf("episodes = %+v", it.Podcast.Episodes)
	}
	episode := it.Podcast.Episodes[0].Expanded(it.ID)
	if *episode.Title != "Episode e1" || episode.AudioTrack.ContentURL != "/api/items/li-p1/file/a1" || episode.AudioTrack.Index != 1 ||
		episode.Size != 100 || *episode.Duration != 12 || *episode.GUID != "g" || *episode.Enclosure.Length != "42" || episode.PublishedAt != nil {
		t.Fatalf("expanded episode = %+v", episode)
	}
	media := it.Expanded(nil).Media.(PodcastExpanded)
	if media.NumEpisodes != 2 || media.Size != 200 {
		t.Fatalf("expanded podcast = %+v", media)
	}
	selected, err := Get(context.Background(), d, "li-p1", Options{Episodes: true, EpisodeIDs: []string{"e2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Podcast.Episodes) != 1 || selected.Podcast.Episodes[0].ID != "e2" {
		t.Fatalf("selected episodes = %+v", selected.Podcast.Episodes)
	}
}

func TestLoadQueryCountIsConstant(t *testing.T) {
	d, count := dbtest.Counting(t)
	var all []string
	for i := range 120 {
		all = append(all, addBook(t, d, i, "T"))
	}
	measure := func(ids []string) int64 {
		count.Store(0)
		if _, err := Load(context.Background(), d, ids, Options{}); err != nil {
			t.Fatal(err)
		}
		return count.Load()
	}
	// Loading batches by relation: the number of queries (items, books, authors,
	// series, podcasts) does not depend on the number of items.
	one, many := measure(all[:1]), measure(all)
	if one == 0 || one != many {
		t.Fatalf("queries: 1 item = %d, 120 items = %d", one, many)
	}
}
