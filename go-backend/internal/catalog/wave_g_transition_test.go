package catalog

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/filterdata"
)

func seedCoverCache(t *testing.T, root, id string) string {
	t.Helper()
	cacheDir := filepath.Join(root, "cache", "covers")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cacheDir, id+"_200x200.webp")
	if err := os.WriteFile(path, []byte("cached cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func eventNames(events []recordedEvent) []string {
	names := make([]string, len(events))
	for i, event := range events {
		names[i] = event.event
	}
	return names
}

func eventIndex(events []recordedEvent, name string, after int) int {
	for i := after + 1; i < len(events); i++ {
		if events[i].event == name {
			return i
		}
	}
	return -1
}

func TestWaveGDeleteItemClosesFeedAndPurgesCoverCache(t *testing.T) {
	r := newCascadeRig(t)
	cache := filepath.Join(t.TempDir(), "metadata")
	// Rebuild the handler against a metadata tree whose cover cache we can inspect.
	seed := seedCoverCache(t, cache, "i1")
	feedRowsAtClose := []int{}
	h := New(r.db, config.Config{MetadataPath: cache}, func(audience, event string, payload any) {
		if event == "rss_feed_closed" {
			var count int
			if err := r.db.QueryRow(`SELECT COUNT(*) FROM feeds WHERE id='feed1'`).Scan(&count); err != nil {
				t.Errorf("read feed row from post-commit event callback: %v", err)
			} else {
				feedRowsAtClose = append(feedRowsAtClose, count)
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, recordedEvent{event, payload})
		r.to[event] = append(r.to[event], audience)
	}, Deps{Filters: filterdata.NewCache()})
	r.mux = waveGHTTPMux(h)

	if w := r.do("DELETE", "/api/items/i1"); w.Code != 200 {
		t.Fatalf("delete item: %d %s", w.Code, w.Body)
	}
	if got := r.ids(`SELECT id FROM feeds WHERE entityType='libraryItem' AND entityId='i1'`); got != "" {
		t.Errorf("deleted item's persisted feed rows = %q, want none", got)
	}
	if _, err := os.Stat(seed); !os.IsNotExist(err) {
		t.Errorf("cover cache still exists after delete: stat err %v", err)
	}
	if closeAt, removedAt := eventIndex(r.events, "rss_feed_closed", -1), eventIndex(r.events, "item_removed", -1); closeAt < 0 || removedAt < 0 || closeAt >= removedAt {
		t.Errorf("rss_feed_closed must precede item_removed; events=%v", eventNames(r.events))
	}
	if len(feedRowsAtClose) != 1 || feedRowsAtClose[0] != 0 {
		t.Errorf("feed row counts observed during close events = %v, want [0]", feedRowsAtClose)
	}
	var closePayload feeds.Minified
	for _, event := range r.events {
		if event.event == "rss_feed_closed" {
			closePayload, _ = event.payload.(feeds.Minified)
		}
	}
	if closePayload.ID != "feed1" || closePayload.EntityID == nil || *closePayload.EntityID != "i1" {
		t.Errorf("rss_feed_closed payload = %#v, want feed1 for i1", closePayload)
	}
	// Retrying after the row is gone is a no-op (404) and must stay silent.
	beforeRetry := len(r.events)
	_, beforeCacheErr := os.Stat(seed)
	if w := r.do("DELETE", "/api/items/i1"); w.Code != http.StatusNotFound {
		t.Errorf("retry delete status = %d, want 404", w.Code)
	}
	if len(r.events) != beforeRetry {
		t.Errorf("missing-item retry emitted events: %v", eventNames(r.events[beforeRetry:]))
	}
	_, afterCacheErr := os.Stat(seed)
	if (beforeCacheErr == nil) != (afterCacheErr == nil) {
		t.Errorf("cache state changed on no-op retry: before=%v after=%v", beforeCacheErr, afterCacheErr)
	}
	if len(feedRowsAtClose) != 1 {
		t.Errorf("feed close callback count after no-op retry = %d, want 1", len(feedRowsAtClose))
	}
	if got := eventNames(r.events); len(got) == 0 {
		t.Errorf("delete emitted no events")
	}
	if got := r.to["item_removed"]; len(got) != 1 {
		t.Errorf("item_removed audiences = %v, want one broadcast", got)
	}
	if closePayload.ID == "" && len(r.events) > 0 {
		for _, event := range r.events {
			if event.event == "rss_feed_closed" {
				t.Errorf("unexpected feed close payload type %T", event.payload)
			}
		}
	}
}

func TestWaveGDeleteLibraryClosesEachFeedAndPurgesCoverCaches(t *testing.T) {
	r := newCascadeRig(t)
	cache := filepath.Join(t.TempDir(), "metadata")
	first := seedCoverCache(t, cache, "i1")
	second := seedCoverCache(t, cache, "i2")
	feedRowsAtClose := []int{}
	h := New(r.db, config.Config{MetadataPath: cache}, func(audience, event string, payload any) {
		if event == "rss_feed_closed" {
			var count int
			if err := r.db.QueryRow(`SELECT COUNT(*) FROM feeds WHERE id IN ('feed1','feed2')`).Scan(&count); err != nil {
				t.Errorf("read feed rows from post-commit event callback: %v", err)
			} else {
				feedRowsAtClose = append(feedRowsAtClose, count)
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, recordedEvent{event, payload})
		r.to[event] = append(r.to[event], audience)
	}, Deps{Filters: filterdata.NewCache()})
	r.mux = waveGHTTPMux(h)

	if w := r.do("DELETE", "/api/libraries/l1"); w.Code != 200 {
		t.Fatalf("delete library: %d %s", w.Code, w.Body)
	}
	if got := r.ids(`SELECT id FROM feeds WHERE entityType='libraryItem' ORDER BY id`); got != "" {
		t.Errorf("deleted library's persisted item feeds = %q, want none", got)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cover cache %q still exists after delete: stat err %v", path, err)
		}
	}
	if len(feedRowsAtClose) != 2 {
		t.Errorf("post-commit feed close observations = %v, want two events", feedRowsAtClose)
	} else {
		for _, count := range feedRowsAtClose {
			if count != 0 {
				t.Errorf("feed rows visible to close callback = %d, want committed deletion", count)
			}
		}
	}
	close1, close2 := eventIndex(r.events, "rss_feed_closed", -1), -1
	if close1 >= 0 {
		close2 = eventIndex(r.events, "rss_feed_closed", close1)
	}
	libraryRemoved := eventIndex(r.events, "library_removed", -1)
	item1, item2 := eventIndex(r.events, "item_removed", -1), -1
	if item1 >= 0 {
		item2 = eventIndex(r.events, "item_removed", item1)
	}
	if close1 < 0 || close2 < 0 || item1 < 0 || item2 < 0 || libraryRemoved < 0 || close1 >= item1 || close2 >= item2 || item1 >= libraryRemoved || item2 >= libraryRemoved {
		t.Errorf("each feed close and item removal must precede library_removed; events=%v", eventNames(r.events))
	}
	if got := r.to["rss_feed_closed"]; len(got) != 2 || got[0] != "*" || got[1] != "*" {
		t.Errorf("rss_feed_closed audiences = %v, want two broadcasts", got)
	}
}

func TestWaveGDeleteRollbackKeepsFeedCacheAndEmitsNothing(t *testing.T) {
	r := newCascadeRig(t)
	cache := filepath.Join(t.TempDir(), "metadata")
	cover := seedCoverCache(t, cache, "i1")
	h := New(r.db, config.Config{MetadataPath: cache}, func(audience, event string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, recordedEvent{event, payload})
		r.to[event] = append(r.to[event], audience)
	}, Deps{Filters: filterdata.NewCache()})
	r.mux = waveGHTTPMux(h)
	r.exec(`CREATE TRIGGER fail_item_delete BEFORE DELETE ON libraryItems BEGIN SELECT RAISE(ABORT,'test rollback'); END`)

	if w := r.do("DELETE", "/api/items/i1"); w.Code != http.StatusInternalServerError {
		t.Fatalf("delete with forced transaction failure: %d %s", w.Code, w.Body)
	}
	if got := r.ids(`SELECT id FROM feeds WHERE id='feed1'`); got != "feed1" {
		t.Errorf("feed row after rollback = %q, want feed1", got)
	}
	if _, err := os.Stat(cover); err != nil {
		t.Errorf("cover cache removed despite rollback: %v", err)
	}
	if len(r.events) != 0 {
		t.Errorf("failed deletion emitted events: %v", eventNames(r.events))
	}
}

func waveGHTTPMux(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.Routes(mux)
	return mux
}
