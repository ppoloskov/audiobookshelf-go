package podcasts

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

type sentEvent struct {
	audience, event string
	payload         any
}

type notifyRig struct {
	h      *Handler
	db     *sql.DB
	root   string
	mu     sync.Mutex
	events []sentEvent
	notes  chan notified
}

type notified struct {
	event string
	data  map[string]any
}

func newNotifyRig(t *testing.T) *notifyRig {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE libraries(id TEXT PRIMARY KEY,name TEXT,mediaType TEXT)`,
		`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
		`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
		`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE playlists(id TEXT PRIMARY KEY,name TEXT,description TEXT,createdAt TEXT,updatedAt TEXT,libraryId TEXT,userId TEXT)`,
		`CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`,
		`CREATE TABLE mediaProgresses(id TEXT PRIMARY KEY,mediaItemId TEXT)`,
		`INSERT INTO libraries VALUES('lib','Casts','podcast')`,
		`INSERT INTO podcasts(id,title,author,description,feedURL,explicit,autoDownloadEpisodes,maxEpisodesToKeep,maxNewEpisodesToDownload,tags,genres,numEpisodes,createdAt,updatedAt) VALUES('pod','Pod','Ann','About','http://feed.invalid/rss',0,1,0,3,'["t1","t2"]','["g1","g2"]',2,'2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO podcastEpisodes(id,podcastId,"index",title,subtitle,description,audioFile,chapters,extraData,createdAt,updatedAt) VALUES('e1','pod',1,'One','S1','D1','{"ino":"i1"}','[]','{}','2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00'),('e2','pod',2,'Two',NULL,NULL,'{"ino":"i2"}','[]','{}','2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,libraryId,createdAt,updatedAt) VALUES('item','1',?,'pod','pod','podcast',0,0,0,0,'[]','{}','lib','2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00')`, root); err != nil {
		t.Fatal(err)
	}
	r := &notifyRig{db: db, root: root, notes: make(chan notified, 8)}
	r.h = New(db, config.Config{}, func(audience, event string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, sentEvent{audience, event, payload})
	}, func(_ context.Context, event string, data map[string]any) error {
		r.notes <- notified{event, data}
		return nil
	})
	return r
}

func (r *notifyRig) note(t *testing.T) notified {
	t.Helper()
	select {
	case n := <-r.notes:
		return n
	case <-time.After(3 * time.Second):
		t.Fatal("no notification fired")
		return notified{}
	}
}

func (r *notifyRig) noNote(t *testing.T) {
	t.Helper()
	select {
	case n := <-r.notes:
		t.Fatalf("unexpected notification %s", n.event)
	case <-time.After(100 * time.Millisecond):
	}
}

// PodcastManager.js:346-351 -> NotificationManager.js:81-121.
func TestFeedFailureNotifications(t *testing.T) {
	useLocalFeeds(t)
	r := newNotifyRig(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", 503) }))
	defer down.Close()
	if _, err := r.db.Exec(`UPDATE podcasts SET feedURL=?`, down.URL); err != nil {
		t.Fatal(err)
	}
	r.h.maxFailedChecks = 2
	r.h.runScheduled("item")
	n := r.note(t)
	if n.event != "onRSSFeedFailed" || n.data["feedUrl"] != down.URL || n.data["numFailed"] != 1 || n.data["title"] != "Pod" {
		t.Fatalf("first failure = %+v", n)
	}
	r.h.runScheduled("item")
	n = r.note(t)
	if n.event != "onRSSFeedDisabled" || n.data["numFailed"] != 2 {
		t.Fatalf("second failure = %+v", n)
	}
	r.noNote(t)
}

// PodcastManager.js:257-261: only auto downloads notify.
func TestEpisodeDownloadedNotification(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	for _, auto := range []bool{false, true} {
		r := newNotifyRig(t)
		path := filepath.Join(r.root, "ep.wav")
		if err := os.WriteFile(path, testToneWAV(.25), 0o644); err != nil {
			t.Fatal(err)
		}
		item, err := r.h.loadItem(context.Background(), "item")
		if err != nil {
			t.Fatal(err)
		}
		d := &downloadTask{id: "d", item: item, auto: auto, ctx: context.Background(),
			episode: episodeFromJSON(t, `{"title":"New Ep","subtitle":"Sub","description":"Desc","enclosure":{"url":"https://e.example/1.mp3"}}`)}
		if err := r.h.registerEpisode(d, path); err != nil {
			t.Fatal(err)
		}
		if !auto {
			r.noNote(t)
			continue
		}
		n := r.note(t)
		want := map[string]any{
			"libraryItemId": "item", "libraryId": "lib", "libraryName": "Casts", "mediaTags": "t1, t2",
			"podcastTitle": "Pod", "podcastAuthor": "Ann", "podcastDescription": "About", "podcastGenres": "g1, g2",
			"episodeTitle": "New Ep", "episodeSubtitle": "Sub", "episodeDescription": "Desc",
		}
		if n.event != "onPodcastEpisodeDownloaded" || n.data["episodeId"] == "" {
			t.Fatalf("notification = %+v", n)
		}
		for k, v := range want {
			if n.data[k] != v {
				t.Errorf("%s = %v, want %v", k, n.data[k], v)
			}
		}
	}
}

// PodcastController.js:526 -> Playlist.js:199-206.
func TestDeleteEpisodeAnnouncesPlaylists(t *testing.T) {
	r := newNotifyRig(t)
	for _, q := range []string{
		`INSERT INTO playlists VALUES('shared','Mix',NULL,'2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00','lib','u1'),('solo','Solo',NULL,'2026-01-01 00:00:00.000 +00:00','2026-01-01 00:00:00.000 +00:00','lib','u2')`,
		`INSERT INTO playlistMediaItems VALUES('x1','e1','podcastEpisode',1,'shared'),('x2','e2','podcastEpisode',2,'shared'),('x3','e1','podcastEpisode',1,'solo')`,
	} {
		if _, err := r.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	item, err := r.h.loadItem(context.Background(), "item")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.h.deleteEpisode(context.Background(), item, "e1", "i1"); err != nil {
		t.Fatal(err)
	}
	got := map[string]sentEvent{}
	for _, e := range r.events {
		got[e.event] = e
	}
	if e := got["playlist_updated"]; e.audience != "u1" || e.payload.(map[string]any)["id"] != "shared" || len(e.payload.(map[string]any)["items"].([]any)) != 1 {
		t.Errorf("playlist_updated = %+v", e)
	}
	if e := got["playlist_removed"]; e.audience != "u2" || e.payload.(map[string]any)["id"] != "solo" || len(e.payload.(map[string]any)["items"].([]any)) != 0 {
		t.Errorf("playlist_removed = %+v", e)
	}
}
