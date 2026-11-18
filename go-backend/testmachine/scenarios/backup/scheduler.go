package backup

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Scheduler invariants of the sub-backup-scheduler requirement.
const (
	invScheduleCreate  = "INV-BACKUP-SCHEDULE-CREATE"
	invScheduleRepeat  = "INV-BACKUP-SCHEDULE-REPEAT"
	invRestoreRollback = "INV-BACKUP-RESTORE-ROLLBACK"
)

// schedulerScenarios characterize the automatic producer, retention and the
// pre-swap rejection that must leave the running install usable. They run after
// the route scenarios because they wait for cron ticks.
func schedulerScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "sub-backup-scheduler", Invariant: invScheduleCreate, Case: "normal", State: "initial", Assertion: "backup-schedule-defaults-off",
			Sources: []string{"server/objects/settings/ServerSettings.js:56-59", "server/managers/BackupManager.js:75-79", "client/pages/config/backups.vue:201-207"},
			Claim:   "A fixture install starts with automatic backups disabled (backupSchedule false), backupsToKeep 2 and maxBackupSize 1, and no archive appears while the schedule is off.",
			Run: func(t *testing.T, e *harness.Env) {
				settings := patchSettings(t, e, map[string]any{})
				if scheduleValue(settings) != "" {
					t.Fatalf("backupSchedule = %q, want disabled at start", settings.BackupSchedule)
				}
				if settings.BackupsToKeep != 2 || settings.MaxBackupSize != 1 {
					t.Fatalf("defaults backupsToKeep/maxBackupSize = %d/%v, want 2/1", settings.BackupsToKeep, settings.MaxBackupSize)
				}
				dir := backupLocation(t, e)
				before := archiveFiles(t, dir)
				waitQuiet(t, 2*time.Second)
				if after := archiveFiles(t, dir); !sameNames(after, before) {
					t.Fatalf("archives appeared without a schedule: %v -> %v", before, after)
				}
			},
		},
		{
			Requirement: "sub-backup-scheduler", Invariant: invScheduleCreate, Case: "error", State: "failure", Assertion: "backup-schedule-invalid-cron-disabled",
			Sources: []string{"server/managers/BackupManager.js:75-101", "server/controllers/MiscController.js:165-171", "server/libs/nodeCron/pattern-validation.js:82-107"},
			Claim:   "An invalid cron expression is accepted into settings but leaves no active schedule: no archive is created after the update, and a later valid setting or false is still accepted.",
			Run: func(t *testing.T, e *harness.Env) {
				settings := patchSettings(t, e, map[string]any{"backupSchedule": "not a cron"})
				if scheduleValue(settings) != "not a cron" {
					t.Fatalf("backupSchedule = %q, want the stored invalid value", settings.BackupSchedule)
				}
				dir := backupLocation(t, e)
				beforeFiles := archiveFiles(t, dir)
				beforeList := getBackups(t, e, root(t, e)).Backups
				waitQuiet(t, 2*time.Second)
				if after := archiveFiles(t, dir); !sameNames(after, beforeFiles) {
					t.Fatalf("invalid cron still created archives: %v -> %v", beforeFiles, after)
				}
				if after := getBackups(t, e, root(t, e)).Backups; len(after) != len(beforeList) {
					t.Fatalf("invalid cron changed the backup count from %d to %d", len(beforeList), len(after))
				}
				if got := scheduleValue(patchSettings(t, e, map[string]any{"backupSchedule": false})); got != "" {
					t.Fatalf("backupSchedule after disabling = %q, want disabled", got)
				}
			},
		},
		{
			Requirement: "sub-backup-scheduler", Invariant: invScheduleCreate, Case: "normal", State: "changed", Assertion: "backup-schedule-fires",
			Sources: []string{"server/managers/BackupManager.js:75-101", "server/managers/BackupManager.js:333-391", "server/controllers/MiscController.js:165-171"},
			Claim:   "Setting backupSchedule to a per-2-seconds cron makes the server create archives by itself: a listed archive's createdAt moves past the value read before the update, and disabling the schedule stops it.",
			Run: func(t *testing.T, e *harness.Env) {
				before := newestCreatedAt(t, e)
				if got := scheduleValue(patchSettings(t, e, map[string]any{"backupSchedule": "*/2 * * * * *"})); got != "*/2 * * * * *" {
					t.Fatalf("backupSchedule = %q, want the per-2-seconds expression", got)
				}
				harness.Eventually(t, 30*time.Second, "the cron schedule to create an archive", func() bool {
					return newestCreatedAt(t, e) > before
				})
				created := newestBackupEntry(t, e)
				if !fileExists(created.FullPath) || created.FileSize != fileSize(t, created.FullPath) {
					t.Fatalf("scheduled archive %s is missing or its size differs", created.FullPath)
				}
				if got := scheduleValue(patchSettings(t, e, map[string]any{"backupSchedule": false})); got != "" {
					t.Fatalf("backupSchedule after disabling = %q, want disabled", got)
				}
			},
		},
		{
			Requirement: "sub-backup-scheduler", Invariant: invScheduleRepeat, Case: "edge", State: "repeat", Assertion: "backup-schedule-restart-single-task",
			Sources: []string{"server/managers/BackupManager.js:88-101", "server/controllers/MiscController.js:165-171"},
			Claim:   "Re-applying the same schedule restarts one scheduler: after it is disabled no further archive appears or changes for two seconds, so no duplicate or leaked task survives repeated updates.",
			Run: func(t *testing.T, e *harness.Env) {
				schedule := "*/2 * * * * *"
				patchSettings(t, e, map[string]any{"backupSchedule": schedule})
				before := newestCreatedAt(t, e)
				harness.Eventually(t, 30*time.Second, "the first schedule to create an archive", func() bool {
					return newestCreatedAt(t, e) > before
				})
				secondStart := newestCreatedAt(t, e)
				patchSettings(t, e, map[string]any{"backupSchedule": "* * * * * *"})
				harness.Eventually(t, 30*time.Second, "the restarted schedule to create an archive", func() bool {
					return newestCreatedAt(t, e) > secondStart
				})
				patchSettings(t, e, map[string]any{"backupSchedule": false})
				quietFiles := archiveFiles(t, backupLocation(t, e))
				quietStamp := newestCreatedAt(t, e)
				waitQuiet(t, 3*time.Second)
				if !sameNames(archiveFiles(t, backupLocation(t, e)), quietFiles) || newestCreatedAt(t, e) != quietStamp {
					t.Fatalf("a backup appeared after the schedule was disabled (files %v, newest %d)", quietFiles, quietStamp)
				}
			},
		},
		{
			Requirement: "sub-backup-scheduler", Invariant: invScheduleCreate, Case: "normal", State: "changed", Assertion: "backup-retention-removes-oldest",
			Sources: []string{"server/managers/BackupManager.js:377-388", "server/objects/settings/ServerSettings.js:42-48"},
			Claim:   "With backupsToKeep set to 1 each create removes exactly the oldest listed archive and its file: the first create removes the oldest upload, the second removes the next-oldest upload, and the newer upload and created archive stay listed with their files.",
			Run: func(t *testing.T, e *harness.Env) {
				oldest := uploadSynthetic(t, e, "2020-06-01T0000", 1590969600000)
				second := uploadSynthetic(t, e, "2020-06-02T0000", 1591056000000)
				patchSettings(t, e, map[string]any{"backupsToKeep": 1})
				defer restoreBackupDefaults(t, e)
				first := createBackup(t, e)
				harness.Eventually(t, 10*time.Second, "the oldest archive to be removed", func() bool {
					return !fileExists(oldest.FullPath)
				})
				listed := getBackups(t, e, root(t, e)).Backups
				if _, found := entryByID(listed, oldest.ID); found {
					t.Fatalf("oldest upload %q is still listed after retention", oldest.ID)
				}
				if _, found := entryByID(listed, second.ID); !found || !fileExists(second.FullPath) {
					t.Fatalf("retention removed the newer upload %q instead of the oldest", second.ID)
				}
				if _, found := entryByID(listed, first.ID); !found {
					t.Fatalf("retention removed the backup it just created %q", first.ID)
				}
				createBackup(t, e)
				harness.Eventually(t, 10*time.Second, "the second-oldest archive to be removed", func() bool {
					return !fileExists(second.FullPath)
				})
				for _, item := range getBackups(t, e, root(t, e)).Backups {
					if !fileExists(item.FullPath) {
						t.Fatalf("listed backup %q has no file after retention", item.ID)
					}
				}
			},
		},
		{
			Requirement: "sub-backup-scheduler", Invariant: invRestoreRollback, Case: "error", State: "failure", Assertion: "backup-apply-rejection-keeps-install",
			Sources: []string{"server/managers/BackupManager.js:200-229", "server/managers/BackupManager.js:263-266", "server/Database.js:304-315"},
			Claim:   "An apply rejected before the swap leaves the running database and metadata usable, removes no backup, writes no temporary database and emits no backup_applied.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-06-03T0000", 1591142400000)
				tampered := archiveBytes(t, []zipEntry{{Name: "details", Data: []byte(detailsText(uploaded.ID, "sqlite", uploaded.CreatedAt, "2.37.2"))}})
				if err := os.WriteFile(uploaded.FullPath, tampered, 0o600); err != nil {
					t.Fatalf("replace archive on disk: %v", err)
				}
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				users := e.Count(t, "SELECT COUNT(*) FROM users")
				items := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				requireStatus(t, applyBackup(t, e, uploaded.ID), http.StatusInternalServerError, "apply archive without database")
				socket.None(t, mark, "backup_applied", 750*time.Millisecond, nil)
				if got := e.Count(t, "SELECT COUNT(*) FROM users"); got != users {
					t.Fatalf("rejected apply changed the user count from %d to %d", users, got)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM libraryItems"); got != items {
					t.Fatalf("rejected apply changed the item count from %d to %d", items, got)
				}
				if fileExists(filepath.Join(e.Config, "absdatabase-temp.sqlite")) {
					t.Fatal("rejected apply left absdatabase-temp.sqlite in the config directory")
				}
				requireInstallUsable(t, e, "Audiobooks")
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "cleanup tampered archive")
			},
		},
	}
}

// waitQuiet waits a bounded window to observe that no background effect
// happens. Cron and file removal have no event stream, so absence needs a
// window; this is not a wait for an effect.
func waitQuiet(t *testing.T, window time.Duration) {
	t.Helper()
	time.Sleep(window)
}

// newestCreatedAt returns the newest listed createdAt, 0 for an empty list.
func newestCreatedAt(t *testing.T, e *harness.Env) int64 {
	t.Helper()
	item, found := newestBackup(getBackups(t, e, root(t, e)).Backups)
	if !found {
		return 0
	}
	return item.CreatedAt
}

// newestBackupEntry returns the listed entry with the newest createdAt.
func newestBackupEntry(t *testing.T, e *harness.Env) backup {
	t.Helper()
	item, found := newestBackup(getBackups(t, e, root(t, e)).Backups)
	if !found {
		t.Fatal("backup list is empty, want a scheduled archive")
	}
	return item
}
