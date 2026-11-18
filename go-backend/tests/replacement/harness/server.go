package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options says how a packet's server is started.
type Options struct {
	Env      map[string]string // extra/overriding environment for the server process
	Args     []string          // extra command-line arguments
	Fresh    bool              // empty config/metadata dirs (uninitialised server) instead of the fixture copy; media is still copied
	Install  string            // reuse this existing install directory (reopen/restart checks); empty = new copy
	Upstream bool              // always launch pinned upstream, whatever the target (rollback: upstream reopens what the target wrote)
	NoWait   bool              // return without waiting for readiness (launch-failure scenarios)
}

// Env is one running (or stopped) server and the install it uses. Fields other
// than the process are updated in place by Restart. An Env is used by one
// scenario at a time; it must not be copied.
type Env struct {
	URL      string            // "http://127.0.0.1:PORT"
	Base     string            // URL + "/audiobookshelf"
	Install  string            // install root
	Config   string            // Install/config
	Metadata string            // Install/metadata
	Media    string            // Install/media
	IDs      map[string]string // fixture ids.json (library, saga1, novel, listener, ...); empty for Fresh
	LogPath  string            // server stdout+stderr, appended across restarts

	options Options
	root    string // repository root
	work    string // disposable directory of this Env (log, isolated HOME); not the install when Install was given
	label   string // packet name, used in work and coverage directory names
	proc    *serverProcess
	client  *http.Client
	users   map[string]fixtureUser

	tokenMu sync.Mutex
	tokens  map[string]string

	socketMu sync.Mutex
	sockets  []*Socket // closed by Stop: upstream waits for open connections before it exits
}

const (
	readyTimeout = 45 * time.Second
	logTailLines = 40
	maxLogBytes  = 8 << 20
)

// nodeError checks once per process that REPLACEMENT_NODE is a Node 22 runtime.
var nodeError = sync.OnceValue(func() error {
	node := os.Getenv("REPLACEMENT_NODE")
	if node == "" {
		return errors.New("REPLACEMENT_NODE is required for the upstream target")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		return fmt.Errorf("inspect Node: %w", err)
	}
	if !strings.HasPrefix(string(version), "v22.") {
		return fmt.Errorf("use the pinned Node 22 runtime, got %s", strings.TrimSpace(string(version)))
	}
	return nil
})

// repoRoot returns REPLACEMENT_ROOT, or the nearest directory above the working
// directory that holds index.js and go-backend/.
func repoRoot() (string, error) {
	if root := os.Getenv("REPLACEMENT_ROOT"); root != "" {
		return root, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		_, indexErr := os.Stat(filepath.Join(dir, "index.js"))
		info, goErr := os.Stat(filepath.Join(dir, "go-backend"))
		if indexErr == nil && goErr == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repository root (index.js and go-backend/) not found above the working directory")
		}
		dir = parent
	}
}

// packetLabel names the packet under test: its package directory.
func packetLabel() string {
	dir, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return filepath.Base(dir)
}

// Start prepares an install, launches the server and, unless o.NoWait, waits
// until it answers /ping. It fails the test on any error and registers a
// cleanup that stops the server, logs its log tail when the test failed, and
// removes the work directory (kept on failure or when REPLACEMENT_WORK is set).
func Start(t *testing.T, o Options) *Env {
	t.Helper()
	if o.Fresh && o.Install != "" {
		t.Fatal("Options.Fresh and Options.Install are mutually exclusive")
	}
	if target := os.Getenv("REPLACEMENT_TARGET"); target != "upstream" && target != "go" {
		t.Fatal("REPLACEMENT_TARGET must be upstream or go; run through make verify-packet / make verify")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	parent, keep := os.Getenv("REPLACEMENT_WORK"), true
	if parent == "" {
		parent, keep = os.TempDir(), false
	} else if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	// macOS temp directories sit behind a symlink (/var -> /private/var).
	// Upstream's scanner compares real paths with the library folder paths it
	// was given, so the install must live under a symlink-free path.
	if parent, err = filepath.EvalSymlinks(parent); err != nil {
		t.Fatal(err)
	}
	e := &Env{options: o, root: root, label: packetLabel(), tokens: map[string]string{}, client: newClient()}
	e.work, err = os.MkdirTemp(parent, "abs-replacement-"+e.label+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.Stop(t)
		if t.Failed() {
			if _, err := os.Stat(e.LogPath); err == nil {
				t.Logf("server log tail (%s):\n%s", e.LogPath, tailLines(e.Log(t), logTailLines))
			}
			t.Logf("work directory kept: %s", e.work)
			return
		}
		if !keep {
			if err := os.RemoveAll(e.work); err != nil {
				t.Errorf("remove work directory: %v", err)
			}
		}
	})
	e.Install = o.Install
	if e.Install == "" {
		e.Install = filepath.Join(e.work, "install")
		if o.Fresh {
			err = freshInstall(context.Background(), root, e.Install)
		} else {
			err = copyFixture(context.Background(), root, e.Install)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	e.Config, e.Metadata, e.Media = filepath.Join(e.Install, "config"), filepath.Join(e.Install, "metadata"), filepath.Join(e.Install, "media")
	e.LogPath = filepath.Join(e.work, "server.log")
	if e.IDs, e.users, err = loadInstallMeta(e.Install); err != nil {
		t.Fatal(err)
	}
	e.launch(t)
	return e
}

// launchCommand picks the executable and arguments for the configured target.
func (e *Env) launchCommand() (command string, args []string, upstream bool, err error) {
	target := os.Getenv("REPLACEMENT_TARGET")
	if e.options.Upstream {
		target = "upstream"
	}
	switch target {
	case "upstream":
		if err := nodeError(); err != nil {
			return "", nil, false, err
		}
		return os.Getenv("REPLACEMENT_NODE"), append([]string{filepath.Join(e.root, "index.js")}, e.options.Args...), true, nil
	case "go":
		bin := os.Getenv("REPLACEMENT_GO_BIN")
		if bin == "" {
			return "", nil, false, errors.New("REPLACEMENT_GO_BIN is required for the go target")
		}
		return bin, slices.Clone(e.options.Args), false, nil
	}
	return "", nil, false, fmt.Errorf("unknown target %q", target)
}

// environment builds the complete child environment; the parent's is not inherited.
func (e *Env) environment(port int, upstream bool) ([]string, error) {
	isolated, err := RuntimeEnvironment(e.root, filepath.Join(e.work, "environment"))
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, entry := range isolated {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	values["CONFIG_PATH"], values["METADATA_PATH"] = e.Config, e.Metadata
	values["HOST"], values["PORT"], values["SOURCE"] = "127.0.0.1", fmt.Sprint(port), "docker"
	values["ROUTER_BASE_PATH"] = "/audiobookshelf"
	if coverage := os.Getenv("REPLACEMENT_V8"); coverage != "" && upstream {
		if err := os.MkdirAll(coverage, 0755); err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp(coverage, e.label+"-")
		if err != nil {
			return nil, err
		}
		values["NODE_V8_COVERAGE"] = dir
	}
	for key, value := range e.options.Env {
		values[key] = value
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	slices.Sort(env)
	return env, nil
}

// launch starts the process on a new port and, unless NoWait, waits for readiness.
func (e *Env) launch(t *testing.T) {
	t.Helper()
	command, args, upstream, err := e.launchCommand()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	env, err := e.environment(port, upstream)
	if err != nil {
		t.Fatal(err)
	}
	e.URL = fmt.Sprintf("http://127.0.0.1:%d", port)
	e.Base = e.URL + "/audiobookshelf"
	e.tokenMu.Lock()
	clear(e.tokens)
	e.tokenMu.Unlock()
	e.proc, err = startServer(context.Background(), e.root, e.LogPath, env, command, args...)
	if err != nil {
		t.Fatalf("start %s: %v", command, err)
	}
	if e.options.NoWait {
		return
	}
	if err := e.WaitReady(readyTimeout); err != nil {
		t.Fatalf("%v\nserver log (last %d lines):\n%s", err, logTailLines, tailLines(e.Log(t), logTailLines))
	}
}

// Stop sends SIGINT to the server's process group, waits 10s, then kills it.
// It is idempotent. A server that exited by itself with a status is not an
// error here; use WaitExit to assert on that.
func (e *Env) Stop(t *testing.T) {
	t.Helper()
	if e.proc == nil {
		return
	}
	proc := e.proc
	e.proc = nil
	e.socketMu.Lock()
	for _, socket := range e.sockets {
		socket.Close()
	}
	e.sockets = nil
	e.socketMu.Unlock()
	for _, err := range flattenErrors(proc.stop()) {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Logf("server exit: %v", err)
			continue
		}
		t.Errorf("stop server: %v", err)
	}
}

// Restart stops the server and starts it again on the same install and options.
// URL and Base change (a new port); the token cache is cleared.
func (e *Env) Restart(t *testing.T) {
	t.Helper()
	e.Stop(t)
	e.launch(t)
}

// WaitReady polls /ping until it answers 200. It fails fast when the process exits first.
func (e *Env) WaitReady(timeout time.Duration) error {
	if e.proc == nil {
		return errors.New("server is not running")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		resp, err := client.Get(e.Base + "/ping")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("server readiness: timed out after %s", timeout)
		case err := <-e.proc.done:
			e.proc.done <- err // stop owns final consumption and the log close
			return fmt.Errorf("server exited before readiness: %v", err)
		case <-ticker.C:
		}
	}
}

// WaitExit waits for the server process to end by itself. It returns the exit
// code (-1 when killed by a signal) and whether it exited within timeout.
func (e *Env) WaitExit(timeout time.Duration) (code int, exited bool) {
	if e.proc == nil {
		return 0, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-e.proc.done:
		e.proc.done <- err // stop owns final consumption and the log close
		return e.proc.command.ProcessState.ExitCode(), true
	case <-timer.C:
		return 0, false
	}
}

// Log returns the server log so far (at most its last 8 MiB).
func (e *Env) Log(t *testing.T) string {
	t.Helper()
	file, err := os.Open(e.LogPath)
	if err != nil {
		t.Fatalf("read server log: %v", err)
	}
	defer file.Close() // read-only
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat server log: %v", err)
	}
	offset := max(info.Size()-maxLogBytes, 0)
	buffer := make([]byte, info.Size()-offset)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && n != len(buffer) {
		t.Fatalf("read server log: %v", err)
	}
	return string(buffer[:n])
}

func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// flattenErrors lists the leaves of an errors.Join tree; nil yields none.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var leaves []error
		for _, inner := range joined.Unwrap() {
			leaves = append(leaves, flattenErrors(inner)...)
		}
		return leaves
	}
	return []error{err}
}
