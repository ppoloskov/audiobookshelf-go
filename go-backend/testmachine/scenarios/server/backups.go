package server

import (
	"net/http"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

var backupIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}$`)

func (s *suite) flowScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "normal", State: "initial", Assertion: "backup-list-empty-at-start",
			Sources: []string{"server/controllers/BackupController.js:20-33", "server/managers/BackupManager.js:38-56"},
			Claim:   "On the fixture install GET /api/backups reports no backups, the install's metadata/backups location and backupPathEnvSet false.",
			Run: func(t *testing.T, e *harness.Env) {
				body := getBackups(t, e, root(t, e))
				if len(body.Backups) != 0 {
					t.Fatalf("initial backup list = %+v, want empty", body.Backups)
				}
				if body.BackupLocation != backupDir(e) {
					t.Fatalf("backupLocation = %q, want %q", body.BackupLocation, backupDir(e))
				}
				if body.BackupPathEnvSet {
					t.Fatal("backupPathEnvSet = true, want false for the fixture install")
				}
				if names := dirNames(t, backupDir(e)); len(names) != 0 {
					t.Fatalf("initial backup directory holds %v, want empty", names)
				}
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "edge", State: "initial", Assertion: "backup-list-shape-and-file",
			Sources: []string{"server/controllers/BackupController.js:20-33", "server/managers/BackupManager.js:345-420", "server/objects/Backup.js:1-80"},
			Claim:   "Creating a backup answers 200 with a backup entry whose id, filename, path, directory, server version, createdAt and fileSize match the archive written to the backup directory.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().UnixMilli()
				created := s.ensureBackup(t, e)
				if !backupIDPattern.MatchString(created.ID) {
					t.Fatalf("backup id = %q, want YYYY-MM-DDTHHmm", created.ID)
				}
				if created.Key != "sqlite" || created.ServerVersion != "2.37.2" {
					t.Fatalf("backup key/version = %q/%q, want sqlite/2.37.2", created.Key, created.ServerVersion)
				}
				if created.Filename != created.ID+".audiobookshelf" || created.Path != "backups/"+created.Filename {
					t.Fatalf("backup filename/path = %q/%q, want the id-based archive name", created.Filename, created.Path)
				}
				if created.BackupDirPath != backupDir(e) || filepath.Dir(created.FullPath) != backupDir(e) {
					t.Fatalf("backup directories = %q/%q, want %q", created.BackupDirPath, created.FullPath, backupDir(e))
				}
				if created.DatePretty == "" || created.CreatedAt < started || created.CreatedAt > time.Now().UnixMilli() {
					t.Fatalf("backup datePretty/createdAt = %q/%d, want a current timestamp", created.DatePretty, created.CreatedAt)
				}
				onDisk := fileSize(t, created.FullPath)
				if onDisk == 0 || onDisk != created.FileSize {
					t.Fatalf("backup fileSize = %d, disk size = %d, want equal and non-zero", created.FileSize, onDisk)
				}
				if names := dirNames(t, backupDir(e)); len(names) != 1 || names[0] != created.Filename {
					t.Fatalf("backup directory = %v, want only %q", names, created.Filename)
				}
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "normal", State: "changed", Assertion: "backup-create-apply-restores-database",
			Sources: []string{"server/managers/BackupManager.js:190-273", "server/controllers/BackupController.js:160-168"},
			Claim:   "Applying a backup created before a library rename answers 200 OK, replaces the active database so the old library name is read back, and keeps the backup listed.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				patchLibraryName(t, e, e.IDs["library"], "After Backup")
				apply := applyBackup(t, e, backup.ID)
				requireStatus(t, apply, http.StatusOK, "apply backup")
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("library name after applying the backup = %q, want Audiobooks", got)
				}
				found := false
				for _, item := range getBackups(t, e, root(t, e)).Backups {
					found = found || item.ID == backup.ID
				}
				if !found {
					t.Fatalf("backup %q is no longer listed after applying it", backup.ID)
				}
				requireStatus(t, e.Get(t, root(t, e), "/api/me"), http.StatusOK, "existing token after apply")
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "error", State: "failure", Assertion: "backup-route-auth-and-unknown-id",
			Sources: []string{"server/controllers/BackupController.js:160-180", "server/routers/ApiRouter.js:200-206"},
			Claim:   "Backup routes are admin-only: ordinary users get 403, anonymous requests 401, and unknown ids 404, without changing the backup list or a library row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := len(getBackups(t, e, root(t, e)).Backups)
				listener := e.Token(t, "listener")
				requireStatus(t, e.Get(t, listener, "/api/backups"), http.StatusForbidden, "ordinary GET /api/backups")
				requireStatus(t, e.Post(t, listener, "/api/backups", nil), http.StatusForbidden, "ordinary POST /api/backups")
				requireStatus(t, e.Post(t, listener, "/api/backups/upload", nil), http.StatusForbidden, "ordinary POST /api/backups/upload")
				requireStatus(t, e.Get(t, "", "/api/backups"), http.StatusUnauthorized, "anonymous GET /api/backups")
				requireStatus(t, e.Get(t, root(t, e), "/api/backups/not-a-backup/apply"), http.StatusNotFound, "apply unknown backup")
				requireStatus(t, e.Delete(t, root(t, e), "/api/backups/not-a-backup"), http.StatusNotFound, "delete unknown backup")
				if after := len(getBackups(t, e, root(t, e)).Backups); after != before {
					t.Fatalf("backup count changed from %d to %d after rejected requests", before, after)
				}
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("library name after rejected backup requests = %q, want Audiobooks", got)
				}
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "edge", State: "repeat", Assertion: "backup-apply-repeat-idempotent",
			Sources: []string{"server/managers/BackupManager.js:190-273"},
			Claim:   "Applying the same backup twice answers 200 OK both times and leaves the restored library name and backup list unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				patchLibraryName(t, e, e.IDs["library"], "Repeat Apply")
				before := len(getBackups(t, e, root(t, e)).Backups)
				for i := range 2 {
					requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "repeated apply backup")
					if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
						t.Fatalf("apply %d library name = %q, want Audiobooks", i+1, got)
					}
				}
				if after := len(getBackups(t, e, root(t, e)).Backups); after != before {
					t.Fatalf("backup count changed from %d to %d after repeats", before, after)
				}
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "normal", State: "changed", Assertion: "backup-apply-restores-users-and-library",
			Sources: []string{"server/managers/BackupManager.js:190-273", "server/Database.js"},
			Claim:   "Applying a backup restores the database rows captured at backup time: a later library rename and a later created user are gone, while the pre-existing root token still authenticates.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				const name = "backup-late-user"
				created := e.Post(t, root(t, e), "/api/users", map[string]any{"username": name, "password": "backup-late-password", "isActive": true, "type": "user"})
				requireStatus(t, created, http.StatusOK, "create user after backup")
				var user struct {
					User struct {
						ID string `json:"id"`
					} `json:"user"`
				}
				created.JSON(t, &user)
				patchLibraryName(t, e, e.IDs["library"], "Late Change")
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup over later changes")
				requireStatus(t, e.Get(t, root(t, e), "/api/users/"+user.User.ID), http.StatusNotFound, "later user after apply")
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("library name after apply = %q, want Audiobooks", got)
				}
				requireStatus(t, e.Get(t, root(t, e), "/api/me"), http.StatusOK, "root token after apply")
			},
		},
		{
			Requirement: "cap-flow-backup-restore-swap", Invariant: "INV-CAP-FLOW-BACKUP-RESTORE-SWAP", Case: "error", State: "failure", Assertion: "backup-hostile-archive-rejected",
			Sources: []string{"server/managers/BackupManager.js:87-180", "server/controllers/BackupController.js:70-80"},
			Claim:   "Uploading an archive without absdatabase.sqlite, a non-zip and a wrong-extension file is rejected with 500/400/500, adds no backup, changes no library and emits no backup_applied.",
			Run: func(t *testing.T, e *harness.Env) {
				before := len(getBackups(t, e, root(t, e)).Backups)
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				hostile := zipWith(t, map[string][]byte{"details": []byte("2026-01-01T0000\nsqlite\n1\n2.37.2")})
				requireStatus(t, uploadBackup(t, e, "hostile.audiobookshelf", hostile), http.StatusInternalServerError, "upload archive without a database")
				requireStatus(t, uploadBackup(t, e, "broken.audiobookshelf", []byte("not a zip")), http.StatusBadRequest, "upload non-zip")
				requireStatus(t, uploadBackup(t, e, "notes.txt", []byte("hello")), http.StatusInternalServerError, "upload wrong extension")
				if after := len(getBackups(t, e, root(t, e)).Backups); after != before {
					t.Fatalf("rejected uploads changed the backup count from %d to %d", before, after)
				}
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("rejected uploads changed the library name to %q", got)
				}
				if names := dirNames(t, backupDir(e)); len(names) != before {
					t.Fatalf("rejected uploads left files in the backup directory: %v", names)
				}
				socket.None(t, mark, "backup_applied", 500*time.Millisecond, nil)
			},
		},
	}
}
