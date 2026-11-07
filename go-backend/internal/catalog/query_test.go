package catalog

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// queryFixture is a book library l1 and a podcast library l2 with enough
// variety that every filter group and sort key orders and selects differently.
func queryFixture(t *testing.T, ignorePrefix bool) *Handler {
	t.Helper()
	db := catalogFixture(t)
	t.Cleanup(func() { _ = db.Close() })
	setup := []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY,value JSON)`,
		fmt.Sprintf(`INSERT INTO settings VALUES ('server-settings','{"sortingIgnorePrefix":%v,"sortingPrefixes":["the","a"]}')`, ignorePrefix),
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`,
		`CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT,feedURL TEXT,title TEXT,description TEXT,preventIndexing BOOLEAN,ownerName TEXT,ownerEmail TEXT)`,
		`CREATE TABLE mediaItemShares (id TEXT,mediaItemType TEXT,mediaItemId TEXT)`,
		`CREATE TABLE podcasts (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize BIGINT,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,podcastId TEXT,createdAt DATETIME,updatedAt DATETIME)`,
		`DELETE FROM libraryItems`, `DELETE FROM books`, `DELETE FROM bookAuthors`, `DELETE FROM bookSeries`, `DELETE FROM series`, `DELETE FROM authors`,
		`INSERT INTO authors VALUES ('a1','Ada Author','Author, Ada',NULL,NULL,NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),('a2','Bob Writer','Writer, Bob',NULL,NULL,NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO series VALUES ('s1','Alpha Saga','Alpha Saga',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),('s2','The Beta Cycle','Beta Cycle',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO libraryFolders VALUES ('fp','/media/pods','l2','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
	}
	// id, title, ignorePrefix title, year, publisher, language, explicit, abridged, duration, narrators, audio, ebook, tags, genres, asin, size, created day, isMissing
	type book struct {
		id, title, tip, year, publisher, lang string
		explicit, abridged                    int
		duration                              float64
		narrators, audio, ebook, tags, genres string
		asin                                  any
		size                                  int
		day                                   int
		missing                               int
		files                                 string
		authors, authorLF                     string
	}
	books := []book{
		{"b1", "The Alpha", "Alpha", "1995", "Pub A", "en", 0, 0, 10, `["Nora"]`, `[{"x":1}]`, `null`, `["red"]`, `["fiction"]`, "A1", 100, 1, 0, `[]`, "Ada Author", "Author, Ada"},
		{"b2", "Beta", "Beta", "1999", "Pub B", "fr", 1, 1, 20, `["Nora","Omar"]`, `[{"x":1},{"x":2}]`, `{"ino":"e"}`, `["blue","red"]`, `["mystery"]`, nil, 300, 2, 0, `[{"isSupplementary":true}]`, "Bob Writer", "Writer, Bob"},
		{"b3", "A Gamma", "Gamma", "2003", "Pub A", "en", 0, 0, 5, `[]`, `[]`, `{"ino":"e"}`, `["blue"]`, `["fiction","mystery"]`, "A3", 200, 3, 1, `[]`, "Ada Author", "Author, Ada"},
		{"b4", "delta", "delta", "2010", "Pub C", "en", 0, 0, 30, `["Omar"]`, `[{"x":1}]`, `null`, `[]`, `[]`, nil, 50, 4, 0, `[]`, "Bob Writer", "Writer, Bob"},
		{"b5", "Epsilon", "Epsilon", "2011", "Pub B", "de", 0, 0, 0, `null`, `[]`, `null`, `["green"]`, `["horror"]`, "A5", 400, 5, 0, `[]`, "Ada Author", "Author, Ada"},
		{"b6", "Alpha Two", "Alpha Two", "", "", "", 0, 0, 15, `[]`, `[{"x":1}]`, `null`, `[]`, `["fiction"]`, nil, 10, 6, 0, `[]`, "", ""},
	}
	for i, b := range books {
		created := fmt.Sprintf("2024-01-%02dT00:00:00Z", b.day)
		setup = append(setup,
			fmt.Sprintf(`INSERT INTO books (id,title,titleIgnorePrefix,publishedYear,publisher,language,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,asin,createdAt,updatedAt) VALUES ('%s','%s','%s','%s','%s','%s',%d,%d,%v,'%s','%s','%s','[]','%s','%s',%s,'%s','%s')`,
				b.id, b.title, b.tip, b.year, b.publisher, b.lang, b.explicit, b.abridged, b.duration, b.narrators, b.audio, b.ebook, b.tags, b.genres, sqlNullable(b.asin), created, created),
			fmt.Sprintf(`INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,libraryId,libraryFolderId,createdAt,updatedAt) VALUES ('i%d','ino%d','/media/books/%s','%s','%s','book',0,%d,0,'2024-02-%02dT00:00:00Z','2024-02-01T00:00:00Z','2024-03-%02dT00:00:00Z',%d,'%s','{}','%s','%s','%s','%s','l1','f1','%s','%s')`,
				i+1, i+1, b.id, b.id, b.id, b.missing, 7-b.day, 10-b.day, b.size, b.files, b.title, b.tip, b.authors, b.authorLF, created, created))
	}
	setup = append(setup,
		`INSERT INTO bookAuthors VALUES ('ba1','b1','a1','2024-01-01T00:00:00Z'),('ba2','b2','a2','2024-01-01T00:00:00Z'),('ba3','b3','a1','2024-01-01T00:00:00Z'),('ba4','b4','a2','2024-01-01T00:00:00Z'),('ba5','b5','a1','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookSeries VALUES ('bs1','1','b1','s1','2024-01-01T00:00:00Z'),('bs2','2','b2','s1','2024-01-01T00:00:00Z'),('bs3','3','b3','s2','2024-01-01T00:00:00Z'),('bs4','1','b4','s2','2024-01-01T00:00:00Z'),('bs5','2.5','b5','s2','2024-01-01T00:00:00Z')`,
		`INSERT INTO mediaProgresses VALUES ('m1','u1','b1',5,0,0,0,'2024-01-05T00:00:00Z','2024-01-05T00:00:00Z',NULL),('m2','u1','b2',20,0,1,0,'2024-01-04T00:00:00Z','2024-01-02T00:00:00Z','2024-01-04T00:00:00Z'),('m3','u1','b3',0,0.5,0,0,'2024-01-06T00:00:00Z','2024-01-03T00:00:00Z',NULL),('m4','u1','b4',0,0,0,0,'2024-01-07T00:00:00Z','2024-01-07T00:00:00Z',NULL),('m5','u2','b5',9,0,0,0,'2024-01-08T00:00:00Z','2024-01-08T00:00:00Z',NULL),('m6','u1','b5',0,0.4,1,0,'2024-01-09T00:00:00Z','2024-01-09T00:00:00Z','2024-01-09T00:00:00Z')`,
		`INSERT INTO feeds (id,entityType,entityId) VALUES ('f1','libraryItem','i2'),('f2','libraryItem','p2')`,
		`INSERT INTO mediaItemShares VALUES ('sh1','book','b3')`,
	)
	pods := []struct {
		id, title, tip, author, lang, tags, genres string
		explicit, eps, day, missing                int
	}{
		{"p1", "Science Now", "Science Now", "Zed", "en", `["sci"]`, `["science"]`, 0, 3, 1, 0},
		{"p2", "The Daily", "Daily", "amy", "fr", `["news"]`, `["news","science"]`, 1, 10, 2, 0},
		{"p3", "Chatter", "Chatter", "", "en", `[]`, `[]`, 0, 0, 3, 1},
	}
	for i, p := range pods {
		created := fmt.Sprintf("2024-01-%02dT00:00:00Z", p.day)
		setup = append(setup,
			fmt.Sprintf(`INSERT INTO podcasts (id,title,titleIgnorePrefix,author,language,explicit,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('%s','%s','%s','%s','%s',%d,'%s','%s',%d,'%s','%s')`, p.id, p.title, p.tip, p.author, p.lang, p.explicit, p.tags, p.genres, p.eps, created, created),
			fmt.Sprintf(`INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES ('%s','pino%d','/media/pods/%s','%s','%s','podcast',0,%d,0,'2024-02-%02dT00:00:00Z','2024-02-01T00:00:00Z','2024-03-%02dT00:00:00Z',%d,'[]','{}','%s','%s','l2','fp','%s','%s')`, p.id, i, p.id, p.id, p.id, p.missing, 5-p.day, p.day, 100*(4-p.day), p.title, p.tip, created, created))
	}
	for _, q := range setup {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	return New(db, config.Config{}, nil)
}

func sqlNullable(v any) string {
	if v == nil {
		return "NULL"
	}
	return "'" + v.(string) + "'"
}

func b64(s string) string { return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(s))) }

// TestLibraryQueryTable records the item ids (in order) and total of every
// filter group and sort key for a book and a podcast library, for an
// unrestricted and a restricted user. Set UPDATE_QUERY_GOLDEN=1 to rewrite
// testdata/query_golden.json.
func TestLibraryQueryTable(t *testing.T) {
	users := map[string]store.User{
		"root":       {ID: "u1", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)},
		"restricted": {ID: "u1", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["l1","l2"],"accessAllTags":false,"accessExplicitContent":false,"itemTagsSelected":["red","sci"],"selectedTagsNotAccessible":false}`)},
		"denytags":   {ID: "u2", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true,"itemTagsSelected":["blue"],"selectedTagsNotAccessible":true}`)},
	}
	filters := []string{"", "issues", "recent", "feed-open", "share-open", "explicit", "abridged",
		"genres." + b64("fiction"), "genres." + b64("science"), "genres." + b64("none"), "tags." + b64("red"), "tags." + b64("blue"), "tags." + b64("sci"),
		"narrators." + b64("Nora"), "narrators." + b64("Omar"), "languages." + b64("en"), "languages." + b64("fr"),
		"publishers." + b64("Pub A"), "publishers." + b64("Pub B"), "authors." + b64("a1"), "authors." + b64("a2"),
		"series." + b64("s1"), "series." + b64("s2"), "series.no-series",
		"progress.not-finished", "progress.not-started", "progress.finished", "progress.in-progress", "progress.audio-in-progress", "progress.ebook-in-progress", "progress.ebook-finished", "progress.bogus",
		"tracks.none", "tracks.multi", "tracks.single", "tracks." + b64("x"),
		"ebooks.ebook", "ebooks.no-ebook", "ebooks.supplementary", "ebooks.no-supplementary", "ebooks.bogus",
		"publishedDecades." + b64("1990"), "publishedDecades." + b64("2000"), "publishedDecades." + b64("2010"), "publishedDecades." + b64("abc"),
		"missing.authors", "missing.series", "missing.issues", "missing.asin", "missing.isbn", "missing.subtitle", "missing.publishedYear", "missing.description", "missing.publisher", "missing.language", "missing.cover", "missing.genres", "missing.tags", "missing.narrators", "missing.chapters", "missing.bogus",
		"bogus.x", "bogus", "genres", "languages", ".x", "genres.!!notbase64!!", "series." + b64("s1") + "x"}
	sorts := []string{"", "addedAt", "size", "birthtimeMs", "mtimeMs", "media.duration", "media.metadata.publishedYear", "media.metadata.authorName", "media.metadata.authorNameLF",
		"media.metadata.title", "media.metadata.author", "media.numTracks", "sequence", "progress", "progress.updatedAt", "progress.createdAt", "progress.finishedAt", "progress.bogus", "bogus"}
	type request struct{ lib, query string }
	var requests []request
	for _, lib := range []string{"l1", "l2"} {
		for _, f := range filters {
			requests = append(requests, request{lib, "filter=" + f})
		}
		for _, s := range sorts {
			for _, d := range []string{"0", "1"} {
				if s == "random" {
					continue
				}
				requests = append(requests, request{lib, "sort=" + s + "&desc=" + d})
				requests = append(requests, request{lib, "sort=" + s + "&desc=" + d + "&filter=series." + b64("s2")})
			}
		}
		requests = append(requests, request{lib, "sort=random"}, request{lib, "sort=addedAt&limit=2&page=1"}, request{lib, "sort=addedAt&limit=2&page=9"})
	}
	for _, s := range []string{"", "media.metadata.title", "addedAt", "sequence"} {
		for _, d := range []string{"0", "1"} {
			for _, f := range []string{"", "series." + b64("s1"), "progress.finished", "missing.authors", "genres." + b64("fiction")} {
				requests = append(requests, request{"l1", "collapseseries=1&sort=" + s + "&desc=" + d + "&filter=" + f})
			}
		}
	}
	got := map[string]string{}
	for _, ignore := range []bool{false, true} {
		for userName, user := range users {
			h := queryFixture(t, ignore)
			for _, rq := range requests {
				target := "/api/libraries/" + rq.lib + "/items?" + rq.query
				w := httptest.NewRecorder()
				if !h.Handle(w, httptest.NewRequest("GET", target, nil), user) {
					t.Fatalf("%s did not route", target)
				}
				key := fmt.Sprintf("%v/%s/%s", ignore, userName, target)
				if w.Code != 200 {
					got[key] = fmt.Sprintf("status %d", w.Code)
					continue
				}
				var body struct {
					Results []struct {
						ID string `json:"id"`
					} `json:"results"`
					Total int `json:"total"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				ids := make([]string, 0, len(body.Results))
				for _, r := range body.Results {
					ids = append(ids, r.ID)
				}
				if strings.Contains(rq.query, "sort=random") {
					// Order is random: record the sorted set.
					sortStrings(ids)
				}
				got[key] = fmt.Sprintf("%s total=%d", strings.Join(ids, ","), body.Total)
			}
		}
	}
	const path = "testdata/query_golden.json"
	if os.Getenv("UPDATE_QUERY_GOLDEN") == "1" {
		raw, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Errorf("golden has %d cases, test ran %d", len(want), len(got))
	}
	for k, v := range got {
		if want[k] != v {
			t.Errorf("%s\n got  %s\n want %s", k, v, want[k])
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
