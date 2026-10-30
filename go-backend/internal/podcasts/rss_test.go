package podcasts

import (
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
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

const feedFixture = `<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Fixture Cast</title><description><![CDATA[<p>Useful <script>alert(1)</script><strong>news</strong></p>]]></description><link>https://example.test/show</link><language>en-us</language><itunes:author>Writer</itunes:author><itunes:image href="https://example.test/cover.jpg"/><itunes:category text="Arts"><itunes:category text="Books"/></itunes:category><itunes:type>episodic</itunes:type><item><title>Episode One</title><description>Plain description</description><content:encoded><![CDATA[<p>Episode <a href="javascript:alert(1)">link</a></p>]]></content:encoded><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate><itunes:subtitle>Chapter one</itunes:subtitle><itunes:season>2</itunes:season><itunes:episode>4</itunes:episode><itunes:duration>01:02:03</itunes:duration><guid>guid-1</guid><enclosure url="https://example.test/episode.mp3" type="audio/mpeg" length="1234"/></item></channel></rss>`

func TestParseFeedShapesAndSanitizesRSS(t *testing.T) {
	feed, err := parseFeed([]byte(feedFixture), "https://example.test/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	meta := feed["metadata"].(map[string]any)
	if meta["title"] != "Fixture Cast" || meta["author"] != "Writer" || meta["image"] != "https://example.test/cover.jpg" || meta["feedUrl"] != "https://example.test/feed.xml" {
		t.Fatalf("metadata mismatch: %#v", meta)
	}
	if strings.Contains(meta["description"].(string), "script") || !strings.Contains(meta["description"].(string), "<strong>news</strong>") {
		t.Fatalf("unsafe or missing channel description: %s", meta["description"])
	}
	eps := feed["episodes"].([]map[string]any)
	if len(eps) != 1 {
		t.Fatalf("episodes: %#v", eps)
	}
	ep := eps[0]
	if ep["title"] != "Episode One" || ep["season"] != "2" || ep["episode"] != "4" || ep["durationSeconds"] != 3723 || ep["publishedAt"] == nil {
		t.Fatalf("episode mismatch: %#v", ep)
	}
	if strings.Contains(ep["description"].(string), "javascript:") {
		t.Fatalf("unsafe episode description: %s", ep["description"])
	}
	if ep["enclosure"].(map[string]any)["url"] != "https://example.test/episode.mp3" {
		t.Fatalf("enclosure mismatch: %#v", ep["enclosure"])
	}
}

func TestCreatePodcastValidatesFolderAndPersistsSourceShape(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "podcasts")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT)`,
		`CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT)`,
		`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON)`,
		`CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE libraryItems(id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME,lastScan DATETIME,lastScanVersion TEXT)`,
		`CREATE TABLE podcastEpisodes(id TEXT PRIMARY KEY,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT)`,
		`CREATE TABLE mediaProgresses(id TEXT PRIMARY KEY,mediaItemId TEXT)`} {
		if _, err = db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO libraries VALUES('lib','podcast'); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, folder); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	body := `{"libraryId":"lib","folderId":"folder","path":"` + filepath.ToSlash(filepath.Join(folder, "New Show")) + `","media":{"autoDownloadEpisodes":false,"metadata":{"title":"The New Show","author":"A Writer","description":"<p>safe</p><script>bad</script>","feedUrl":"https://example.test/feed","genres":["Arts"]}}}`
	r := httptest.NewRequest("POST", "/api/podcasts", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !h.Handle(w, r, store.User{Type: "admin"}) {
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
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 || strings.Contains(w.Body.String(), "audioTrack") {
		t.Fatalf("legacy episode response mismatch: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("PATCH", "/api/podcasts/"+itemID+"/episode/ep1", strings.NewReader(`{"title":"Updated Episode","description":"<p>okay</p><script>bad</script>","enclosure":null}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Updated Episode") || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("episode patch mismatch: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("DELETE", "/api/podcasts/"+itemID+"/episode/ep1", nil)
	w = httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
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
	h.SetHTTPClientForTests(downloadServer.Client())
	feedURL := downloadServer.URL + "/feed"
	feed, feedErr := h.getFeed(context.Background(), feedURL, false)
	if feedErr != nil {
		t.Fatal(feedErr)
	}
	episodesJSON, _ := json.Marshal(feed["episodes"])
	r = httptest.NewRequest("POST", "/api/podcasts/"+itemID+"/download-episodes", bytes.NewReader(episodesJSON))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 {
		t.Fatalf("download route returned %d: %s", w.Code, w.Body.String())
	}
	select {
	case payload := <-finished:
		if payload.(map[string]any)["failed"] == true {
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
	second := feed["episodes"].([]map[string]any)[0]
	secondCopy := make(map[string]any, len(second))
	for key, value := range second {
		secondCopy[key] = value
	}
	second = secondCopy
	second["title"], second["guid"], second["publishedAt"] = "Newer Episode", "newer-guid", time.Now().Add(time.Hour).UnixMilli()
	second["enclosure"] = map[string]any{"url": downloadServer.URL + "/audio.wav?newer=1", "type": "audio/wav", "length": len(tone)}
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.enqueue(item, []map[string]any{second}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-finished:
		if payload.(map[string]any)["failed"] == true {
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
	h.SetHTTPClientForTests(opmlServer.Client())
	opmlBody := `{"feeds":["` + opmlServer.URL + `/feed"],"libraryId":"lib","folderId":"folder","autoDownloadEpisodes":false}`
	r = httptest.NewRequest("POST", "/api/podcasts/opml/create", strings.NewReader(opmlBody))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	if !h.Handle(w, r, store.User{Type: "admin"}) || w.Code != 200 {
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
	h.SetQuickMatchHandler(func(_ context.Context, got string, override bool) (int, error) {
		if got != itemID || !override {
			t.Errorf("quick-match arguments item=%q override=%v", got, override)
		}
		return 2, nil
	})
	r = httptest.NewRequest("POST", "/api/podcasts/"+itemID+"/match-episodes?override=1", nil)
	w = httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"numEpisodesUpdated":2`) {
		t.Fatalf("quick-match route: %d %s", w.Code, w.Body.String())
	}
	failedFeed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "fixture unavailable", 503) }))
	defer failedFeed.Close()
	h.SetHTTPClientForTests(failedFeed.Client())
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedFixture))
	}))
	defer server.Close()
	h := New(nil, config.Config{}, nil)
	h.SetHTTPClientForTests(server.Client())
	r := httptest.NewRequest("POST", "/api/podcasts/feed", strings.NewReader(`{"rssFeed":"`+server.URL+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if !h.Handle(w, r, store.User{Type: "admin"}) {
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
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "example.test/feed") {
		t.Fatalf("OPML parse failed: %d %s", w.Code, w.Body.String())
	}
}

func TestOPMLImportCreationOutcomesAndEventPayloads(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "podcasts")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
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
	h := New(db, config.Config{}, nil)
	h.SetHTTPClientForTests(server.Client())
	events := make(chan struct {
		name string
		data map[string]any
	}, 8)
	h.emit = func(_ string, name string, payload any) {
		if task, ok := payload.(map[string]any); ok && (name == "task_started" || name == "task_finished") {
			events <- struct {
				name string
				data map[string]any
			}{name, task}
		}
	}
	finished := map[string]any{"id": "test", "description": "", "descriptionKey": "", "descriptionSubs": nil, "isFinished": false, "finishedAt": nil}
	h.runOPMLImport([]string{server.URL + "/new", server.URL + "/new", server.URL + "/failure"}, "lib", "folder", folder, false, finished)
	if finished["description"] != "Added 1 podcasts" || finished["descriptionKey"] != "MessageTaskOpmlImportFinished" || finished["descriptionSubs"].([]any)[0] != 1 || finished["isFinished"] != true || finished["finishedAt"] == nil {
		t.Fatalf("finished task payload mismatch: %#v", finished)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM podcasts WHERE title='Fixture Cast'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("created podcasts=%d err=%v", count, err)
	}
	var failures []map[string]any
	for len(events) > 0 {
		event := <-events
		if event.data["isFailed"] == true {
			failures = append(failures, event.data)
		}
	}
	if len(failures) != 2 {
		t.Fatalf("failure tasks=%#v", failures)
	}
	if failures[0]["description"] != `Creating podcast "Fixture Cast"` || failures[0]["descriptionKey"] != "MessageTaskOpmlImportFeedPodcastDescription" ||
		failures[0]["descriptionSubs"].([]string)[0] != "Fixture Cast" || failures[0]["error"] != "Podcast already exists at path" || failures[0]["errorKey"] != "MessageTaskOpmlImportFeedPodcastExists" {
		t.Fatalf("duplicate task payload mismatch: %#v", failures[0])
	}
	// PodcastManager.js:605-622: "Failed to create podcast folder" uses the
	// Creating-podcast description for a path failure.
	if failures[1]["description"] != `Creating podcast "Failure Show"` || failures[1]["descriptionKey"] != "MessageTaskOpmlImportFeedPodcastDescription" ||
		failures[1]["error"] != "Failed to create podcast library item" || failures[1]["errorKey"] != "MessageTaskOpmlImportFeedPodcastFailed" {
		t.Fatalf("other failure payload mismatch: %#v", failures[1])
	}
}
