package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

func episodeFromJSON(t *testing.T, js string) Episode {
	var e Episode
	if err := json.Unmarshal([]byte(js), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// registerCase is a downloaded episode and the podcastEpisodes row it must
// become. The expectations are derived by hand from the input (PodcastEpisode
// in the TS model: blank title -> "Episode", missing type -> "full", empty or
// unparseable values stored as NULL) and from the known 0.25 s mono 8 kHz WAV
// the test writes: 44 header bytes + 2000 samples x 2 bytes = 4044 bytes.
type registerCase struct {
	name, episode string
	want          map[string]any
}

var registerCases = []registerCase{
	{"feed", `{"title":"Episode One","subtitle":"Sub","description":"<p>Hi</p>","pubDate":"Mon, 02 Jan 2006 15:04:05 GMT","episodeType":"bonus","season":"2","episode":"4","publishedAt":1136214245000,"guid":"g-1","enclosure":{"url":"https://e.example/1.mp3","type":"audio/mpeg","length":"1234"},"chapters":[{"id":0,"title":"c","start":0,"end":1}]}`,
		map[string]any{"title": "Episode One", "subtitle": "Sub", "description": "<p>Hi</p>", "pubDate": "Mon, 02 Jan 2006 15:04:05 GMT", "episodeType": "bonus", "season": "2", "episode": "4",
			"publishedAt": "2006-01-02T15:04:05Z", "enclosureURL": "https://e.example/1.mp3", "enclosureType": "audio/mpeg", "enclosureSize": int64(1234), "extraData": `{"guid":"g-1"}`}},
	{"minimal", `{"title":"Only","enclosure":{"url":"https://e.example/2.mp3"}}`,
		map[string]any{"title": "Only", "subtitle": nil, "description": nil, "pubDate": nil, "episodeType": "full", "season": nil, "episode": nil,
			"publishedAt": nil, "enclosureURL": "https://e.example/2.mp3", "enclosureType": nil, "enclosureSize": nil, "extraData": `{}`}},
	{"blank and unparseable", `{"title":"   ","episodeType":"","publishedAt":0,"guid":"","enclosure":{"url":"https://e.example/3.mp3","type":"","length":"abc"}}`,
		map[string]any{"title": "Episode", "episodeType": "full", "publishedAt": nil, "enclosureType": nil, "enclosureSize": nil, "extraData": `{}`}},
	{"nulls", `{"title":null,"subtitle":null,"season":null,"publishedAt":null,"enclosure":{"url":"https://e.example/4.mp3","length":"7"}}`,
		map[string]any{"title": "Episode", "subtitle": nil, "season": nil, "publishedAt": nil, "enclosureSize": int64(7), "extraData": `{}`}},
}

func TestRegisterEpisodeStoresRowAndUpdatesItem(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	for _, c := range registerCases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			for _, ddl := range []string{
				`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT)`,
				`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
				`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
				`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
				`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
				`CREATE TABLE playlists(id TEXT PRIMARY KEY,name TEXT,description TEXT,createdAt TEXT,updatedAt TEXT,libraryId TEXT,userId TEXT)`,
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
			h := New(db, config.Config{}, nil, nil)
			item, err := h.loadItem(context.Background(), "item")
			if err != nil {
				t.Fatal(err)
			}
			d := &downloadTask{id: "d", item: item, episode: episodeFromJSON(t, c.episode), ctx: context.Background()}
			if err := h.registerEpisode(d, path); err != nil {
				t.Fatal(err)
			}
			cols := make([]string, 0, len(c.want))
			for col := range c.want {
				cols = append(cols, `"`+col+`"`)
			}
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := db.QueryRow(`SELECT ` + strings.Join(cols, ",") + ` FROM podcastEpisodes WHERE podcastId='pod'`).Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, col := range cols {
				col = strings.Trim(col, `"`)
				got := vals[i]
				if b, ok := got.([]byte); ok {
					got = string(b)
				}
				if ts, ok := got.(time.Time); ok { // the driver parses DATETIME columns
					got = ts.UTC().Format(time.RFC3339Nano)
				}
				if got != c.want[col] {
					t.Errorf("%s = %#v, want %#v", col, got, c.want[col])
				}
			}
			var audio struct {
				Duration float64
				Codec    string
				Channels int
				Metadata struct {
					Filename, Ext string
					Size          int
				}
			}
			var raw string
			if err := db.QueryRow(`SELECT audioFile FROM podcastEpisodes`).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &audio) != nil {
				t.Fatalf("audioFile %q: %v", raw, err)
			}
			if audio.Duration != 0.25 || audio.Codec != "pcm_s16le" || audio.Channels != 1 || audio.Metadata.Filename != "ep.wav" || audio.Metadata.Ext != ".wav" || audio.Metadata.Size != 4044 {
				t.Errorf("audioFile = %s", raw)
			}
			var numEpisodes, size, files int
			var libraryFiles string
			if err := db.QueryRow(`SELECT (SELECT numEpisodes FROM podcasts), size, libraryFiles FROM libraryItems`).Scan(&numEpisodes, &size, &libraryFiles); err != nil {
				t.Fatal(err)
			}
			files = strings.Count(libraryFiles, `"fileType":"audio"`)
			if numEpisodes != 1 || size != 4044 || files != 1 {
				t.Errorf("item: numEpisodes=%d size=%d audio files=%d, want 1/4044/1", numEpisodes, size, files)
			}
		})
	}
}
