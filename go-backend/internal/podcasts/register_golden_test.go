package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

var updateRegisterGolden = flag.Bool("update-register-golden", false, "rewrite testdata/register_episode.golden.json")

var registerCases = map[string]string{
	"feed":    `{"title":"Episode One","subtitle":"Sub","description":"<p>Hi</p>","pubDate":"Mon, 02 Jan 2006 15:04:05 GMT","episodeType":"bonus","season":"2","episode":"4","publishedAt":1136214245000,"guid":"g-1","enclosure":{"url":"https://e.example/1.mp3","type":"audio/mpeg","length":"1234"},"chapters":[{"id":0,"title":"c","start":0,"end":1}]}`,
	"minimal": `{"title":"Only","enclosure":{"url":"https://e.example/2.mp3"}}`,
	"odd":     `{"title":"   ","episodeType":"","publishedAt":0,"guid":"","enclosure":{"url":"https://e.example/3.mp3","type":"","length":"abc"}}`,
	"nulls":   `{"title":null,"subtitle":null,"season":null,"publishedAt":null,"enclosure":{"url":"https://e.example/4.mp3","length":"7"}}`,
}

func TestRegisterEpisodeRowGolden(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	got := map[string]any{}
	for name, js := range registerCases {
		root := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		for _, ddl := range []string{
			`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT)`,
			`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
			`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
			`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
			`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
			`CREATE TABLE playlists(id TEXT PRIMARY KEY,userId TEXT)`,
			`CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`,
			`CREATE TABLE mediaProgresses(id TEXT PRIMARY KEY,mediaItemId TEXT)`,
			`INSERT INTO libraries VALUES('lib','podcast')`,
			`INSERT INTO podcasts(id,title,explicit,autoDownloadEpisodes,maxEpisodesToKeep,maxNewEpisodesToDownload,tags,genres,numEpisodes,createdAt,updatedAt) VALUES('pod','Pod',0,0,0,3,'[]','[]',0,'2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00')`,
			`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,libraryId,createdAt,updatedAt) VALUES('item','1',?, 'pod','pod','podcast',0,0,0,0,'[]','{}','lib','2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00')`,
		} {
			args := []any{}
			if strings.Contains(ddl, "VALUES('item'") {
				args = append(args, root)
			}
			if _, err := db.Exec(ddl, args...); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(root, "ep.wav")
		if err := os.WriteFile(path, testToneWAV(.25), 0o644); err != nil {
			t.Fatal(err)
		}
		h := New(db, config.Config{}, nil)
		item, err := h.loadItem(context.Background(), "item")
		if err != nil {
			t.Fatal(err)
		}
		d := &downloadTask{id: "d", item: item, episode: episodeFromJSON(t, js), ctx: context.Background()}
		if err := h.registerEpisode(d, path); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		row := map[string]any{}
		rows, err := db.Query(`SELECT * FROM podcastEpisodes`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					vals[i] = string(b)
				}
				if s, ok := vals[i].(string); ok && (c == "audioFile" || c == "chapters" || c == "extraData") {
					var j any
					if json.Unmarshal([]byte(s), &j) == nil {
						vals[i] = j
					}
				}
				switch c {
				case "id", "createdAt", "updatedAt":
					vals[i] = "<" + c + ">"
				}
				row[c] = vals[i]
			}
		}
		rows.Close()
		scrubAudio(row)
		got[name] = row
		var count, size int
		var files string
		_ = db.QueryRow(`SELECT (SELECT numEpisodes FROM podcasts), size, libraryFiles FROM libraryItems`).Scan(&count, &size, &files)
		got[name+".item"] = map[string]any{"numEpisodes": count, "size": size, "libraryFiles": strings.Count(files, "fileType")}
		db.Close()
	}
	const file = "testdata/register_episode.golden.json"
	gotValue := canonicalJSON(t, got)
	if *updateRegisterGolden {
		pretty, _ := json.MarshalIndent(gotValue, "", "  ")
		if err := os.WriteFile(file, append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		gotBytes, _ := json.MarshalIndent(gotValue, "", "  ")
		_ = os.WriteFile(os.TempDir()+"/register.got.json", gotBytes, 0o644)
		t.Fatalf("registered episode rows differ from %s (got in %s/register.got.json)", file, os.TempDir())
	}
}

// scrubAudio drops what depends on the temp dir and the clock.
func scrubAudio(row map[string]any) {
	audio, _ := row["audioFile"].(map[string]any)
	for _, k := range []string{"addedAt", "updatedAt", "ino"} {
		delete(audio, k)
	}
	if meta, _ := audio["metadata"].(map[string]any); meta != nil {
		for _, k := range []string{"path", "mtimeMs", "ctimeMs", "birthtimeMs"} {
			delete(meta, k)
		}
	}
}
