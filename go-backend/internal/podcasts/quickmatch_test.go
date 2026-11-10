package podcasts

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"

	_ "modernc.org/sqlite"
)

func TestQuickMatchEpisodesOverrideRepeatAndTies(t *testing.T) {
	useLocalFeeds(t)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Feed</title><item><title>The Story</title><itunes:subtitle>Episode One</itunes:subtitle><description>Details</description><itunes:episode>1</itunes:episode><itunes:season>1</itunes:season><pubDate>Wed, 01 Jan 2025 00:00:00 GMT</pubDate><enclosure url="https://feed.test/audio.mp3" length="1234" type="audio/mpeg"/></item><item><title>The Story</title><description>Later tie</description><enclosure url="https://feed.test/other.mp3"/></item></channel></rss>`))
	}))
	defer feed.Close()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "podcasts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE podcasts(id UUID PRIMARY KEY,feedURL TEXT)`,
		`CREATE TABLE libraryItems(id UUID PRIMARY KEY,mediaId UUID,mediaType TEXT)`,
		`CREATE TABLE podcastEpisodes(id UUID PRIMARY KEY,podcastId UUID,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`INSERT INTO podcasts(id,feedURL) VALUES('pod-1','` + feed.URL + `')`,
		`INSERT INTO libraryItems(id,mediaId,mediaType) VALUES('item-podcast','pod-1','podcast')`,
		`INSERT INTO podcastEpisodes(id,podcastId,title,createdAt,updatedAt) VALUES('ep-1','pod-1','The Story','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE podcastEpisodes SET subtitle='',description='Old' WHERE id='ep-1'`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil, nil)
	defer h.Close()
	updated, err := h.QuickMatchEpisodes(context.Background(), "item-podcast", false)
	if err != nil || updated != 1 {
		t.Fatalf("quick match updated=%d err=%v", updated, err)
	}
	var title, subtitle, audioURL string
	var size int64
	if err := db.QueryRow(`SELECT title,subtitle,enclosureURL,enclosureSize FROM podcastEpisodes WHERE id='ep-1'`).Scan(&title, &subtitle, &audioURL, &size); err != nil {
		t.Fatal(err)
	}
	if title != "The Story" || subtitle != "" || audioURL != "https://feed.test/audio.mp3" || size != 1234 {
		t.Fatalf("matched episode=%q/%q/%q/%d", title, subtitle, audioURL, size)
	}
	updated, err = h.QuickMatchEpisodes(context.Background(), "item-podcast", true)
	if err != nil || updated != 0 {
		t.Fatalf("repeat updated=%d err=%v", updated, err)
	}
	if _, err := db.Exec(`UPDATE podcastEpisodes SET enclosureURL=NULL WHERE id='ep-1'`); err != nil {
		t.Fatal(err)
	}
	updated, err = h.QuickMatchEpisodes(context.Background(), "item-podcast", true)
	if err != nil || updated != 1 {
		t.Fatalf("override updated=%d err=%v", updated, err)
	}
	var description string
	if err := db.QueryRow(`SELECT subtitle,description,enclosureURL FROM podcastEpisodes WHERE id='ep-1'`).Scan(&subtitle, &description, &audioURL); err != nil {
		t.Fatal(err)
	}
	if subtitle != "Episode One" || description != "Details" || audioURL != "https://feed.test/audio.mp3" {
		t.Fatalf("override/tie: %q/%q/%q", subtitle, description, audioURL)
	}
}

func TestQuickMatchEpisodesPersistsFeedFields(t *testing.T) {
	useLocalFeeds(t)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Feed</title><item><title>The Story</title><itunes:subtitle>Episode One</itunes:subtitle><description>Details</description><itunes:episode>1</itunes:episode><itunes:season>1</itunes:season><pubDate>Wed, 01 Jan 2025 00:00:00 GMT</pubDate><enclosure url="https://feed.test/audio.mp3" length="1234" type="audio/mpeg"/></item></channel></rss>`))
	}))
	defer feed.Close()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "podcasts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE podcasts(id UUID PRIMARY KEY,feedURL TEXT)`,
		`CREATE TABLE libraryItems(id UUID PRIMARY KEY,mediaId UUID,mediaType TEXT)`,
		`CREATE TABLE podcastEpisodes(id UUID PRIMARY KEY,podcastId UUID,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`INSERT INTO podcasts(id,feedURL) VALUES('pod-1','` + feed.URL + `')`,
		`INSERT INTO libraryItems(id,mediaId,mediaType) VALUES('item-podcast','pod-1','podcast')`,
		`INSERT INTO podcastEpisodes(id,podcastId,title,createdAt,updatedAt) VALUES('ep-1','pod-1','The Story','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	h := New(db, config.Config{}, nil, nil)
	defer h.Close()
	updated, err := h.QuickMatchEpisodes(context.Background(), "item-podcast", false)
	if err != nil || updated != 1 {
		t.Fatalf("quick match updated=%d err=%v", updated, err)
	}
	var title, subtitle, audioURL string
	var size int64
	if err := db.QueryRow(`SELECT title,subtitle,enclosureURL,enclosureSize FROM podcastEpisodes WHERE id='ep-1'`).Scan(&title, &subtitle, &audioURL, &size); err != nil {
		t.Fatal(err)
	}
	if title != "The Story" || subtitle != "Episode One" || audioURL != "https://feed.test/audio.mp3" || size != 1234 {
		t.Fatalf("matched episode=%q/%q/%q/%d", title, subtitle, audioURL, size)
	}
}
