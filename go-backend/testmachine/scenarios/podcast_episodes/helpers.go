package podcast_episodes

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventWait    = 20 * time.Second // one socket event sent during a request
	downloadWait = 90 * time.Second // a queued episode to download and register
	quietWindow  = 800 * time.Millisecond
)

// wantStatus fails unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: status %d, want %d: %.400s", what, resp.Status, status, resp.Body)
	}
}

// decode unmarshals a JSON body into a new T.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %T: %v: %.400s", value, err, body)
	}
	return value
}

// strValue reads a nullable string, treating nil as "".
func strValue(t *testing.T, what string, value *string) string {
	t.Helper()
	if value == nil {
		return ""
	}
	return *value
}

// requireValue asserts a nullable string value, treating nil as "".
func requireValue(t *testing.T, what string, value *string, want string) {
	t.Helper()
	if got := strValue(t, what, value); got != want {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// requireString asserts a column value of a stored row.
func requireString(t *testing.T, row map[string]any, column, want string) {
	t.Helper()
	if got, _ := row[column].(string); got != want {
		t.Fatalf("%s = %q, want %q (row %+v)", column, got, want, row)
	}
}

// itemOf reads a library item through the API.
func itemOf(t *testing.T, e *harness.Env, id string) libraryItemJSON {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id+"?expanded=1")
	wantStatus(t, "get item", resp, http.StatusOK)
	return decode[libraryItemJSON](t, resp.Body)
}

// episodeOf returns one episode of an item by id.
func episodeOf(t *testing.T, item libraryItemJSON, id string) episodeJSON {
	t.Helper()
	for _, episode := range item.Media.Episodes {
		if episode.ID == id {
			return episode
		}
	}
	t.Fatalf("item %s has no episode %s", item.ID, id)
	return episodeJSON{}
}

// episodeRow reads one stored podcast episode row.
func episodeRow(t *testing.T, e *harness.Env, episodeID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, `SELECT id, "index", season, episode, episodeType, title, subtitle, description,
		pubDate, enclosureURL, enclosureSize, enclosureType, publishedAt, audioFile, chapters,
		extraData, podcastId, createdAt, updatedAt FROM podcastEpisodes WHERE id = ?`, episodeID)
	if len(rows) != 1 {
		t.Fatalf("episode %s: %d rows", episodeID, len(rows))
	}
	return rows[0]
}

// libraryFolder returns the id and full path of the first folder of a library.
func libraryFolder(t *testing.T, e *harness.Env, libraryID string) (folderID, fullPath string) {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID)
	wantStatus(t, "get library", resp, http.StatusOK)
	object := resp.Object(t)
	folderID, _ = harness.Dig(object, "folders", 0, "id").(string)
	fullPath, _ = harness.Dig(object, "folders", 0, "fullPath").(string)
	if folderID == "" || fullPath == "" {
		t.Fatalf("library %s has no folder: %.400s", libraryID, resp.Body)
	}
	return folderID, fullPath
}

// createShow creates a podcast library item without a feed and returns it.
func createShow(t *testing.T, e *harness.Env, title string) libraryItemJSON {
	t.Helper()
	folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
	body := map[string]any{
		"libraryId": e.IDs["podcastLibrary"],
		"folderId":  folderID,
		"path":      filepath.Join(folderPath, title),
		"media":     map[string]any{"metadata": map[string]any{"title": title}},
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/podcasts", body)
	wantStatus(t, "create podcast", resp, http.StatusOK)
	item := decode[libraryItemJSON](t, resp.Body)
	if item.ID == "" || item.Media.ID == "" {
		t.Fatalf("created podcast %q has no ids: %.400s", title, resp.Body)
	}
	return item
}

// showByTitle finds a podcast library item created by an earlier scenario.
func showByTitle(t *testing.T, e *harness.Env, title string) libraryItemJSON {
	t.Helper()
	rows := e.Rows(t, `SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id = li.mediaId
		WHERE li.mediaType = 'podcast' AND p.title = ?`, title)
	if len(rows) != 1 {
		t.Fatalf("%d podcast items titled %q", len(rows), title)
	}
	id, _ := rows[0]["id"].(string)
	return itemOf(t, e, id)
}

// ensureShow returns the named show, creating it on first use.
func ensureShow(t *testing.T, e *harness.Env, title string) libraryItemJSON {
	t.Helper()
	rows := e.Rows(t, `SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id = li.mediaId
		WHERE li.mediaType = 'podcast' AND p.title = ?`, title)
	if len(rows) == 1 {
		id, _ := rows[0]["id"].(string)
		return itemOf(t, e, id)
	}
	return createShow(t, e, title)
}

// patchMedia updates podcast media through the library item route.
func patchMedia(t *testing.T, e *harness.Env, itemID string, media map[string]any) libraryItemJSON {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", media)
	wantStatus(t, "patch media", resp, http.StatusOK)
	var payload struct {
		Updated     bool            `json:"updated"`
		LibraryItem libraryItemJSON `json:"libraryItem"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("decode patch media: %v: %.400s", err, resp.Body)
	}
	return payload.LibraryItem
}

// createUser creates an account with permissions and returns its token.
// Permission keys are nested under "permissions"; librariesAccessible and
// itemTagsSelected are accepted there and stored at the top of permissions.
func createUser(t *testing.T, e *harness.Env, username string, permissions map[string]any) string {
	t.Helper()
	body := map[string]any{
		"username": username, "password": username + "-password", "type": "user", "isActive": true,
		"permissions": permissions,
	}
	wantStatus(t, "create user", e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK)
	token, _ := e.Login(t, username, username+"-password")
	return token
}

// libraryRestrictedToken creates a user that may only see the book library.
func libraryRestrictedToken(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	return createUser(t, e, username, map[string]any{
		"accessAllLibraries":  false,
		"librariesAccessible": []string{e.IDs["library"]},
	})
}

// snapshot counts the rows that a rejected or failed request must not change.
type snapshot struct {
	items    int
	podcasts int
	episodes int
	files    int
}

func takeSnapshot(t *testing.T, e *harness.Env) snapshot {
	t.Helper()
	return snapshot{
		items:    e.Count(t, "SELECT count(*) FROM libraryItems"),
		podcasts: e.Count(t, "SELECT count(*) FROM podcasts"),
		episodes: e.Count(t, "SELECT count(*) FROM podcastEpisodes"),
		files:    e.Count(t, "SELECT count(*) FROM mediaProgresses"),
	}
}

// requireUnchanged fails unless the snapshot still matches.
func (s snapshot) requireUnchanged(t *testing.T, e *harness.Env, what string) {
	t.Helper()
	if after := takeSnapshot(t, e); after != s {
		t.Fatalf("%s changed rows: %+v, want %+v", what, after, s)
	}
}

// downloadEpisode builds one episode request body.
func downloadEpisode(title, guid, audioURL, pubDate string, publishedAt int64) map[string]any {
	return map[string]any{
		"title":       title,
		"subtitle":    title + " subtitle",
		"description": "<p>" + title + "</p>",
		"pubDate":     pubDate,
		"publishedAt": publishedAt,
		"episodeType": "full",
		"season":      "2",
		"episode":     "7",
		"enclosure": map[string]any{
			"url":    audioURL,
			"type":   "audio/mpeg",
			"length": "96673",
		},
		"guid": guid,
	}
}

// postDownloads asks the server to download episodes.
func postDownloads(t *testing.T, e *harness.Env, itemID string, token string, episodes []map[string]any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/podcasts/"+itemID+"/download-episodes", episodes)
}

// queueOf reads the episode downloads of a podcast item.
func queueOf(t *testing.T, e *harness.Env, itemID string) downloadsPayload {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/podcasts/"+itemID+"/downloads")
	wantStatus(t, "podcast downloads", resp, http.StatusOK)
	return decode[downloadsPayload](t, resp.Body)
}

// libraryQueueOf reads the episode download queue of a library.
func libraryQueueOf(t *testing.T, e *harness.Env, libraryID string, query string) queueDetails {
	t.Helper()
	path := "/api/libraries/" + libraryID + "/episode-downloads"
	if query != "" {
		path += "?" + query
	}
	resp := e.Get(t, e.Token(t, "root"), path)
	wantStatus(t, "library episode downloads", resp, http.StatusOK)
	return decode[queueDetails](t, resp.Body)
}

// requireEmptyQueue proves the queue payload has no current download and no
// queued entry.
func requireEmptyQueue(t *testing.T, what string, details queueDetails) {
	t.Helper()
	if details.CurrentDownload != nil {
		t.Fatalf("%s: current download = %+v, want null", what, details.CurrentDownload)
	}
	if details.Queue == nil || len(details.Queue) != 0 {
		t.Fatalf("%s: queue = %+v, want []", what, details.Queue)
	}
}

// readEpisodeFile returns the bytes of an episode audio file.
func readEpisodeFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read episode file %q: %v", path, err)
	}
	return data
}

// fixtureEpisodeMP3 returns a valid episode audio payload for stub servers.
func fixtureEpisodeMP3(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	return readEpisodeFile(t, filepath.Join(e.Media, "podcasts", "Fixture Cast", "episode-1.mp3"))
}

// deadURL returns an absolute URL on a closed local port, so a download fails
// without leaving the machine.
func deadURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + address + "/episode.mp3"
}
