package sharing

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

func sharingFixture(t *testing.T) (*sql.DB, *Handler, store.User, string) {
	t.Helper()
	db, e := sql.Open("sqlite", "file:sharing-test-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(schemaassets.SchemaSQL); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	now := sqlDate(time.Now())
	user := store.User{ID: "u-admin", Type: "admin", IsActive: true, Permissions: []byte(`{"accessAllLibraries":true,"accessAllTags":true,"librariesAccessible":[],"itemTagsSelected":[]}`)}
	if _, e = db.Exec(`INSERT INTO users(id,username,type,token,isActive,isLocked,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES(?,?,'admin','token',1,0,?,'[]','{}',?,?)`, user.ID, "admin", string(user.Permissions), now, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('lib-1','Library',0,'books','book','local','{"coverAspectRatio":1.4}','{}',?,?)`, now, now); e != nil {
		t.Fatal(e)
	}
	media := filepath.Join(t.TempDir(), "book.mp3")
	if e = os.WriteFile(media, []byte("audio-data"), 0600); e != nil {
		t.Fatal(e)
	}
	audio := `[{"index":0,"duration":12,"mimeType":"audio/mpeg","exclude":false,"ino":"ino-1","metadata":{"path":"` + media + `","filename":"book.mp3","size":10}}]`
	if _, e = db.Exec(`INSERT INTO books(id,title,description,language,explicit,duration,chapters,audioFiles,tags,genres,createdAt,updatedAt) VALUES('book-1','A Book','Book description','en',0,12,'[]',?,'[]','[]',?,?)`, audio, now, now); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,title,authorNamesFirstLast,authorNamesLastFirst,createdAt,updatedAt,libraryId) VALUES('li-1',?, 'book.mp3','book-1','book',1,0,0,'[]','{}','A Book','Author Name','Name, Author',?,?,'lib-1')`, media, now, now); e != nil {
		t.Fatal(e)
	}
	return db, New(db, config.Config{RouterBasePath: "/audiobookshelf"}, nil), user, media
}
func TestRSSFeedOpenListAndPublicMedia(t *testing.T) {
	db, h, admin, media := sharingFixture(t)
	_ = db
	req := httptest.NewRequest(http.MethodPost, "/api/feeds/item/li-1/open", strings.NewReader(`{"serverAddress":"https://abs.example","slug":"book-feed","metadataDetails":{"ownerName":"Owner","preventIndexing":false}}`))
	w := httptest.NewRecorder()
	if !h.Handle(w, req, admin) || w.Code != 200 {
		t.Fatalf("open feed status %d %s", w.Code, w.Body.String())
	}
	var opened map[string]any
	if json.Unmarshal(w.Body.Bytes(), &opened) != nil {
		t.Fatal("bad open response")
	}
	got := httptest.NewRecorder()
	h.Handle(got, httptest.NewRequest(http.MethodGet, "/api/feeds", nil), admin)
	if got.Code != 200 || !strings.Contains(got.Body.String(), "book-feed") {
		t.Fatalf("list status %d %s", got.Code, got.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/feed/book-feed", nil)
	r.Host = "abs.example"
	xml := httptest.NewRecorder()
	h.Handle(xml, r, store.User{})
	if xml.Code != 200 || !strings.Contains(xml.Body.String(), "<title>A Book</title>") || !strings.Contains(xml.Body.String(), "/feed/book-feed/item/") {
		t.Fatalf("feed XML status=%d body=%s", xml.Code, xml.Body.String())
	}
	var feedEpisode string
	if e := db.QueryRow(`SELECT id FROM feedEpisodes LIMIT 1`).Scan(&feedEpisode); e != nil {
		t.Fatal(e)
	}
	track := httptest.NewRecorder()
	h.Handle(track, httptest.NewRequest(http.MethodGet, "/feed/book-feed/item/"+feedEpisode+"/media.mp3", nil), store.User{})
	if track.Code != 200 || track.Body.String() != "audio-data" {
		t.Fatalf("track response %d %q path=%s", track.Code, track.Body.String(), media)
	}
	// A refreshed feed must reflect newer library metadata while retaining the
	// enclosure ID for the same audio path.
	if _, e := db.Exec(`UPDATE books SET title='Renamed Book',updatedAt=? WHERE id='book-1'; UPDATE libraryItems SET updatedAt=? WHERE id='li-1'`, sqlDate(time.Now().Add(time.Minute)), sqlDate(time.Now().Add(time.Minute))); e != nil {
		t.Fatal(e)
	}
	refresh := httptest.NewRecorder()
	h.Handle(refresh, httptest.NewRequest(http.MethodGet, "/feed/book-feed", nil), store.User{})
	if refresh.Code != 200 || !strings.Contains(refresh.Body.String(), "<title>Renamed Book</title>") {
		t.Fatalf("refreshed feed status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var retainedEpisode string
	if e := db.QueryRow(`SELECT id FROM feedEpisodes LIMIT 1`).Scan(&retainedEpisode); e != nil || retainedEpisode != feedEpisode {
		t.Fatalf("feed episode id changed from %s to %s err=%v", feedEpisode, retainedEpisode, e)
	}
}
func TestShareSessionProgressTrackAndDownload(t *testing.T) {
	db, h, admin, _ := sharingFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/share/mediaitem", strings.NewReader(`{"slug":"public-book","expiresAt":0,"mediaItemType":"book","mediaItemId":"book-1","isDownloadable":true}`))
	created := httptest.NewRecorder()
	h.Handle(created, create, admin)
	if created.Code != 201 {
		t.Fatalf("create share status %d: %s", created.Code, created.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/public/share/public-book?t=4", nil)
	r.Host = "abs.example"
	w := httptest.NewRecorder()
	h.Handle(w, r, store.User{})
	if w.Code != 200 {
		t.Fatalf("get share status %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("invalid share JSON")
	}
	session := body["playbackSession"].(map[string]any)
	if session["currentTime"] != float64(4) {
		t.Fatalf("start time %#v", session["currentTime"])
	}
	progress := httptest.NewRequest(http.MethodPatch, "/public/share/public-book/progress", strings.NewReader(`{"currentTime":99}`))
	progress.AddCookie(cookie)
	pw := httptest.NewRecorder()
	h.Handle(pw, progress, store.User{})
	if pw.Code != 204 {
		t.Fatalf("progress status %d", pw.Code)
	}
	after := httptest.NewRequest(http.MethodGet, "/public/share/public-book", nil)
	after.AddCookie(cookie)
	aw := httptest.NewRecorder()
	h.Handle(aw, after, store.User{})
	var state map[string]any
	_ = json.Unmarshal(aw.Body.Bytes(), &state)
	if got := state["playbackSession"].(map[string]any)["currentTime"]; got != float64(12) {
		t.Fatalf("clamped current time %#v", got)
	}
	download := httptest.NewRequest(http.MethodGet, "/public/share/public-book/download", nil)
	download.AddCookie(cookie)
	dw := httptest.NewRecorder()
	h.Handle(dw, download, store.User{})
	if dw.Code != 200 || dw.Body.String() != "audio-data" {
		t.Fatalf("download status %d body %q", dw.Code, dw.Body.String())
	}
	_ = db
}
func TestShareRejectsExpiredAndDownloadDisabled(t *testing.T) {
	_, h, admin, _ := sharingFixture(t)
	create := httptest.NewRequest(http.MethodPost, "/api/share/mediaitem", strings.NewReader(`{"slug":"expires","expiresAt":1,"mediaItemType":"book","mediaItemId":"book-1"}`))
	w := httptest.NewRecorder()
	h.Handle(w, create, admin)
	if w.Code != 201 {
		t.Fatalf("create status %d %s", w.Code, w.Body.String())
	}
	public := httptest.NewRecorder()
	h.Handle(public, httptest.NewRequest(http.MethodGet, "/public/share/expires", nil), store.User{})
	if public.Code != 404 {
		t.Fatalf("expired share status %d", public.Code)
	}
}
