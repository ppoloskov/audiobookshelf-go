package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

func TestRuntimeMatchEpisodesFetchesFeedAndMatches(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO libraries(id,name,mediaType,createdAt,updatedAt) VALUES('lib-runtime','Runtime','podcast',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-runtime','/tmp','lib-runtime',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Runtime fixture</title><item><title>The Story</title><enclosure url="https://example.test/story.mp3" length="1234" type="audio/mpeg"/><pubDate>Wed, 01 Jan 2025 00:00:00 GMT</pubDate></item></channel></rss>`))
	}))
	defer feed.Close()
	_, err = db.DB().Exec(`INSERT INTO podcasts(id,title,titleIgnorePrefix,feedURL,tags,genres,createdAt,updatedAt) VALUES('podcast-runtime','Runtime','Runtime',?,'[]','[]',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, feed.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB().Exec(`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,libraryId,libraryFolderId,createdAt,updatedAt) VALUES('item-runtime','1',?,'','podcast-runtime','podcast',0,0,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0,'[]','{}','lib-runtime','folder-runtime',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2025-01-01T00:00:00Z"
	_, err = db.DB().Exec(`INSERT INTO podcastEpisodes(id,podcastId,"index",title,audioFile,chapters,extraData,createdAt,updatedAt) VALUES('episode-runtime','podcast-runtime',1,'The Story','{}','[]','{}',?,?)`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newRuntime(db.DB(), cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rt.podcasts.SetHTTPClientForTests(feed.Client())
	r := httptest.NewRequest(http.MethodPost, "/api/podcasts/item-runtime/match-episodes?override=1", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`))
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"numEpisodesUpdated":1`) {
		t.Fatalf("match-episodes status=%d body=%s", w.Code, w.Body.String())
	}
	var enclosure string
	if err := db.DB().QueryRow(`SELECT enclosureURL FROM podcastEpisodes WHERE id='episode-runtime'`).Scan(&enclosure); err != nil {
		t.Fatal(err)
	}
	if enclosure != "https://example.test/story.mp3" {
		t.Fatalf("matched episode enclosure=%q", enclosure)
	}
}
