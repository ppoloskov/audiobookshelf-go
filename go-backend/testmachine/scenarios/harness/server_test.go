package harness

import (
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFakeServer is the server process body for the lifecycle tests: it serves
// /audiobookshelf/ping on $PORT and exits on SIGINT, or at once with
// HARNESS_FAKE_EXIT set.
func TestFakeServer(t *testing.T) {
	if os.Getenv("HARNESS_FAKE_SERVER") == "" {
		return
	}
	if code := os.Getenv("HARNESS_FAKE_EXIT"); code != "" {
		exit, err := strconv.Atoi(code)
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(exit)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("PORT"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/audiobookshelf/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"success":true}`)) })
	go func() { _ = http.Serve(listener, mux) }() // ends with the process
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	<-signals
}

// useFakeTarget makes Start launch this test binary as the "go" target.
func useFakeTarget(t *testing.T) {
	t.Helper()
	t.Setenv("REPLACEMENT_TARGET", "go")
	t.Setenv("REPLACEMENT_GO_BIN", os.Args[0])
	t.Setenv("REPLACEMENT_WORK", t.TempDir())
	t.Setenv("FFMPEG_PATH", os.Args[0])
	t.Setenv("FFPROBE_PATH", os.Args[0])
	t.Setenv("REPLACEMENT_V8", "")
}

func fakeOptions(extra map[string]string) Options {
	env := map[string]string{"HARNESS_FAKE_SERVER": "1"}
	for key, value := range extra {
		env[key] = value
	}
	return Options{Fresh: true, Args: []string{"-test.run=^TestFakeServer$"}, Env: env}
}

func TestStartStopRestart(t *testing.T) {
	useFakeTarget(t)
	e := Start(t, fakeOptions(nil))
	firstURL := e.URL
	if resp := e.Get(t, "", "/ping"); resp.Status != 200 {
		t.Fatalf("ping status %d", resp.Status)
	}
	if len(e.IDs) != 0 || !strings.HasSuffix(e.Config, "config") {
		t.Fatalf("fresh env: ids %v config %s", e.IDs, e.Config)
	}
	e.Restart(t)
	if e.URL == firstURL {
		t.Log("restart reused the port") // allowed, but unusual
	}
	if resp := e.Get(t, "", "/ping"); resp.Status != 200 {
		t.Fatalf("ping after restart: %d", resp.Status)
	}
	e.Stop(t)
	e.Stop(t) // idempotent
	if _, exited := e.WaitExit(10 * time.Millisecond); exited {
		t.Fatal("WaitExit on a stopped Env must report not exited")
	}
	if e.Log(t) == "" {
		t.Fatal("server log is empty; the helper process prints its test summary")
	}
}

func TestEnvironmentOverridesAndNoInheritance(t *testing.T) {
	useFakeTarget(t)
	t.Setenv("NODE_OPTIONS", "--inspect")
	t.Setenv("JWT_SECRET_KEY", "parent")
	e := &Env{options: Options{Env: map[string]string{"TZ": "Europe/Berlin", "EXTRA": "1"}}, root: t.TempDir(), work: t.TempDir(), label: "t", Config: "/c", Metadata: "/m"}
	env, err := e.environment(4242, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		got[key] = value
	}
	for key, want := range map[string]string{"CONFIG_PATH": "/c", "METADATA_PATH": "/m", "HOST": "127.0.0.1", "PORT": "4242", "SOURCE": "docker", "ROUTER_BASE_PATH": "/audiobookshelf", "TZ": "Europe/Berlin", "EXTRA": "1", "NODE_ENV": "production"} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	for _, key := range []string{"NODE_OPTIONS", "JWT_SECRET_KEY"} {
		if _, ok := got[key]; ok {
			t.Errorf("parent %s leaked", key)
		}
	}
}

func TestStartedProcessExitIsObservable(t *testing.T) {
	useFakeTarget(t)
	o := fakeOptions(map[string]string{"HARNESS_FAKE_EXIT": "3"})
	o.NoWait = true
	e := Start(t, o)
	code, exited := e.WaitExit(10 * time.Second)
	if !exited || code != 3 {
		t.Fatalf("exit = %d exited=%v, want 3 true", code, exited)
	}
	if err := e.WaitReady(time.Second); err == nil {
		t.Fatal("WaitReady succeeded for an exited server")
	}
}
