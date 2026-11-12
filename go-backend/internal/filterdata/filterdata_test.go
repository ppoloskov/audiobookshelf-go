package filterdata

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

func newDB(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.New(t)
	exec(t, d, `INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Books',0,'books','book','google','{}','{}',?,?),('l2','Casts',1,'podcast','podcast','itunes','{}','{}',?,?)`, stamp, stamp, stamp, stamp)
	return d
}

func exec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func addBook(t *testing.T, d *db.DB, id, tags, genres, narrators, publisher, year, language string, missing bool) {
	t.Helper()
	exec(t, d, `INSERT INTO books(id,title,tags,genres,narrators,publisher,publishedYear,language,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, id, tags, genres, narrators, publisher, year, language, stamp, stamp)
	exec(t, d, `INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES(?,?,?,?,'book',0,?,0,1,'[]','{}',?,?,'l1')`, "li-"+id, "/"+id, id, id, missing, stamp, stamp)
}

func get(t *testing.T, c *Cache, d *db.DB, library, mediaType string) Data {
	t.Helper()
	data, err := c.Get(context.Background(), d, library, mediaType)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBookLibraryMenus(t *testing.T) {
	d := newDB(t)
	addBook(t, d, "b1", `["zeta","Alpha"]`, `["Fantasy"]`, `["Zed Narr"]`, "Orbit", "1987", "English", false)
	addBook(t, d, "b2", `["alpha","Alpha"]`, `["fantasy","Horror"]`, `[]`, "Orbit", "1995", "english", true)
	addBook(t, d, "b3", `[]`, `[]`, `["amy"]`, "Tor", "not a year", "", false)
	addBook(t, d, "b4", `not json`, `[]`, `[]`, "", "3000", "", false) // a bad list is empty; empty text and out-of-range years are no menu entry
	exec(t, d, `INSERT INTO authors(id,name,createdAt,updatedAt,libraryId) VALUES('a1','bob',?,?,'l1'),('a2','Amy',?,?,'l1'),('a3','Other lib',?,?,'l2')`, stamp, stamp, stamp, stamp, stamp, stamp)
	exec(t, d, `INSERT INTO series(id,name,createdAt,updatedAt,libraryId) VALUES('s1','Saga',?,?,'l1'),('s2',NULL,?,?,'l1')`, stamp, stamp, stamp, stamp)

	got := get(t, NewCache(), d, "l1", "book")
	want := Data{
		Tags:             []string{"Alpha", "alpha", "zeta"}, // distinct by exact value, case-insensitive order
		Genres:           []string{"Fantasy", "fantasy", "Horror"},
		Narrators:        []string{"amy", "Zed Narr"},
		Publishers:       []string{"Orbit", "Tor"},
		PublishedDecades: []string{"1980", "1990"},
		Languages:        []string{"English", "english"},
		Authors:          []Named{{"a2", "Amy"}, {"a1", "bob"}}, // this library only
		Series:           []Named{{"s2", "No Title"}, {"s1", "Saga"}},
		BookCount:        4, AuthorCount: 2, SeriesCount: 2, NumIssues: 1,
	}
	got.LoadedAt = 0
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestEmptyLibraryEncodesEmptyLists(t *testing.T) {
	raw, err := json.Marshal(get(t, NewCache(), newDB(t), "l1", "book"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"authors", "series", "genres", "tags", "narrators", "languages", "publishers", "publishedDecades"} {
		if list, ok := doc[field].([]any); !ok || len(list) != 0 {
			t.Errorf("%s = %v, clients need []", field, doc[field])
		}
	}
	if _, err := NewCache().Get(context.Background(), newDB(t), "l1", "video"); err == nil {
		t.Fatal("unknown media type computed")
	}
}

func TestPodcastLibrary(t *testing.T) {
	d := newDB(t)
	exec(t, d, `INSERT INTO podcasts(id,title,tags,genres,language,createdAt,updatedAt) VALUES('p1','Show','["t"]','["News"]','en',?,?)`, stamp, stamp)
	exec(t, d, `INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES('li-p1','/p','p','p1','podcast',0,0,1,1,'[]','{}',?,?,'l2')`, stamp, stamp)
	got := get(t, NewCache(), d, "l2", "podcast")
	if got.Authors == nil || got.Series == nil || got.PodcastCount != 1 || got.NumIssues != 1 || !reflect.DeepEqual(got.Tags, []string{"t"}) || !reflect.DeepEqual(got.Genres, []string{"News"}) || !reflect.DeepEqual(got.Languages, []string{"en"}) || got.BookCount != 0 {
		t.Fatalf("%+v", got)
	}
}

// The document is computed once, served from memory, and rebuilt after Invalidate.
func TestInvalidateRecomputes(t *testing.T) {
	d := newDB(t)
	addBook(t, d, "b1", `["one"]`, `[]`, `[]`, "P", "2000", "en", false)
	c := NewCache()
	if got := get(t, c, d, "l1", "book"); got.BookCount != 1 {
		t.Fatalf("%+v", got)
	}
	addBook(t, d, "b2", `["two"]`, `[]`, `[]`, "P", "2000", "en", false)
	if got := get(t, c, d, "l1", "book"); got.BookCount != 1 {
		t.Fatalf("a cached document was recomputed: %+v", got)
	}
	c.Invalidate("l2") // another library
	if got := get(t, c, d, "l1", "book"); got.BookCount != 1 {
		t.Fatalf("invalidating l2 dropped l1: %+v", got)
	}
	c.Invalidate("l1")
	if got := get(t, c, d, "l1", "book"); got.BookCount != 2 || !reflect.DeepEqual(got.Tags, []string{"one", "two"}) {
		t.Fatalf("not recomputed: %+v", got)
	}
	addBook(t, d, "b3", `[]`, `[]`, `[]`, "P", "2000", "en", false)
	c.Invalidate("")
	if got := get(t, c, d, "l1", "book"); got.BookCount != 3 {
		t.Fatalf("Invalidate(\"\") kept l1: %+v", got)
	}
	(*Cache)(nil).Invalidate("l1") // a nil cache ignores it
}

func TestTTLBackstop(t *testing.T) {
	d := newDB(t)
	addBook(t, d, "b1", `[]`, `[]`, `[]`, "P", "2000", "en", false)
	c := NewCache()
	get(t, c, d, "l1", "book")
	addBook(t, d, "b2", `[]`, `[]`, `[]`, "P", "2000", "en", false)
	c.mu.Lock()
	age := func(by time.Duration) {
		data := c.libs["l1"]
		data.LoadedAt = time.Now().Add(-by).UnixMilli()
		c.libs["l1"] = data
	}
	age(TTL - time.Minute)
	c.mu.Unlock()
	if got := get(t, c, d, "l1", "book"); got.BookCount != 1 {
		t.Fatalf("rebuilt before the TTL: %+v", got)
	}
	c.mu.Lock()
	age(TTL + time.Minute)
	c.mu.Unlock()
	if got := get(t, c, d, "l1", "book"); got.BookCount != 2 {
		t.Fatalf("not rebuilt after the TTL: %+v", got)
	}
}

func TestResultsAreCopies(t *testing.T) {
	d := newDB(t)
	addBook(t, d, "b1", `["a"]`, `[]`, `[]`, "P", "2000", "en", false)
	c := NewCache()
	get(t, c, d, "l1", "book").Tags[0] = "mutated"
	if got := get(t, c, d, "l1", "book"); got.Tags[0] != "a" {
		t.Fatal("callers can mutate the cached document")
	}
}

func TestConcurrentGetAndInvalidate(t *testing.T) {
	d := newDB(t)
	addBook(t, d, "b1", `["a"]`, `[]`, `[]`, "P", "2000", "en", false)
	c := NewCache()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if i%2 == 0 {
					c.Invalidate("l1")
				} else if _, err := c.Get(context.Background(), d, "l1", "book"); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
