package backup

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// downloadScenarios characterize GET /api/backups/:id/download.
func downloadScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r125", Invariant: "INV-ROUTE-R125-ROUTE", Case: "normal", State: "stateless", Assertion: "backup-download-archive-bytes",
			Sources: []string{"server/controllers/BackupController.js:133-146", "client/components/tables/BackupsTable.vue:88-90"},
			Claim:   "GET /api/backups/:id/download answers 200 with an attachment named <id>.audiobookshelf and a body byte-identical to the archive on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				resp := e.Get(t, root(t, e), "/api/backups/"+created.ID+"/download")
				requireStatus(t, resp, http.StatusOK, "download backup")
				onDisk, err := os.ReadFile(created.FullPath)
				if err != nil {
					t.Fatalf("read archive: %v", err)
				}
				if !bytes.Equal(resp.Body, onDisk) {
					t.Fatalf("download body is %d bytes, archive %d bytes, want identical", len(resp.Body), len(onDisk))
				}
				if got, want := resp.Header.Get("Content-Disposition"), "attachment; filename="+created.Filename; got != want {
					t.Fatalf("Content-Disposition = %q, want %q", got, want)
				}
				if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
					t.Fatalf("Content-Type = %q, want application/octet-stream", got)
				}
			},
		},
		{
			Requirement: "route-r125", Invariant: "INV-ROUTE-R125-ROUTE", Case: "edge", State: "stateless", Assertion: "backup-download-token-query",
			Sources: []string{"server/controllers/BackupController.js:133-146", "server/Auth.js:120-131", "client/components/tables/BackupsTable.vue:88-90"},
			Claim:   "The download route accepts the client's ?token= query credential without an Authorization header and serves the same archive bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				resp := e.Do(t, harness.Request{Path: "/api/backups/" + created.ID + "/download", Query: url.Values{"token": {root(t, e)}}})
				requireStatus(t, resp, http.StatusOK, "download with token query")
				if int64(len(resp.Body)) != created.FileSize {
					t.Fatalf("token download is %d bytes, want %d", len(resp.Body), created.FileSize)
				}
				requireStatus(t, e.Get(t, "", "/api/backups/"+created.ID+"/download"), http.StatusUnauthorized, "download without token")
			},
		},
		{
			Requirement: "route-r125", Invariant: "INV-ROUTE-R125-ROUTE", Case: "edge", State: "stateless", Assertion: "backup-download-x-accel-redirect",
			Sources: []string{"server/controllers/BackupController.js:133-141", "server/Server.js:86"},
			Claim:   "With USE_X_ACCEL set the download route answers 204 with X-Accel-Redirect holding the prefix plus the archive full path and no body, delegating the bytes to the proxy.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{Env: map[string]string{"USE_X_ACCEL": "/protected"}})
				created := createBackup(t, e)
				resp := e.Get(t, root(t, e), "/api/backups/"+created.ID+"/download")
				requireStatus(t, resp, http.StatusNoContent, "X-Accel download")
				if got, want := resp.Header.Get("X-Accel-Redirect"), "/protected"+created.FullPath; got != want {
					t.Fatalf("X-Accel-Redirect = %q, want %q", got, want)
				}
				if len(resp.Body) != 0 {
					t.Fatalf("X-Accel download carried %d body bytes, want none", len(resp.Body))
				}
			},
		},
		{
			Requirement: "route-r125", Invariant: "INV-ROUTE-R125-ROUTE", Case: "error", State: "stateless", Assertion: "backup-download-forbidden",
			Sources: []string{"server/controllers/BackupController.js:133-146", "server/controllers/BackupController.js:165-179"},
			Claim:   "An ordinary user gets 403 and an anonymous caller 401 for a download, and an unknown id gets 404; none returns archive bytes or changes the install.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/backups/"+created.ID+"/download"), http.StatusForbidden, "ordinary download")
				requireStatus(t, e.Get(t, "", "/api/backups/"+created.ID+"/download"), http.StatusUnauthorized, "anonymous download")
				requireStatus(t, e.Get(t, root(t, e), "/api/backups/not-a-backup/download"), http.StatusNotFound, "unknown download")
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
	}
}

// applyScenarios characterize GET /api/backups/:id/apply up to the runtime
// swap: the positive request, the access rules and the pre-swap validation that
// must leave the running install untouched.
func applyScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r126", Invariant: "INV-ROUTE-R126-ROUTE", Case: "normal", State: "stateless", Assertion: "backup-apply-existing-archive",
			Sources: []string{"server/controllers/BackupController.js:155-157", "server/managers/BackupManager.js:200-267"},
			Claim:   "Applying a listed archive answers 200, keeps it listed, and leaves the server answering /api/me with the fixture library readable.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				requireStatus(t, applyBackup(t, e, created.ID), http.StatusOK, "apply backup")
				requireStatus(t, e.Get(t, root(t, e), "/api/me"), http.StatusOK, "GET /api/me after apply")
				if got := libraryName(t, e, e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("library name after apply = %q, want Audiobooks", got)
				}
				if _, found := entryByID(getBackups(t, e, root(t, e)).Backups, created.ID); !found {
					t.Fatalf("applied backup %q is no longer listed", created.ID)
				}
			},
		},
		{
			Requirement: "route-r126", Invariant: "INV-ROUTE-R126-ROUTE", Case: "error", State: "stateless", Assertion: "backup-apply-access-rules",
			Sources: []string{"server/controllers/BackupController.js:165-179", "server/routers/ApiRouter.js:204"},
			Claim:   "An ordinary user's apply gets 403, an anonymous one 401 and an unknown id 404, with the backup list, archive files and install unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createBackup(t, e)
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/backups/"+created.ID+"/apply"), http.StatusForbidden, "ordinary apply")
				requireStatus(t, e.Get(t, "", "/api/backups/"+created.ID+"/apply"), http.StatusUnauthorized, "anonymous apply")
				requireStatus(t, applyBackup(t, e, "0000-00-00T0000"), http.StatusNotFound, "apply unknown id")
				after := getBackups(t, e, root(t, e)).Backups
				if len(after) != len(before) {
					t.Fatalf("rejected applies changed the backup count from %d to %d", len(before), len(after))
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r126", Invariant: "INV-ROUTE-R126-VALIDATION-001", Case: "error", State: "stateless", Assertion: "backup-apply-archive-without-database",
			Sources: []string{"server/managers/BackupManager.js:200-229", "server/managers/BackupManager.js:103-138"},
			Claim:   "An archive that no longer holds absdatabase.sqlite is rejected with 500 before the database is replaced, leaving rows, the backup list and a usable install unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				uploaded := uploadSynthetic(t, e, "2020-03-01T0000", 1583020800000)
				tampered := archiveBytes(t, []zipEntry{{Name: "details", Data: []byte(detailsText(uploaded.ID, "sqlite", uploaded.CreatedAt, "2.37.2"))}})
				if err := os.WriteFile(uploaded.FullPath, tampered, 0o600); err != nil {
					t.Fatalf("replace archive on disk: %v", err)
				}
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, applyBackup(t, e, uploaded.ID), http.StatusInternalServerError, "apply archive without database")
				if after := getBackups(t, e, root(t, e)).Backups; len(after) != len(before) {
					t.Fatalf("rejected apply changed the backup count from %d to %d", len(before), len(after))
				}
				if fileExists(filepath.Join(e.Config, "absdatabase-temp.sqlite")) {
					t.Fatal("rejected apply left absdatabase-temp.sqlite behind")
				}
				requireInstallUsable(t, e, "Audiobooks")
				requireStatus(t, deleteBackup(t, e, uploaded.ID), http.StatusOK, "cleanup tampered archive")
			},
		},
		{
			Requirement: "route-r126", Invariant: "INV-ROUTE-R126-VALIDATION-001", Case: "edge", State: "stateless", Assertion: "backup-apply-extracts-metadata-entries",
			Sources: []string{"server/managers/BackupManager.js:216-255", "server/managers/BackupManager.js:247-252"},
			Claim:   "Applying a valid archive extracts its metadata-items entries under the install's metadata/items directory: the marker file it carries appears with its content after the request answers 200.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				const id = "2021-05-01T1200"
				content := applyArchive(t, e, id, zipEntry{Name: "metadata-items/apply-probe/marker.txt", Data: []byte("extracted\n")})
				uploaded := uploadArchiveExpect(t, e, "apply-probe.audiobookshelf", content, id)
				requireStatus(t, applyBackup(t, e, uploaded.ID), http.StatusOK, "apply archive with metadata entry")
				onDisk := filepath.Join(e.Metadata, "items", "apply-probe", "marker.txt")
				data, err := os.ReadFile(onDisk)
				if err != nil {
					t.Fatalf("read extracted metadata marker: %v", err)
				}
				if string(data) != "extracted\n" {
					t.Fatalf("extracted marker = %q, want %q", data, "extracted\n")
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r126", Invariant: "INV-ROUTE-R126-VALIDATION-001", Case: "edge", State: "stateless", Assertion: "backup-apply-symlink-entry-not-followed",
			Sources: []string{"server/managers/BackupManager.js:247-255", "server/libs/nodeStreamZip/index.js:493-558"},
			Claim:   "A symlink entry inside an archive's metadata tree never becomes a symlink in the install: the apply either rejects it or writes a regular file, and no symlink appears under metadata/items.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				const id = "2021-05-02T1200"
				content := applyArchive(t, e, id, zipEntry{Name: "metadata-items/symlink-probe/probe.txt", Symlink: "/etc/passwd"})
				uploaded := uploadArchiveExpect(t, e, "symlink-probe.audiobookshelf", content, id)
				resp := applyBackup(t, e, uploaded.ID)
				if resp.Status >= 400 {
					if fileExists(filepath.Join(e.Metadata, "items", "symlink-probe", "probe.txt")) {
						t.Fatalf("rejected symlink apply still extracted its probe file")
					}
					requireInstallUsable(t, e, "Audiobooks")
					return
				}
				requireStatus(t, resp, http.StatusOK, "apply symlink-entry archive")
				probe := filepath.Join(e.Metadata, "items", "symlink-probe", "probe.txt")
				info, err := os.Lstat(probe)
				if err != nil {
					t.Fatalf("stat extracted symlink entry: %v", err)
				}
				if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					t.Fatalf("extracted entry mode = %v, want a regular file, not a symlink", info.Mode())
				}
				if data, err := os.ReadFile(probe); err != nil || string(data) != "/etc/passwd" {
					t.Fatalf("extracted entry = %q/%v, want the link target stored as file content", data, err)
				}
				roots := []string{filepath.Join(e.Metadata, "items"), filepath.Join(e.Metadata, "authors")}
				for _, root := range roots {
					if !fileExists(root) {
						continue
					}
					err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
						if walkErr != nil {
							return walkErr
						}
						if entry.Type()&os.ModeSymlink != 0 {
							t.Fatalf("apply created symlink %q", path)
						}
						return nil
					})
					if err != nil {
						t.Fatalf("walk %s: %v", root, err)
					}
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
	}
}
