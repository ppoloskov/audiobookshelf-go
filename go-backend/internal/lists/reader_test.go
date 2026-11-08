package lists

import (
	"context"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/playlistview"
)

func TestPlaylistReadValues(t *testing.T) {
	db := schemaDB(t)
	created := dbtime.Format(time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC))
	updated := dbtime.Format(time.Date(2024, 4, 6, 7, 8, 9, 0, time.UTC))
	exec(t, db, `INSERT INTO playlists (id,name,description,createdAt,updatedAt,libraryId,userId) VALUES ('playlist-1','Road trip',NULL,?,?, 'library-1','user-1')`, created, updated)
	addBook(t, db, "item-book-1", "book-1", "First")
	addBook(t, db, "item-book-2", "book-2", "Second")
	exec(t, db, `INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('podcast-1','Show',0,0,'[]','[]',1,?,?)`, stamp, stamp)
	exec(t, db, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('item-podcast','/p','p','podcast-1','podcast',0,0,0,5,'[]',?,?,'library-1')`, stamp, stamp)
	exec(t, db, `INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,extraData,createdAt,updatedAt) VALUES ('episode-1','podcast-1','Episode 1','{"ino":"e1","duration":3,"metadata":{"filename":"e.mp3","size":7}}','[]','{}',?,?)`, stamp, stamp)
	for i, row := range [][2]string{
		{"book-1", "book"}, {"episode-1", "podcastEpisode"}, {"missing-book", "book"}, {"missing-episode", "podcastEpisode"}, {"book-2", "book"},
	} {
		exec(t, db, `INSERT INTO playlistMediaItems (id,mediaItemId,mediaItemType,"order",createdAt,playlistId) VALUES (?,?,?,?,?, 'playlist-1')`, "entry-"+row[0], row[0], row[1], i+1, created)
	}

	got, libraryID, err := playlistview.Load(context.Background(), db, "playlist-1")
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
	items := asJSON(t, got)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items=%#v, want book, episode, book (orphans dropped)", items)
	}
	var ids []any
	for _, raw := range items {
		ids = append(ids, raw.(map[string]any)["libraryItemId"])
	}
	if !reflect.DeepEqual(ids, []any{"item-book-1", "item-podcast", "item-book-2"}) {
		t.Fatalf("item order=%v", ids)
	}
	book := items[0].(map[string]any)
	if _, has := book["episodeId"]; has || book["libraryItem"].(map[string]any)["media"].(map[string]any)["audioFiles"] == nil {
		t.Fatalf("book entry should embed the expanded item: %#v", book)
	}
	entry := items[1].(map[string]any)
	media := entry["libraryItem"].(map[string]any)["media"].(map[string]any)
	if entry["episodeId"] != "episode-1" || entry["episode"].(map[string]any)["title"] != "Episode 1" || entry["episode"].(map[string]any)["audioTrack"] == nil {
		t.Fatalf("episode entry=%#v", entry)
	}
	if _, has := media["episodes"]; has || media["numEpisodes"] != float64(0) {
		t.Fatalf("episode entry's podcast must be minified: %#v", media)
	}
}
