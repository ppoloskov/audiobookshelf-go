package lists

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/store"
)

// CollectionController.delete closes the RSS feed before destroying the
// collection and emits rss_feed_closed before collection_removed.
func TestWaveGDeleteCollectionClosesRSSFeedBeforeRemoval(t *testing.T) {
	r := newListRig(t)
	exec(t, r.db, `INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('col','Shelf','library-1',?,?)`, stamp, stamp)
	exec(t, r.db, `INSERT INTO feeds(id,slug,entityType,entityId,createdAt,updatedAt) VALUES('feed','shelf','collection','col',?,?)`, stamp, stamp)
	exec(t, r.db, `INSERT INTO feedEpisodes(id,title,createdAt,updatedAt,feedId) VALUES('feed-episode','One',?,?,'feed')`, stamp, stamp)

	denied := store.User{ID: "other", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	req := httptest.NewRequest(http.MethodDelete, "/api/collections/col", nil)
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(store.ContextWithUser(req.Context(), denied)))
	if w.Code != 403 || r.count(`SELECT COUNT(*) FROM feeds WHERE entityId='col'`) != 1 || r.count(`SELECT COUNT(*) FROM feedEpisodes WHERE feedId='feed'`) != 1 || len(r.take()) != 0 {
		t.Fatalf("denied delete: status=%d feeds=%d feedEpisodes=%d events=%+v", w.Code, r.count(`SELECT COUNT(*) FROM feeds WHERE entityId='col'`), r.count(`SELECT COUNT(*) FROM feedEpisodes WHERE feedId='feed'`), r.take())
	}

	if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if got := r.count(`SELECT COUNT(*) FROM feeds WHERE entityId='col'`); got != 0 {
		t.Errorf("collection feed rows after delete = %d, want 0", got)
	}
	if got := r.count(`SELECT COUNT(*) FROM feedEpisodes WHERE feedId='feed'`); got != 0 {
		t.Errorf("feed episode rows after feed close = %d, want 0", got)
	}
	ev := r.take()
	if len(ev) != 2 || ev[0].event != "rss_feed_closed" || ev[0].audience != events.AllUsers || ev[1].event != "collection_removed" || ev[1].audience != events.AllUsers {
		t.Errorf("delete events = %+v, want rss_feed_closed then collection_removed to all users", ev)
	}
	if w := r.do("owner", "DELETE", "/api/collections/col", ""); w.Code != 404 || len(r.take()) != 0 {
		t.Errorf("repeat delete = status %d events %+v; want 404 and no events", w.Code, r.take())
	}
}

func TestWaveGDeleteCollectionCloseFailureRollsBackAndStaysSilent(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
	}{
		{"feed_close", `CREATE TRIGGER reject_feed_close BEFORE DELETE ON feeds BEGIN SELECT RAISE(ABORT,'close rejected'); END`},
		{"collection_delete_after_feed_close", `CREATE TRIGGER reject_collection_delete BEFORE DELETE ON collections BEGIN SELECT RAISE(ABORT,'delete rejected'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newListRig(t)
			exec(t, r.db, `INSERT INTO collections(id,name,libraryId,createdAt,updatedAt) VALUES('col','Shelf','library-1',?,?)`, stamp, stamp)
			exec(t, r.db, `INSERT INTO feeds(id,slug,entityType,entityId,createdAt,updatedAt) VALUES('feed','shelf','collection','col',?,?)`, stamp, stamp)
			exec(t, r.db, `INSERT INTO feedEpisodes(id,title,createdAt,updatedAt,feedId) VALUES('feed-episode','One',?,?,'feed')`, stamp, stamp)
			exec(t, r.db, tc.trigger)

			w := r.do("owner", "DELETE", "/api/collections/col", "")
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("delete status=%d body=%s, want 500", w.Code, w.Body)
			}
			for _, query := range []string{
				`SELECT COUNT(*) FROM collections WHERE id='col'`,
				`SELECT COUNT(*) FROM feeds WHERE id='feed'`,
				`SELECT COUNT(*) FROM feedEpisodes WHERE id='feed-episode'`,
			} {
				if got := r.count(query); got != 1 {
					t.Errorf("%s = %d, want 1 after rollback", query, got)
				}
			}
			if got := r.take(); len(got) != 0 {
				t.Errorf("failed delete emitted events: %+v", got)
			}
		})
	}
}
