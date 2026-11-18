package backup

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// initialScenarios assert the fixture install's empty backup state. This runs
// before any scenario creates an archive.
func initialScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r122", Invariant: "INV-ROUTE-R122-ROUTE", Case: "normal", State: "stateless", Assertion: "backup-list-empty-at-start",
			Sources: []string{"server/controllers/BackupController.js:27-32", "server/managers/BackupManager.js:50-63", "server/objects/settings/ServerSettings.js:56"},
			Claim:   "On the fixture install GET /api/backups answers 200 with an empty backups array, backupLocation equal to metadata/backups (which exists and is empty) and backupPathEnvSet false.",
			Run: func(t *testing.T, e *harness.Env) {
				body := getBackups(t, e, root(t, e))
				if len(body.Backups) != 0 {
					t.Fatalf("initial backups = %+v, want empty", body.Backups)
				}
				want := filepath.Join(e.Metadata, "backups")
				if body.BackupLocation != want {
					t.Fatalf("backupLocation = %q, want %q", body.BackupLocation, want)
				}
				if body.BackupPathEnvSet {
					t.Fatal("backupPathEnvSet = true for an install without BACKUP_PATH")
				}
				if names := dirNames(t, want); len(names) != 0 {
					t.Fatalf("backup directory holds %v, want empty", names)
				}
			},
		},
	}
}

// listScenarios characterize GET /api/backups after archives exist.
func listScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r122", Invariant: "INV-ROUTE-R122-ROUTE", Case: "edge", State: "stateless", Assertion: "backup-list-entry-shape",
			Sources: []string{"server/controllers/BackupController.js:27-32", "server/objects/Backup.js:12-70", "server/objects/Backup.js:72-80"},
			Claim:   "GET /api/backups lists a created archive with id YYYY-MM-DDTHHmm, key sqlite, serverVersion 2.37.2, filename <id>.audiobookshelf, path backups/<filename>, fullPath and backupDirPath under backupLocation, a datePretty rendering of createdAt and fileSize equal to the file on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				body := getBackups(t, e, root(t, e))
				item, ok := entryByID(body.Backups, created.ID)
				if !ok {
					t.Fatalf("created backup %q is not listed: %+v", created.ID, body.Backups)
				}
				if !backupIDPattern.MatchString(item.ID) {
					t.Fatalf("backup id = %q, want YYYY-MM-DDTHHmm", item.ID)
				}
				if item.Key == nil || *item.Key != "sqlite" {
					t.Fatalf("backup key = %v, want sqlite", item.Key)
				}
				if item.ServerVersion == nil || *item.ServerVersion != "2.37.2" {
					t.Fatalf("backup serverVersion = %v, want 2.37.2", item.ServerVersion)
				}
				if item.Filename != item.ID+".audiobookshelf" || item.Path != "backups/"+item.Filename {
					t.Fatalf("filename/path = %q/%q, want <id>.audiobookshelf under backups/", item.Filename, item.Path)
				}
				if item.BackupDirPath != body.BackupLocation || filepath.Dir(item.FullPath) != body.BackupLocation || filepath.Base(item.FullPath) != item.Filename {
					t.Fatalf("fullPath/backupDirPath = %q/%q, want %q with filename %q", item.FullPath, item.BackupDirPath, body.BackupLocation, item.Filename)
				}
				wantPretty := time.UnixMilli(item.CreatedAt).UTC().Format("Mon, Jan 2 2006 15:04")
				if item.DatePretty != wantPretty {
					t.Fatalf("datePretty = %q, want %q", item.DatePretty, wantPretty)
				}
				if got := fileSize(t, item.FullPath); got == 0 || got != item.FileSize {
					t.Fatalf("fileSize = %d, disk = %d, want equal and non-zero", item.FileSize, got)
				}
			},
		},
		{
			Requirement: "route-r122", Invariant: "INV-ROUTE-R122-ROUTE", Case: "error", State: "stateless", Assertion: "backup-list-forbidden",
			Sources: []string{"server/controllers/BackupController.js:165-179", "server/routers/ApiRouter.js:200"},
			Claim:   "GET /api/backups is admin-only: an ordinary user gets 403 and an anonymous request 401, and neither changes the backup list, the directory or the install.",
			Run: func(t *testing.T, e *harness.Env) {
				before := getBackups(t, e, root(t, e))
				beforeFiles := archiveFiles(t, before.BackupLocation)
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/backups"), http.StatusForbidden, "ordinary GET /api/backups")
				requireStatus(t, e.Get(t, "", "/api/backups"), http.StatusUnauthorized, "anonymous GET /api/backups")
				after := getBackups(t, e, root(t, e))
				if len(after.Backups) != len(before.Backups) || !sameNames(archiveFiles(t, after.BackupLocation), beforeFiles) {
					t.Fatalf("rejected reads changed backups %d->%d files %v->%v", len(before.Backups), len(after.Backups), beforeFiles, archiveFiles(t, after.BackupLocation))
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r122", Invariant: "INV-ROUTE-R122-ROUTE", Case: "edge", State: "stateless", Assertion: "backup-list-env-path",
			Sources: []string{"server/controllers/BackupController.js:27-32", "server/managers/BackupManager.js:34-36", "server/managers/BackupManager.js:50-63", "server/objects/settings/ServerSettings.js:219-222"},
			Claim:   "With BACKUP_PATH set the server creates that directory on demand, GET /api/backups reports it as backupLocation with backupPathEnvSet true, and a created archive is written there.",
			Run: func(t *testing.T, _ *harness.Env) {
				dir := filepath.Join(t.TempDir(), "env", "backups")
				e := harness.Start(t, harness.Options{Env: map[string]string{"BACKUP_PATH": dir}})
				body := getBackups(t, e, root(t, e))
				if body.BackupLocation != dir {
					t.Fatalf("backupLocation = %q, want BACKUP_PATH %q", body.BackupLocation, dir)
				}
				if !body.BackupPathEnvSet {
					t.Fatal("backupPathEnvSet = false with BACKUP_PATH set")
				}
				if names := dirNames(t, dir); len(names) != 0 {
					t.Fatalf("BACKUP_PATH directory holds %v before any backup", names)
				}
				created := createBackup(t, e)
				if filepath.Dir(created.FullPath) != dir || !fileExists(created.FullPath) {
					t.Fatalf("archive %s was not written under BACKUP_PATH %s", created.FullPath, dir)
				}
			},
		},
	}
}

// createScenarios characterize POST /api/backups.
func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r123", Invariant: "INV-ROUTE-R123-ROUTE", Case: "normal", State: "initial", Assertion: "backup-create-response-shape",
			Sources: []string{"server/controllers/BackupController.js:41-43", "server/managers/BackupManager.js:183-192"},
			Claim:   "POST /api/backups answers 200 with an object holding only a backups array that includes the new archive entry, and the archive exists on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Post(t, root(t, e), "/api/backups", nil)
				requireStatus(t, resp, http.StatusOK, "POST /api/backups")
				var raw map[string]json.RawMessage
				resp.JSON(t, &raw)
				if len(raw) != 1 {
					t.Fatalf("create response keys = %v, want only backups", keysOf(raw))
				}
				if _, ok := raw["backups"]; !ok {
					t.Fatalf("create response has no backups key: %v", keysOf(raw))
				}
				created := createBackup(t, e)
				if !fileExists(created.FullPath) || fileSize(t, created.FullPath) != created.FileSize {
					t.Fatalf("created archive %s missing or size mismatch", created.FullPath)
				}
			},
		},
		{
			Requirement: "route-r123", Invariant: "INV-ROUTE-R123-ROUTE", Case: "error", State: "failure", Assertion: "backup-create-forbidden",
			Sources: []string{"server/controllers/BackupController.js:165-179", "server/routers/ApiRouter.js:201"},
			Claim:   "An ordinary user's POST /api/backups gets 403 and an anonymous one 401; neither creates an archive or changes the backup list or the install.",
			Run: func(t *testing.T, e *harness.Env) {
				before := getBackups(t, e, root(t, e))
				beforeFiles := archiveFiles(t, before.BackupLocation)
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/backups", nil), http.StatusForbidden, "ordinary POST /api/backups")
				requireStatus(t, e.Post(t, "", "/api/backups", nil), http.StatusUnauthorized, "anonymous POST /api/backups")
				after := getBackups(t, e, root(t, e))
				if len(after.Backups) != len(before.Backups) || !sameNames(archiveFiles(t, after.BackupLocation), beforeFiles) {
					t.Fatalf("rejected creates changed backups %d->%d files %v->%v", len(before.Backups), len(after.Backups), beforeFiles, archiveFiles(t, after.BackupLocation))
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r123", Invariant: "INV-ROUTE-R123-REPEAT", Case: "edge", State: "repeat", Assertion: "backup-create-repeated",
			Sources: []string{"server/managers/BackupManager.js:366-375", "server/objects/Backup.js:72-80"},
			Claim:   "Two creates in a row answer 200 each, leave no duplicate backup id and every listed archive exists with the reported fileSize; when both fall in one minute the second replaces the single entry instead of adding another.",
			Run: func(t *testing.T, e *harness.Env) {
				first := createBackup(t, e)
				second := createBackup(t, e)
				list := getBackups(t, e, root(t, e)).Backups
				seen := make(map[string]bool)
				for _, item := range list {
					if seen[item.ID] {
						t.Fatalf("backup id %q is listed twice after repeated creates", item.ID)
					}
					seen[item.ID] = true
					if !fileExists(item.FullPath) || fileSize(t, item.FullPath) != item.FileSize {
						t.Fatalf("backup %q file missing or size mismatch", item.ID)
					}
				}
				if second.CreatedAt < first.CreatedAt {
					t.Fatalf("second create createdAt %d before first %d", second.CreatedAt, first.CreatedAt)
				}
				if first.ID == second.ID {
					if count := len(archiveFiles(t, backupLocation(t, e))); count != len(list) {
						t.Fatalf("same-minute repeat left %d files for %d list entries", count, len(list))
					}
				}
			},
		},
		{
			Requirement: "route-r123", Invariant: "INV-ROUTE-R123-ROUTE", Case: "normal", State: "changed", Assertion: "backup-create-after-upload",
			Sources: []string{"server/managers/BackupManager.js:183-192", "server/managers/BackupManager.js:333-391"},
			Claim:   "After an uploaded backup is deleted, POST /api/backups answers with a list that no longer names the deleted id and does name the newly created archive.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-02-02T0000", 1580601600000)
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "delete uploaded backup")
				resp := e.Post(t, root(t, e), "/api/backups", nil)
				requireStatus(t, resp, http.StatusOK, "POST /api/backups after delete")
				var body backupsResponse
				resp.JSON(t, &body)
				if _, found := entryByID(body.Backups, uploaded.ID); found {
					t.Fatalf("create response still lists deleted backup %q", uploaded.ID)
				}
				created, ok := newestBackup(body.Backups)
				if !ok || created.ID == uploaded.ID || !fileExists(created.FullPath) {
					t.Fatalf("create response has no new archive: %+v", body.Backups)
				}
			},
		},
	}
}

// deleteScenarios characterize DELETE /api/backups/:id.
func deleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r124", Invariant: "INV-ROUTE-R124-ROUTE", Case: "normal", State: "initial", Assertion: "backup-delete-removes-entry-and-file",
			Sources: []string{"server/controllers/BackupController.js:49-59", "server/managers/BackupManager.js:393-402"},
			Claim:   "DELETE /api/backups/:id answers 200 with only a backups array that omits the id, removes the archive file and keeps the other archives listed.",
			Run: func(t *testing.T, e *harness.Env) {
				keep := createBackup(t, e)
				uploaded := uploadSynthetic(t, e, "2020-02-03T0000", 1580688000000)
				resp := deleteBackup(t, e, uploaded.ID)
				requireStatus(t, resp, http.StatusOK, "DELETE uploaded backup")
				var body backupsResponse
				resp.JSON(t, &body)
				if _, found := entryByID(body.Backups, uploaded.ID); found {
					t.Fatalf("deleted backup %q is still listed", uploaded.ID)
				}
				if fileExists(uploaded.FullPath) {
					t.Fatalf("deleted archive %s is still on disk", uploaded.FullPath)
				}
				kept, found := entryByID(getBackups(t, e, root(t, e)).Backups, keep.ID)
				if !found || !fileExists(kept.FullPath) {
					t.Fatalf("deleting %q also removed %q", uploaded.ID, keep.ID)
				}
			},
		},
		{
			Requirement: "route-r124", Invariant: "INV-ROUTE-R124-ROUTE", Case: "error", State: "failure", Assertion: "backup-delete-forbidden",
			Sources: []string{"server/controllers/BackupController.js:49-59", "server/controllers/BackupController.js:165-179"},
			Claim:   "An ordinary user's delete gets 403, an anonymous one 401 and an unknown id 404; none removes the archive file or changes the listed backups.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-02-04T0000", 1580774400000)
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/backups/"+uploaded.ID), http.StatusForbidden, "ordinary delete")
				requireStatus(t, e.Delete(t, "", "/api/backups/"+uploaded.ID), http.StatusUnauthorized, "anonymous delete")
				requireStatus(t, deleteBackup(t, e, "0000-00-00T0000"), http.StatusNotFound, "delete unknown id")
				if !fileExists(uploaded.FullPath) {
					t.Fatalf("rejected delete removed archive %s", uploaded.FullPath)
				}
				after := getBackups(t, e, root(t, e)).Backups
				if len(after) != len(before) {
					t.Fatalf("rejected deletes changed the backup count from %d to %d", len(before), len(after))
				}
				requireInstallUsable(t, e, "Audiobooks")
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "cleanup uploaded backup")
			},
		},
		{
			Requirement: "route-r124", Invariant: "INV-ROUTE-R124-REPEAT", Case: "edge", State: "repeat", Assertion: "backup-delete-repeated",
			Sources: []string{"server/controllers/BackupController.js:49-59", "server/managers/BackupManager.js:393-402"},
			Claim:   "Deleting the same backup twice answers 200 and then 404, and the second delete leaves the remaining backups and their files untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-02-05T0000", 1580860800000)
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "first delete")
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusNotFound, "second delete")
				after := getBackups(t, e, root(t, e)).Backups
				if len(after) != len(before) {
					t.Fatalf("repeated delete changed the backup count from %d to %d", len(before), len(after))
				}
				for _, item := range after {
					if !fileExists(item.FullPath) {
						t.Fatalf("repeated delete removed unrelated archive %q", item.ID)
					}
				}
			},
		},
		{
			Requirement: "route-r124", Invariant: "INV-ROUTE-R124-ROUTE", Case: "normal", State: "changed", Assertion: "backup-delete-targets-one-id",
			Sources: []string{"server/controllers/BackupController.js:55-59", "server/managers/BackupManager.js:396-397"},
			Claim:   "With two uploaded backups listed, deleting one removes exactly that id and file while the other stays listed with its file intact.",
			Run: func(t *testing.T, e *harness.Env) {
				first := uploadSynthetic(t, e, "2020-02-06T0000", 1580947200000)
				second := uploadSynthetic(t, e, "2020-02-07T0000", 1581033600000)
				requireStatus(t, deleteBackup(t, e, first.ID), http.StatusOK, "delete first of two")
				list := getBackups(t, e, root(t, e)).Backups
				if _, found := entryByID(list, first.ID); found {
					t.Fatalf("deleted id %q still listed", first.ID)
				}
				kept, found := entryByID(list, second.ID)
				if !found || !fileExists(kept.FullPath) {
					t.Fatalf("unrelated upload %q was removed with %q", second.ID, first.ID)
				}
				if fileExists(first.FullPath) {
					t.Fatalf("deleted archive %s still exists", first.FullPath)
				}
				requireStatus(t, deleteBackup(t, e, second.ID), http.StatusOK, "cleanup second upload")
			},
		},
		{
			Requirement: "route-r124", Invariant: "INV-ROUTE-R124-ROUTE", Case: "edge", State: "failure", Assertion: "backup-delete-unremovable-file",
			Sources: []string{"server/managers/BackupManager.js:393-402", "server/controllers/BackupController.js:49-59"},
			Claim:   "A delete whose archive file cannot be removed (read-only backup directory) answers 200 with the entry still listed and the file intact; after permissions are restored the same delete removes both.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-02-08T0000", 1581120000000)
				dir := backupLocation(t, e)
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatalf("make backup directory read-only: %v", err)
				}
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "delete in read-only directory")
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatalf("restore backup directory permissions: %v", err)
				}
				if _, found := entryByID(getBackups(t, e, root(t, e)).Backups, uploaded.ID); !found || !fileExists(uploaded.FullPath) {
					t.Fatalf("failed delete removed entry or file of %q", uploaded.ID)
				}
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "delete after restoring permissions")
				if fileExists(uploaded.FullPath) {
					t.Fatalf("archive %s still exists after a successful delete", uploaded.FullPath)
				}
			},
		},
	}
}

// backupIDPattern is the id format Backup.setData assigns: YYYY-MM-DDTHHmm.
var backupIDPattern = mustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}$`)

// mustCompile compiles a pattern the packet depends on.
func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// keysOf returns a map's keys for a failure message.
func keysOf(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// uploadSynthetic uploads an archive with an explicit id and timestamp. It
// carries a placeholder database entry: upload validates the entry's presence
// and details, not the database bytes.
func uploadSynthetic(t *testing.T, e *harness.Env, id string, createdAt int64) backup {
	t.Helper()
	return uploadArchiveExpect(t, e, id+".audiobookshelf", uploadArchive(t, id, "sqlite", "2.37.2", createdAt,
		zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00placeholder")}), id)
}

// uploadArchiveExpect uploads content and returns the listed entry with id.
func uploadArchiveExpect(t *testing.T, e *harness.Env, name string, content []byte, id string) backup {
	t.Helper()
	resp := uploadBackup(t, e, name, content)
	requireStatus(t, resp, http.StatusOK, "upload "+name)
	var body backupsResponse
	resp.JSON(t, &body)
	item, found := entryByID(body.Backups, id)
	if !found {
		t.Fatalf("upload %q did not list backup %q: %+v", name, id, body.Backups)
	}
	return item
}
