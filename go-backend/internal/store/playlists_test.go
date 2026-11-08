package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRemoveFromPlaylistsRenumbersAndDropsEmpty(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE playlists(id TEXT PRIMARY KEY, userId TEXT)`,
		`CREATE TABLE playlistMediaItems(id TEXT PRIMARY KEY, mediaItemId TEXT, mediaItemType TEXT, "order" INTEGER, playlistId TEXT)`,
		`INSERT INTO playlists VALUES ('p1','u'),('p2','u'),('p3','u')`,
		`INSERT INTO playlistMediaItems VALUES ('a','m1','x',1,'p1'),('b','m2','x',2,'p1'),('c','m3','x',3,'p1'),('d','m2','x',1,'p2')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveFromPlaylists(context.Background(), db, []string{"m2"}); err != nil {
		t.Fatal(err)
	}
	var got string
	rows, _ := db.Query(`SELECT id, "order", playlistId FROM playlistMediaItems ORDER BY id`)
	for rows.Next() {
		var id, pl string
		var order int
		_ = rows.Scan(&id, &order, &pl)
		got += fmt.Sprintf("%s%d%s ", id, order, pl)
	}
	rows.Close()
	if got != "a1p1 c2p1 " {
		t.Errorf("entries = %q", got)
	}
	var playlists string
	_ = db.QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM playlists ORDER BY id)`).Scan(&playlists)
	if playlists != "p1,p3" { // p2 emptied and removed; p3 never held the media and is untouched
		t.Errorf("playlists = %q", playlists)
	}
}
