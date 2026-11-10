package podcasts

import (
	"audiobookshelf-compat/internal/events"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
	_ "modernc.org/sqlite"
)

const feedFixture = `<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Fixture Cast</title><description><![CDATA[<p>Useful <script>alert(1)</script><strong>news</strong></p>]]></description><link>https://example.test/show</link><language>en-us</language><itunes:author>Writer</itunes:author><itunes:image href="https://example.test/cover.jpg"/><itunes:category text="Arts"><itunes:category text="Books"/></itunes:category><itunes:type>episodic</itunes:type><item><title>Episode One</title><description>Plain description</description><content:encoded><![CDATA[<p>Episode <a href="javascript:alert(1)">link</a></p>]]></content:encoded><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate><itunes:subtitle>Chapter one</itunes:subtitle><itunes:season>2</itunes:season><itunes:episode>4</itunes:episode><itunes:duration>01:02:03</itunes:duration><guid>guid-1</guid><enclosure url="https://example.test/episode.mp3" type="audio/mpeg" length="1234"/></item></channel></rss>`

func TestParseFeedShapesAndSanitizesRSS(t *testing.T) {
	feed, err := parseFeed([]byte(feedFixture), "https://example.test/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	meta := feed.Metadata
	if meta.Title != "Fixture Cast" || meta.Author != "Writer" || meta.Image != "https://example.test/cover.jpg" || meta.FeedURL != "https://example.test/feed.xml" {
		t.Fatalf("metadata mismatch: %#v", meta)
	}
	if strings.Contains(meta.Description, "script") || !strings.Contains(meta.Description, "<strong>news</strong>") {
		t.Fatalf("unsafe or missing channel description: %s", meta.Description)
	}
	eps := feed.Episodes
	if len(eps) != 1 {
		t.Fatalf("episodes: %#v", eps)
	}
	ep := eps[0]
	if ep.Title != "Episode One" || ep.Season != "2" || ep.Episode != "4" || ep.DurationSeconds == nil || *ep.DurationSeconds != 3723 || ep.PublishedAt == nil {
		t.Fatalf("episode mismatch: %#v", ep)
	}
	if strings.Contains(ep.Description, "javascript:") {
		t.Fatalf("unsafe episode description: %s", ep.Description)
	}
	if ep.Enclosure.URL != "https://example.test/episode.mp3" {
		t.Fatalf("enclosure mismatch: %#v", ep.Enclosure)
	}
}

func TestCreatePodcastValidatesFolderAndPersistsSourceShape(t *testing.T) {
	useLocalFeeds(t)
	root := t.TempDir()
	folder := filepath.Join(root, "podcasts")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT)`,
		`CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT)`,
		`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
		`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
		`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE playlists(id TEXT PRIMARY KEY,name TEXT,description TEXT,createdAt TEXT,updatedAt TEXT,libraryId TEXT,userId TEXT)`,
		`CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`,
		`CREATE TABLE mediaProgresses(id TEXT PRIMARY KEY,mediaItemId TEXT)`} {
		if _, err = db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO libraries VALUES('lib','podcast'); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, folder); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil, nil)
	body := `{"libraryId":"lib","folderId":"folder","path":"` + filepath.ToSlash(filepath.Join(folder, "New Show")) + `","media":{"autoDownloadEpisodes":false,"metadata":{"title":"The New Show","author":"A Writer","description":"<p>safe</p><script>bad</script>","feedUrl":"https://example.test/feed","genres":["Arts"]}}}`
	r := httptest.NewRequest("POST", "/api/podcasts", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !serveAdmin(h, w, r) {
		t.Fatal("create route not handled")
	}
	if w.Code != 200 {
		t.Fatalf("create returned %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "The New Show") || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("unexpected expanded item: %s", w.Body.String())
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM podcasts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("podcast rows=%d err=%v", n, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE path=?`, filepath.ToSlash(filepath.Join(folder, "New Show"))).Scan(&n); err != nil || n != 1 {
		t.Fatalf("library item rows=%d err=%v", n, err)
	}
	var itemID, mediaID string
	if err = db.QueryRow(`SELECT id,mediaId FROM libraryItems WHERE path=?`, filepath.ToSlash(filepath.Join(folder, "New Show"))).Scan(&itemID, &mediaID); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	audio := `{"ino":"audio-1","duration":15,"mimeType":"audio/mpeg","metadata":{"filename":"one.mp3","path":"` + filepath.ToSlash(filepath.Join(folder, "New Show", "one.mp3")) + `","size":99}}`
	if _, err = db.Exec(`INSERT INTO podcastEpisodes(id,podcastId,"index",title,enclosureURL,enclosureSize,enclosureType,audioFile,chapters,extraData,createdAt,updatedAt) VALUES('ep1',?,1,'Episode','https://example.test/ep.mp3',123,'audio/mpeg',?,'[]','{}',?,?)`, mediaID, audio, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	files := `[{"ino":"audio-1","fileType":"audio","metadata":{"filename":"one.mp3","path":"` + filepath.ToSlash(filepath.Join(folder, "New Show", "one.mp3")) + `","size":99}}]`
	if _, err = db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id=?`, files, itemID); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/api/podcasts/"+itemID+"/episode/ep1", nil)
	w = httptest.NewRecorder()
	serveAdmin(h, w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "audioTrack") {
		t.Fatalf("legacy episode response mismatch: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("PATCH", "/api/podcasts/"+itemID+"/episode/ep1", strings.NewReader(`{"title":"Updated Episode","description":"<p>okay</p><script>bad</script>","enclosure":null}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	serveAdmin(h, w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Updated Episode") || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("episode patch mismatch: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("DELETE", "/api/podcasts/"+itemID+"/episode/ep1", nil)
	w = httptest.NewRecorder()
	serveAdmin(h, w, r)
	if w.Code != 200 {
		t.Fatalf("episode delete status %d: %s", w.Code, w.Body.String())
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM podcastEpisodes WHERE id='ep1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("episode delete count=%d err=%v", n, err)
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable for downloaded episode fixture")
	}
	tone := testToneWAV(.25)
	var finished = make(chan any, 1)
	h.emit = func(_ string, event string, payload any) {
		if event == "episode_download_finished" {
			select {
			case finished <- payload:
			default:
			}
		}
	}
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio.wav" {
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(tone)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprintf(w, `<rss><channel><title>Fixture</title><item><title>Downloaded Episode</title><guid>download-guid</guid><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate><enclosure url="http://%s/audio.wav" type="audio/wav" length="%d"/></item></channel></rss>`, r.Host, len(tone))
	}))
	defer downloadServer.Close()
	feedURL := downloadServer.URL + "/feed"
	feed, feedErr := h.getFeed(context.Background(), feedURL)
	if feedErr != nil {
		t.Fatal(feedErr)
	}
	episodesJSON, _ := json.Marshal(feed.Episodes)
	r = httptest.NewRequest("POST", "/api/podcasts/"+itemID+"/download-episodes", bytes.NewReader(episodesJSON))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	serveAdmin(h, w, r)
	if w.Code != 200 {
		t.Fatalf("download route returned %d: %s", w.Code, w.Body.String())
	}
	select {
	case payload := <-finished:
		if payload.(wire.EpisodeDownload).Failed {
			t.Fatalf("episode download failed: %#v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("episode download did not finish")
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?`, mediaID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("downloaded episode rows=%d err=%v", n, err)
	}
	if _, err = db.Exec(`UPDATE podcasts SET autoDownloadEpisodes=1,maxEpisodesToKeep=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	second := feed.Episodes[0]
	newer := time.Now().Add(time.Hour).UnixMilli()
	guid := "newer-guid"
	second.Title, second.GUID, second.PublishedAt = "Newer Episode", &guid, &newer
	second.Enclosure = enclosure{URL: downloadServer.URL + "/audio.wav?newer=1", Type: "audio/wav", Length: numText(strconv.Itoa(len(tone)))}
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.enqueue(item, []Episode{second}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-finished:
		if payload.(wire.EpisodeDownload).Failed {
			t.Fatalf("retention fixture download failed: %#v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retention fixture download did not finish")
	}
	var retainedTitle string
	if err = db.QueryRow(`SELECT title FROM podcastEpisodes WHERE podcastId=?`, mediaID).Scan(&retainedTitle); err != nil || retainedTitle != "Newer Episode" {
		t.Fatalf("episode retention kept %q err=%v", retainedTitle, err)
	}
	opmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(feedFixture)) }))
	defer opmlServer.Close()
	opmlBody := `{"feeds":["` + opmlServer.URL + `/feed"],"libraryId":"lib","folderId":"folder","autoDownloadEpisodes":false}`
	r = httptest.NewRequest("POST", "/api/podcasts/opml/create", strings.NewReader(opmlBody))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	if !serveAdmin(h, w, r) || w.Code != 200 {
		t.Fatalf("OPML create route: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRow(`SELECT COUNT(*) FROM podcasts WHERE title='Fixture Cast'`).Scan(&n)
		if err == nil && n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n != 1 {
		t.Fatalf("OPML import did not create podcast: count=%d err=%v", n, err)
	}
	failedFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "fixture unavailable", 503) }))
	defer failedFeed.Close()
	if _, err = db.Exec(`UPDATE podcasts SET feedURL=?,autoDownloadEpisodes=1 WHERE id=?`, failedFeed.URL, mediaID); err != nil {
		t.Fatal(err)
	}
	h.maxFailedChecks = 1
	h.runScheduled(itemID)
	var autoDownload bool
	if err = db.QueryRow(`SELECT autoDownloadEpisodes FROM podcasts WHERE id=?`, mediaID).Scan(&autoDownload); err != nil || autoDownload {
		t.Fatalf("failed check did not disable auto download: auto=%v err=%v", autoDownload, err)
	}
}

func testToneWAV(duration float64) []byte {
	const rate = 8000
	frames := int(duration * rate)
	buf := new(bytes.Buffer)
	_, _ = buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+frames*2))
	_, _ = buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	_, _ = buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(frames*2))
	for i := 0; i < frames; i++ {
		sample := int16(math.Sin(2*math.Pi*440*float64(i)/rate) * 6000)
		_ = binary.Write(buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

func TestFeedAndOPMLRoutesAgainstControlledHTTPFixture(t *testing.T) {
	useLocalFeeds(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedFixture))
	}))
	defer server.Close()
	h := New(nil, config.Config{}, nil, nil)
	r := httptest.NewRequest("POST", "/api/podcasts/feed", strings.NewReader(`{"rssFeed":"`+server.URL+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !serveAdmin(h, w, r) {
		t.Fatal("feed route not handled")
	}
	if w.Code != 200 {
		t.Fatalf("feed status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Fixture Cast") {
		t.Fatalf("feed response missing channel: %s", w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/podcasts/opml/parse", strings.NewReader(`{"opmlText":"<opml><body><outline text=\"Show\" type=\"rss\" xmlUrl=\"https://example.test/feed\"/></body></opml>"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	serveAdmin(h, w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "example.test/feed") {
		t.Fatalf("OPML parse failed: %d %s", w.Code, w.Body.String())
	}
}

func TestOPMLImportCreationOutcomesAndEventPayloads(t *testing.T) {
	useLocalFeeds(t)
	root := t.TempDir()
	folder := filepath.Join(root, "podcasts")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT)`,
		`CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT)`,
		`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
		`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
		`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO libraries VALUES('lib','podcast'); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, folder); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "Failure Show"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture := feedFixture
		if strings.Contains(r.URL.Path, "failure") {
			fixture = strings.Replace(fixture, "Fixture Cast", "Failure Show", 1)
		}
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	h := New(db, config.Config{}, nil, nil)
	type taskEvent struct {
		name string
		task events.Task
	}
	taskEvents := make(chan taskEvent, 8)
	h.emit = func(_ string, name string, payload any) {
		if task, ok := payload.(events.Task); ok && (name == "task_started" || name == "task_finished") {
			taskEvents <- taskEvent{name, task}
		}
	}
	h.runOPMLImport([]string{server.URL + "/new", server.URL + "/new", server.URL + "/failure"}, "lib", "folder", folder, false, events.NewTask("test", "opml-import", struct{}{}))
	var finished events.Task
	var failures []events.Task
	for len(taskEvents) > 0 {
		event := <-taskEvents
		if event.task.IsFailed {
			failures = append(failures, event.task)
		} else if event.name == "task_finished" {
			finished = event.task
		}
	}
	if str(finished.Description) != "Added 1 podcasts" || str(finished.DescriptionKey) != "MessageTaskOpmlImportFinished" || finished.DescriptionSubs[0] != 1 || !finished.IsFinished || finished.FinishedAt == nil {
		t.Fatalf("finished task payload mismatch: %#v", finished)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM podcasts WHERE title='Fixture Cast'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("created podcasts=%d err=%v", count, err)
	}
	if len(failures) != 2 {
		t.Fatalf("failure tasks=%#v", failures)
	}
	if str(failures[0].Description) != `Creating podcast "Fixture Cast"` || str(failures[0].DescriptionKey) != "MessageTaskOpmlImportFeedPodcastDescription" ||
		failures[0].DescriptionSubs[0] != "Fixture Cast" || str(failures[0].Error) != "Podcast already exists at path" || str(failures[0].ErrorKey) != "MessageTaskOpmlImportFeedPodcastExists" {
		t.Fatalf("duplicate task payload mismatch: %#v", failures[0])
	}
	// PodcastManager.js:605-622: "Failed to create podcast folder" uses the
	// Creating-podcast description for a path failure.
	if str(failures[1].Description) != `Creating podcast "Failure Show"` || str(failures[1].DescriptionKey) != "MessageTaskOpmlImportFeedPodcastDescription" ||
		str(failures[1].Error) != "Failed to create podcast library item" || str(failures[1].ErrorKey) != "MessageTaskOpmlImportFeedPodcastFailed" {
		t.Fatalf("other failure payload mismatch: %#v", failures[1])
	}
}

// serveAdmin dispatches r through the handler's mux as an admin and reports
// whether a route matched.
func serveAdmin(h *Handler, w http.ResponseWriter, r *http.Request) bool {
	mux := http.NewServeMux()
	h.Routes(mux)
	if _, pattern := mux.Handler(r); pattern == "" {
		return false
	}
	mux.ServeHTTP(w, r.WithContext(store.ContextWithUser(r.Context(), store.User{Type: "admin"})))
	return true
}

func TestEpisodePatchPresenceAndChapters(t *testing.T) {
	rig := newNotifyRig(t)
	defer rig.h.Close()
	for _, tc := range []struct {
		body, title, chapters string
		events                int
	}{
		{`{}`, "One", `[]`, 0},
		{`{"title":null,"subtitle":3,"chapters":[{"title":"bad","start":0}]}`, "One", `[]`, 0},
		{`{"Title":"wrong case","enclosure":42}`, "One", `[]`, 0},
		{`{"title":"","chapters":[{"title":"part","start":1,"custom":"kept"}]}`, "", `[{"custom":"kept","start":1,"title":"part"}]`, 1},
	} {
		w := httptest.NewRecorder()
		serveAdmin(rig.h, w, httptest.NewRequest("PATCH", "/api/podcasts/item/episode/e1", strings.NewReader(tc.body)))
		if w.Code != 200 {
			t.Fatalf("%s: status %d: %s", tc.body, w.Code, w.Body)
		}
		var title, chapters string
		if err := rig.db.QueryRow(`SELECT title,chapters FROM podcastEpisodes WHERE id='e1'`).Scan(&title, &chapters); err != nil {
			t.Fatal(err)
		}
		if title != tc.title || chapters != tc.chapters {
			t.Fatalf("%s: title=%q chapters=%s", tc.body, title, chapters)
		}
		rig.mu.Lock()
		events := len(rig.events)
		rig.mu.Unlock()
		if events != tc.events {
			t.Fatalf("%s: events=%d want %d", tc.body, events, tc.events)
		}
	}
}

func TestCreatePodcastDirectoryOwnershipAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, failTable string
		existing        bool
	}{
		{"new podcast insert failure", "podcasts", false},
		{"existing directory podcast failure", "podcasts", true},
		{"new item insert failure", "libraryItems", false},
		{"existing directory item failure", "libraryItems", true},
		{"new directory success", "", false},
		{"existing directory success", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newNotifyRig(t)
			defer rig.h.Close()
			if _, err := rig.db.Exec(`CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, rig.root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(rig.root, "new-podcast")
			if tc.existing {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failTable != "" {
				if _, err := rig.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON ` + tc.failTable + ` BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			body := map[string]any{"libraryId": "lib", "folderId": "folder", "path": path, "media": map[string]any{"metadata": map[string]any{"title": "Created"}}}
			_, status, _, err := rig.h.createPodcastFromBody(context.Background(), body)
			if (err != nil) != (tc.failTable != "") {
				t.Fatalf("status %d err %v", status, err)
			}
			_, statErr := os.Stat(path)
			wantDir := tc.existing || tc.failTable == ""
			if (statErr == nil) != wantDir {
				t.Fatalf("directory exists=%v, want %v: %v", statErr == nil, wantDir, statErr)
			}
			var podcasts, items int
			if err := rig.db.QueryRow(`SELECT (SELECT COUNT(*) FROM podcasts), (SELECT COUNT(*) FROM libraryItems)`).Scan(&podcasts, &items); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.failTable == "" {
				want++
			}
			if podcasts != want || items != want {
				t.Fatalf("rows podcasts=%d items=%d want=%d", podcasts, items, want)
			}
		})
	}
}

func TestCreatePodcastMetadataCoercion(t *testing.T) {
	rig := newNotifyRig(t)
	defer rig.h.Close()
	if _, err := rig.db.Exec(`CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, rig.root); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"libraryId": "lib", "folderId": "folder", "path": filepath.Join(rig.root, "created"), "media": map[string]any{"metadata": map[string]any{"title": "Created", "author": float64(12.5), "releaseDate": "", "feedUrl": nil, "description": "<script>bad</script>"}}}
	_, status, _, err := rig.h.createPodcastFromBody(context.Background(), body)
	if err != nil {
		t.Fatalf("status %d: %v", status, err)
	}
	var author string
	var date, feed, description sql.NullString
	if err := rig.db.QueryRow(`SELECT author,releaseDate,feedURL,description FROM podcasts WHERE title='Created'`).Scan(&author, &date, &feed, &description); err != nil {
		t.Fatal(err)
	}
	if author != "12.5" || date.Valid || feed.Valid || !description.Valid || description.String != "" {
		t.Fatalf("metadata: %q %#v %#v %#v", author, date, feed, description)
	}
}
