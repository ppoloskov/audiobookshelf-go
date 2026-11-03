package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

func TestApplyLoggingSettings(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.ChangeLevel(before); l.ApplySettings(0) })
	ApplyLoggingSettings(map[string]any{"logLevel": float64(3)})
	if l.Level() != logging.LevelWarn {
		t.Fatalf("level = %v, want WARN", l.Level())
	}
	// A falsy stored level keeps the current one (ServerSettings.js:150).
	ApplyLoggingSettings(map[string]any{"logLevel": float64(0)})
	if l.Level() != logging.LevelWarn {
		t.Fatalf("level after 0 = %v, want WARN", l.Level())
	}
	ApplyLoggingSettings(map[string]any{})
	if l.Level() != logging.LevelWarn {
		t.Fatalf("level after absent = %v, want WARN", l.Level())
	}
}

// The runtime wires set_log_listener to the logger: an admin socket that
// registered a level receives "log" events with the upstream LogObject shape.
func TestSetLogListenerDeliversLogEvents(t *testing.T) {
	clientModules, _ := filepath.Abs("../../../client/node_modules")
	if _, err := os.Stat(filepath.Join(clientModules, "socket.io-client")); err != nil {
		t.Skip("socket.io-client is not installed")
	}
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	rt, err := newRuntime(db.DB(), cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	srv := httptest.NewServer(rt)
	defer srv.Close()

	const probe = `
const { io } = require('socket.io-client');
const socket = io(process.env.ABS_URL, { path: '/socket.io', transports: ['polling'], upgrade: false });
const timer = setTimeout(() => { console.error('timeout'); process.exit(1) }, 8000);
socket.on('connect', () => socket.emit('auth', process.env.ABS_TOKEN));
socket.on('init', () => { socket.emit('set_log_listener', 2); console.log('listening') });
socket.on('log', (entry) => {
  if (entry.message !== 'log listener probe') return;
  const keys = Object.keys(entry).join(',');
  if (keys !== 'timestamp,source,message,levelName,level' || entry.levelName !== 'INFO' || entry.level !== 2) {
    console.error('bad entry ' + JSON.stringify(entry)); process.exit(1)
  }
  clearTimeout(timer); console.log('got ' + JSON.stringify(entry)); process.exit(0)
});
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", probe)
	cmd.Env = append(cmd.Environ(), "NODE_PATH="+clientModules, "ABS_URL="+srv.URL,
		"ABS_TOKEN="+makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`))
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("socket probe failed: %v\n%s", err, output.String())
			}
			return
		case <-tick.C:
			logging.Info("log listener probe")
		}
	}
}

func TestLoggerDataEndpointThroughRuntime(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	rt, err := newRuntime(db.DB(), cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	logging.Info("logger-data marker")
	r := httptest.NewRequest(http.MethodGet, "/api/logger-data", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`))
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(body, `{"currentDailyLogs":[`) || !strings.Contains(body, `"message":"logger-data marker","levelName":"INFO","level":2}`) {
		t.Fatalf("status=%d body=%.300s", w.Code, body)
	}
}
