package item

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/db"
)

// cascade adds two books c1 and c2 (items ci1, ci2) that share one author, with
// everything that can hang off an item, so a deletion shows what goes and what
// stays.
func (r *rig) cascade() {
	for _, q := range []string{
		`INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('a-only','Only One','One, Only','l1',?,?),('a-both','Both Books','Books, Both','l1',?,?)`,
		`INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES('s-saga','Saga','Saga','l1',?,?)`,
		`INSERT INTO books(id,title,titleIgnorePrefix,narrators,audioFiles,chapters,tags,genres,createdAt,updatedAt) VALUES('c1','First','First','[]','[]','[]','[]','[]',?,?),('c2','Other','Other','[]','[]','[]','[]','[]',?,?)`,
		`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES
			('ci1','11','/nonexistent/books/First','First','c1','book',0,0,0,1,'[]','{}','First','First','l1','f1',?,?),
			('ci2','12','/nonexistent/books/Other','Other','c2','book',0,0,0,1,'[]','{}','Other','Other','l1','f1',?,?)`,
		`INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES('ca1','c1','a-only',?),('ca2','c1','a-both',?),('ca3','c2','a-both',?)`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('cs1','1','c1','s-saga',?)`,
		`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,duration,currentTime,isFinished,userId,createdAt,updatedAt) VALUES('mp1','c1','book',10,5,0,'u1',?,?),('mp2','c1','book',10,10,1,'u2',?,?),('mp3','c2','book',10,1,0,'u1',?,?)`,
		`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES('p-shared','Mix','l1','u1',?,?),('p-solo','Solo','l1','u2',?,?)`,
		`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES('x1','c1','book',1,'p-shared',?),('x2','c2','book',2,'p-shared',?),('x3','c1','book',1,'p-solo',?)`,
		`INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('c-mixed','Mixed','l1',?,?)`,
		`INSERT INTO collectionBooks(id,"order",bookId,collectionId,createdAt) VALUES('cb1',1,'c1','c-mixed',?),('cb2',2,'c2','c-mixed',?)`,
		`INSERT INTO feeds(id,slug,entityType,entityId,createdAt,updatedAt) VALUES('feed1','f','libraryItem','ci1',?,?),('feed2','g','libraryItem','ci2',?,?)`,
		`INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,createdAt,updatedAt) VALUES('sh1','c1','book','one',?,?),('sh2','c2','book','two',?,?)`,
		`INSERT INTO playbackSessions(id,mediaItemId,mediaItemType,userId,libraryId,createdAt,updatedAt) VALUES('ps1','c2','book','u1','l1',?,?)`,
	} {
		r.exec(q, args(strings.Count(q, "?"))...)
	}
}

func (r *rig) audiences(event string) []string {
	var out []string
	for _, e := range r.takeEvents() {
		if e.name == event {
			out = append(out, e.audience)
		}
	}
	return out
}

// Deleting an item removes what hangs off it - progress of every user, playlist
// entries (and playlists left empty), collection entries, its feed and shares,
// the authors and series it leaves empty - and nothing that belongs to the
// other book.
func TestDeleteItemCascadesToEverythingReferencingIt(t *testing.T) {
	r := newRig(t)
	r.cascade()
	r.filters.AddAuthor("l1", "Only One", "a-only")
	rec := r.do(rootUser, "DELETE", "/api/items/ci1", "")
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ what, query, want string }{
		{"items", `SELECT id FROM libraryItems WHERE id LIKE 'ci%'`, "ci2"},
		{"books", `SELECT id FROM books WHERE id LIKE 'c%'`, "c2"},
		{"progress of every user", `SELECT id FROM mediaProgresses ORDER BY id`, "mp3"},
		{"emptied playlist removed", `SELECT id FROM playlists ORDER BY id`, "p-shared"},
		{"playlist entries", `SELECT id FROM playlistMediaItems ORDER BY id`, "x2"},
		{"remaining entry renumbered", `SELECT "order" FROM playlistMediaItems`, "1"},
		{"collection entries", `SELECT id FROM collectionBooks ORDER BY id`, "cb2"},
		{"feeds", `SELECT id FROM feeds ORDER BY id`, "feed2"},
		{"shares", `SELECT id FROM mediaItemShares ORDER BY id`, "sh2"},
		{"empty author goes, shared author stays", `SELECT id FROM authors WHERE id LIKE 'a-%'`, "a-both"},
		{"author links", `SELECT id FROM bookAuthors WHERE id LIKE 'ca%'`, "ca3"},
		{"empty series", `SELECT id FROM series WHERE id='s-saga'`, ""},
		{"playback sessions are history", `SELECT id FROM playbackSessions`, "ps1"},
	} {
		if got := r.values(c.query); got != c.want {
			t.Errorf("%s: %q, want %q", c.what, got, c.want)
		}
	}
	events := r.takeEvents()
	var names []string
	audience := map[string]string{}
	for _, e := range events {
		names = append(names, e.name)
		audience[e.name] += e.audience + ","
	}
	if audience["item_removed"] != "*," || audience["rss_feed_closed"] != "*," {
		t.Errorf("audiences = %v", audience)
	}
	// Playlist events are the list package's: item reports who lost what.
	if got := announcedPlaylists(r.announced); got != "p-shared:u1:updated,p-solo:u2:removed" {
		t.Errorf("announced playlists = %s", got)
	}
	// Single delete announces the swept author before the series, and the feed before the item.
	order := strings.Join(names, ",")
	if !strings.Contains(order, "rss_feed_closed,item_removed,author_removed,series_removed") {
		t.Errorf("events = %s", order)
	}
	if rec := r.do(rootUser, "GET", "/api/items/ci1", ""); rec.Code != 404 {
		t.Errorf("deleted item still served: %d", rec.Code)
	}
}

func announcedPlaylists(changes []PlaylistChange) string {
	var out []string
	for _, c := range changes {
		state := "updated"
		if c.Removed {
			state = "removed"
		}
		out = append(out, c.Playlist.ID+":"+db.Val(c.Playlist.UserID)+":"+state)
	}
	return strings.Join(out, ",")
}

func TestDeleteItemPurgesCoverCacheAndMetadata(t *testing.T) {
	r := newRig(t)
	r.cascade()
	cover := filepath.Join(r.cacheDir, "ci1_200x200.webp")
	meta := filepath.Join(r.metaDir, "ci1", "metadata.json")
	mustWrite(t, cover, "cached")
	mustWrite(t, meta, "{}")
	var feedRowsAtClose []string
	r.h.emit = func(audience, name string, payload any) {
		if name == "rss_feed_closed" {
			// Announced after the commit: the row is already gone.
			feedRowsAtClose = append(feedRowsAtClose, r.values(`SELECT id FROM feeds WHERE id='feed1'`))
			if f := payload.(feedView); f.ID != "feed1" || *f.EntityID != "ci1" {
				t.Errorf("rss_feed_closed payload = %+v", f)
			}
		}
	}
	if rec := r.do(rootUser, "DELETE", "/api/items/ci1", ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	for _, path := range []string{cover, filepath.Dir(meta)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists (%v)", path, err)
		}
	}
	if len(feedRowsAtClose) != 1 || feedRowsAtClose[0] != "" {
		t.Errorf("feed rows seen by close events = %q", feedRowsAtClose)
	}
	// A retry finds nothing and stays silent.
	before := 0
	r.h.emit = func(string, string, any) { before++ }
	if rec := r.do(rootUser, "DELETE", "/api/items/ci1", ""); rec.Code != 404 || before != 0 {
		t.Errorf("retry: %d, %d events", rec.Code, before)
	}
}

func TestDeleteItemRollsBackOnFailure(t *testing.T) {
	r := newRig(t)
	r.cascade()
	cover := filepath.Join(r.cacheDir, "ci1_200x200.webp")
	mustWrite(t, cover, "cached")
	r.exec(`CREATE TRIGGER fail_item_delete BEFORE DELETE ON libraryItems BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	if rec := r.do(rootUser, "DELETE", "/api/items/ci1", ""); rec.Code != 500 || strings.Contains(rec.Body.String(), "rollback") {
		t.Fatalf("status %d body %q, want a fixed 500", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT id FROM feeds WHERE id='feed1'`); got != "feed1" {
		t.Errorf("feed after rollback = %q", got)
	}
	if got := r.values(`SELECT id FROM mediaProgresses WHERE id='mp1'`); got != "mp1" {
		t.Errorf("progress after rollback = %q", got)
	}
	if _, err := os.Stat(cover); err != nil {
		t.Errorf("cover cache removed despite rollback: %v", err)
	}
	if len(r.takeEvents()) != 0 {
		t.Error("a failed deletion emitted events")
	}
}

func TestDeleteItemPermissions(t *testing.T) {
	r := newRig(t)
	if rec := r.do(rootUser, "DELETE", "/api/items/missing", ""); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	if rec := r.do(readerUser, "DELETE", "/api/items/i1", ""); rec.Code != 403 {
		t.Errorf("no delete permission: %d", rec.Code)
	}
	if rec := r.do(podUser, "DELETE", "/api/items/i1", ""); rec.Code != 403 {
		t.Errorf("library not accessible: %d", rec.Code)
	}
	if got := r.values(`SELECT count(*) FROM libraryItems`); got != "3" {
		t.Errorf("items = %s", got)
	}
}

// ?hard=1 removes the item's folder when it lies inside the library, and only then.
func TestDeleteHardRemovesFilesInsideTheLibraryOnly(t *testing.T) {
	r := newRig(t)
	root := t.TempDir()
	inside, outside := filepath.Join(root, "Second"), filepath.Join(t.TempDir(), "Elsewhere")
	mustWrite(t, filepath.Join(inside, "a.mp3"), "x")
	mustWrite(t, filepath.Join(outside, "a.mp3"), "x")
	r.exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, root)
	r.exec(`UPDATE libraryItems SET path=? WHERE id='i2'`, inside)
	r.exec(`UPDATE libraryItems SET path=? WHERE id='i1'`, outside)
	if rec := r.do(rootUser, "DELETE", "/api/items/i2?hard=1", ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Errorf("hard delete kept the folder (%v)", err)
	}
	if rec := r.do(rootUser, "DELETE", "/api/items/i1?hard=1", ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("hard delete removed a folder outside the library: %v", err)
	}
}

func TestBatchDelete(t *testing.T) {
	r := newRig(t)
	r.cascade()
	root := t.TempDir()
	dir := filepath.Join(root, "Other")
	mustWrite(t, filepath.Join(dir, "a.mp3"), "x")
	r.exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, root)
	r.exec(`UPDATE libraryItems SET path=? WHERE id='ci2'`, dir)
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"no ids", `{}`, 400},
		{"empty ids", `{"libraryItemIds":[]}`, 400},
		{"ids not an array", `{"libraryItemIds":"ci1"}`, 400},
		{"nothing found", `{"libraryItemIds":["nope"]}`, 404},
	} {
		if rec := r.do(rootUser, "POST", "/api/items/batch/delete", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	if rec := r.do(readerUser, "POST", "/api/items/batch/delete", `{"libraryItemIds":["ci1"]}`); rec.Code != 403 {
		t.Errorf("no delete permission: %d", rec.Code)
	}
	if rec := r.do(podUser, "POST", "/api/items/batch/delete", `{"libraryItemIds":["ci1"]}`); rec.Code != 403 {
		t.Errorf("library not accessible: %d", rec.Code)
	}
	// Soft: the item and its now-empty author go, the files stay; ci1's series first.
	rec := r.do(rootUser, "POST", "/api/items/batch/delete", `{"libraryItemIds":["ci2"]}`)
	if rec.Code != 200 || rec.Body.String() != "OK" {
		t.Fatalf("soft delete: %d %q", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("soft delete removed files")
	}
	if got := r.takeEventNames(); got != "rss_feed_closed,item_removed" {
		t.Errorf("events = %s", got)
	}
	// Hard, two items at once: series are swept before authors.
	rec = r.do(rootUser, "POST", "/api/items/batch/delete?hard=1", `{"libraryItemIds":["ci1","unknown"]}`)
	if rec.Code != 200 {
		t.Fatalf("hard delete: %d %q", rec.Code, rec.Body)
	}
	if got := r.takeEventNames(); !strings.HasSuffix(got, "item_removed,series_removed,author_removed,author_removed") {
		t.Errorf("events = %s", got)
	}
	if got := r.values(`SELECT count(*) FROM libraryItems WHERE id LIKE 'ci%'`); got != "0" {
		t.Errorf("items left = %s", got)
	}
}
