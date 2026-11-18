package backup

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // read a backup's zipped database snapshot

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// backup is the BackupController projection of one archive (server/objects/Backup.js toJSON).
type backup struct {
	ID            string  `json:"id"`
	Key           *string `json:"key"`
	BackupDirPath string  `json:"backupDirPath"`
	DatePretty    string  `json:"datePretty"`
	FullPath      string  `json:"fullPath"`
	Path          string  `json:"path"`
	Filename      string  `json:"filename"`
	FileSize      int64   `json:"fileSize"`
	CreatedAt     int64   `json:"createdAt"`
	ServerVersion *string `json:"serverVersion"`
}

// backupsResponse is the body of GET /api/backups and of the upload/create/delete routes.
type backupsResponse struct {
	Backups          []backup `json:"backups"`
	BackupLocation   string   `json:"backupLocation"`
	BackupPathEnvSet bool     `json:"backupPathEnvSet"`
}

// serverSettings is the subset of the settings patch response the backup UI reads.
type serverSettings struct {
	BackupPath     string  `json:"backupPath"`
	BackupSchedule any     `json:"backupSchedule"`
	BackupsToKeep  int     `json:"backupsToKeep"`
	MaxBackupSize  float64 `json:"maxBackupSize"`
}

type settingsResponse struct {
	ServerSettings serverSettings `json:"serverSettings"`
}

// zipEntry is one entry of an archive the scenarios build.
type zipEntry struct {
	Name    string
	Data    []byte
	Symlink string // non-empty: write a symlink entry whose target is this string
}

// requireStatus fails unless the response has the wanted status.
func requireStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d (body %.300q)", what, resp.Status, want, resp.Body)
	}
}

// root returns the fixture root token.
func root(t *testing.T, e *harness.Env) string {
	t.Helper()
	return e.Token(t, "root")
}

// getBackups reads the backup list for a token.
func getBackups(t *testing.T, e *harness.Env, token string) backupsResponse {
	t.Helper()
	resp := e.Get(t, token, "/api/backups")
	requireStatus(t, resp, http.StatusOK, "GET /api/backups")
	var body backupsResponse
	resp.JSON(t, &body)
	return body
}

// backupLocation returns the directory the server reports for archives.
func backupLocation(t *testing.T, e *harness.Env) string {
	t.Helper()
	return getBackups(t, e, root(t, e)).BackupLocation
}

// createBackup creates an archive and returns the entry it created. The created
// entry is the one with the newest createdAt: two creates in one minute replace
// the same id and push its timestamp forward.
func createBackup(t *testing.T, e *harness.Env) backup {
	t.Helper()
	started := time.Now().UnixMilli()
	resp := e.Post(t, root(t, e), "/api/backups", nil)
	requireStatus(t, resp, http.StatusOK, "POST /api/backups")
	var body backupsResponse
	resp.JSON(t, &body)
	created, ok := newestBackup(body.Backups)
	if !ok || created.CreatedAt < started-2000 {
		t.Fatalf("POST /api/backups returned no backup created by this request: %+v", body.Backups)
	}
	return created
}

// newestBackup returns the entry with the newest createdAt.
func newestBackup(list []backup) (backup, bool) {
	var newest backup
	found := false
	for _, item := range list {
		if !found || item.CreatedAt > newest.CreatedAt {
			newest, found = item, true
		}
	}
	return newest, found
}

// entryByID finds a listed backup, reporting whether it is present.
func entryByID(list []backup, id string) (backup, bool) {
	for _, item := range list {
		if item.ID == id {
			return item, true
		}
	}
	return backup{}, false
}

// uploadBackup posts one multipart backup upload named name.
func uploadBackup(t *testing.T, e *harness.Env, name string, content []byte) harness.Response {
	t.Helper()
	body, contentType := harness.Multipart(map[string]string{}, harness.File{Field: "file", Name: name, Content: content})
	return e.Do(t, harness.Request{Method: "POST", Path: "/api/backups/upload", Token: root(t, e), Body: body, Header: map[string]string{"Content-Type": contentType}})
}

// deleteBackup deletes one backup id.
func deleteBackup(t *testing.T, e *harness.Env, id string) harness.Response {
	t.Helper()
	return e.Delete(t, root(t, e), "/api/backups/"+id)
}

// applyBackup applies one backup id.
func applyBackup(t *testing.T, e *harness.Env, id string) harness.Response {
	t.Helper()
	return e.Get(t, root(t, e), "/api/backups/"+id+"/apply")
}

// patchSettings writes server settings and returns the resulting settings.
func patchSettings(t *testing.T, e *harness.Env, payload map[string]any) serverSettings {
	t.Helper()
	resp := e.Patch(t, root(t, e), "/api/settings", payload)
	requireStatus(t, resp, http.StatusOK, "PATCH /api/settings")
	var body settingsResponse
	resp.JSON(t, &body)
	return body.ServerSettings
}

// restoreBackupDefaults returns the backup settings to the fixture defaults.
func restoreBackupDefaults(t *testing.T, e *harness.Env) {
	t.Helper()
	patchSettings(t, e, map[string]any{"backupsToKeep": 2, "maxBackupSize": 1, "backupSchedule": false})
}

// scheduleValue returns the cron expression of a settings payload, "" when
// automatic backups are disabled.
func scheduleValue(settings serverSettings) string {
	text, _ := settings.BackupSchedule.(string)
	return text
}

// archiveBytes builds a zip archive from entries in declaration order.
func archiveBytes(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		if entry.Symlink != "" {
			header.SetMode(os.ModeSymlink | 0o777)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", entry.Name, err)
		}
		data := entry.Data
		if entry.Symlink != "" {
			data = []byte(entry.Symlink)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatalf("write zip entry %q: %v", entry.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

// detailsText is the upstream `details` entry: id, key, createdAt, serverVersion
// joined with newlines and no trailing newline (Backup.detailsString).
func detailsText(id, key string, createdAt int64, version string) string {
	return id + "\n" + key + "\n" + itoa(createdAt) + "\n" + version
}

// itoa formats an integer without importing strconv at every call site.
func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// uploadArchive builds an uploadable archive with the given details entry plus
// optional additional entries.
func uploadArchive(t *testing.T, id, key, version string, createdAt int64, extra ...zipEntry) []byte {
	t.Helper()
	entries := []zipEntry{{Name: "details", Data: []byte(detailsText(id, key, createdAt, version))}}
	entries = append(entries, extra...)
	return archiveBytes(t, entries)
}

// databaseBytes reads the running install's database file, the bytes a backup
// snapshot carries.
func databaseBytes(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.Config, "absdatabase.sqlite"))
	if err != nil {
		t.Fatalf("read install database: %v", err)
	}
	return raw
}

// applyArchive builds an archive that carries the running database plus extra
// metadata entries, so applying it is valid and observable without a version
// change.
func applyArchive(t *testing.T, e *harness.Env, id string, extra ...zipEntry) []byte {
	t.Helper()
	entries := []zipEntry{
		{Name: "absdatabase.sqlite", Data: databaseBytes(t, e)},
		{Name: "details", Data: []byte(detailsText(id, "sqlite", time.Now().UnixMilli(), "2.37.2"))},
	}
	entries = append(entries, extra...)
	return archiveBytes(t, entries)
}

// zipNames lists an archive's entry names in archive order.
func zipNames(t *testing.T, path string) []string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer reader.Close() // read-only archive
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	return names
}

// zipEntryData returns one entry's bytes.
func zipEntryData(t *testing.T, path, name string) []byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer reader.Close() // read-only archive
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatalf("open entry %q: %v", name, err)
		}
		data, err := io.ReadAll(io.LimitReader(stream, 64<<20))
		closeErr := stream.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read entry %q: %v / %v", name, err, closeErr)
		}
		return data
	}
	t.Fatalf("archive %s has no entry %q (entries %v)", path, name, zipNames(t, path))
	return nil
}

// archiveDatabase extracts absdatabase.sqlite to a temporary file and opens it
// read-only: the zipped database must be a readable SQLite snapshot.
func archiveDatabase(t *testing.T, archivePath string) *sql.DB {
	t.Helper()
	data := zipEntryData(t, archivePath, "absdatabase.sqlite")
	target := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	handle, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatalf("open snapshot database: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("close snapshot database: %v", err)
		}
	})
	if err := handle.Ping(); err != nil {
		t.Fatalf("ping snapshot database: %v", err)
	}
	return handle
}

// snapshotString reads one string column from an archive's database.
func snapshotString(t *testing.T, handle *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := handle.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

// snapshotInt reads one integer column from an archive's database.
func snapshotInt(t *testing.T, handle *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := handle.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

// dirNames lists a directory's entry names.
func dirNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %s: %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// archiveFiles lists the .audiobookshelf files in a directory.
func archiveFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	for _, name := range dirNames(t, dir) {
		if filepath.Ext(name) == ".audiobookshelf" {
			files = append(files, name)
		}
	}
	return files
}

// fileSize returns a file's size.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// fileExists reports whether a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasPrefixAny reports whether value starts with any prefix.
func hasPrefixAny(value string, prefixes ...string) bool {
	return slices.ContainsFunc(prefixes, func(prefix string) bool {
		return len(value) >= len(prefix) && value[:len(prefix)] == prefix
	})
}

// sameNames compares two name lists after sorting them.
func sameNames(a, b []string) bool {
	left := slices.Clone(a)
	right := slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// library is the part of a GET /api/libraries/:id body these scenarios read.
type library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// libraryName reads a library's name through the API.
func libraryName(t *testing.T, e *harness.Env, id string) string {
	t.Helper()
	resp := e.Get(t, root(t, e), "/api/libraries/"+id)
	requireStatus(t, resp, http.StatusOK, "GET /api/libraries/"+id)
	var body library
	resp.JSON(t, &body)
	return body.Name
}

// renameLibrary renames a library through the API.
func renameLibrary(t *testing.T, e *harness.Env, id, name string) {
	t.Helper()
	resp := e.Patch(t, root(t, e), "/api/libraries/"+id, map[string]any{"name": name})
	requireStatus(t, resp, http.StatusOK, "PATCH library name")
}

// requireInstallUsable asserts the running install still answers for the root
// user and holds the fixture library name.
func requireInstallUsable(t *testing.T, e *harness.Env, expectLibrary string) {
	t.Helper()
	requireStatus(t, e.Get(t, root(t, e), "/api/me"), http.StatusOK, "GET /api/me after rejected request")
	if got := libraryName(t, e, e.IDs["library"]); got != expectLibrary {
		t.Fatalf("library name = %q, want %q: the rejected request changed the install", got, expectLibrary)
	}
}

// trimLines splits details text into its lines.
func trimLines(text string) []string {
	return strings.Split(text, "\n")
}
