package playback_streams

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// hlsPath is the capability URL of a stream file.
func hlsPath(sessionID, file string) string {
	return "/hls/" + sessionID + "/" + file
}

// trackPath is the capability URL of a public session track.
func trackPath(sessionID string, index int) string {
	return fmt.Sprintf("/public/session/%s/track/%d", sessionID, index)
}

// getHLS reads an HLS file without a bearer token: the route is a capability
// URL mounted outside the authenticated /api prefix.
func getHLS(t *testing.T, e *harness.Env, sessionID, file string, header map[string]string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: hlsPath(sessionID, file), Header: header})
}

// waitSegment waits until a segment of the stream is served and returns it.
func waitSegment(t *testing.T, e *harness.Env, sessionID string, number int) []byte {
	t.Helper()
	var body []byte
	harness.Eventually(t, 30*time.Second, fmt.Sprintf("segment output-%d.ts of %s", number, sessionID), func() bool {
		resp := getHLS(t, e, sessionID, fmt.Sprintf("output-%d.ts", number), nil)
		if resp.Status != http.StatusOK {
			return false
		}
		body = resp.Body
		return true
	})
	return body
}

// waitPlaylist waits until the playlist of the stream is served and returns it.
func waitPlaylist(t *testing.T, e *harness.Env, sessionID string) string {
	t.Helper()
	var body string
	harness.Eventually(t, 30*time.Second, "playlist of "+sessionID, func() bool {
		resp := getHLS(t, e, sessionID, "output.m3u8", nil)
		if resp.Status != http.StatusOK {
			return false
		}
		body = string(resp.Body)
		return true
	})
	return body
}

// playlistSegments returns the segment file names of a playlist.
func playlistSegments(playlist string) []string {
	var files []string
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			files = append(files, line)
		}
	}
	return files
}

// segmentNumber parses the number of an "output-<n>.ts" segment file.
func segmentNumber(t *testing.T, name string) int {
	t.Helper()
	base := strings.TrimSuffix(strings.TrimPrefix(name, "output-"), ".ts")
	number, err := strconv.Atoi(base)
	if err != nil {
		t.Fatalf("segment file %q: %v", name, err)
	}
	return number
}

// streamFolderExists reports whether the metadata/streams/<id> folder exists.
func streamFolderExists(e *harness.Env, sessionID string) bool {
	_, err := os.Stat(filepath.Join(e.Install, "metadata", "streams", sessionID))
	return err == nil
}

// mediaPath is the absolute path of a media file below the install.
func mediaPath(e *harness.Env, rel string) string {
	return filepath.Join(e.Media, rel)
}

// mediaFile reads a file below the install's media folder.
func mediaFile(t *testing.T, e *harness.Env, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Media, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// itemFolder returns the media folder of a library item.
func itemFolder(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT path FROM libraryItems WHERE id = ?", itemID)
	if len(rows) != 1 {
		t.Fatalf("item %s has %d rows", itemID, len(rows))
	}
	path, _ := rows[0]["path"].(string)
	return path
}

// quietWatchers disables the file watcher of both fixture libraries so a file
// change of a scenario is not rescanned behind its back.
func quietWatchers(t *testing.T, e *harness.Env) {
	t.Helper()
	for _, key := range []string{"library", "podcastLibrary"} {
		resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+e.IDs[key], map[string]any{"settings": map[string]any{"disableWatcher": true}})
		requireStatus(t, resp, http.StatusOK, "disable watcher of "+key)
	}
}

// scanLibrary triggers a library scan and waits until it finished.
func scanLibrary(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	stamp := func() string {
		rows := e.Rows(t, "SELECT lastScan FROM libraries WHERE id = ?", libraryID)
		text, _ := rows[0]["lastScan"].(string)
		return text
	}
	before := stamp()
	requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries/"+libraryID+"/scan", nil), http.StatusOK, "library scan")
	harness.Eventually(t, 60*time.Second, "library scan to finish", func() bool { return stamp() != before })
}

// newAudiobook writes one audio file into a new audiobook folder, scans the
// library and returns the new item id.
func newAudiobook(t *testing.T, e *harness.Env, title, filename string, content []byte) string {
	t.Helper()
	folder := filepath.Join(e.Media, "audiobooks", "Zed Disposable", title)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, filename), content, 0o644); err != nil {
		t.Fatal(err)
	}
	scanLibrary(t, e, e.IDs["library"])
	var id string
	harness.Eventually(t, 30*time.Second, "scanned item "+title, func() bool {
		rows := e.Rows(t, "SELECT id FROM libraryItems WHERE path = ?", folder)
		if len(rows) != 1 {
			return false
		}
		id, _ = rows[0]["id"].(string)
		return id != ""
	})
	return id
}

// fixtureMP3 returns the bytes of the 3 s fixture mp3.
func fixtureMP3(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	return mediaFile(t, e, filepath.Join("audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3"))
}

// slowFFmpegEnv gives a dedicated server an ffmpeg that starts its transcode
// late and keeps the process alive after it finished, so the stream's
// two-second segment check runs both before any segment exists and while the
// transcode is still reported as running.
func slowFFmpegEnv(t *testing.T) map[string]string {
	t.Helper()
	real := os.Getenv("FFMPEG_PATH")
	if real == "" {
		t.Fatal("FFMPEG_PATH is not set")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	// Capability probes (-formats) and the version probe must answer at once
	// and unchanged; only the HLS transcode is delayed and kept alive.
	script := "#!/bin/sh\n" +
		"case \"$*\" in *\" -f hls \"*) ;; *) exec " + real + " \"$@\" ;; esac\n" +
		"sleep 3\n" +
		real + " \"$@\"\n" +
		"status=$?\n" +
		"sleep 4\n" +
		"exit $status\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"FFMPEG_PATH": path, "PATH": dir + ":" + os.Getenv("PATH")}
}
