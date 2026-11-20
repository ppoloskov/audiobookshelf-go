package oracle

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	readyTimeout = 45 * time.Second
	stopTimeout  = 10 * time.Second
	logTailLines = 40
	maxLogBytes  = 8 << 20
)

// Options configures how a twin TS+Go server pair is launched.
type Options struct {
	Env         map[string]string // extra or overriding environment variables
	Args        []string          // extra command-line arguments
	Fresh       bool              // empty config/metadata with copied media (mutually exclusive with TSInstall/GoInstall)
	TSInstall   string            // reuse an existing install directory for TS; empty creates a fixture copy
	GoInstall   string            // reuse an existing install directory for Go; empty creates a fixture copy
	NoWait      bool              // return without waiting for /ping readiness
	NoSockets   bool              // skip connecting default Audience watchers
	CoverageDir string            // directory for Node V8 coverage (NODE_V8_COVERAGE)
	GoCoverDir  string            // directory for Go coverage (GOCOVERDIR); triggers -cover build
}

// Target represents one TS or Go server instance and its install directories.
type Target struct {
	Name     string
	URL      string
	Base     string
	Install  string
	Config   string
	Metadata string
	Media    string
	LogPath  string
	IDs      map[string]string

	options  Options
	root     string
	work     string
	proc     *serverProcess
	client   *http.Client
	users    map[string]fixtureUser
	tokenMu  sync.Mutex
	tokens   map[string]string
	socketMu sync.Mutex
	sockets  []*Socket
	isTS     bool
}

// Twin holds the running TS and Go targets, socket audiences, outbound stubs,
// and the differential oracle comparator.
type Twin struct {
	TS      *Target
	Go      *Target
	WatchTS *Audience
	WatchGo *Audience
	Stubs   *StubSet
	Diff    *Differ
	root    string
}

type serverProcess struct {
	command *exec.Cmd
	log     *os.File
	done    chan error
}

var errNotGraceful = errors.New("server did not stop gracefully; killed and reaped")

var cachedGoBin = sync.OnceValues(func() (string, error) {
	return buildGoBinary(nil, []string{"CGO_ENABLED=0"})
})

var cachedGoCoverBin = sync.OnceValues(func() (string, error) {
	return buildGoBinary([]string{"-cover"}, nil)
})

func buildGoBinary(extraArgs, extraEnv []string) (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", fmt.Errorf("resolve repo root for go build: %w", err)
	}
	tmp, err := os.CreateTemp("", "audiobookshelf-go-oracle-*")
	if err != nil {
		return "", fmt.Errorf("create temp binary path: %w", err)
	}
	binPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp binary placeholder: %w", err)
	}
	args := make([]string, 0, 3+len(extraArgs)+1)
	args = append(args, "build")
	args = append(args, extraArgs...)
	args = append(args, "-o", binPath, "./cmd/audiobookshelf-go")
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Join(root, "go-backend")
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(binPath) // best-effort cleanup of failed build output
		return "", fmt.Errorf("build audiobookshelf-go: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return binPath, nil
}

// RepoRoot returns ORACLE_ROOT or REPLACEMENT_ROOT, or walks upward from the
// working directory to find the repository root containing index.js and go-backend/.
func RepoRoot() (string, error) {
	for _, key := range []string{"ORACLE_ROOT", "REPLACEMENT_ROOT"} {
		if root := os.Getenv(key); root != "" {
			return root, nil
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		_, indexErr := os.Stat(filepath.Join(dir, "index.js"))
		info, goErr := os.Stat(filepath.Join(dir, "go-backend"))
		if indexErr == nil && goErr == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repository root (index.js and go-backend/) not found above working directory")
		}
		dir = parent
	}
}

func resolveNodeBinary() (string, error) {
	node := os.Getenv("ORACLE_NODE")
	if node == "" {
		node = os.Getenv("REPLACEMENT_NODE")
	}
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			return "", fmt.Errorf("resolve node binary: %w", err)
		}
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("inspect node version (%s): %w", node, err)
	}
	if !strings.HasPrefix(string(version), "v22.") {
		return "", fmt.Errorf("pinned Node 22 runtime required, got %s", strings.TrimSpace(string(version)))
	}
	return node, nil
}

func resolveGoBinary(useCover bool) (string, error) {
	for _, key := range []string{"ORACLE_GO_BIN", "REPLACEMENT_GO_BIN"} {
		if bin := os.Getenv(key); bin != "" {
			return bin, nil
		}
	}
	if useCover {
		return cachedGoCoverBin()
	}
	return cachedGoBin()
}

// StartTwin prepares isolated installs, launches pinned upstream TS and Go in
// parallel, connects default Socket.IO watchers, and wires the Differ.
func StartTwin(t *testing.T, o Options) *Twin {
	t.Helper()
	if o.Fresh && (o.TSInstall != "" || o.GoInstall != "") {
		t.Fatal("Options.Fresh and TSInstall/GoInstall are mutually exclusive")
	}
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	parent := os.Getenv("ORACLE_WORK")
	if parent == "" {
		parent = os.TempDir()
	} else if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatalf("create ORACLE_WORK dir: %v", err)
	}
	// Resolve symlinks (/var -> /private/var on macOS) so library folder paths match realpath checks.
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("eval symlinks on work parent: %v", err)
	}
	workRoot, err := os.MkdirTemp(parent, "abs-oracle-")
	if err != nil {
		t.Fatalf("create oracle work dir: %v", err)
	}
	tw := &Twin{
		Stubs: StartStubs(t),
		root:  workRoot,
	}
	t.Cleanup(func() {
		tw.Stop(t)
		if t.Failed() {
			for _, tgt := range []*Target{tw.TS, tw.Go} {
				if tgt != nil {
					if _, statErr := os.Stat(tgt.LogPath); statErr == nil {
						t.Logf("%s server log tail (%s):\n%s", tgt.Name, tgt.LogPath, tailLines(tgt.Log(t), logTailLines))
					}
				}
			}
			t.Logf("oracle work directory kept: %s", tw.root)
			return
		}
		if os.Getenv("ORACLE_KEEP_WORK") != "1" {
			if err := os.RemoveAll(tw.root); err != nil {
				t.Errorf("remove oracle work directory: %v", err)
			}
		}
	})

	tsTarget, err := initTarget("ts", root, filepath.Join(tw.root, "ts"), o.TSInstall, true, o)
	if err != nil {
		t.Fatalf("init ts target: %v", err)
	}
	goTarget, err := initTarget("go", root, filepath.Join(tw.root, "go"), o.GoInstall, false, o)
	if err != nil {
		t.Fatalf("init go target: %v", err)
	}
	tw.TS = tsTarget
	tw.Go = goTarget

	// Pre-resolve/build binaries before parallel launch.
	if _, err := resolveNodeBinary(); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveGoBinary(effectiveGoCoverDir(o) != ""); err != nil {
		t.Fatal(err)
	}

	var tsErr, goErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tsErr = tw.TS.start()
	}()
	go func() {
		defer wg.Done()
		goErr = tw.Go.start()
	}()
	wg.Wait()

	if tsErr != nil {
		dumpTargetFailure(t, tw.TS, tsErr)
	}
	if goErr != nil {
		dumpTargetFailure(t, tw.Go, goErr)
	}

	tw.Diff = NewDiffer(tw.TS.Install, tw.Go.Install, DefaultDivergences())
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)
	if !o.NoWait && tw.Stubs == nil {
		tw.Stubs = StartStubs(t)
	}
	if tw.Stubs != nil {
		tw.Diff.Corr.RegisterStubs(tw.Stubs.TSHTTPURL, tw.Stubs.GoHTTPURL, tw.Stubs.TSSMTPAddr, tw.Stubs.GoSMTPAddr)
	}
	for key, tsID := range tw.TS.IDs {
		if goID, ok := tw.Go.IDs[key]; ok && tsID != "" && goID != "" {
			tw.Diff.Corr.BindID(tsID, goID)
		}
	}

	if !o.NoSockets && !o.Fresh && !o.NoWait {
		tw.WatchTS = NewAudience(t, tw.TS, "root", "listener", "finisher")
		tw.WatchGo = NewAudience(t, tw.Go, "root", "listener", "finisher")
	}
	return tw
}

func dumpTargetFailure(t *testing.T, tgt *Target, err error) {
	t.Helper()
	if _, statErr := os.Stat(tgt.LogPath); statErr == nil {
		t.Fatalf("%s start failed: %v\nserver log (last %d lines):\n%s", tgt.Name, err, logTailLines, tailLines(tgt.Log(t), logTailLines))
	}
	t.Fatalf("%s start failed: %v", tgt.Name, err)
}

func initTarget(name, root, work, install string, isTS bool, o Options) (*Target, error) {
	if err := os.MkdirAll(work, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	if install == "" {
		install = filepath.Join(work, "install")
		var err error
		if o.Fresh {
			err = freshInstall(context.Background(), root, install)
		} else {
			err = copyFixture(context.Background(), root, install)
		}
		if err != nil {
			return nil, err
		}
	}
	ids, users, err := loadInstallMeta(install)
	if err != nil {
		return nil, fmt.Errorf("load install metadata: %w", err)
	}
	return &Target{
		Name:     name,
		Install:  install,
		Config:   filepath.Join(install, "config"),
		Metadata: filepath.Join(install, "metadata"),
		Media:    filepath.Join(install, "media"),
		LogPath:  filepath.Join(work, "server.log"),
		IDs:      ids,
		options:  o,
		root:     root,
		work:     work,
		client:   newClient(),
		users:    users,
		tokens:   map[string]string{},
		isTS:     isTS,
	}, nil
}

func effectiveCoverageDir(o Options) string {
	if o.CoverageDir != "" {
		return o.CoverageDir
	}
	return os.Getenv("ORACLE_V8_DIR")
}

func effectiveGoCoverDir(o Options) string {
	if o.GoCoverDir != "" {
		return o.GoCoverDir
	}
	return os.Getenv("ORACLE_GOCOVER_DIR")
}

func (tgt *Target) launchCommand() (string, []string, error) {
	if tgt.isTS {
		node, err := resolveNodeBinary()
		if err != nil {
			return "", nil, err
		}
		args := append([]string{filepath.Join(tgt.root, "index.js")}, tgt.options.Args...)
		return node, args, nil
	}
	bin, err := resolveGoBinary(effectiveGoCoverDir(tgt.options) != "")
	if err != nil {
		return "", nil, err
	}
	return bin, slices.Clone(tgt.options.Args), nil
}

func (tgt *Target) environment(port int) ([]string, error) {
	isolated, err := runtimeEnvironment(tgt.root, filepath.Join(tgt.work, "environment"))
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(isolated)+len(tgt.options.Env)+8)
	for _, entry := range isolated {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	values["CONFIG_PATH"] = tgt.Config
	values["METADATA_PATH"] = tgt.Metadata
	values["HOST"] = "127.0.0.1"
	values["PORT"] = strconv.Itoa(port)
	values["SOURCE"] = "docker"
	values["ROUTER_BASE_PATH"] = "/audiobookshelf"

	if tgt.isTS {
		if covDir := effectiveCoverageDir(tgt.options); covDir != "" {
			if err := os.MkdirAll(covDir, 0755); err != nil {
				return nil, fmt.Errorf("create v8 coverage dir: %w", err)
			}
			sub, err := os.MkdirTemp(covDir, tgt.Name+"-")
			if err != nil {
				return nil, fmt.Errorf("create v8 coverage subdir: %w", err)
			}
			values["NODE_V8_COVERAGE"] = sub
		}
	} else {
		if goCovDir := effectiveGoCoverDir(tgt.options); goCovDir != "" {
			if err := os.MkdirAll(goCovDir, 0755); err != nil {
				return nil, fmt.Errorf("create go coverage dir: %w", err)
			}
			values["GOCOVERDIR"] = goCovDir
		}
	}

	for k, v := range tgt.options.Env {
		values[k] = v
	}
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	return env, nil
}

func (tgt *Target) start() error {
	command, args, err := tgt.launchCommand()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("allocate port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return fmt.Errorf("release ephemeral port: %w", err)
	}
	env, err := tgt.environment(port)
	if err != nil {
		return err
	}
	tgt.URL = fmt.Sprintf("http://127.0.0.1:%d", port)
	tgt.Base = tgt.URL + "/audiobookshelf"

	tgt.tokenMu.Lock()
	clear(tgt.tokens)
	tgt.tokenMu.Unlock()

	proc, err := startServer(context.Background(), tgt.root, tgt.LogPath, env, command, args...)
	if err != nil {
		return fmt.Errorf("start %s: %w", command, err)
	}
	tgt.proc = proc
	if tgt.options.NoWait {
		return nil
	}
	return tgt.WaitReady(readyTimeout)
}

// Stop stops the target server gracefully (SIGINT then SIGKILL after 10s).
// It is idempotent and closes any tracked sockets first.
func (tgt *Target) Stop(t *testing.T) {
	t.Helper()
	for _, err := range tgt.stop() {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Logf("%s server exit: %v", tgt.Name, err)
			continue
		}
		t.Errorf("stop %s server: %v", tgt.Name, err)
	}
}

func (tgt *Target) stop() []error {
	if tgt.proc == nil {
		return nil
	}
	proc := tgt.proc
	tgt.proc = nil

	tgt.socketMu.Lock()
	sockets := tgt.sockets
	tgt.sockets = nil
	tgt.socketMu.Unlock()
	for _, sock := range sockets {
		sock.Close()
	}

	return flattenErrors(proc.stop())
}

// Restart stops the target and starts it again on the same install and options.
func (tgt *Target) Restart(t *testing.T) {
	t.Helper()
	tgt.Stop(t)
	if err := tgt.start(); err != nil {
		dumpTargetFailure(t, tgt, err)
	}
}

// WaitReady polls /ping until it returns 200 OK or the process exits.
func (tgt *Target) WaitReady(timeout time.Duration) error {
	if tgt.proc == nil {
		return errors.New("server is not running")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		resp, err := client.Get(tgt.Base + "/ping")
		if err == nil {
			_ = resp.Body.Close() // body content ignored for /ping probe
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("server readiness timed out after %s", timeout)
		case waitErr := <-tgt.proc.done:
			tgt.proc.done <- waitErr // stop() owns final consumption and log close
			return fmt.Errorf("server exited before readiness: %w", waitErr)
		case <-ticker.C:
		}
	}
}

// WaitExit waits for the server process to exit on its own within timeout.
func (tgt *Target) WaitExit(timeout time.Duration) (int, bool) {
	if tgt.proc == nil {
		return 0, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waitErr := <-tgt.proc.done:
		tgt.proc.done <- waitErr // stop() owns final consumption and log close
		return tgt.proc.command.ProcessState.ExitCode(), true
	case <-timer.C:
		return 0, false
	}
}

// Log returns the server log contents (up to the last 8 MiB).
func (tgt *Target) Log(t *testing.T) string {
	t.Helper()
	file, err := os.Open(tgt.LogPath)
	if err != nil {
		t.Fatalf("read %s server log: %v", tgt.Name, err)
	}
	defer file.Close() // read-only
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat %s server log: %v", tgt.Name, err)
	}
	offset := max(info.Size()-maxLogBytes, 0)
	buf := make([]byte, info.Size()-offset)
	n, err := file.ReadAt(buf, offset)
	if err != nil && n != len(buf) {
		t.Fatalf("read %s server log: %v", tgt.Name, err)
	}
	return string(buf[:n])
}

// Stop closes socket audiences and stops both TS and Go servers in parallel.
func (tw *Twin) Stop(t *testing.T) {
	t.Helper()
	if tw.WatchTS != nil {
		tw.WatchTS.Close()
	}
	if tw.WatchGo != nil {
		tw.WatchGo.Close()
	}

	var tsErrs, goErrs []error
	var wg sync.WaitGroup
	if tw.TS != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tsErrs = tw.TS.stop()
		}()
	}
	if tw.Go != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			goErrs = tw.Go.stop()
		}()
	}
	wg.Wait()

	for _, item := range []struct {
		name string
		errs []error
	}{{"ts", tsErrs}, {"go", goErrs}} {
		for _, err := range item.errs {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				t.Logf("%s server exit: %v", item.name, err)
				continue
			}
			t.Errorf("stop %s server: %v", item.name, err)
		}
	}
}

// Restart stops both TS and Go servers, starts them again in parallel, clears
// cached tokens, and reconnects Audience watchers if they were connected.
func (tw *Twin) Restart(t *testing.T) {
	t.Helper()
	var tsRoles, goRoles []string
	if tw.WatchTS != nil {
		tsRoles = slices.Clone(tw.WatchTS.roles)
		tw.WatchTS.Close()
	}
	if tw.WatchGo != nil {
		goRoles = slices.Clone(tw.WatchGo.roles)
		tw.WatchGo.Close()
	}

	tw.Stop(t)

	var tsErr, goErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tsErr = tw.TS.start()
	}()
	go func() {
		defer wg.Done()
		goErr = tw.Go.start()
	}()
	wg.Wait()

	if tsErr != nil {
		dumpTargetFailure(t, tw.TS, tsErr)
	}
	if goErr != nil {
		dumpTargetFailure(t, tw.Go, goErr)
	}

	if len(tsRoles) > 0 {
		tw.WatchTS = NewAudience(t, tw.TS, tsRoles...)
	}
	if len(goRoles) > 0 {
		tw.WatchGo = NewAudience(t, tw.Go, goRoles...)
	}
}

// RollbackGoToTS stops the Go target and launches pinned upstream TS directly
// against Go's install directory to verify database and metadata compatibility.
func (tw *Twin) RollbackGoToTS(t *testing.T) *Target {
	t.Helper()
	if tw.WatchGo != nil {
		tw.WatchGo.Close()
		tw.WatchGo = nil
	}
	tw.Go.Stop(t)
	return tw.startRollbackTarget(t, "ts-rollback", tw.Go, true)
}

// RollbackTSToGo stops the TS target and launches Go directly against TS's
// install directory to verify database and metadata compatibility.
func (tw *Twin) RollbackTSToGo(t *testing.T) *Target {
	t.Helper()
	if tw.WatchTS != nil {
		tw.WatchTS.Close()
		tw.WatchTS = nil
	}
	tw.TS.Stop(t)
	return tw.startRollbackTarget(t, "go-rollback", tw.TS, false)
}

func (tw *Twin) startRollbackTarget(t *testing.T, name string, source *Target, isTS bool) *Target {
	t.Helper()
	work, err := os.MkdirTemp(tw.root, name+"-")
	if err != nil {
		t.Fatalf("create rollback work dir: %v", err)
	}
	opts := source.options
	opts.Args = slices.Clone(source.options.Args)
	opts.Env = cloneStringMap(source.options.Env)

	tgt, err := initTarget(name, source.root, work, source.Install, isTS, opts)
	if err != nil {
		t.Fatalf("init %s target: %v", name, err)
	}
	t.Cleanup(func() {
		tgt.Stop(t)
		if t.Failed() {
			if _, statErr := os.Stat(tgt.LogPath); statErr == nil {
				t.Logf("%s server log tail (%s):\n%s", tgt.Name, tgt.LogPath, tailLines(tgt.Log(t), logTailLines))
			}
		}
	})
	if err := tgt.start(); err != nil {
		dumpTargetFailure(t, tgt, err)
	}
	return tgt
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func startServer(ctx context.Context, root, logPath string, env []string, command string, args ...string) (*serverProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, syscall.SIGINT) }
	cmd.WaitDelay = stopTimeout
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	proc := &serverProcess{command: cmd, log: logFile, done: make(chan error, 1)}
	go func() { proc.done <- cmd.Wait() }()
	return proc, nil
}

func (p *serverProcess) stop() (err error) {
	defer func() {
		err = errors.Join(err, signalGroup(p.command.Process.Pid, syscall.SIGKILL), p.log.Close())
	}()
	select {
	case waitErr := <-p.done:
		return waitErr
	default:
	}
	err = signalGroup(p.command.Process.Pid, syscall.SIGINT)
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case waitErr := <-p.done:
		err = errors.Join(err, waitErr)
	case <-timer.C:
		err = errors.Join(err, signalGroup(p.command.Process.Pid, syscall.SIGKILL))
		waitErr := <-p.done
		err = errors.Join(err, errNotGraceful, waitErr)
	}
	return err
}

func signalGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		if errors.Is(err, syscall.EPERM) {
			empty, inspectErr := groupHasNoLiveMembers(pid)
			if inspectErr == nil && empty {
				return nil
			}
			return errors.Join(fmt.Errorf("signal process group %d with %s: %w", pid, sig, err), inspectErr)
		}
		return fmt.Errorf("signal process group %d with %s: %w", pid, sig, err)
	}
	return nil
}

func groupHasNoLiveMembers(pid int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pgid=,stat=").Output()
	if err != nil {
		return false, fmt.Errorf("inspect process group: %w", err)
	}
	rows := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false, nil
		}
		group, err := strconv.Atoi(fields[0])
		if err != nil {
			return false, nil
		}
		rows++
		if group == pid && !strings.HasPrefix(fields[1], "Z") {
			return false, nil
		}
	}
	return rows > 0, nil
}

func runtimeEnvironment(root, dir string) ([]string, error) {
	var env []string
	for _, entry := range []struct{ key, path string }{
		{"HOME", "home"},
		{"TMPDIR", "tmp"},
		{"TMP", "tmp"},
		{"TEMP", "tmp"},
		{"XDG_CACHE_HOME", "cache"},
		{"XDG_CONFIG_HOME", "config"},
		{"XDG_DATA_HOME", "data"},
	} {
		path := filepath.Join(dir, entry.path)
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, fmt.Errorf("create isolated %s: %w", entry.key, err)
		}
		env = append(env, entry.key+"="+path)
	}
	env = append(env,
		"PATH="+os.Getenv("PATH"),
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"NODE_ENV=production",
		"ROUTER_BASE_PATH=/audiobookshelf",
		"SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1",
		"ABS_GO_CLIENT_PATH="+filepath.Join(root, "client/dist"),
	)
	for _, tool := range []struct{ key, name string }{
		{"FFMPEG_PATH", "ffmpeg"},
		{"FFPROBE_PATH", "ffprobe"},
	} {
		path := os.Getenv(tool.key)
		if path == "" {
			path = tool.name
		}
		resolved, err := exec.LookPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", tool.name, err)
		}
		abs, err := filepath.Abs(resolved)
		if err != nil {
			return nil, fmt.Errorf("abs path for %s: %w", tool.name, err)
		}
		env = append(env, tool.key+"="+abs)
	}
	return env, nil
}

func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

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
