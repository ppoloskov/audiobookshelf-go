package userapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// MeController.batchUpdateMediaProgress logs and skips invalid entries,
// persists valid entries, and emits user_updated once if any entry succeeds.
func TestWaveGBatchProgressSkipsInvalidAndAnnouncesOnce(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	for _, stmt := range []string{
		`INSERT INTO libraries(id,displayOrder) VALUES('lib1',1)`,
		`INSERT INTO books(id,title) VALUES('book1','Book')`,
		`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId) VALUES('li1','book1','book','lib1')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	var events []string
	h := newUserAPIForTest(db, config.Config{}, func(_ string, event string, _ any) { events = append(events, event) })
	req := httptest.NewRequest(http.MethodPatch, "/api/me/progress/batch/update", strings.NewReader(`[
		{"libraryItemId":"missing","currentTime":7,"duration":100},
		{"libraryItemId":"li1","currentTime":42,"duration":100}
	]`))
	w := httptest.NewRecorder()
	if !h.Handle(w, req, user) || w.Code != http.StatusOK {
		t.Fatalf("batch update handled/status/body = %v/%d/%s", true, w.Code, w.Body.String())
	}
	var rows int
	var mediaItemID string
	var currentTime float64
	if err := db.QueryRow(`SELECT COUNT(*),MAX(mediaItemId),MAX(currentTime) FROM mediaProgresses WHERE userId='u1'`).Scan(&rows, &mediaItemID, &currentTime); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || mediaItemID != "book1" || currentTime != 42 {
		t.Errorf("progress rows=%d item=%q currentTime=%v; want one book1 row at 42", rows, mediaItemID, currentTime)
	}
	if len(events) != 1 || events[0] != "user_updated" {
		t.Errorf("events = %v, want one user_updated after successful entry", events)
	}
}
