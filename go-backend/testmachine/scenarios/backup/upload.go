package backup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// uploadScenarios characterize POST /api/backups/upload.
func uploadScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r127", Invariant: "INV-ROUTE-R127-ROUTE", Case: "normal", State: "initial", Assertion: "backup-upload-valid-archive",
			Sources: []string{"server/controllers/BackupController.js:70-80", "server/managers/BackupManager.js:103-181"},
			Claim:   "Uploading a .audiobookshelf archive with details and absdatabase.sqlite answers 200 with only a backups array, lists the archive with its details id, key, serverVersion, createdAt and a fileSize equal to the stored file, and keeps the file in the backup directory.",
			Run: func(t *testing.T, e *harness.Env) {
				const id = "2020-04-01T0000"
				const createdAt = int64(1585699200000)
				content := uploadArchive(t, id, "sqlite", "2.37.2", createdAt,
					zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00placeholder")})
				resp := uploadBackup(t, e, "valid-upload.audiobookshelf", content)
				requireStatus(t, resp, http.StatusOK, "upload valid archive")
				var raw map[string]json.RawMessage
				resp.JSON(t, &raw)
				if len(raw) != 1 {
					t.Fatalf("upload response keys = %v, want only backups", keysOf(raw))
				}
				var body backupsResponse
				resp.JSON(t, &body)
				item, found := entryByID(body.Backups, id)
				if !found {
					t.Fatalf("upload did not list backup %q: %+v", id, body.Backups)
				}
				if item.Key == nil || *item.Key != "sqlite" || item.ServerVersion == nil || *item.ServerVersion != "2.37.2" {
					t.Fatalf("uploaded key/version = %v/%v, want sqlite/2.37.2", item.Key, item.ServerVersion)
				}
				if item.CreatedAt != createdAt || item.ID != id {
					t.Fatalf("uploaded id/createdAt = %q/%d, want %q/%d", item.ID, item.CreatedAt, id, createdAt)
				}
				if item.Filename != "valid-upload.audiobookshelf" || item.Path != "backups/"+item.Filename {
					t.Fatalf("uploaded filename/path = %q/%q", item.Filename, item.Path)
				}
				if filepath.Dir(item.FullPath) != backupLocation(t, e) || item.BackupDirPath != backupLocation(t, e) {
					t.Fatalf("uploaded fullPath = %q, want under %q", item.FullPath, backupLocation(t, e))
				}
				if got := fileSize(t, item.FullPath); got == 0 || got != item.FileSize || int64(len(content)) != got {
					t.Fatalf("uploaded fileSize = %d, disk = %d, upload = %d", item.FileSize, got, len(content))
				}
				if _, found := entryByID(getBackups(t, e, root(t, e)).Backups, id); !found {
					t.Fatalf("uploaded backup %q is not listed", id)
				}
				requireStatus(t, deleteBackup(t, e, id), http.StatusOK, "cleanup uploaded archive")
			},
		},
		{
			Requirement: "route-r127", Invariant: "INV-ROUTE-R127-ROUTE", Case: "edge", State: "initial", Assertion: "backup-upload-details-quirks",
			Sources: []string{"server/objects/Backup.js:41-43", "server/managers/BackupManager.js:154-176", "server/managers/BackupManager.js:110", "client/components/tables/BackupsTable.vue:24-27"},
			Claim:   "The details key '1' from pre-2.2.23 archives is listed as null so clients hide restore, any non-empty serverVersion is accepted as-is, and a traversal-looking upload filename is sanitized into the backup directory without writing elsewhere.",
			Run: func(t *testing.T, e *harness.Env) {
				legacy := uploadArchiveExpect(t, e, "legacy.audiobookshelf",
					uploadArchive(t, "2020-04-02T0000", "1", "2.2.20", 1585785600000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}), "2020-04-02T0000")
				if legacy.Key != nil {
					t.Fatalf("legacy key = %q, want null for the '1' placeholder", *legacy.Key)
				}
				defer deleteBackup(t, e, legacy.ID)

				old := uploadArchiveExpect(t, e, "old-version.audiobookshelf",
					uploadArchive(t, "2020-04-03T0000", "sqlite", "0.9.9", 1585872000000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}), "2020-04-03T0000")
				if old.ServerVersion == nil || *old.ServerVersion != "0.9.9" {
					t.Fatalf("old-version serverVersion = %v, want 0.9.9 accepted as-is", old.ServerVersion)
				}
				defer deleteBackup(t, e, old.ID)

				traversal := uploadArchiveExpect(t, e, "../../escape.audiobookshelf",
					uploadArchive(t, "2020-04-04T0000", "sqlite", "2.37.2", 1585958400000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}), "2020-04-04T0000")
				if strings.ContainsAny(traversal.Filename, `/\`) {
					t.Fatalf("uploaded filename %q keeps a path separator", traversal.Filename)
				}
				if filepath.Dir(traversal.FullPath) != backupLocation(t, e) {
					t.Fatalf("traversal upload landed at %q, want inside %q", traversal.FullPath, backupLocation(t, e))
				}
				for _, outside := range []string{
					filepath.Join(e.Metadata, "escape.audiobookshelf"),
					filepath.Join(e.Install, "escape.audiobookshelf"),
				} {
					if fileExists(outside) {
						t.Fatalf("traversal upload wrote %q", outside)
					}
				}
				defer deleteBackup(t, e, traversal.ID)
			},
		},
		{
			Requirement: "route-r127", Invariant: "INV-ROUTE-R127-VALIDATION-001", Case: "error", State: "failure", Assertion: "backup-upload-rejections",
			Sources: []string{"server/controllers/BackupController.js:70-80", "server/managers/BackupManager.js:103-166"},
			Claim:   "Uploads with no file part or a file under another field, a wrong extension, a non-zip body, a missing absdatabase.sqlite, a missing details entry, a details entry over 1 MiB, a details entry without serverVersion, a metadata entry that traverses with ../ or a move that cannot enter a read-only backup directory are rejected with 500/500/500/400/500/400/400/500/400/500, add nothing to the list, leave no file in the backup directory or outside it, and leave the install unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				dir := backupLocation(t, e)
				before := getBackups(t, e, root(t, e)).Backups
				beforeFiles := archiveFiles(t, dir)
				noFile, contentType := harness.Multipart(map[string]string{"note": "no file"})
				requireStatus(t, e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/backups/upload", Token: root(t, e), Body: noFile, Header: map[string]string{"Content-Type": contentType}}), http.StatusInternalServerError, "upload without a file part")
				requireStatus(t, uploadBackup(t, e, "notes.txt", []byte("hello")), http.StatusInternalServerError, "upload wrong extension")
				otherField, otherType := harness.Multipart(map[string]string{}, harness.File{Field: "other", Name: "other.audiobookshelf", Content: uploadArchive(t, "2020-05-05T0000", "sqlite", "2.37.2", 1588636800000)})
				requireStatus(t, e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/backups/upload", Token: root(t, e), Body: otherField, Header: map[string]string{"Content-Type": otherType}}), http.StatusInternalServerError, "upload under a field other than file")
				requireStatus(t, uploadBackup(t, e, "broken.audiobookshelf", []byte("not a zip")), http.StatusBadRequest, "upload non-zip")
				requireStatus(t, uploadBackup(t, e, "no-db.audiobookshelf",
					uploadArchive(t, "2020-05-01T0000", "sqlite", "2.37.2", 1588291200000)), http.StatusInternalServerError, "upload without database")
				requireStatus(t, uploadBackup(t, e, "no-details.audiobookshelf",
					archiveBytes(t, []zipEntry{{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}})), http.StatusBadRequest, "upload without details")
				requireStatus(t, uploadBackup(t, e, "big-details.audiobookshelf",
					archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: bytes.Repeat([]byte("a"), 1<<20+1)},
					})), http.StatusBadRequest, "upload oversized details")
				requireStatus(t, uploadBackup(t, e, "three-line.audiobookshelf",
					archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: []byte("2020-05-02T0000\nsqlite\n1588377600000")},
					})), http.StatusInternalServerError, "upload details without serverVersion")
				requireStatus(t, uploadBackup(t, e, "traversal.audiobookshelf",
					archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: []byte(detailsText("2020-05-04T0000", "sqlite", 1588550400000, "2.37.2"))},
						{Name: "metadata-items/../escape-marker.txt", Data: []byte("escaped\n")},
					})), http.StatusBadRequest, "upload traversal entry")
				for _, outside := range []string{
					filepath.Join(e.Metadata, "escape-marker.txt"),
					filepath.Join(e.Install, "escape-marker.txt"),
					filepath.Join(filepath.Dir(e.Install), "escape-marker.txt"),
				} {
					if fileExists(outside) {
						t.Fatalf("rejected upload wrote %q outside the archive", outside)
					}
				}
				harness.Eventually(t, 10*time.Second, "rejected uploads to leave no file", func() bool {
					return sameNames(archiveFiles(t, dir), beforeFiles)
				})
				if after := getBackups(t, e, root(t, e)).Backups; len(after) != len(before) {
					t.Fatalf("rejected uploads changed the backup count from %d to %d", len(before), len(after))
				}
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatalf("make backup directory read-only: %v", err)
				}
				requireStatus(t, uploadBackup(t, e, "move-fails.audiobookshelf",
					uploadArchive(t, "2020-05-06T0000", "sqlite", "2.37.2", 1588723200000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")})), http.StatusInternalServerError, "upload that cannot move into the backup directory")
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatalf("restore backup directory permissions: %v", err)
				}
				if after := getBackups(t, e, root(t, e)).Backups; len(after) != len(before) {
					t.Fatalf("failed move changed the backup count from %d to %d", len(before), len(after))
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r127", Invariant: "INV-ROUTE-R127-REPEAT", Case: "edge", State: "repeat", Assertion: "backup-upload-same-id-replaces",
			Sources: []string{"server/managers/BackupManager.js:170-180"},
			Claim:   "Uploading an archive with an id that is already listed replaces that one entry: the list keeps a single entry for the id, its fileSize matches the newer upload and no second file appears.",
			Run: func(t *testing.T, e *harness.Env) {
				const id = "2020-05-03T0000"
				first := uploadArchive(t, id, "sqlite", "2.37.2", 1588464000000,
					zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00first")})
				uploadArchiveExpect(t, e, "replace-one.audiobookshelf", first, id)
				beforeCount := countID(t, e, id)
				second := uploadArchive(t, id, "sqlite", "2.37.3", 1588464000000,
					zipEntry{Name: "absdatabase.sqlite", Data: bytes.Repeat([]byte("SQLite format 3\x00second"), 200)})
				item := uploadArchiveExpect(t, e, "replace-one.audiobookshelf", second, id)
				if got := countID(t, e, id); got != beforeCount || got != 1 {
					t.Fatalf("id %q is listed %d times after re-upload, want 1", id, got)
				}
				if item.ServerVersion == nil || *item.ServerVersion != "2.37.3" {
					t.Fatalf("re-uploaded serverVersion = %v, want 2.37.3", item.ServerVersion)
				}
				if got := fileSize(t, item.FullPath); got != int64(len(second)) || got != item.FileSize {
					t.Fatalf("re-uploaded fileSize = %d, disk = %d, upload = %d", item.FileSize, got, len(second))
				}
				requireStatus(t, deleteBackup(t, e, id), http.StatusOK, "cleanup replaced upload")
			},
		},
		{
			Requirement: "route-r127", Invariant: "INV-ROUTE-R127-ROUTE", Case: "normal", State: "changed", Assertion: "backup-upload-changed-metadata",
			Sources: []string{"server/managers/BackupManager.js:154-180", "server/objects/Backup.js:41-70"},
			Claim:   "Re-uploading an id with different details updates the listed createdAt and serverVersion so a follow-up GET reflects the new archive, not the first upload.",
			Run: func(t *testing.T, e *harness.Env) {
				const id = "2020-05-04T0000"
				first := uploadArchiveExpect(t, e, "changed.audiobookshelf",
					uploadArchive(t, id, "sqlite", "2.30.0", 1588550400000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}), id)
				if first.ServerVersion == nil || *first.ServerVersion != "2.30.0" {
					t.Fatalf("first upload version = %v, want 2.30.0", first.ServerVersion)
				}
				second := uploadArchiveExpect(t, e, "changed.audiobookshelf",
					uploadArchive(t, id, "sqlite", "2.37.2", 1588550500000,
						zipEntry{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")}), id)
				if second.CreatedAt != 1588550500000 || second.ServerVersion == nil || *second.ServerVersion != "2.37.2" {
					t.Fatalf("re-uploaded createdAt/version = %d/%v, want 1588550500000/2.37.2", second.CreatedAt, second.ServerVersion)
				}
				listed, found := entryByID(getBackups(t, e, root(t, e)).Backups, id)
				if !found || listed.CreatedAt != 1588550500000 || listed.ServerVersion == nil || *listed.ServerVersion != "2.37.2" {
					t.Fatalf("GET list after re-upload = %+v, want the second details", listed)
				}
				requireStatus(t, deleteBackup(t, e, id), http.StatusOK, "cleanup changed upload")
			},
		},
	}
}

// countID counts the list entries with one id.
func countID(t *testing.T, e *harness.Env, id string) int {
	t.Helper()
	count := 0
	for _, item := range getBackups(t, e, root(t, e)).Backups {
		if item.ID == id {
			count++
		}
	}
	return count
}
