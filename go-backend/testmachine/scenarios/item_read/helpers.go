package item_read

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// maxFixtureFileBytes bounds the files the scenarios read into memory.
const maxFixtureFileBytes = 8 << 20

// wantStatus fails the test unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d: %.200s", what, resp.Status, want, resp.Body)
	}
}

// get performs a GET with extra request headers.
func get(t *testing.T, e *harness.Env, token, path string, header map[string]string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: path, Token: token, Header: header})
}

// readDisk reads a small file of the install.
func readDisk(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() > maxFixtureFileBytes {
		t.Fatalf("%s is larger than %d bytes", path, maxFixtureFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// contentType returns the media type (without parameters) of the response.
func contentType(t *testing.T, resp harness.Response) string {
	t.Helper()
	value := resp.Header.Get("Content-Type")
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", value, err)
	}
	return media
}

// attachment returns the disposition type and filename of the response.
func attachment(t *testing.T, resp harness.Response) (kind, filename string) {
	t.Helper()
	value := resp.Header.Get("Content-Disposition")
	kind, params, err := mime.ParseMediaType(value)
	if err != nil {
		t.Fatalf("Content-Disposition %q: %v", value, err)
	}
	return kind, params["filename"]
}

// fileInfo is one stored library file.
type fileInfo struct {
	Ino, Name, Path string
	Size            int64
}

// filesOfItem lists the stored library files of an item in order.
func filesOfItem(t *testing.T, e *harness.Env, itemID string) []fileInfo {
	t.Helper()
	var out []fileInfo
	for _, raw := range dbList(t, itemRow(t, e, itemID)["libraryFiles"]) {
		file, _ := raw.(map[string]any)
		metadata, _ := file["metadata"].(map[string]any)
		path, _ := metadata["path"].(string)
		name, _ := metadata["filename"].(string)
		ino, _ := file["ino"].(string)
		size, _ := metadata["size"].(float64)
		out = append(out, fileInfo{Ino: ino, Name: name, Path: path, Size: int64(size)})
	}
	return out
}

// fileNamed returns the stored library file with the given file name.
func fileNamed(t *testing.T, e *harness.Env, itemID, name string) fileInfo {
	t.Helper()
	for _, file := range filesOfItem(t, e, itemID) {
		if file.Name == name {
			return file
		}
	}
	t.Fatalf("item %s has no library file named %q", itemID, name)
	return fileInfo{}
}

// itemPath returns the item's folder (or file) path from the database.
func itemPath(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	path, _ := itemRow(t, e, itemID)["path"].(string)
	return path
}

// wantFileResponse checks a whole-file response: status 200, the media type and the exact file bytes.
func wantFileResponse(t *testing.T, resp harness.Response, file fileInfo, mediaType string) {
	t.Helper()
	wantStatus(t, "serve "+file.Name, resp, http.StatusOK)
	if got := contentType(t, resp); got != mediaType {
		t.Fatalf("%s: Content-Type %q, want %q", file.Name, got, mediaType)
	}
	if !bytes.Equal(resp.Body, readDisk(t, file.Path)) {
		t.Fatalf("%s: body (%d bytes) differs from the file on disk (%d bytes)", file.Name, len(resp.Body), file.Size)
	}
}

// zipEntries reads a zip archive into entry name to content; directory entries are skipped.
func zipEntries(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response is not a readable zip (%d bytes): %v", len(body), err)
	}
	out := map[string][]byte{}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", entry.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(file, maxFixtureFileBytes))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read zip entry %s: %v %v", entry.Name, err, closeErr)
		}
		out[entry.Name] = data
	}
	return out
}

// directoryFiles reads every regular file under dir into slash-separated relative name to content.
func directoryFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = readDisk(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// wantZipOfDirectory checks that body is a zip whose entries are exactly the files under dir, unchanged.
func wantZipOfDirectory(t *testing.T, body []byte, dir string) {
	t.Helper()
	got, want := zipEntries(t, body), directoryFiles(t, dir)
	names := func(files map[string][]byte) []string {
		out := make([]string, 0, len(files))
		for name := range files {
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(names(got), names(want)) {
		t.Fatalf("zip entries %v, want the files of %s: %v", names(got), dir, names(want))
	}
	for name, content := range want {
		if !bytes.Equal(got[name], content) {
			t.Fatalf("zip entry %s differs from the file on disk (%d bytes vs %d)", name, len(got[name]), len(content))
		}
	}
}

// snapshotItems captures the item, book and episode rows, to prove that a rejected request changed nothing.
func snapshotItems(t *testing.T, e *harness.Env) string {
	t.Helper()
	var parts []string
	for _, query := range []string{"SELECT * FROM libraryItems ORDER BY id", "SELECT * FROM books ORDER BY id", "SELECT * FROM podcastEpisodes ORDER BY id"} {
		for _, row := range e.Rows(t, query) {
			parts = append(parts, fmt.Sprint(row))
		}
	}
	return strings.Join(parts, "\n")
}

// removeIfExists deletes a file, ignoring a missing one.
func removeIfExists(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove %s: %v", path, err)
	}
}

// bytesContain reports whether the body contains the text.
func bytesContain(body []byte, text string) bool { return bytes.Contains(body, []byte(text)) }
