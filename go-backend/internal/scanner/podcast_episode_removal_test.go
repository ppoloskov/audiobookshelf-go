package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// PodcastScanner.js:66-102: episodes whose audio file left the folder are
// destroyed after Playlist.removeMediaItemsFromPlaylists and their progress is
// deleted; the playlist owners are told, as for the other episode deletes.
func TestScanRemovesEpisodeOfDeletedAudioFileWithPlaylistsAndProgress(t *testing.T) {
	fakeFFprobe(t)
	root := t.TempDir()
	show := filepath.Join(root, "Show")
	if err := os.MkdirAll(show, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.mp3", "b.mp3"} {
		if err := os.WriteFile(filepath.Join(show, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db := scannerTestDB(t, root)
	for _, ddl := range []string{
		"CREATE TABLE podcasts (id UUID PRIMARY KEY, title VARCHAR(255), titleIgnorePrefix VARCHAR(255), author VARCHAR(255), releaseDate VARCHAR(255), feedURL VARCHAR(255), imageURL VARCHAR(255), description TEXT, itunesPageURL VARCHAR(255), itunesId VARCHAR(255), itunesArtistId VARCHAR(255), language VARCHAR(255), podcastType VARCHAR(255), explicit TINYINT(1), autoDownloadEpisodes TINYINT(1), autoDownloadSchedule VARCHAR(255), lastEpisodeCheck DATETIME, maxEpisodesToKeep INTEGER, maxNewEpisodesToDownload INTEGER, coverPath VARCHAR(255), tags JSON, genres JSON, numEpisodes INTEGER, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL)",
		"CREATE TABLE podcastEpisodes (id UUID PRIMARY KEY, `index` INTEGER, season VARCHAR(255), episode VARCHAR(255), episodeType VARCHAR(255), title VARCHAR(255), subtitle VARCHAR(1000), description TEXT, pubDate VARCHAR(255), enclosureURL VARCHAR(255), enclosureSize BIGINT, enclosureType VARCHAR(255), publishedAt DATETIME, audioFile JSON, chapters JSON, extraData JSON, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, podcastId UUID REFERENCES podcasts (id) ON DELETE CASCADE ON UPDATE CASCADE)",
		"CREATE TABLE users (id UUID PRIMARY KEY, username VARCHAR(255))",
		"CREATE TABLE playlists (id UUID PRIMARY KEY, name VARCHAR(255), description TEXT, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, libraryId UUID, userId UUID)",
		"CREATE TABLE playlistMediaItems (id UUID PRIMARY KEY, mediaItemId UUID, mediaItemType VARCHAR(255), `order` INTEGER, createdAt DATETIME NOT NULL, playlistId UUID)",
		"UPDATE libraries SET mediaType='podcast' WHERE id='lib-1'",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	emit, got := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	episode := func(title string) string {
		var id string
		if err := db.QueryRow(`SELECT id FROM podcastEpisodes WHERE title=?`, title).Scan(&id); err != nil {
			t.Fatalf("episode %s: %v", title, err)
		}
		return id
	}
	a, b := episode("a"), episode("b")
	const ts = "2020-01-01 00:00:00.000 +00:00"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO playlists(id,name,createdAt,updatedAt,userId) VALUES('pl-only','Only',?,?,'user-1')`, []any{ts, ts}},
		{`INSERT INTO playlists(id,name,createdAt,updatedAt,userId) VALUES('pl-both','Both',?,?,'user-2')`, []any{ts, ts}},
		{`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('m1',?,'podcastEpisode',1,?,'pl-only')`, []any{a, ts}},
		{`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('m2',?,'podcastEpisode',1,?,'pl-both')`, []any{a, ts}},
		{`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES('m3',?,'podcastEpisode',2,?,'pl-both')`, []any{b, ts}},
		{`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,currentTime,isFinished,createdAt,updatedAt,userId) VALUES('pr-a',?,'podcastEpisode',1,0,?,?,'user-1')`, []any{a, ts, ts}},
		{`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,currentTime,isFinished,createdAt,updatedAt,userId) VALUES('pr-b',?,'podcastEpisode',1,0,?,?,'user-1')`, []any{b, ts, ts}},
	} {
		if _, err = db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Remove(filepath.Join(show, "a.mp3")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) (n int) {
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM podcastEpisodes WHERE id=?`, a); n != 0 {
		t.Fatalf("removed file's episode still stored")
	}
	if n := count(`SELECT COUNT(*) FROM podcastEpisodes WHERE id=?`, b); n != 1 {
		t.Fatalf("surviving episode count=%d", n)
	}
	if n := count(`SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId=?`, a); n != 0 {
		t.Fatalf("progress of removed episode kept")
	}
	if n := count(`SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId=?`, b); n != 1 {
		t.Fatalf("progress of surviving episode=%d", n)
	}
	if n := count(`SELECT COUNT(*) FROM playlists WHERE id='pl-only'`); n != 0 {
		t.Fatalf("playlist left empty was kept")
	}
	if n := count(`SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemId=?`, a); n != 0 {
		t.Fatalf("playlist entries of removed episode kept")
	}
	if n := count(`SELECT "order" FROM playlistMediaItems WHERE mediaItemId=?`, b); n != 1 {
		t.Fatalf("remaining entry order=%d, want renumbered to 1", n)
	}
	removed := eventsNamed(got, "playlist_removed")
	if len(removed) != 1 || removed[0].scope != "user-1" {
		t.Fatalf("playlist_removed=%+v", removed)
	}
	updated := eventsNamed(got, "playlist_updated")
	if len(updated) != 1 || updated[0].scope != "user-2" {
		t.Fatalf("playlist_updated=%+v", updated)
	}
}
