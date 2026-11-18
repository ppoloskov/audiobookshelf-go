package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

func TestServerProcessHelper(t *testing.T) {
	if os.Getenv("VERIFY_CHILD_HELPER") != "1" {
		return
	} // subprocess fixture, not a server assertion
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if pidFile := os.Getenv("VERIFY_DESCENDANT_PID"); pidFile != "" {
		if os.Getenv("VERIFY_IS_DESCENDANT") == "1" {
			if err := os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			if err := os.WriteFile(pidFile+".stopped", []byte("stopped"), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		child := exec.Command(os.Args[0], "-test.run=^TestServerProcessHelper$")
		child.Env = append(os.Environ(), "VERIFY_IS_DESCENDANT=1")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		// Deliberately emulate upstream's former orphan: the coordinator owns
		// process-group cleanup, including a parent that does not wait here.
		// Wait only for the signal acknowledgement, not the child process, so
		// final group cleanup cannot race its fixture marker write.
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			if _, err := os.Stat(pidFile + ".stopped"); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return
	}
	<-ctx.Done()
}

func TestServerStopSignalsDescendantGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	process, err := startServer(context.Background(), ".", filepath.Join(dir, "child.log"),
		[]string{"VERIFY_CHILD_HELPER=1", "VERIFY_DESCENDANT_PID=" + pidFile}, os.Args[0], "-test.run=^TestServerProcessHelper$")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = process.stop()
		} // cleanup still reaps a failed test's fixture
	})
	waitFile := func(file string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(file); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("subprocess handshake file missing: %s", filepath.Base(file))
	}
	waitFile(pidFile)
	err = process.stop()
	stopped = true
	if err != nil {
		t.Fatal(err)
	}
	waitFile(pidFile + ".stopped")
}

func TestServerProcessIsReapedOnStop(t *testing.T) {
	log := filepath.Join(t.TempDir(), "child.log")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process, err := startServer(ctx, ".", log, []string{"VERIFY_CHILD_HELPER=1"}, os.Args[0], "-test.run=^TestServerProcessHelper$")
	if err != nil {
		t.Fatal(err)
	}
	// The helper may receive SIGINT before installing its handler. Either normal
	// completion or an interrupt exit is acceptable; a missing Wait is not.
	_ = process.stop()
	if process.command.ProcessState == nil {
		t.Fatal("stop returned without reaping its child")
	}
	if _, err := process.log.WriteString("after close"); err == nil {
		t.Fatal("stop left the log open")
	}
}

func TestServerProcessContextCancellationIsReaped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	process, err := startServer(ctx, ".", filepath.Join(t.TempDir(), "child.log"), []string{"VERIFY_CHILD_HELPER=1"}, os.Args[0], "-test.run=^TestServerProcessHelper$")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { _ = process.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled server process was not reaped")
	}
	if process.command.ProcessState == nil {
		t.Fatal("cancellation returned without Wait")
	}
}

func TestCleanupDoesNotIgnoreLiveGroupMembers(t *testing.T) {
	if noLiveGroupMembers("111 Z\n222 S\n", 222) {
		t.Fatal("live descendant was treated as a zombie")
	}
	if !noLiveGroupMembers("111 Z\n222 S\n", 111) {
		t.Fatal("adopted zombie was treated as active work")
	}
	if !noLiveGroupMembers("111 S\n", 222) {
		t.Fatal("another process group affected owned cleanup")
	}
	for _, value := range []string{"", "not-process-data\n"} {
		if noLiveGroupMembers(value, 111) {
			t.Fatal("uncertain process inventory accepted")
		}
	}
}
