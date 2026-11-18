package playlists

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// item is one entry of the expanded playlist JSON of every playlist route and event.
type item struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId"`
	Episode       *struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Duration  float64   `json:"duration"`
		AudioFile *struct{} `json:"audioFile"`
	} `json:"episode"`
	LibraryItem struct {
		ID        string `json:"id"`
		LibraryID string `json:"libraryId"`
		MediaType string `json:"mediaType"`
		IsMissing *bool  `json:"isMissing"`
		IsInvalid *bool  `json:"isInvalid"`
		Media     struct {
			Metadata struct {
				Title   string `json:"title"`
				Authors []struct {
					Name string `json:"name"`
				} `json:"authors"`
			} `json:"metadata"`
			Duration  float64    `json:"duration"`
			CoverPath *string    `json:"coverPath"`
			Tracks    []struct{} `json:"tracks"`
		} `json:"media"`
	} `json:"libraryItem"`
}

// playlist is the expanded playlist JSON.
type playlist struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	LibraryID   string  `json:"libraryId"`
	UserID      string  `json:"userId"`
	Description *string `json:"description"`
	LastUpdate  int64   `json:"lastUpdate"`
	CreatedAt   int64   `json:"createdAt"`
	Items       []item  `json:"items"`
}

// refs returns the items as "libraryItemId" (book) or "libraryItemId/episodeId", in payload order.
func (p playlist) refs() []string {
	refs := make([]string, 0, len(p.Items))
	for _, entry := range p.Items {
		refs = append(refs, ref(entry.LibraryItemID, entry.EpisodeID))
	}
	return refs
}

func ref(libraryItemID, episodeID string) string {
	if episodeID == "" {
		return libraryItemID
	}
	return libraryItemID + "/" + episodeID
}

// book and episode build request items.
func book(libraryItemID string) map[string]any { return map[string]any{"libraryItemId": libraryItemID} }
func episode(libraryItemID, episodeID string) map[string]any {
	return map[string]any{"libraryItemId": libraryItemID, "episodeId": episodeID}
}

// bookNull is a book request item as the web client sends it: episodeId present and null.
func bookNull(libraryItemID string) map[string]any {
	return map[string]any{"libraryItemId": libraryItemID, "episodeId": nil}
}

// fixture names the fixture rows the scenarios use.
type fixture struct{ saga1, saga2, novel, ebook, disposable, podcast, ep1, ep2, ep3 string }

func fixtureOf(e *harness.Env) fixture {
	return fixture{e.IDs["saga1"], e.IDs["saga2"], e.IDs["novel"], e.IDs["ebook"], e.IDs["disposable"], e.IDs["podcast"], e.IDs["ep1"], e.IDs["ep2"], e.IDs["ep3"]}
}

// call sends one JSON request and returns the response.
func call(t *testing.T, e *harness.Env, token, method, path string, body any) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: method, Path: path, Token: token, JSON: body})
}

func wantStatus(t *testing.T, resp harness.Response, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("status = %d, want %d; body %.300s", resp.Status, want, resp.Body)
	}
}

func decode(t *testing.T, resp harness.Response) playlist {
	t.Helper()
	wantStatus(t, resp, http.StatusOK)
	var p playlist
	resp.JSON(t, &p)
	return p
}

func sameStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// createAs creates a playlist and returns it. Library is the fixture book library.
func createAs(t *testing.T, e *harness.Env, token, name string, items ...map[string]any) playlist {
	t.Helper()
	return decode(t, call(t, e, token, http.MethodPost, "/api/playlists", map[string]any{
		"libraryId": e.IDs["library"], "name": name, "items": items,
	}))
}

// createPodcastAs creates a playlist in the fixture podcast library.
func createPodcastAs(t *testing.T, e *harness.Env, token, name string, items ...map[string]any) playlist {
	t.Helper()
	return decode(t, call(t, e, token, http.MethodPost, "/api/playlists", map[string]any{
		"libraryId": e.IDs["podcastLibrary"], "name": name, "items": items,
	}))
}

// fetchAs reads one playlist.
func fetchAs(t *testing.T, e *harness.Env, token, id string) playlist {
	t.Helper()
	return decode(t, e.Get(t, token, "/api/playlists/"+id))
}

// playlistRow returns the persisted playlists row.
func playlistRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, description, libraryId, userId, createdAt, updatedAt FROM playlists WHERE id = ?", id)
	if len(rows) != 1 {
		t.Fatalf("playlists row %s: %d rows", id, len(rows))
	}
	return rows[0]
}

func noPlaylistRow(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	if n := len(e.Rows(t, "SELECT id FROM playlists WHERE id = ?", id)); n != 0 {
		t.Fatalf("playlist %s must be gone, found %d rows", id, n)
	}
	if n := len(e.Rows(t, "SELECT id FROM playlistMediaItems WHERE playlistId = ?", id)); n != 0 {
		t.Fatalf("playlist %s must have no playlistMediaItems, found %d", id, n)
	}
}

// links returns the persisted links of a playlist in order: the media item id,
// its type and the order value, as "type:mediaItemId@order".
func links(t *testing.T, e *harness.Env, playlistID string) []string {
	t.Helper()
	rows := e.Rows(t, "SELECT mediaItemId, mediaItemType, `order` AS position FROM playlistMediaItems WHERE playlistId = ? ORDER BY `order`, id", playlistID)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, fmt.Sprintf("%v:%v@%v", row["mediaItemType"], row["mediaItemId"], row["position"]))
	}
	return out
}

// mediaID returns the books.id or podcasts.id behind a library item.
func mediaID(t *testing.T, e *harness.Env, libraryItemID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT mediaId FROM libraryItems WHERE id = ?", libraryItemID)
	if len(rows) != 1 {
		t.Fatalf("library item %s not found", libraryItemID)
	}
	return fmt.Sprint(rows[0]["mediaId"])
}

// wantBookLinks asserts the persisted links are exactly the books of the given
// library items, of type book, numbered 1..n.
func wantBookLinks(t *testing.T, e *harness.Env, playlistID string, libraryItemIDs ...string) {
	t.Helper()
	want := make([]string, len(libraryItemIDs))
	for i, id := range libraryItemIDs {
		want[i] = fmt.Sprintf("book:%s@%d", mediaID(t, e, id), i+1)
	}
	sameStrings(t, "persisted links of "+playlistID, links(t, e, playlistID), want)
}

// wantEpisodeLinks asserts the persisted links are exactly the episodes, numbered 1..n.
func wantEpisodeLinks(t *testing.T, e *harness.Env, playlistID string, episodeIDs ...string) {
	t.Helper()
	want := make([]string, len(episodeIDs))
	for i, id := range episodeIDs {
		want[i] = fmt.Sprintf("podcastEpisode:%s@%d", id, i+1)
	}
	sameStrings(t, "persisted links of "+playlistID, links(t, e, playlistID), want)
}

// millis converts a database datetime column to Unix milliseconds.
func millis(t *testing.T, value any) int64 {
	t.Helper()
	switch v := value.(type) {
	case time.Time:
		return v.UnixMilli()
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.000 -07:00", time.RFC3339Nano, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				return parsed.UnixMilli()
			}
		}
	}
	t.Fatalf("cannot read %v (%T) as a time", value, value)
	return 0
}

// wantRow asserts the persisted row agrees with the payload the client got.
func wantRow(t *testing.T, e *harness.Env, p playlist) {
	t.Helper()
	row := playlistRow(t, e, p.ID)
	description := ""
	if p.Description != nil {
		description = *p.Description
	}
	var rowDescription string
	if row["description"] != nil {
		rowDescription = fmt.Sprint(row["description"])
	}
	if fmt.Sprint(row["name"]) != p.Name || rowDescription != description || (row["description"] == nil) != (p.Description == nil) ||
		fmt.Sprint(row["libraryId"]) != p.LibraryID || fmt.Sprint(row["userId"]) != p.UserID ||
		millis(t, row["updatedAt"]) != p.LastUpdate || millis(t, row["createdAt"]) != p.CreatedAt {
		t.Fatalf("row %v does not match payload %+v", row, p)
	}
}

// snapshot is every playlist and link row; equal snapshots mean nothing changed.
func snapshot(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, description, libraryId, userId, createdAt, updatedAt FROM playlists ORDER BY id")
	linkRows := e.Rows(t, "SELECT id, playlistId, mediaItemId, mediaItemType, `order`, createdAt FROM playlistMediaItems ORDER BY id")
	encoded, err := json.Marshal([]any{rows, linkRows})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// unchanged runs act and asserts the playlist tables are the same afterwards.
func unchanged(t *testing.T, e *harness.Env, act func()) {
	t.Helper()
	before := snapshot(t, e)
	act()
	if after := snapshot(t, e); after != before {
		t.Fatalf("a rejected request changed playlist rows:\nbefore %s\nafter  %s", before, after)
	}
}

// users made through the admin API; created once per server.
var (
	userMu     sync.Mutex
	userTokens = map[string]string{}
	userIDs    = map[string]string{}
)

// extraUser creates (once) a user through POST /api/users and returns its access token.
// Every call site names a user of its own, so playlists of different scenarios stay apart.
func extraUser(t *testing.T, e *harness.Env, name string, permissions map[string]any) string {
	t.Helper()
	key := e.Install + "/" + name
	userMu.Lock()
	defer userMu.Unlock()
	if token, ok := userTokens[key]; ok {
		return token
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", map[string]any{
		"username": name, "password": "playlists-" + name, "type": "user", "isActive": true, "permissions": permissions,
	})
	wantStatus(t, resp, http.StatusOK)
	token, user := e.Login(t, name, "playlists-"+name)
	userTokens[key] = token
	userIDs[key], _ = user["id"].(string)
	return token
}

// userID returns the id of a user made by extraUser.
func userID(e *harness.Env, name string) string {
	userMu.Lock()
	defer userMu.Unlock()
	return userIDs[e.Install+"/"+name]
}

// plainUser is a user with access to every library and no extra permission.
func plainUser(t *testing.T, e *harness.Env, name string) string {
	return extraUser(t, e, name, map[string]any{
		"download": true, "update": false, "delete": false, "accessAllLibraries": true,
		"accessAllTags": true, "accessExplicitContent": true,
	})
}

// list reads GET /api/playlists as the token's user.
func list(t *testing.T, e *harness.Env, token string) []playlist {
	t.Helper()
	resp := e.Get(t, token, "/api/playlists")
	wantStatus(t, resp, http.StatusOK)
	var body struct {
		Playlists []playlist `json:"playlists"`
	}
	resp.JSON(t, &body)
	return body.Playlists
}

func hasPlaylist(list []playlist, id string) bool {
	return slices.ContainsFunc(list, func(p playlist) bool { return p.ID == id })
}
