package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// config is what every mode of the coordinator needs.
type config struct {
	root     string
	work     string
	node     string
	parallel int
}

const allPackets = "./tests/replacement/..."

// execGroup runs a command in its own process group and kills the whole group
// when it ends or the context is cancelled, so no descendant outlives the call.
func execGroup(ctx context.Context, dir string, env []string, stdout, stderr *os.File, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return harness.SignalGroup(cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	var groupErr error
	if cmd.Process != nil {
		groupErr = harness.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
	}
	if groupErr != nil {
		return fmt.Errorf("cleanup command process group: %w (command result: %v)", groupErr, runErr)
	}
	if runErr != nil {
		return fmt.Errorf("%s %s: %w", command, strings.Join(args, " "), runErr)
	}
	return groupErr
}

// runCommand writes the command's stdout and stderr to logPath.
func runCommand(ctx context.Context, dir, logPath string, env []string, command string, args ...string) error {
	log, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create command log: %w", err)
	}
	runErr := execGroup(ctx, dir, env, log, log, command, args...)
	closeErr := log.Close()
	if runErr != nil {
		return fmt.Errorf("%w (log %s)", runErr, logPath)
	}
	if closeErr != nil {
		return fmt.Errorf("close command log: %w", closeErr)
	}
	return nil
}

// runGoTest preserves the command result as well as its event stream. Callers
// may grade ordinary test failures (exit 1), but must not discard the exit.
func runGoTest(ctx context.Context, cfg config, jsonPath string, env []string, packages ...string) error {
	out, err := os.Create(jsonPath)
	if err != nil {
		return fmt.Errorf("create test log: %w", err)
	}
	errFile, err := os.Create(jsonPath + ".stderr")
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("create test stderr log: %w", err)
	}
	args := append([]string{"test", "-tags=replacement", "-json", "-count=1", "-run", "^TestReplacement$", "-p", fmt.Sprint(cfg.parallel), "-timeout", "120m"}, packages...)
	runErr := execGroup(ctx, filepath.Join(cfg.root, "go-backend"), env, out, errFile, "go", args...)
	closeErr := errors.Join(out.Close(), errFile.Close())
	if closeErr != nil {
		return fmt.Errorf("close test logs: %w (command result: %v)", closeErr, runErr)
	}
	return runErr
}

// ordinaryTestFailure permits grading failed scenarios; usage errors, signals
// and cancellation are machine failures rather than ordinary test failures.
func ordinaryTestFailure(ctx context.Context, err error) bool {
	var exit *exec.ExitError
	return ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1
}

// discoverPlan runs the packets with REPLACEMENT_PLAN_ONLY=1 (no servers) and
// collects their declarations, ordered by packet then declaration order.
func discoverPlan(ctx context.Context, cfg config, packages ...string) ([]harness.Declaration, error) {
	dir := filepath.Join(cfg.work, "plan")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	jsonPath := filepath.Join(dir, "plan.jsonl")
	runErr := runGoTest(ctx, cfg, jsonPath, []string{"REPLACEMENT_PLAN_ONLY=1"}, packages...)
	if runErr != nil && !ordinaryTestFailure(ctx, runErr) {
		return nil, runErr
	}
	file, err := os.Open(jsonPath)
	if err != nil {
		return nil, err
	}
	plan, parseErr := parseDeclarations(file)
	closeErr := file.Close()
	if parseErr != nil {
		return nil, parseErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if runErr != nil {
		return nil, runErr
	}
	sortPlan(plan)
	return plan, nil
}

// sortPlan orders by packet name, keeping each packet's declaration order.
func sortPlan(plan []harness.Declaration) {
	slices.SortStableFunc(plan, func(a, b harness.Declaration) int { return strings.Compare(a.Packet, b.Packet) })
}

// runTarget runs the replacement packages of `packages` against one
// implementation ("upstream" or "go") and grades the declared plan.
func runTarget(ctx context.Context, cfg config, target string, plan []harness.Declaration, packages ...string) (targetResult, sourceExecution, error) {
	result := targetResult{Status: "running"}
	execution := sourceExecution{Status: "unavailable", Note: "No whole-source line/branch denominator has been established; raw execution is not behavioral proof."}
	dir := filepath.Join(cfg.work, target)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return result, execution, err
	}
	template := filepath.Join(cfg.root, "go-backend/internal/contracttest/testdata/install")
	before, err := digestPaths(template, ".")
	if err != nil {
		return result, execution, err
	}
	result.FixtureDigest = before
	env := []string{"REPLACEMENT_TARGET=" + target, "REPLACEMENT_NODE=" + cfg.node, "REPLACEMENT_ROOT=" + cfg.root, "REPLACEMENT_WORK=" + dir}
	runtimeRoot, runtimePaths := cfg.root, []string{"index.js", "server", "package.json"}
	if target == "go" {
		bin, err := buildGo(ctx, cfg, dir)
		if err != nil {
			return result, execution, err
		}
		env = append(env, "REPLACEMENT_GO_BIN="+bin)
		runtimeRoot, runtimePaths = dir, []string{"audiobookshelf-go"}
	} else {
		env = append(env, "REPLACEMENT_V8="+filepath.Join(cfg.work, "v8"))
	}
	runtimeBefore, err := digestPaths(runtimeRoot, runtimePaths...)
	if err != nil {
		return result, execution, err
	}
	result.RuntimeDigest = runtimeBefore
	result.TestLog = filepath.Join(dir, "assertions.jsonl")
	runErr := runGoTest(ctx, cfg, result.TestLog, env, packages...)
	if runErr != nil && !ordinaryTestFailure(ctx, runErr) {
		return result, execution, runErr
	}
	file, err := os.Open(result.TestLog)
	if err != nil {
		return result, execution, err
	}
	graded, parseErr := parseResults(file, plan)
	closeErr := file.Close()
	if parseErr != nil {
		return result, execution, parseErr
	}
	if closeErr != nil {
		return result, execution, closeErr
	}
	result.Packets, result.Assertions, result.Anomalies = graded.Packets, graded.Passed, graded.Anomalies
	failed := 0
	for _, packet := range graded.Packets {
		failed += packet.Failed
	}
	if runErr != nil && failed == 0 && len(graded.Anomalies) == 0 {
		result.Anomalies = append(result.Anomalies, "go test exited unsuccessfully despite passing scenario events: "+runErr.Error())
	}
	switch {
	case failed != 0:
		result.Status = "failed"
		result.Error = fmt.Sprintf("%d declared scenarios failed", failed)
	case len(result.Anomalies) != 0:
		result.Status = "failed"
		result.Error = fmt.Sprintf("%d anomalies in the test run", len(result.Anomalies))
	default:
		result.Status = "assertions_passed"
	}
	after, err := digestPaths(template, ".")
	if err != nil {
		return result, execution, err
	}
	if before != after {
		return result, execution, fmt.Errorf("%s run changed the fixture template", target)
	}
	runtimeAfter, err := digestPaths(runtimeRoot, runtimePaths...)
	if err != nil {
		return result, execution, err
	}
	if runtimeBefore != runtimeAfter {
		return result, execution, fmt.Errorf("%s executable inputs changed during assertions", target)
	}
	if target == "upstream" {
		files, err := filepath.Glob(filepath.Join(cfg.work, "v8/*/*.json"))
		if err != nil {
			return result, execution, err
		}
		execution.RawFiles = len(files)
		if len(files) > 0 {
			execution.Status = "raw_v8_only"
		}
	}
	return result, execution, nil
}

// buildGo builds the Go server once with CGO disabled.
func buildGo(ctx context.Context, cfg config, dir string) (string, error) {
	bin := filepath.Join(dir, "audiobookshelf-go")
	if err := runCommand(ctx, filepath.Join(cfg.root, "go-backend"), filepath.Join(dir, "build.log"), []string{"CGO_ENABLED=0"}, "go", "build", "-trimpath", "-o", bin, "./cmd/audiobookshelf-go"); err != nil {
		return "", err
	}
	return bin, nil
}

// checkNode requires the pinned Node 22 runtime.
func checkNode(ctx context.Context, node string) error {
	version, err := exec.CommandContext(ctx, node, "--version").Output()
	if err != nil {
		return fmt.Errorf("inspect Node: %w", err)
	}
	if !strings.HasPrefix(string(version), "v22.") {
		return fmt.Errorf("use the pinned Node 22 runtime, got %s", strings.TrimSpace(string(version)))
	}
	return nil
}
