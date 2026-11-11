package adminops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

func TestLoggerDataAdminOnlyAndShape(t *testing.T) {
	h := New(adminopsDB(t), config.Config{}, nil, Deps{})
	logging.Info("adminops logger-data marker")
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodGet, "/api/logger-data", nil), store.User{Type: "user"})
	if w.Code != 403 {
		t.Fatalf("non-admin status %d", w.Code)
	}
	w = httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodGet, "/api/logger-data", nil), store.User{Type: "admin"})
	var out struct {
		CurrentDailyLogs []struct {
			Message   string `json:"message"`
			LevelName string `json:"levelName"`
			Level     int    `json:"level"`
		} `json:"currentDailyLogs"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.CurrentDailyLogs) == 0 {
		t.Fatalf("status %d body %.200s", w.Code, w.Body.String())
	}
	found := false
	for _, e := range out.CurrentDailyLogs {
		found = found || (e.Message == "adminops logger-data marker" && e.LevelName == "INFO" && e.Level == 2)
	}
	if !found {
		t.Fatal("marker entry missing from currentDailyLogs")
	}
}

// A 500 answers in Express framing and records the cause with the route.
func TestServerErrorLogsCauseAndKeepsResponse(t *testing.T) {
	db := adminopsDB(t)
	if _, err := db.Exec(`DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil, Deps{})
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodPatch, "/api/settings?x=1", strings.NewReader(`{"language":"fr"}`)), store.User{Type: "admin"})
	if w.Code != 500 || w.Body.String() != "database error" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	for _, e := range logging.Default().Entries() {
		if e.LevelName == "ERROR" && strings.HasPrefix(e.Message, "[HTTP] PATCH /api/settings: database error: ") && strings.Contains(e.Message, "no such table: settings") {
			return
		}
	}
	t.Fatal("underlying error was not logged")
}

func TestSettingsPatchLogLevelChangesLogger(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.SetLevel(before) })
	h := New(adminopsDB(t), config.Config{}, nil, Deps{})
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodPatch, "/api/settings", strings.NewReader(`{"logLevel":4}`)), store.User{Type: "admin"})
	if w.Code != 200 || l.Level() != logging.LevelError {
		t.Fatalf("status %d level %v", w.Code, l.Level())
	}
}
