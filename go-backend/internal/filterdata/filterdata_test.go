package filterdata

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	schemaassets "audiobookshelf-compat/contracts/parity"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "db.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	exec(t, db, schemaassets.SchemaSQL)
	exec(t, db, `INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Books',0,'books','book','google','{}','{}',?,?),('l2','Casts',1,'podcast','podcast','itunes','{}','{}',?,?)`, stamp, stamp, stamp, stamp)
	return db
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func addBook(t *testing.T, db *sql.DB, id, tags, genres, narrators, publisher, year, language string, missing bool) {
	t.Helper()
	exec(t, db, `INSERT INTO books(id,title,tags,genres,narrators,publisher,publishedYear,language,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, id, tags, genres, narrators, publisher, year, language, stamp, stamp)
	exec(t, db, `INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES(?,?,?,?,'book',0,?,0,1,'[]','{}',?,?,'l1')`, "li-"+id, "/"+id, id, id, missing, stamp, stamp)
}

func TestLoadBookLibraryCollectsSortedDistinctValues(t *testing.T) {
	db := openDB(t)
	addBook(t, db, "b1", `["zeta","Alpha"]`, `["Fantasy"]`, `["Zed Narr"]`, "Orbit", "1987", "English", false)
	addBook(t, db, "b2", `["alpha","Alpha"]`, `["fantasy","Horror"]`, `[]`, "Orbit", "1995", "english", true)
	addBook(t, db, "b3", `[]`, `[]`, `["amy"]`, "Tor", "not a year", "", false)
	addBook(t, db, "b4", `[]`, `[]`, `[]`, "", "", "", false) // empty publisher/language are falsy upstream
	exec(t, db, `INSERT INTO authors(id,name,createdAt,updatedAt,libraryId) VALUES('a1','bob',?,?,'l1'),('a2','Amy',?,?,'l1'),('a3','Other lib',?,?,'l2')`, stamp, stamp, stamp, stamp, stamp, stamp)
	exec(t, db, `INSERT INTO series(id,name,createdAt,updatedAt,libraryId) VALUES('s1','Saga',?,?,'l1'),('s2',NULL,?,?,'l1')`, stamp, stamp, stamp, stamp)

	d, err := Load(context.Background(), db, "l1", "book")
	if err != nil {
		t.Fatal(err)
	}
	// Distinct by exact value, ordered case-insensitively.
	if want := []string{"Alpha", "alpha", "zeta"}; !reflect.DeepEqual(d.Tags, want) {
		t.Errorf("tags %v, want %v", d.Tags, want)
	}
	if want := []string{"Fantasy", "fantasy", "Horror"}; !reflect.DeepEqual(d.Genres, want) {
		t.Errorf("genres %v, want %v", d.Genres, want)
	}
	if want := []string{"amy", "Zed Narr"}; !reflect.DeepEqual(d.Narrators, want) {
		t.Errorf("narrators %v, want %v", d.Narrators, want)
	}
	if want := []string{"Orbit", "Tor"}; !reflect.DeepEqual(d.Publishers, want) {
		t.Errorf("publishers %v", d.Publishers)
	}
	if want := []string{"1980", "1990"}; !reflect.DeepEqual(d.PublishedDecades, want) {
		t.Errorf("decades %v, want %v", d.PublishedDecades, want)
	}
	if want := []string{"English", "english"}; !reflect.DeepEqual(d.Languages, want) {
		t.Errorf("languages %v", d.Languages)
	}
	if d.BookCount != 4 || d.NumIssues != 1 || d.PodcastCount != 0 {
		t.Errorf("counts book=%d issues=%d podcast=%d", d.BookCount, d.NumIssues, d.PodcastCount)
	}
	// Authors of this library only, by lowercase name; a nameless series is "No Title".
	if want := []Named{{"a2", "Amy"}, {"a1", "bob"}}; !reflect.DeepEqual(d.Authors, want) || d.AuthorCount != 2 {
		t.Errorf("authors %v", d.Authors)
	}
	if want := []Named{{"s2", "No Title"}, {"s1", "Saga"}}; !reflect.DeepEqual(d.Series, want) || d.SeriesCount != 2 {
		t.Errorf("series %v", d.Series)
	}
}

func TestLoadEmptyLibraryHasEmptyListsNotNull(t *testing.T) {
	d, err := Load(context.Background(), openDB(t), "l1", "book")
	if err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string]bool{"authors": d.Authors == nil, "series": d.Series == nil, "genres": d.Genres == nil, "tags": d.Tags == nil, "narrators": d.Narrators == nil, "languages": d.Languages == nil, "publishers": d.Publishers == nil, "decades": d.PublishedDecades == nil} {
		if list {
			t.Errorf("%s is nil, clients need []", name)
		}
	}
}

func TestLoadRejectsUnknownMediaType(t *testing.T) {
	if _, err := Load(context.Background(), openDB(t), "l1", "video"); err == nil {
		t.Fatal("unknown media type loaded")
	}
}

func TestLoadPodcastLibrary(t *testing.T) {
	db := openDB(t)
	exec(t, db, `INSERT INTO podcasts(id,title,tags,genres,language,createdAt,updatedAt) VALUES('p1','Show','["t"]','["News"]','en',?,?)`, stamp, stamp)
	exec(t, db, `INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES('li-p1','/p','p','p1','podcast',0,0,0,1,'[]','{}',?,?,'l2')`, stamp, stamp)
	d, err := Load(context.Background(), db, "l2", "podcast")
	if err != nil {
		t.Fatal(err)
	}
	if d.PodcastCount != 1 || !reflect.DeepEqual(d.Tags, []string{"t"}) || !reflect.DeepEqual(d.Genres, []string{"News"}) || !reflect.DeepEqual(d.Languages, []string{"en"}) {
		t.Fatalf("%+v", d)
	}
}

// The filter menus serve the cached entry for 30 minutes (libraryFilters.js:455-461)
// and rebuild it afterwards, so counts go stale in between.
func TestCacheServesEntryUntilTTLThenReloads(t *testing.T) {
	db := openDB(t)
	addBook(t, db, "b1", `["one"]`, `[]`, `[]`, "P", "2000", "en", false)
	c := NewCache()
	ctx := context.Background()
	d, err := c.Get(ctx, db, "l1", "book")
	if err != nil || d.BookCount != 1 {
		t.Fatalf("first load %+v %v", d, err)
	}
	addBook(t, db, "b2", `["two"]`, `[]`, `[]`, "P", "2000", "en", false)
	if d, _ = c.Get(ctx, db, "l1", "book"); d.BookCount != 1 {
		t.Fatalf("a fresh entry was rebuilt: %+v", d)
	}
	// Age the entry just under and just over the TTL.
	stored, _ := c.Fresh("l1")
	stored.LoadedAt = time.Now().Add(-TTL + time.Minute).UnixMilli()
	c.Store("l1", stored)
	if d, _ = c.Get(ctx, db, "l1", "book"); d.BookCount != 1 {
		t.Fatalf("entry rebuilt before the TTL: %+v", d)
	}
	stored.LoadedAt = time.Now().Add(-TTL - time.Minute).UnixMilli()
	c.Store("l1", stored)
	if _, ok := c.Fresh("l1"); ok {
		t.Fatal("an entry older than the TTL is fresh")
	}
	if d, _ = c.Get(ctx, db, "l1", "book"); d.BookCount != 2 || !reflect.DeepEqual(d.Tags, []string{"one", "two"}) {
		t.Fatalf("expired entry not rebuilt: %+v", d)
	}
	c.Invalidate("l1")
	if _, ok := c.Fresh("l1"); ok {
		t.Fatal("Invalidate kept the entry")
	}
}

func TestCacheReturnsCopies(t *testing.T) {
	c := NewCache()
	c.Store("l1", Data{LoadedAt: time.Now().UnixMilli(), Tags: []string{"a"}})
	d, _ := c.Fresh("l1")
	d.Tags[0] = "mutated"
	if again, _ := c.Fresh("l1"); again.Tags[0] != "a" {
		t.Fatal("callers can mutate the cached entry")
	}
}

func TestInvalidateAllDropsEveryLibrary(t *testing.T) {
	c := NewCache()
	fresh := Data{LoadedAt: time.Now().UnixMilli()}
	c.Store("l1", fresh)
	c.Store("l2", fresh)
	c.Invalidate("")
	if _, ok := c.Fresh("l1"); ok {
		t.Fatal("l1 survived")
	}
	if _, ok := c.Fresh("l2"); ok {
		t.Fatal("l2 survived")
	}
}

// Patches append in place (unsorted, counts untouched) and are no-ops without an entry.
func TestPatchesAppendToExistingEntryOnly(t *testing.T) {
	c := NewCache()
	c.AddAuthor("l1", "Ghost", "a0")
	c.AddBook("l1", BookValues{Genres: []string{"x"}})
	if _, ok := c.Fresh("l1"); ok {
		t.Fatal("a patch created an entry")
	}

	c.Store("l1", Data{LoadedAt: time.Now().UnixMilli(), Authors: []Named{{"a1", "Zed"}}, Genres: []string{"Fantasy"}, AuthorCount: 1})
	c.AddAuthor("l1", "Amy", "a2")
	c.AddAuthor("l1", "Amy again", "a2") // same id: ignored
	c.AddSeries("l1", "Saga", "s1")
	c.AddSeries("l1", "Saga", "s1")
	c.AddBook("l1", BookValues{Genres: []string{"Fantasy", "Horror", ""}, Tags: []string{"t"}, Languages: []string{"en"}})
	d, _ := c.Fresh("l1")
	if want := []Named{{"a1", "Zed"}, {"a2", "Amy"}}; !reflect.DeepEqual(d.Authors, want) {
		t.Errorf("authors %v (appended in place, not sorted)", d.Authors)
	}
	if !reflect.DeepEqual(d.Series, []Named{{"s1", "Saga"}}) {
		t.Errorf("series %v", d.Series)
	}
	if !reflect.DeepEqual(d.Genres, []string{"Fantasy", "Horror"}) || !reflect.DeepEqual(d.Tags, []string{"t"}) || !reflect.DeepEqual(d.Languages, []string{"en"}) {
		t.Errorf("book values %+v", d)
	}
	if d.AuthorCount != 1 {
		t.Errorf("patches changed the stale count: %d", d.AuthorCount)
	}

	c.RemoveAuthor("l1", "a1")
	c.RemoveSeries("l1", "s1")
	c.StoreNumIssues("l1", 4)
	d, _ = c.Fresh("l1")
	if !reflect.DeepEqual(d.Authors, []Named{{"a2", "Amy"}}) || len(d.Series) != 0 || d.NumIssues != 4 {
		t.Errorf("after removals %+v", d)
	}
}

func TestResetIssuesRecountsOnlyAnExistingEntry(t *testing.T) {
	db := openDB(t)
	addBook(t, db, "b1", `[]`, `[]`, `[]`, "P", "2000", "en", true)
	c := NewCache()
	c.ResetIssues(context.Background(), db, "l1")
	if _, ok := c.Fresh("l1"); ok {
		t.Fatal("ResetIssues created an entry")
	}
	c.Store("l1", Data{LoadedAt: time.Now().UnixMilli()})
	c.ResetIssues(context.Background(), db, "l1")
	if d, _ := c.Fresh("l1"); d.NumIssues != 1 {
		t.Fatalf("numIssues=%d", d.NumIssues)
	}
}

func TestNilCachePatchesAreNoOps(t *testing.T) {
	var c *Cache
	c.AddAuthor("l1", "a", "1")
	c.RemoveSeries("l1", "1")
}
