package podcastread

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const testNow = "2024-01-02 03:04:05.000 +00:00"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema, err := os.ReadFile("../../../contracts/parity/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func addTestPodcast(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('p1','A Show',1,0,'[]','[]',7,?,?)`, testNow, testNow); err != nil {
		t.Fatal(err)
	}
}

func TestPodcastWithoutEpisodes(t *testing.T) {
	db := testDB(t)
	addTestPodcast(t, db)
	loaded, err := LoadMany(context.Background(), db, []string{"p1"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	podcast := loaded["p1"]
	if podcast == nil {
		t.Fatal("p1 was not loaded")
	}
	if podcast.Episodes != nil {
		t.Fatal("episodes loaded without Options.Episodes")
	}
	min := podcast.Media("li-p1", []string{"a"}, false, false)
	if *min.NumEpisodes != 0 || *min.Size != 0 || min.Metadata.TitleIgnorePrefix == nil || *min.Metadata.TitleIgnorePrefix != "Show, A" {
		t.Fatalf("minified podcast = %+v", min)
	}
	expanded := podcast.Media("li-p1", nil, false, true)
	raw, _ := json.Marshal(expanded)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if eps, ok := got["episodes"].([]any); !ok || len(eps) != 0 || got["numEpisodes"] != float64(0) {
		t.Fatalf("expanded empty podcast: %s", raw)
	}
}

func TestPodcastEpisodesLoadAndProject(t *testing.T) {
	db := testDB(t)
	addTestPodcast(t, db)
	if _, err := db.Exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,extraData,createdAt,updatedAt) VALUES ('e1','p1','Episode One','{"ino":"a1","duration":12,"metadata":{"filename":"e.mp3","size":100}}','[]','{}',?,?)`, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadMany(context.Background(), db, []string{"p1"}, Options{Episodes: true})
	if err != nil {
		t.Fatal(err)
	}
	podcast := loaded["p1"]
	if podcast == nil || len(podcast.Episodes) != 1 {
		t.Fatalf("episodes = %+v", podcast)
	}
	expanded := podcast.Episodes[0].Expanded("li-p1")
	if expanded.Title == nil || *expanded.Title != "Episode One" || expanded.AudioTrack["contentUrl"] != "/api/items/li-p1/file/a1" {
		t.Fatalf("expanded episode = %+v", expanded)
	}
	media := podcast.Media("li-p1", nil, false, true)
	if media.NumEpisodes == nil || *media.NumEpisodes != 1 || media.Size == nil || *media.Size != 100 {
		t.Fatalf("podcast media = %+v", media)
	}
}
