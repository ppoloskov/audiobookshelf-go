package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvironmentHelper(t *testing.T) {
	path := os.Getenv("VERIFY_ENV_OUTPUT")
	if path == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	values := make(map[string]string)
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "BACKUP_PATH", "ROUTER_BASE_PATH", "JWT_SECRET_KEY", "PATH", "FFMPEG_PATH", "HOME"} {
		values[key] = os.Getenv(key)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VERIFY_ENV_EXIT") == "1" {
		return
	}
	<-ctx.Done()
}

func TestServerEnvironmentDoesNotInheritParent(t *testing.T) {
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "JWT_SECRET_KEY"} {
		t.Setenv(key, "parent-only")
	}
	t.Setenv("BACKUP_PATH", "/parent/backups")
	t.Setenv("ROUTER_BASE_PATH", "/parent-base")
	dir := t.TempDir()
	output := filepath.Join(dir, "environment.json")
	env := []string{"VERIFY_ENV_OUTPUT=" + output, "PATH=" + os.Getenv("PATH"), "FFMPEG_PATH=/verified/ffmpeg", "HOME=" + dir, "BACKUP_PATH=" + dir, "ROUTER_BASE_PATH=/audiobookshelf"}
	process, err := startServer(context.Background(), ".", filepath.Join(dir, "helper.log"), env, os.Args[0], "-test.run=^TestEnvironmentHelper$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.stop(); err != nil {
			t.Errorf("stop environment helper: %v", err)
		}
	})
	var raw []byte
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		raw, err = os.ReadFile(output)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"NODE_OPTIONS": "", "NODE_PATH": "", "JWT_SECRET_KEY": "", "PATH": os.Getenv("PATH"), "FFMPEG_PATH": "/verified/ffmpeg", "HOME": dir, "BACKUP_PATH": dir, "ROUTER_BASE_PATH": "/audiobookshelf"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestRollbackEnvironmentIsolatesApplicationSettings(t *testing.T) {
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "JWT_SECRET_KEY", "BACKUP_PATH", "ROUTER_BASE_PATH", "REACT_CLIENT_PATH", "ABS_GO_CLIENT_PATH", "ALLOW_CORS"} {
		t.Setenv(key, "parent-only")
	}
	// Use an existing executable so the test needs no installed media tools.
	t.Setenv("FFMPEG_PATH", os.Args[0])
	t.Setenv("FFPROBE_PATH", os.Args[0])
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	env, err := RuntimeEnvironment(root, filepath.Join(dir, "environment"))
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || values[key] != "" {
			t.Fatalf("invalid or duplicate environment key %q", key)
		}
		values[key] = value
	}
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "JWT_SECRET_KEY", "BACKUP_PATH", "REACT_CLIENT_PATH", "ALLOW_CORS"} {
		if _, ok := values[key]; ok {
			t.Errorf("parent application setting %s leaked into runtime", key)
		}
	}
	for key, want := range map[string]string{"PATH": os.Getenv("PATH"), "FFMPEG_PATH": os.Args[0], "FFPROBE_PATH": os.Args[0], "ROUTER_BASE_PATH": "/audiobookshelf", "ABS_GO_CLIENT_PATH": filepath.Join(root, "client/dist")} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}
	for _, key := range []string{"HOME", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		if !strings.HasPrefix(values[key], filepath.Join(dir, "environment")+string(filepath.Separator)) {
			t.Fatalf("%s is outside disposable environment: %q", key, values[key])
		}
		if stat, err := os.Stat(values[key]); err != nil || !stat.IsDir() {
			t.Errorf("%s directory missing: %v", key, err)
		}
	}
	output := filepath.Join(dir, "rollback-env.json")
	env = append(env, "VERIFY_ENV_OUTPUT="+output, "VERIFY_ENV_EXIT=1")
	if err := RunIsolated(context.Background(), ".", filepath.Join(dir, "rollback.log"), env, os.Args[0], "-test.run=^TestEnvironmentHelper$"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"NODE_OPTIONS": "", "NODE_PATH": "", "JWT_SECRET_KEY": "", "BACKUP_PATH": "", "HOME": values["HOME"], "PATH": values["PATH"], "FFMPEG_PATH": os.Args[0], "ROUTER_BASE_PATH": "/audiobookshelf"} {
		if got[key] != want {
			t.Errorf("rollback subprocess %s = %q, want %q", key, got[key], want)
		}
	}
}
