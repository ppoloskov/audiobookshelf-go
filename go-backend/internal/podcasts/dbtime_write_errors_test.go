package podcasts

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// Regression for the craft-review MAJOR finding (craft 2026-10-30): the manual
// checknew flow must not report success when the lastEpisodeCheck write fails.
func TestManualCheckNewReturns500WhenEpisodeCheckWriteFails(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title><item><title>New Episode</title><guid>guid-new</guid><pubDate>Tue, 29 Oct 2024 10:00:00 GMT</pubDate><enclosure url="http://127.0.0.1/ep.mp3" length="1" type="audio/mpeg"/></item></channel></rss>`))
	}))
	defer feed.Close()

	db, err := sql.Open("sqlite", "file:podcast-write-error-test?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	h.SetHTTPClientForTests(feed.Client())
	// Drop the only connection so the podcast update write fails while the
	// feed fetch (pure HTTP) still succeeds. go-sqlite pools open lazily and
	// hold a single connection; closing it deterministically fails the write.
	if _, err := db.Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	item := podcastItem{ID: "pod-1", MediaID: "pod-1", FeedURL: sql.NullString{String: feed.URL + "/rss", Valid: true}}
	w := httptest.NewRecorder()
	h.checkNew(w, httptest.NewRequest(http.MethodGet, "/checknew?limit=3", nil), item)
	if w.Code == http.StatusOK {
		t.Fatalf("checknew reported success despite failed lastEpisodeCheck write: %d %s", w.Code, w.Body.String())
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("checknew status = %d, want 500 (body %s)", w.Code, w.Body.String())
	}
}
