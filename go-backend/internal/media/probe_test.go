package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunStartedNotifiesOnlyAfterStart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", bin)
	started := 0
	if err := RunStarted(context.Background(), func() { started++ }); err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("start callback count = %d, want 1", started)
	}
}

func TestRunStartedWaitDelayDoesNotLimitUncancelledCommand(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 1.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", bin)
	started := time.Now()
	if err := RunStarted(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < commandWaitDelay {
		t.Fatalf("uncancelled command returned after %s, want it to run beyond WaitDelay %s", elapsed, commandWaitDelay)
	}
}

func TestRunStartedDoesNotNotifyWhenStartFails(t *testing.T) {
	t.Setenv("FFMPEG_PATH", filepath.Join(t.TempDir(), "missing-ffmpeg"))
	started := false
	err := RunStarted(context.Background(), func() { started = true })
	if err == nil {
		t.Fatal("RunStarted succeeded for a missing executable")
	}
	if started {
		t.Fatal("start callback ran for a missing executable")
	}
}
