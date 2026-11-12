package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
	"golang.org/x/net/websocket"
)

func TestApplyLoggingSettings(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.SetLevel(before) })
	if keep := ApplyLoggingSettings(settings.Server{ServerPublic: settings.ServerPublic{LogLevel: 3, LoggerDailyLogsToKeep: 9}}); keep != 9 {
		t.Fatalf("keep = %d, want 9", keep)
	}
	if l.Level() != logging.LevelWarn {
		t.Fatalf("level = %v, want WARN", l.Level())
	}
	// A falsy stored level keeps the current one (ServerSettings.js:150).
	ApplyLoggingSettings(settings.Server{})
	if l.Level() != logging.LevelWarn {
		t.Fatalf("level after 0 = %v, want WARN", l.Level())
	}
}

// The runtime wires set_log_listener to the logger: an admin socket that
// registered a level receives "log" events with the upstream LogObject shape.
func TestSetLogListenerDeliversLogEvents(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	db, err := OpenDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	rt := manager.current.Load()
	srv := httptest.NewServer(rt)
	defer srv.Close()

	socket := openSocket(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/socket.io/?EIO=4&transport=websocket", makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`))
	if err := websocket.Message.Send(socket, `42["set_log_listener",2]`); err != nil {
		t.Fatal(err)
	}
	// Log until the registration is in effect and the probe line arrives.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				logging.Info("log listener probe")
			}
		}
	}()
	_ = socket.SetDeadline(time.Now().Add(10 * time.Second))
	for {
		var msg string
		if err := websocket.Message.Receive(socket, &msg); err != nil {
			t.Fatalf("no log event arrived: %v", err)
		}
		if !strings.HasPrefix(msg, `42["log",`) {
			continue
		}
		var frame []json.RawMessage
		if err := json.Unmarshal([]byte(msg[2:]), &frame); err != nil || len(frame) != 2 {
			t.Fatalf("log frame %q: %v", msg, err)
		}
		var entry map[string]any
		if err := json.Unmarshal(frame[1], &entry); err != nil {
			t.Fatal(err)
		}
		if entry["message"] != "log listener probe" {
			continue
		}
		if len(entry) != 5 || entry["levelName"] != "INFO" || entry["level"] != float64(2) || entry["source"] == nil || entry["timestamp"] == nil {
			t.Fatalf("log entry = %v, want timestamp, source, message, levelName and level", entry)
		}
		return
	}
}

func TestLoggerDataEndpointThroughRuntime(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	db, err := OpenDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	rt := manager.current.Load()
	logging.Info("logger-data marker")
	r := httptest.NewRequest(http.MethodGet, "/api/logger-data", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`))
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, r)
	body := w.Body.String()
	var data struct {
		CurrentDailyLogs []map[string]any `json:"currentDailyLogs"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%.300s err=%v", w.Code, body, err)
	}
	found := false
	for _, entry := range data.CurrentDailyLogs {
		if entry["message"] == "logger-data marker" && entry["levelName"] == "INFO" && entry["level"] == float64(2) {
			found = true
		}
	}
	if !found {
		t.Fatalf("logger-data marker missing: %.300s", body)
	}
}
