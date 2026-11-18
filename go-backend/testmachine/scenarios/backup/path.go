package backup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// pathScenarios characterize PATCH /api/backups/path.
func pathScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "normal", State: "initial", Assertion: "backup-path-update-creates-directory",
			Sources: []string{"server/controllers/BackupController.js:88-124", "server/managers/BackupManager.js:68-73"},
			Claim:   "PATCH /api/backups/path with a new absolute path answers 200, creates that directory, persists it in server settings, reports it as backupLocation and writes later archives into it.",
			Run: func(t *testing.T, e *harness.Env) {
				original := backupLocation(t, e)
				newDir := filepath.Join(e.Install, "custom-backups")
				resp := e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": newDir})
				requireStatus(t, resp, http.StatusOK, "PATCH backup path")
				if got := backupLocation(t, e); got != newDir {
					t.Fatalf("backupLocation = %q, want %q", got, newDir)
				}
				if !fileExists(newDir) {
					t.Fatalf("new backup directory %q was not created", newDir)
				}
				created := createBackup(t, e)
				if filepath.Dir(created.FullPath) != newDir {
					t.Fatalf("archive written to %q, want the new path %q", created.FullPath, newDir)
				}
				if !storedBackupPath(t, e, newDir) {
					t.Fatalf("server settings row does not persist backupPath %q", newDir)
				}
				requireStatus(t, deleteBackup(t, e, created.ID), http.StatusOK, "cleanup archive in new path")
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": original}), http.StatusOK, "restore backup path")
				if got := backupLocation(t, e); got != original {
					t.Fatalf("backupLocation after restore = %q, want %q", got, original)
				}
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-REPEAT", Case: "edge", State: "repeat", Assertion: "backup-path-unchanged",
			Sources: []string{"server/controllers/BackupController.js:94-99"},
			Claim:   "PATCH /api/backups/path with the current path answers 200 without changing backupLocation, the archive list or the settings row.",
			Run: func(t *testing.T, e *harness.Env) {
				current := backupLocation(t, e)
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": current}), http.StatusOK, "PATCH unchanged backup path")
				if got := backupLocation(t, e); got != current {
					t.Fatalf("backupLocation = %q, want unchanged %q", got, current)
				}
				after := getBackups(t, e, root(t, e)).Backups
				if len(after) != len(before) || !storedBackupPath(t, e, current) {
					t.Fatalf("unchanged-path PATCH altered the list %d->%d or the settings row", len(before), len(after))
				}
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "normal", State: "changed", Assertion: "backup-path-reloads-archives",
			Sources: []string{"server/controllers/BackupController.js:118-123", "server/managers/BackupManager.js:68-73", "server/managers/BackupManager.js:269-331"},
			Claim:   "Changing the path makes the server reload archives from the new directory: an archive copied there is listed (while the old directory's file stays), a new archive is written there, and switching back lists the original directory again.",
			Run: func(t *testing.T, e *harness.Env) {
				original := backupLocation(t, e)
				archive := createBackup(t, e)
				newDir := filepath.Join(e.Install, "reload-backups")
				if err := os.MkdirAll(newDir, 0o755); err != nil {
					t.Fatalf("create reload directory: %v", err)
				}
				copied := filepath.Join(newDir, archive.Filename)
				if err := os.WriteFile(copied, mustReadFile(t, archive.FullPath), 0o600); err != nil {
					t.Fatalf("copy archive into new path: %v", err)
				}
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": newDir}), http.StatusOK, "switch to reload path")
				if got := backupLocation(t, e); got != newDir {
					t.Fatalf("backupLocation = %q, want %q", got, newDir)
				}
				listed, found := entryByID(getBackups(t, e, root(t, e)).Backups, archive.ID)
				if !found || listed.FullPath != copied || !fileExists(listed.FullPath) {
					t.Fatalf("reloaded list = %+v, want the archive copied into %q", listed, newDir)
				}
				if !fileExists(archive.FullPath) {
					t.Fatalf("changing the path removed the archive in the old directory %q", archive.FullPath)
				}
				created := createBackup(t, e)
				if filepath.Dir(created.FullPath) != newDir {
					t.Fatalf("archive after path change written to %q, want %q", created.FullPath, newDir)
				}
				requireStatus(t, deleteBackup(t, e, created.ID), http.StatusOK, "cleanup archive in reload path")
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": original}), http.StatusOK, "restore original path")
				if _, found := entryByID(getBackups(t, e, root(t, e)).Backups, archive.ID); !found {
					t.Fatalf("original archive %q is not listed after switching back", archive.ID)
				}
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "edge", State: "changed", Assertion: "backup-path-load-skips-invalid-archives",
			Sources: []string{"server/managers/BackupManager.js:269-331", "server/controllers/BackupController.js:118-123"},
			Claim:   "Reloading a directory skips archives without details, with details over 1 MiB or that are not zips, lists archives without serverVersion or key (serverVersion null, key empty), and ignores a second file with an already-loaded id.",
			Run: func(t *testing.T, e *harness.Env) {
				original := backupLocation(t, e)
				archive := createBackup(t, e)
				dir := filepath.Join(e.Install, "loader-backups")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("create loader directory: %v", err)
				}
				archiveRaw := mustReadFile(t, archive.FullPath)
				files := map[string][]byte{
					archive.Filename:           archiveRaw,
					"duplicate.audiobookshelf": archiveRaw,
					"no-details.audiobookshelf": archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
					}),
					"big-details.audiobookshelf": archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: bytes.Repeat([]byte("x"), 1<<20+1)},
					}),
					"corrupt.audiobookshelf": []byte("not a zip"),
					"no-version.audiobookshelf": archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: []byte("2019-01-01T0000\nsqlite\n1546300800000")},
					}),
					"empty-key.audiobookshelf": archiveBytes(t, []zipEntry{
						{Name: "absdatabase.sqlite", Data: []byte("SQLite format 3\x00")},
						{Name: "details", Data: []byte("2019-01-02T0000\n\n1546387200000\n2.37.2")},
					}),
				}
				for name, content := range files {
					if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
						t.Fatalf("write loader archive %s: %v", name, err)
					}
				}
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": dir}), http.StatusOK, "switch to loader path")
				listed := getBackups(t, e, root(t, e)).Backups
				if len(listed) != 3 {
					t.Fatalf("loaded backups = %+v, want the valid, no-version and empty-key archives", listed)
				}
				matches := 0
				for _, item := range listed {
					if item.ID == archive.ID {
						matches++
					}
				}
				if matches != 1 {
					t.Fatalf("archive id %q is listed %d times, want once despite the duplicate file", archive.ID, matches)
				}
				noVersion, found := entryByID(listed, "2019-01-01T0000")
				if !found || noVersion.ServerVersion != nil {
					t.Fatalf("no-version archive = %+v, want serverVersion null", noVersion)
				}
				emptyKey, found := entryByID(listed, "2019-01-02T0000")
				if !found || emptyKey.Key == nil || *emptyKey.Key != "" {
					t.Fatalf("empty-key archive = %+v, want key \"\"", emptyKey)
				}
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": original}), http.StatusOK, "restore original path")
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "normal", State: "changed", Assertion: "backup-path-env-override",
			Sources: []string{"server/controllers/BackupController.js:101-124", "server/objects/settings/ServerSettings.js:219-222", "server/managers/BackupManager.js:34-36"},
			Claim:   "With BACKUP_PATH set the server warns but accepts a PATCH to another directory (200, backupLocation and the settings row updated), and after a restart the environment path wins again as backupLocation.",
			Run: func(t *testing.T, _ *harness.Env) {
				envDir := filepath.Join(t.TempDir(), "env-backups")
				e := harness.Start(t, harness.Options{Env: map[string]string{"BACKUP_PATH": envDir}})
				if got := backupLocation(t, e); got != envDir {
					t.Fatalf("backupLocation = %q, want the environment path %q", got, envDir)
				}
				newDir := filepath.Join(e.Install, "override-backups")
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": newDir}), http.StatusOK, "PATCH path with BACKUP_PATH set")
				if got := backupLocation(t, e); got != newDir {
					t.Fatalf("backupLocation after PATCH = %q, want %q", got, newDir)
				}
				if !storedBackupPath(t, e, newDir) {
					t.Fatalf("settings row does not persist the patched path %q", newDir)
				}
				e.Restart(t)
				if got := backupLocation(t, e); got != envDir {
					t.Fatalf("backupLocation after restart = %q, want the environment path %q", got, envDir)
				}
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "error", State: "failure", Assertion: "backup-path-rejected-validation",
			Sources: []string{"server/controllers/BackupController.js:90-94", "server/controllers/BackupController.js:165-179"},
			Claim:   "A missing, empty, whitespace-only, null or non-string path answers 400, an ordinary user 403 and an anonymous caller 401; none changes backupLocation, the archive list or the persisted backupPath.",
			Run: func(t *testing.T, e *harness.Env) {
				current := backupLocation(t, e)
				before := getBackups(t, e, root(t, e)).Backups
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), "/api/backups/path", map[string]any{"path": filepath.Join(e.Install, "forbidden")}), http.StatusForbidden, "ordinary PATCH path")
				requireStatus(t, e.Patch(t, "", "/api/backups/path", map[string]any{"path": filepath.Join(e.Install, "anonymous")}), http.StatusUnauthorized, "anonymous PATCH path")
				for _, payload := range []map[string]any{
					{},
					{"path": ""},
					{"path": "   "},
					{"path": nil},
					{"path": 5},
				} {
					requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", payload), http.StatusBadRequest, "invalid backup path "+formatPayload(payload))
				}
				if got := backupLocation(t, e); got != current {
					t.Fatalf("backupLocation = %q after rejected updates, want %q", got, current)
				}
				after := getBackups(t, e, root(t, e)).Backups
				if len(after) != len(before) || !storedBackupPath(t, e, current) {
					t.Fatalf("rejected path updates changed the list %d->%d or the settings row", len(before), len(after))
				}
				for _, name := range []string{"forbidden", "anonymous"} {
					if fileExists(filepath.Join(e.Install, name)) {
						t.Fatalf("rejected PATCH created directory %q", name)
					}
				}
				requireInstallUsable(t, e, "Audiobooks")
			},
		},
		{
			Requirement: "route-r128", Invariant: "INV-ROUTE-R128-ROUTE", Case: "edge", State: "failure", Assertion: "backup-path-unwritable",
			Sources: []string{"server/controllers/BackupController.js:108-117"},
			Claim:   "A path that cannot be created (a component is an existing regular file) answers 400 and leaves backupLocation, the settings row and the filesystem unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				current := backupLocation(t, e)
				bad := filepath.Join(e.Config, "absdatabase.sqlite", "child")
				requireStatus(t, e.Patch(t, root(t, e), "/api/backups/path", map[string]any{"path": bad}), http.StatusBadRequest, "PATCH unwritable backup path")
				if got := backupLocation(t, e); got != current {
					t.Fatalf("backupLocation = %q after rejected update, want %q", got, current)
				}
				if fileExists(bad) {
					t.Fatalf("rejected path update created %q", bad)
				}
				if !storedBackupPath(t, e, current) {
					t.Fatalf("rejected path update changed the persisted backupPath")
				}
			},
		},
	}
}

// storedBackupPath reports whether the settings row persists the given path.
func storedBackupPath(t *testing.T, e *harness.Env, path string) bool {
	t.Helper()
	rows := e.Rows(t, "SELECT value FROM settings WHERE key = ?", "server-settings")
	if len(rows) != 1 {
		t.Fatalf("settings rows = %d, want one", len(rows))
	}
	raw, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatalf("settings value has type %T, want string", rows[0]["value"])
	}
	return strings.Contains(raw, `"backupPath":`+jsonQuote(path))
}

// jsonQuote quotes a string as JSON so a substring check cannot match a prefix.
func jsonQuote(value string) string {
	raw, _ := json.Marshal(value) // a string always marshals
	return string(raw)
}

// formatPayload describes a settings payload for a failure message.
func formatPayload(payload map[string]any) string {
	raw, _ := json.Marshal(payload) // payload comes from the scenario table
	return string(raw)
}

// mustReadFile reads a file the scenario just wrote.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
