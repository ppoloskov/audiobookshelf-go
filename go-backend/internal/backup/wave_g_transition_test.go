package backup

import (
	"context"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// BackupManager.js stops the old task before trying the replacement; an
// invalid cron is caught and does not leave a valid task running. Go mirrors
// that transition (cron=nil), and a subsequent valid update must recover.
func TestWaveGInvalidScheduleStopsTaskAndValidRetryRecovers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "backups")
	h, err := New(config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}, Options{
		BackupPath: path, BackupSchedule: "0 3 * * *",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	if h.cron == nil {
		t.Fatal("valid initial schedule did not start")
	}

	if err := h.UpdateOptions(context.Background(), Options{BackupPath: path, BackupSchedule: "not a cron"}); err == nil {
		t.Fatal("invalid schedule update succeeded")
	}
	if h.cron != nil {
		t.Fatal("invalid replacement left a running schedule; TS stops the prior task before scheduling")
	}
	if got := h.options().BackupSchedule; got != "not a cron" {
		t.Fatalf("stored schedule after rejected cron = %q, want attempted setting (TS settings persist before scheduler update)", got)
	}

	if err := h.UpdateOptions(context.Background(), Options{BackupPath: path, BackupSchedule: "*/5 * * * *"}); err != nil {
		t.Fatalf("valid retry: %v", err)
	}
	if h.cron == nil || h.options().BackupSchedule != "*/5 * * * *" {
		t.Fatal("valid retry did not restore the scheduled state")
	}
}
