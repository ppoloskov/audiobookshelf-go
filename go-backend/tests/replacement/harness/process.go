package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errNotGraceful is joined into stop's error when SIGKILL was needed.
var errNotGraceful = errors.New("server did not stop gracefully; killed and reaped")

type serverProcess struct {
	command *exec.Cmd
	log     *os.File
	done    chan error
}

// startServer appends to logPath (a restarted server keeps one log) and owns the one Wait goroutine. stop signals, waits and, if necessary,
// kills and reaps it before closing the log; no waiter outlives the invocation.
func startServer(ctx context.Context, root, logPath string, env []string, command string, args ...string) (*serverProcess, error) {
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Cancel = func() error { return SignalGroup(cmd.Process.Pid, syscall.SIGINT) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	process := &serverProcess{cmd, log, make(chan error, 1)}
	go func() { process.done <- cmd.Wait() }()
	return process, nil
}

// RuntimeEnvironment supplies only tools and controlled application settings.
// Stored backup paths are relocated with the fixture; BACKUP_PATH is deliberately
// absent because setting it also changes upstream's backup-settings behavior.
func RuntimeEnvironment(root, dir string) ([]string, error) {
	var env []string
	for _, entry := range []struct{ key, path string }{
		{"HOME", "home"}, {"TMPDIR", "tmp"}, {"TMP", "tmp"}, {"TEMP", "tmp"},
		{"XDG_CACHE_HOME", "cache"}, {"XDG_CONFIG_HOME", "config"},
	} {
		path := filepath.Join(dir, entry.path)
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, fmt.Errorf("create isolated %s: %w", entry.key, err)
		}
		env = append(env, entry.key+"="+path)
	}
	env = append(env, "PATH="+os.Getenv("PATH"), "LANG=C", "LC_ALL=C", "TZ=UTC",
		"NODE_ENV=production", "ROUTER_BASE_PATH=/audiobookshelf", "SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1",
		"ABS_GO_CLIENT_PATH="+filepath.Join(root, "client/dist"))
	for _, tool := range []struct{ key, name string }{{"FFMPEG_PATH", "ffmpeg"}, {"FFPROBE_PATH", "ffprobe"}} {
		path := os.Getenv(tool.key)
		if path == "" {
			path = tool.name
		}
		path, err := exec.LookPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s for isolated runtime: %w", tool.name, err)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve absolute %s path: %w", tool.name, err)
		}
		env = append(env, tool.key+"="+path)
	}
	return env, nil
}

// RunIsolated gives rollback and all children the same explicit runtime
// environment as scenario servers, while retaining process-group cleanup.
func RunIsolated(ctx context.Context, root, logPath string, env []string, command string, args ...string) error {
	process, err := startServer(ctx, root, logPath, env, command, args...)
	if err != nil {
		return err
	}
	waitErr := <-process.done
	process.done <- waitErr // stop owns final consumption, descendant cleanup and log close
	if err := process.stop(); err != nil {
		return fmt.Errorf("%s %s: %w (log %s)", command, strings.Join(args, " "), err, logPath)
	}
	return nil
}

func (server *serverProcess) stop() (err error) {
	defer func() {
		err = errors.Join(err, SignalGroup(server.command.Process.Pid, syscall.SIGKILL), server.log.Close())
	}()
	select {
	case waitErr := <-server.done:
		return waitErr
	default:
	}
	err = SignalGroup(server.command.Process.Pid, syscall.SIGINT)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case waitErr := <-server.done:
		err = errors.Join(err, waitErr)
	case <-timer.C:
		err = errors.Join(err, SignalGroup(server.command.Process.Pid, syscall.SIGKILL))
		waitErr := <-server.done
		err = errors.Join(err, errNotGraceful, waitErr)
	}
	return err
}

// SignalGroup touches only the isolated process group created by this machine.
// It also terminates upstream media descendants if the Node parent leaves them.
func SignalGroup(pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		// If a group signal is refused, independently check that no live group
		// member remains. Adopted zombies can do no work; their parent reaps
		// them. Never suppress an error while live work or uncertainty remains.
		if errors.Is(err, syscall.EPERM) {
			empty, inspectErr := groupHasNoLiveMembers(pid)
			if inspectErr == nil && empty {
				return nil
			}
			return errors.Join(fmt.Errorf("signal isolated group %d with %s: %w", pid, signal, err), inspectErr)
		}
		return fmt.Errorf("signal isolated group %d with %s: %w", pid, signal, err)
	}
	return nil
}

func groupHasNoLiveMembers(pid int) (bool, error) {
	// Cleanup must still finish after the caller's cancellation, but is bounded.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pgid=,stat=").Output()
	if err != nil {
		return false, fmt.Errorf("inspect owned process group: %w", err)
	}
	return noLiveGroupMembers(string(raw), pid), nil
}

func noLiveGroupMembers(output string, pid int) bool {
	rows := 0
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false
		}
		group, err := strconv.Atoi(fields[0])
		if err != nil {
			return false
		}
		rows++
		if group == pid && !strings.HasPrefix(fields[1], "Z") {
			return false
		}
	}
	return rows > 0
}
