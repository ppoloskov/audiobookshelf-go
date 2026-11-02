package userapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

// playlistOrderNames mixes case, accents, digits and punctuation. The want
// slice is the output of
//
//	node -e 'console.log(names.sort((a, b) => a.localeCompare(b)))'
//
// on Node 22 (ICU 78), i.e. Playlist.getOldPlaylistsForUserAndLibrary's
// `name.localeCompare` ordering (server/models/Playlist.js:85). Byte order
// would put every capital letter before every lowercase letter.
var playlistOrderNames = []string{"b", "B", "a", "A", "Á", "á", "10", "9", "Zoe", "zoe", "Éclair", "eclair", "_x", "-x", "Commute"}
var playlistOrderWant = []string{"_x", "-x", "10", "9", "a", "A", "á", "Á", "b", "B", "Commute", "eclair", "Éclair", "zoe", "Zoe"}

func playlistOrderFixture(t *testing.T) (*sql.DB, *Handler, store.User) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:playlist-order-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schemaassets.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000 +00:00")
	perms := `{"accessAllLibraries":true,"accessAllTags":true,"librariesAccessible":[],"itemTagsSelected":[]}`
	if _, err := db.Exec(`INSERT INTO users(id,username,type,token,isActive,isLocked,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES('u1','reader','user','tok',1,0,?,'[]','{}',?,?)`, perms, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('lib1','Library',0,'books','book','google','{}','{}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for i, name := range playlistOrderNames {
		if _, err := db.Exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES(?,?,'lib1','u1',?,?)`, "p"+string(rune('a'+i)), name, now, now); err != nil {
			t.Fatal(err)
		}
	}
	user := store.User{ID: "u1", Username: "reader", Type: "user", Token: "tok", IsActive: true, Permissions: json.RawMessage(perms), Bookmarks: json.RawMessage(`[]`)}
	return db, newUserAPIForTest(db, config.Config{}, nil), user
}

func playlistNames(t *testing.T, body []byte, key string) []string {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(payload[key], &list); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	out := make([]string, 0)
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}

func TestPlaylistListsSortByLocaleCompare(t *testing.T) {
	_, h, user := playlistOrderFixture(t)
	for _, tc := range []struct{ path, key string }{
		{"/api/playlists", "playlists"},
		{"/api/libraries/lib1/playlists", "results"},
	} {
		w := httptest.NewRecorder()
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, tc.path, nil), user) || w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", tc.path, w.Code, w.Body.String())
		}
		if got := playlistNames(t, w.Body.Bytes(), tc.key); !slices.Equal(got, playlistOrderWant) {
			t.Errorf("GET %s order = %q, want %q", tc.path, got, playlistOrderWant)
		}
	}
}
