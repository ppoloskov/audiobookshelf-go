package catalog

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// The personalized shelves are recorded by value for each user, library and
// settings variant. Regenerate with UPDATE_GOLDEN=1 only for an intended
// behaviour change.

const (
	goldenAudio = `[{"ino":"audio-1","metadata":{"filename":"01.mp3","ext":".mp3","path":"/media/books/01.mp3","relPath":"01.mp3","size":100,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"duration":12.5,"mimeType":"audio/mpeg","exclude":false}]`
	goldenEbook = `{"ino":"eb","metadata":{"filename":"a.epub","ext":".epub","path":"/media/books/a.epub","relPath":"a.epub","size":5,"mtimeMs":1,"ctimeMs":1,"birthtimeMs":1},"ebookFormat":"epub","addedAt":1,"updatedAt":1}`
	goldenOld   = "2024-01-01T00:00:00Z"
	goldenNew   = "2099-01-01T00:00:00Z"
)

func seedShelfGolden(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%v: %s", err, query)
		}
	}
	exec(`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,podcastId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`)
	exec(`CREATE TABLE podcasts (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`)
	exec(`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize BIGINT,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,podcastId TEXT,createdAt DATETIME,updatedAt DATETIME)`)
	exec(`UPDATE libraries SET settings='{}' WHERE id='l1'`)
	exec(`UPDATE libraryItems SET createdAt=? WHERE id='i1'`, goldenOld)

	type book struct {
		n        int
		audio    bool
		ebook    bool
		explicit int
		tags     string
		created  string
	}
	for _, b := range []book{
		{2, true, false, 0, `[]`, goldenNew}, {3, true, false, 0, `[]`, goldenNew}, {4, false, true, 0, `[]`, goldenNew},
		{5, true, false, 0, `["x"]`, goldenNew}, {6, true, false, 1, `[]`, goldenNew}, {7, true, false, 0, `["secret"]`, goldenOld},
		{8, true, false, 0, `[]`, goldenNew}, {9, false, true, 0, `[]`, goldenOld}, {10, true, false, 0, `[]`, goldenOld},
	} {
		audio, ebook := `[]`, `null`
		if b.audio {
			audio = goldenAudio
		}
		if b.ebook {
			ebook = goldenEbook
		}
		bookID, itemID, title := fmt.Sprintf("b%d", b.n), fmt.Sprintf("i%d", b.n), fmt.Sprintf("Golden Book %d", b.n)
		exec(`INSERT INTO books VALUES (?,?,?,NULL,'2024',NULL,NULL,NULL,NULL,NULL,'en',?,0,NULL,12.5,'[]',?,?,'[]',?,'["fiction"]',?,?)`, bookID, title, title, b.explicit, audio, ebook, b.tags, b.created, b.created)
		exec(`INSERT INTO libraryItems VALUES (?,?,?,?,?,'book',0,0,0,?,?,?,100,NULL,NULL,'[]','{}',?,?,'','','l1','f1',?,?)`, itemID, itemID, "/media/books/"+title, title, bookID, goldenOld, goldenOld, goldenOld, title, title, b.created, b.created)
	}
	for _, a := range [][2]string{{"a2", goldenNew}, {"a3", goldenNew}, {"a4", goldenOld}} {
		exec(`INSERT INTO authors VALUES (?,?,?,NULL,NULL,NULL,'l1',?,?)`, a[0], "Author "+a[0], a[0]+", Author", a[1], a[1])
	}
	for _, rel := range [][2]string{{"b2", "a2"}, {"b5", "a2"}, {"b6", "a3"}, {"b7", "a4"}, {"b3", "a1"}} {
		exec(`INSERT INTO bookAuthors VALUES (?,?,?,?)`, "ba-"+rel[0], rel[0], rel[1], goldenOld)
	}
	for _, s := range [][2]string{{"s2", goldenNew}, {"s3", goldenNew}, {"s4", goldenNew}, {"s5", goldenOld}} {
		exec(`INSERT INTO series VALUES (?,?,?,NULL,'l1',?,?)`, s[0], "Series "+s[0], "Series "+s[0], s[1], s[1])
	}
	for _, rel := range [][3]string{{"b2", "s1", "2"}, {"b3", "s1", "10"}, {"b5", "s2", "1"}, {"b6", "s2", "2"}, {"b8", "s3", ""}, {"b10", "s4", "1"}, {"b9", "s5", "1.5"}} {
		var seq any = rel[2]
		if rel[2] == "" {
			seq = nil
		}
		exec(`INSERT INTO bookSeries VALUES (?,?,?,?,?)`, "bs-"+rel[0], seq, rel[0], rel[1], goldenOld)
	}

	progress := func(user, media, podcast string, current, ebook float64, finished, hidden int, updated string) {
		exec(`INSERT INTO mediaProgresses VALUES (?,?,?,?,?,?,?,?,?,?,NULL)`, user+media, user, media, podcast, current, ebook, finished, hidden, updated, goldenOld)
	}
	progress("listener", "b1", "", 12.5, 0, 1, 0, "2024-02-01T00:00:00Z")
	progress("listener", "b4", "", 0, 0.5, 0, 0, "2024-02-02T00:00:00Z")
	progress("listener", "b5", "", 10, 0, 0, 0, "2024-02-03T00:00:00Z")
	progress("listener", "b7", "", 12.5, 0, 1, 0, "2024-02-04T00:00:00Z")
	progress("listener", "b9", "", 0, 1, 1, 0, "2024-02-05T00:00:00Z")
	progress("listener", "b10", "", 3, 0, 0, 0, "2024-02-06T00:00:00Z")
	progress("finisher", "b1", "", 12.5, 0, 1, 0, "2024-03-01T00:00:00Z")
	progress("finisher", "b2", "", 12.5, 0, 1, 0, "2024-03-02T00:00:00Z")
	progress("root", "b5", "", 10, 0, 0, 1, "2024-03-03T00:00:00Z")
	progress("root", "b3", "", 4, 0, 0, 0, "2024-03-04T00:00:00Z")
	progress("root", "b1", "", 12.5, 0, 1, 0, "2024-03-05T00:00:00Z")

	exec(`INSERT INTO libraryFolders VALUES ('f2','/media/podcasts','l2',?,?)`, goldenOld, goldenOld)
	for _, p := range [][4]string{{"p1", "0", goldenNew, `[]`}, {"p2", "1", goldenOld, `[]`}, {"p3", "0", goldenNew, `["secret"]`}} {
		exec(`INSERT INTO podcasts VALUES (?,?,?,'Host',NULL,NULL,NULL,NULL,NULL,NULL,NULL,'en','episodic',?,0,NULL,NULL,0,3,NULL,?,'[]',3,?,?)`, p[0], "Cast "+p[0], "Cast "+p[0], p[1], p[3], p[2], p[2])
		exec(`INSERT INTO libraryItems VALUES (?,?,?,?,?,'podcast',0,0,0,?,?,?,500,NULL,NULL,'[]','{}',?,?,NULL,NULL,'l2','f2',?,?)`, "ip"+p[0][1:], "ino-"+p[0], "/media/podcasts/"+p[0], p[0], p[0], goldenOld, goldenOld, goldenOld, "Cast "+p[0], "Cast "+p[0], p[2], p[2])
		for n := 1; n <= 3; n++ {
			created := fmt.Sprintf("2099-01-0%dT00:00:00Z", n)
			if n == 3 {
				created = goldenOld
			}
			exec(`INSERT INTO podcastEpisodes VALUES (?,?,'1',?,'full',?,'Sub','Desc','2024-01-01','https://example.test/e.mp3',123,'audio/mpeg',?,'{"ino":"ep","metadata":{"filename":"e.mp3","size":123},"duration":30}','[]','{"guid":"g"}',?,?,?)`,
				fmt.Sprintf("e%s%d", p[0], n), n, n, fmt.Sprintf("Episode %s-%d", p[0], n), created, p[0], created, created)
		}
	}
	progress("listener", "ep1"+"1", "p1", 5, 0, 0, 0, "2024-04-01T00:00:00Z")
	progress("listener", "ep1"+"2", "p1", 30, 0, 1, 0, "2024-04-02T00:00:00Z")
	progress("listener", "ep3"+"1", "p3", 30, 0, 1, 0, "2024-04-03T00:00:00Z")
	progress("listener", "ep2"+"1", "p2", 30, 0, 1, 0, "2024-04-04T00:00:00Z")
	progress("root", "ep1"+"3", "p1", 7, 0, 0, 0, "2024-04-05T00:00:00Z")
	progress("root", "ep2"+"2", "p2", 7, 0, 0, 1, "2024-04-06T00:00:00Z")
}

func TestPersonalizedShelvesGolden(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	seedShelfGolden(t, db)
	h := New(db, config.Config{}, nil, testDeps())
	perms := func(extra string) json.RawMessage {
		return json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true,"accessAllTags":true` + extra + `}`)
	}
	users := map[string]store.User{
		"root":     {ID: "root", Type: "root", IsActive: true, Permissions: perms(``)},
		"listener": {ID: "listener", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":false,"accessAllTags":false,"itemTagsSelected":["secret"],"selectedTagsNotAccessible":true}`)},
		"finisher": {ID: "finisher", Type: "user", IsActive: true, Permissions: perms(``), ExtraData: json.RawMessage(`{"seriesHideFromContinueListening":["s4"]}`)},
		"tagged":   {ID: "tagged", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":false,"accessAllTags":false,"itemTagsSelected":["x"]}`)},
		"inactive": {ID: "inactive", Type: "user", IsActive: false, Permissions: perms(``)},
	}
	settings := map[string]string{"plain": `{}`, "later+hideSingle": `{"hideSingleBookSeries":true,"onlyShowLaterBooksInContinueSeries":true}`}
	got := map[string]any{}
	for settingName, setting := range settings {
		if _, err := db.Exec(`UPDATE libraries SET settings=? WHERE id='l1'`, setting); err != nil {
			t.Fatal(err)
		}
		for userName, user := range users {
			for _, lib := range []string{"l1", "l2"} {
				for _, query := range []string{"", "?minified=1&include=numEpisodesIncomplete", "?limit=2"} {
					w := httptest.NewRecorder()
					if !h.Handle(w, httptest.NewRequest("GET", "/api/libraries/"+lib+"/personalized"+query, nil), user) || w.Code != 200 {
						t.Fatalf("%s %s %s status=%d body=%s", userName, lib, query, w.Code, w.Body.String())
					}
					var shelves []map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &shelves); err != nil {
						t.Fatal(err)
					}
					// Discover is random: its order always, its membership under a limit.
					kept := shelves[:0]
					for _, shelf := range shelves {
						if shelf["id"] == "discover" {
							if query == "?limit=2" {
								continue
							}
							entities := shelf["entities"].([]any)
							sort.Slice(entities, func(i, j int) bool {
								return entities[i].(map[string]any)["id"].(string) < entities[j].(map[string]any)["id"].(string)
							})
						}
						kept = append(kept, shelf)
					}
					shelves = kept
					got[settingName+"/"+userName+"/"+lib+query] = shelves
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "personalized_golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(encoded)) {
		t.Fatalf("personalized shelves changed; diff the output against %s (UPDATE_GOLDEN=1 rewrites it)", path)
	}
}
