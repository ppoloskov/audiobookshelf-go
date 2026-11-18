package server

import (
	"archive/zip"
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

// library is the part of a GET /api/libraries entry the clients read.
type library struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Folders   []struct {
		ID        string `json:"id"`
		FullPath  string `json:"fullPath"`
		LibraryID string `json:"libraryId"`
	} `json:"folders"`
}

type librariesResponse struct {
	Libraries []library `json:"libraries"`
}

// backup is the BackupsController projection of a backup file.
type backup struct {
	ID            string `json:"id"`
	Key           string `json:"key"`
	BackupDirPath string `json:"backupDirPath"`
	DatePretty    string `json:"datePretty"`
	FullPath      string `json:"fullPath"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	FileSize      int64  `json:"fileSize"`
	CreatedAt     int64  `json:"createdAt"`
	ServerVersion string `json:"serverVersion"`
}

type backupsResponse struct {
	Backups          []backup `json:"backups"`
	BackupLocation   string   `json:"backupLocation"`
	BackupPathEnvSet bool     `json:"backupPathEnvSet"`
}

// requireStatus fails unless the response has the wanted status.
func requireStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d (body %.200q)", what, resp.Status, want, resp.Body)
	}
}

// getLibraries reads the library list for a token.
func getLibraries(t *testing.T, e *harness.Env, token string) librariesResponse {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries")
	requireStatus(t, resp, http.StatusOK, "GET /api/libraries")
	var body librariesResponse
	resp.JSON(t, &body)
	return body
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

// libraryName reads one library through the API.
func libraryName(t *testing.T, e *harness.Env, token, id string) string {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+id)
	requireStatus(t, resp, http.StatusOK, "GET /api/libraries/"+id)
	var body library
	resp.JSON(t, &body)
	return body.Name
}

// nameOf returns the name of the library with that id.
func nameOf(libraries []library, id string) string {
	for _, item := range libraries {
		if item.ID == id {
			return item.Name
		}
	}
	return ""
}

// patchLibraryName renames a library through the API.
func patchLibraryName(t *testing.T, e *harness.Env, id, name string) {
	t.Helper()
	resp := e.Do(t, harness.Request{Method: "PATCH", Path: "/api/libraries/" + id, Token: root(t, e), JSON: map[string]any{"name": name}})
	requireStatus(t, resp, http.StatusOK, "PATCH library name")
}

// ensureBackup returns the packet's shared backup, creating it when the flow
// scenarios have not done so yet.
func (s *suite) ensureBackup(t *testing.T, e *harness.Env) backup {
	t.Helper()
	if s.backupID != "" {
		for _, item := range getBackups(t, e, root(t, e)).Backups {
			if item.ID == s.backupID {
				return item
			}
		}
	}
	resp := e.Post(t, root(t, e), "/api/backups", nil)
	requireStatus(t, resp, http.StatusOK, "POST /api/backups")
	var created backupsResponse
	resp.JSON(t, &created)
	if len(created.Backups) == 0 {
		t.Fatal("POST /api/backups returned no backup")
	}
	s.backupID = created.Backups[len(created.Backups)-1].ID
	return created.Backups[len(created.Backups)-1]
}

// applyBackup applies a backup id.
func applyBackup(t *testing.T, e *harness.Env, id string) harness.Response {
	t.Helper()
	return e.Get(t, root(t, e), "/api/backups/"+id+"/apply")
}

// countEvents returns the events named name received since mark.
func countEvents(s *harness.Socket, from int, name string) []harness.Event {
	var found []harness.Event
	for _, event := range s.Events()[from:] {
		if event.Name == name {
			found = append(found, event)
		}
	}
	return found
}

// backupAppliedArgs asserts that one backup_applied event arrived and returns
// its decoded arguments.
func backupAppliedArgs(t *testing.T, s *harness.Socket, from int, what string) []json.RawMessage {
	t.Helper()
	event := s.Wait(t, from, "backup_applied", 10*time.Second, nil)
	if got := len(countEvents(s, from, "backup_applied")); got != 1 {
		t.Fatalf("%s: %d backup_applied events, want exactly 1", what, got)
	}
	return event.Args
}

// requireNullArgument pins the upstream emit('backup_applied') argument list:
// Socket.IO serializes the omitted data as exactly one null argument.
func requireNullArgument(t *testing.T, args []json.RawMessage, what string) {
	t.Helper()
	if len(args) != 1 {
		t.Fatalf("%s: backup_applied carried %d arguments, want exactly one: %s", what, len(args), args)
	}
	if string(args[0]) != "null" {
		t.Fatalf("%s: backup_applied argument = %s, want null", what, args[0])
	}
	var decoded any
	if err := json.Unmarshal(args[0], &decoded); err != nil {
		t.Fatalf("%s: decode backup_applied argument: %v", what, err)
	}
	if decoded != nil {
		t.Fatalf("%s: backup_applied argument decoded as %#v, want nil", what, decoded)
	}
}

// zipWith builds a .audiobookshelf archive with the given entries.
func zipWith(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

// uploadBackup posts one multipart backup upload.
func uploadBackup(t *testing.T, e *harness.Env, name string, content []byte) harness.Response {
	t.Helper()
	body, contentType := harness.Multipart(map[string]string{}, harness.File{Field: "file", Name: name, Content: content})
	return e.Do(t, harness.Request{Method: "POST", Path: "/api/backups/upload", Token: root(t, e), Body: body, Header: map[string]string{"Content-Type": contentType}})
}

// logPayload is the LogObject upstream emits on the log socket event.
type logPayload struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	LevelName string `json:"levelName"`
	Level     int    `json:"level"`
}

// logEvent decodes one log event argument list.
func logEvent(t *testing.T, event harness.Event) logPayload {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("log event carried %d arguments, want one: %s", len(event.Args), event.Args)
	}
	var payload logPayload
	if err := json.Unmarshal(event.Args[0], &payload); err != nil {
		t.Fatalf("decode log payload: %v", err)
	}
	return payload
}

// waitLog waits for a log event at the given level whose message contains the
// marker, and asserts its payload shape.
func waitLog(t *testing.T, s *harness.Socket, from, level int, marker string) logPayload {
	t.Helper()
	event := s.Wait(t, from, "log", 10*time.Second, func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var payload logPayload
		if json.Unmarshal(args[0], &payload) != nil {
			return false
		}
		return payload.Level == level && strings.Contains(payload.Message, marker)
	})
	payload := logEvent(t, event)
	if payload.Timestamp == "" || payload.Source == "" || payload.Message == "" || payload.LevelName == "" {
		t.Fatalf("log payload has an empty field: %+v", payload)
	}
	return payload
}

// fileSize returns the size of a file on disk.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// dirNames lists a directory.
func dirNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %s: %v", path, err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// backupDir joins the install's backup directory.
func backupDir(e *harness.Env) string {
	return filepath.Join(e.Metadata, "backups")
}
