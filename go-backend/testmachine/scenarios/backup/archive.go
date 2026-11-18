package backup

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// invariant ids of the format-backup-archive requirement.
const (
	invArchiveLayout  = "INV-FORMAT-BACKUP-ARCHIVE"
	invArchiveDB      = "INV-FORMAT-BACKUP-DB-001"
	invArchiveMeta    = "INV-FORMAT-BACKUP-METADATA-001"
	invArchiveFailure = "INV-FORMAT-BACKUP-FAILURE-001"
	invArchiveDir     = "INV-FORMAT-BACKUP-DIR-001"
)

// archiveDirScenarios characterize BackupManager.init: the directory is
// created on demand, and an uncreatable directory stops the boot.
func archiveDirScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "format-backup-archive", Invariant: invArchiveDir, Case: "error", State: "failure", Assertion: "backup-init-uncreatable-directory-stops-boot",
			Sources: []string{"server/managers/BackupManager.js:50-63", "server/managers/BackupManager.js:34-36", "server/Server.js:204"},
			Claim:   "When the configured backup directory cannot be created the manager throws during startup, so the process exits nonzero instead of serving with a broken backup path.",
			Run: func(t *testing.T, _ *harness.Env) {
				blocker := filepath.Join(t.TempDir(), "blocker")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write blocker file: %v", err)
				}
				e := harness.Start(t, harness.Options{NoWait: true, Env: map[string]string{"BACKUP_PATH": filepath.Join(blocker, "backups")}})
				code, exited := e.WaitExit(30 * time.Second)
				if !exited {
					t.Fatalf("server still running with an uncreatable backup directory (log tail %s)", e.Log(t))
				}
				if code == 0 {
					t.Fatalf("server exited with code 0 after an uncreatable backup directory")
				}
			},
		},
	}
}

// archiveFailureScenarios characterize a create that aborts on maxBackupSize.
// They run before any archive exists: the abort's delayed cleanup removes the
// partially written minute-named file, which would otherwise be a valid archive
// of the same id, so the assertion needs a directory without that name.
func archiveFailureScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "format-backup-archive", Invariant: invArchiveFailure, Case: "error", State: "failure", Assertion: "backup-archive-failed-create-leaves-nothing",
			Sources: []string{"server/managers/BackupManager.js:431-503", "server/managers/BackupManager.js:333-391", "server/controllers/BackupController.js:41-43"},
			Claim:   "A create that exceeds maxBackupSize answers 500, leaves no new archive or temporary sqlite in the install, and leaves the backup list unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				dir := backupLocation(t, e)
				patchSettings(t, e, map[string]any{"maxBackupSize": 0.000001})
				defer restoreBackupDefaults(t, e)
				beforeFiles := archiveFiles(t, dir)
				beforeList := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, e.Post(t, root(t, e), "/api/backups", nil), http.StatusInternalServerError, "create over maxBackupSize")
				harness.Eventually(t, 10*time.Second, "the aborted archive to be removed", func() bool {
					return sameNames(archiveFiles(t, dir), beforeFiles)
				})
				if after := getBackups(t, e, root(t, e)).Backups; len(after) != len(beforeList) {
					t.Fatalf("failed create changed the backup count from %d to %d", len(beforeList), len(after))
				}
				for _, name := range dirNames(t, e.Config) {
					if strings.HasPrefix(name, "absdatabase.") && strings.HasSuffix(name, ".sqlite") && name != "absdatabase.sqlite" {
						t.Fatalf("failed create left a temporary database %q in config", name)
					}
				}
			},
		},
	}
}

// formatScenarios characterize the archive BackupManager writes: its entry set,
// the database snapshot, the metadata trees and the same-minute replace path.
func formatScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "format-backup-archive", Invariant: invArchiveLayout, Case: "normal", State: "initial", Assertion: "backup-archive-layout",
			Sources: []string{"server/managers/BackupManager.js:431-503", "server/objects/Backup.js:49-80", "server/objects/Backup.js:27-40"},
			Claim:   "A created backup is a zip that holds absdatabase.sqlite, one metadata-items/<item dir>/... tree for every metadata item folder, the metadata-authors/... tree and a details entry; details holds id, key sqlite, createdAt and serverVersion 2.37.2 on four lines.",
			Run: func(t *testing.T, e *harness.Env) {
				writeAuthorMetadata(t, e, "archive-author.txt", "archive author marker\n")
				created := createBackup(t, e)
				if !fileExists(created.FullPath) {
					t.Fatalf("archive %s does not exist", created.FullPath)
				}
				names := zipNames(t, created.FullPath)
				if !slices.Contains(names, "details") {
					t.Fatalf("archive has no details entry: %v", names)
				}
				if !slices.Contains(names, "absdatabase.sqlite") {
					t.Fatalf("archive has no absdatabase.sqlite entry: %v", names)
				}
				for _, item := range itemMetadataDirs(t, e) {
					want := "metadata-items/" + item + "/metadata.json"
					if !slices.Contains(names, want) {
						t.Fatalf("archive has no %q entry (item folders %v)", want, itemMetadataDirs(t, e))
					}
				}
				if !slices.Contains(names, "metadata-items/"+e.IDs["novel"]+"/cover.jpg") {
					t.Fatalf("archive has no cover.jpg for the fixture novel: %v", names)
				}
				if !slices.Contains(names, "metadata-authors/archive-author.txt") {
					t.Fatalf("archive has no metadata-authors entry: %v", names)
				}
				for _, name := range names {
					if name == "absdatabase.sqlite" || name == "details" {
						continue
					}
					if !hasPrefixAny(name, "metadata-items/", "metadata-authors/") {
						t.Fatalf("archive entry %q is outside the three backup sources: %v", name, names)
					}
				}
				details := trimLines(string(zipEntryData(t, created.FullPath, "details")))
				if len(details) != 4 {
					t.Fatalf("details = %q, want four lines", details)
				}
				if details[0] != created.ID || details[1] != "sqlite" || details[2] != itoa(created.CreatedAt) || details[3] != "2.37.2" {
					t.Fatalf("details = %q, want [%s sqlite %d 2.37.2]", details, created.ID, created.CreatedAt)
				}
				if !strings.HasSuffix(created.Filename, ".audiobookshelf") || created.Path != "backups/"+created.Filename {
					t.Fatalf("archive filename/path = %q/%q, want backups/<id>.audiobookshelf", created.Filename, created.Path)
				}
				if fileSize(t, created.FullPath) != created.FileSize || created.FileSize == 0 {
					t.Fatalf("archive fileSize = %d, disk = %d, want equal and non-zero", created.FileSize, fileSize(t, created.FullPath))
				}
			},
		},
		{
			Requirement: "format-backup-archive", Invariant: invArchiveMeta, Case: "edge", State: "initial", Assertion: "backup-archive-metadata-tree",
			Sources: []string{"server/managers/BackupManager.js:431-503", "server/managers/BackupManager.js:21-23"},
			Claim:   "The archive copies the metadata trees byte for byte: every metadata/items/<id>/metadata.json and cover.jpg matches the file on disk and the author marker written under metadata/authors is archived with its content.",
			Run: func(t *testing.T, e *harness.Env) {
				writeAuthorMetadata(t, e, "tree-author.txt", "tree marker\n")
				created := createBackup(t, e)
				for _, item := range itemMetadataDirs(t, e) {
					onDisk := filepath.Join(e.Metadata, "items", item)
					for _, name := range dirNames(t, onDisk) {
						diskBytes, err := os.ReadFile(filepath.Join(onDisk, name))
						if err != nil {
							t.Fatalf("read %s/%s: %v", item, name, err)
						}
						archived := zipEntryData(t, created.FullPath, "metadata-items/"+item+"/"+name)
						if string(archived) != string(diskBytes) {
							t.Fatalf("archived metadata-items/%s/%s differs from disk (%d vs %d bytes)", item, name, len(archived), len(diskBytes))
						}
					}
				}
				if got := string(zipEntryData(t, created.FullPath, "metadata-authors/tree-author.txt")); got != "tree marker\n" {
					t.Fatalf("archived author marker = %q, want %q", got, "tree marker\n")
				}
				for _, name := range zipNames(t, created.FullPath) {
					if strings.HasPrefix(name, "metadata/") || strings.Contains(name, "backups/") || strings.Contains(name, "logs/") {
						t.Fatalf("archive entry %q includes data outside items/authors", name)
					}
				}
			},
		},
		{
			Requirement: "format-backup-archive", Invariant: invArchiveDB, Case: "edge", State: "changed", Assertion: "backup-archive-db-snapshot",
			Sources: []string{"server/managers/BackupManager.js:341-351", "server/managers/BackupManager.js:404-429"},
			Claim:   "The zipped absdatabase.sqlite is the live database at creation time: after a library rename the snapshot's libraries row carries the new name and it holds the same user and item counts as the running install.",
			Run: func(t *testing.T, e *harness.Env) {
				libraryID := e.IDs["library"]
				defer renameLibrary(t, e, libraryID, "Audiobooks")
				taken := time.Now().UnixMilli()
				renameLibrary(t, e, libraryID, "Snapshot Rename")
				created := createBackup(t, e)
				if created.CreatedAt < taken-2000 {
					t.Fatalf("backup createdAt = %d, want the create request's time", created.CreatedAt)
				}
				snapshot := archiveDatabase(t, created.FullPath)
				if got := snapshotString(t, snapshot, "SELECT name FROM libraries WHERE id = ?", libraryID); got != "Snapshot Rename" {
					t.Fatalf("snapshot library name = %q, want Snapshot Rename", got)
				}
				if got, want := snapshotInt(t, snapshot, "SELECT COUNT(*) FROM users"), e.Count(t, "SELECT COUNT(*) FROM users"); got != want {
					t.Fatalf("snapshot user count = %d, running install = %d", got, want)
				}
				if got, want := snapshotInt(t, snapshot, "SELECT COUNT(*) FROM libraryItems"), e.Count(t, "SELECT COUNT(*) FROM libraryItems"); got != want {
					t.Fatalf("snapshot libraryItems count = %d, running install = %d", got, want)
				}
			},
		},
		{
			Requirement: "format-backup-archive", Invariant: invArchiveLayout, Case: "edge", State: "repeat", Assertion: "backup-archive-recreate-same-minute",
			Sources: []string{"server/objects/Backup.js:72-80", "server/managers/BackupManager.js:368-375"},
			Claim:   "Repeated creates leave one archive and one list entry per minute id: no duplicate ids, every listed id has exactly one .audiobookshelf file whose size equals the entry's fileSize, and the details timestamp equals createdAt.",
			Run: func(t *testing.T, e *harness.Env) {
				first := createBackup(t, e)
				second := createBackup(t, e)
				list := getBackups(t, e, root(t, e)).Backups
				seen := make(map[string]bool)
				for _, item := range list {
					if seen[item.ID] {
						t.Fatalf("backup id %q is listed twice: %+v", item.ID, list)
					}
					seen[item.ID] = true
					if !fileExists(item.FullPath) {
						t.Fatalf("listed backup %q has no file at %s", item.ID, item.FullPath)
					}
					if got := fileSize(t, item.FullPath); got != item.FileSize {
						t.Fatalf("backup %q fileSize = %d, disk = %d", item.ID, item.FileSize, got)
					}
					details := trimLines(string(zipEntryData(t, item.FullPath, "details")))
					if details[0] != item.ID || details[2] != itoa(item.CreatedAt) {
						t.Fatalf("backup %q details = %q, want id %s and createdAt %d", item.ID, details, item.ID, item.CreatedAt)
					}
				}
				if second.CreatedAt < first.CreatedAt {
					t.Fatalf("second create createdAt %d is before the first %d", second.CreatedAt, first.CreatedAt)
				}
				if first.ID == second.ID && len(archiveFiles(t, backupLocation(t, e))) != len(list) {
					t.Fatalf("same-minute replace left %d files for %d entries", len(archiveFiles(t, backupLocation(t, e))), len(list))
				}
			},
		},
	}
}

// itemMetadataDirs lists the metadata/items subdirectories, sorted.
func itemMetadataDirs(t *testing.T, e *harness.Env) []string {
	t.Helper()
	items := filepath.Join(e.Metadata, "items")
	names := dirNames(t, items)
	slices.Sort(names)
	return names
}

// writeAuthorMetadata writes one file under metadata/authors, creating the
// directory BackupManager archives.
func writeAuthorMetadata(t *testing.T, e *harness.Env, name, content string) {
	t.Helper()
	dir := filepath.Join(e.Metadata, "authors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create authors directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write author metadata: %v", err)
	}
}
