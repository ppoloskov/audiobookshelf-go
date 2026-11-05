package lists

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	_ "modernc.org/sqlite"
)

func TestPlaylistReadValues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, query := range []string{
		`CREATE TABLE playlists (id TEXT PRIMARY KEY,name TEXT,description TEXT,createdAt DATETIME,updatedAt DATETIME,libraryId TEXT,userId TEXT)`,
		`CREATE TABLE playlistMediaItems (id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,createdAt DATETIME,playlistId TEXT)`,
		`CREATE TABLE libraryItems (id TEXT PRIMARY KEY,mediaId TEXT,mediaType TEXT)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY,podcastId TEXT)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	created := dbtime.Format(time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC))
	updated := dbtime.Format(time.Date(2024, 4, 6, 7, 8, 9, 0, time.UTC))
	if _, err := db.Exec(`INSERT INTO playlists VALUES ('playlist-1','Road trip',NULL,?,?, 'library-1','user-1')`, created, updated); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, mediaID, mediaType string
		order                  int
	}{
		{"entry-book-1", "book-1", "book", 1},
		{"entry-episode", "episode-1", "podcastEpisode", 2},
		{"entry-orphan-book", "missing-book", "book", 3},
		{"entry-orphan-episode", "missing-episode", "podcastEpisode", 4},
		{"entry-hidden-episode", "episode-hidden", "podcastEpisode", 5},
		{"entry-book-2", "book-2", "book", 6},
	} {
		if _, err := db.Exec(`INSERT INTO playlistMediaItems VALUES (?,?,?,?,?, 'playlist-1')`, row.id, row.mediaID, row.mediaType, row.order, created); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, mediaID, mediaType string }{
		{"item-book-1", "book-1", "book"},
		{"item-podcast", "podcast-1", "podcast"},
		{"item-book-2", "book-2", "book"},
	} {
		if _, err := db.Exec(`INSERT INTO libraryItems VALUES (?,?,?)`, row.id, row.mediaID, row.mediaType); err != nil {
			t.Fatal(err)
		}
	}
	for _, episode := range []string{"episode-1", "episode-hidden"} {
		if _, err := db.Exec(`INSERT INTO podcastEpisodes VALUES (?, 'podcast-1')`, episode); err != nil {
			t.Fatal(err)
		}
	}
	reader := &playlistItemReader{}

	got, libraryID, err := Playlist(context.Background(), db, reader, "playlist-1")
	if err != nil {
		t.Fatal(err)
	}
	if libraryID != "library-1" {
		t.Fatalf("libraryID=%q, want library-1", libraryID)
	}
	if got["description"] != nil {
		t.Fatalf("description=%#v, want null", got["description"])
	}
	if got["lastUpdate"] != time.Date(2024, 4, 6, 7, 8, 9, 0, time.UTC).UnixMilli() || got["createdAt"] != time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC).UnixMilli() {
		t.Fatalf("playlist times: lastUpdate=%v createdAt=%v", got["lastUpdate"], got["createdAt"])
	}
	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("items has type %T", got["items"])
	}
	wantItems := []any{
		map[string]any{"libraryItemId": "item-book-1", "libraryItem": map[string]any{"id": "item-book-1", "expanded": true}},
		map[string]any{"episodeId": "episode-1", "episode": map[string]any{"id": "episode-1", "title": "Episode 1"}, "libraryItemId": "item-podcast", "libraryItem": map[string]any{"id": "item-podcast", "minified": true}},
		map[string]any{"libraryItemId": "item-book-2", "libraryItem": map[string]any{"id": "item-book-2", "expanded": true}},
	}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("playlist items=%#v, want %#v", items, wantItems)
	}
	if !reflect.DeepEqual(reader.expanded, []string{"item-book-1", "item-podcast", "item-podcast", "item-book-2"}) || !reflect.DeepEqual(reader.minified, []string{"item-podcast"}) {
		t.Fatalf("item reader calls: expanded=%v minified=%v", reader.expanded, reader.minified)
	}
}

type playlistItemReader struct {
	expanded []string
	minified []string
}

func (r *playlistItemReader) ItemJSON(_ context.Context, libraryItemID string, expanded bool) (map[string]any, error) {
	r.expanded = append(r.expanded, libraryItemID)
	if !expanded {
		return nil, nil
	}
	if libraryItemID == "item-podcast" {
		return map[string]any{"id": libraryItemID, "media": map[string]any{"episodes": []map[string]any{{"id": "episode-1", "title": "Episode 1"}}}}, nil
	}
	return map[string]any{"id": libraryItemID, "expanded": true}, nil
}

func (r *playlistItemReader) ItemMinifiedJSON(_ context.Context, libraryItemID string) (map[string]any, error) {
	r.minified = append(r.minified, libraryItemID)
	return map[string]any{"id": libraryItemID, "minified": true}, nil
}

var _ items.Reader = (*playlistItemReader)(nil)
