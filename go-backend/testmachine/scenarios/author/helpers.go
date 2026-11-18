package author

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const eventTimeout = 10 * time.Second

// absentWindow is how long a scenario waits to be sure an event does not arrive.
const absentWindow = 600 * time.Millisecond

// authorJSON is the author object clients read (Author.toOldJSON, plus numBooks in events).
type authorJSON struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	NumBooks    *int    `json:"numBooks"`
}

// authorID returns the id of the fixture or created author with that name.
func authorID(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM authors WHERE name = ?", name)
	if len(rows) != 1 {
		t.Fatalf("want exactly one author named %q, got %d", name, len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// authorRow returns the persisted author row, or nil when it does not exist.
func authorRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, lastFirst, asin, description, imagePath, libraryId, createdAt, updatedAt FROM authors WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// mustRow fails the test when the author row does not exist.
func mustRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	row := authorRow(t, e, id)
	if row == nil {
		t.Fatalf("author %s has no row", id)
	}
	return row
}

// rowText returns a text column, "<NULL>" for NULL.
func rowText(row map[string]any, column string) string {
	if row[column] == nil {
		return "<NULL>"
	}
	return fmt.Sprint(row[column])
}

// decode unmarshals a JSON body into a new T.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %T: %v: %s", value, err, body)
	}
	return value
}

// wantStatus fails unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want status %d, got %d: %s", what, status, resp.Status, resp.Body)
	}
}

// authorURL is the route path of an author sub-resource.
func authorURL(id, suffix string) string { return "/api/authors/" + id + suffix }

// user is a user created by a scenario.
type user struct{ token, id string }

// createUser creates a user as root and logs in. permissions is merged over the defaults of a "user" account.
func createUser(t *testing.T, e *harness.Env, name string, permissions map[string]any) user {
	t.Helper()
	body := map[string]any{"username": name, "password": name + "-password", "type": "user", "isActive": true, "permissions": permissions}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, "create user "+name, resp, http.StatusOK)
	token, loggedIn := e.Login(t, name, name+"-password")
	id, _ := loggedIn["id"].(string)
	return user{token: token, id: id}
}

// watchers are root and listener sockets with the event index at their creation.
type watchers struct {
	root, listener         *harness.Socket
	rootMark, listenerMark int
}

// watch connects a root and a listener socket and marks them.
func watch(t *testing.T, e *harness.Env) *watchers {
	t.Helper()
	w := &watchers{root: e.Socket(t, e.Token(t, "root")), listener: e.Socket(t, e.Token(t, "listener"))}
	w.rootMark, w.listenerMark = w.root.Mark(), w.listener.Mark()
	return w
}

// first is the first event argument decoded as a JSON object.
func first(args []json.RawMessage) map[string]any {
	if len(args) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(args[0], &object); err != nil {
		return nil
	}
	return object
}

// idIs matches events whose first argument has the id.
func idIs(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool { return first(args)["id"] == id }
}

// waitBoth waits for the event on the root and the listener socket and returns the root payload;
// both sockets must receive the same payload because the event goes to every authorised client.
func (w *watchers) waitBoth(t *testing.T, name string, match func([]json.RawMessage) bool) json.RawMessage {
	t.Helper()
	rootEvent := w.root.Wait(t, w.rootMark, name, eventTimeout, match)
	listenerEvent := w.listener.Wait(t, w.listenerMark, name, eventTimeout, match)
	if !bytes.Equal(canonical(t, rootEvent.Args[0]), canonical(t, listenerEvent.Args[0])) {
		t.Fatalf("%s payload differs between sockets: %s vs %s", name, rootEvent.Args[0], listenerEvent.Args[0])
	}
	return rootEvent.Args[0]
}

// none asserts neither socket gets the event (matching) now or within the window.
func (w *watchers) none(t *testing.T, name string, match func([]json.RawMessage) bool) {
	t.Helper()
	w.root.None(t, w.rootMark, name, absentWindow, match)
	w.listener.None(t, w.listenerMark, name, absentWindow, match)
}

// canonical re-encodes JSON with sorted keys for comparison.
func canonical(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("payload is not JSON: %v: %s", err, raw)
	}
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// authorEvent decodes an author payload and checks numBooks when want is non-negative.
func authorEvent(t *testing.T, raw json.RawMessage, wantNumBooks int) authorJSON {
	t.Helper()
	got := decode[authorJSON](t, raw)
	switch {
	case wantNumBooks < 0 && got.NumBooks != nil:
		t.Fatalf("payload must have no numBooks: %s", raw)
	case wantNumBooks >= 0 && (got.NumBooks == nil || *got.NumBooks != wantNumBooks):
		t.Fatalf("payload numBooks must be %d: %s", wantNumBooks, raw)
	}
	return got
}

// imageBytes encodes a width x height test image as "jpeg" or "png".
func imageBytes(t *testing.T, kind string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / width), G: uint8(y * 255 / height), B: 128, A: 255})
		}
	}
	var out bytes.Buffer
	var err error
	switch kind {
	case "jpeg":
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 90})
	case "png":
		err = png.Encode(&out, img)
	default:
		t.Fatalf("unknown image kind %q", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// stubResponse is what the image stub serves at a path.
type stubResponse struct {
	status      int
	contentType string
	body        []byte
}

// imageStubHandler serves the stub routes and increments hits for every request when it is not nil.
func imageStubHandler(hits *atomic.Int64, routes map[string]stubResponse) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		route, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if route.contentType != "" {
			w.Header().Set("Content-Type", route.contentType)
		} else {
			w.Header()["Content-Type"] = nil // a nil entry stops net/http from sniffing a type
		}
		w.WriteHeader(route.status)
		_, _ = w.Write(route.body) // a failed write is the client giving up; nothing to report
	})
}

// imageStub serves the paths on 127.0.0.1 (the harness whitelists that host for downloads).
func imageStub(t *testing.T, routes map[string]stubResponse) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(imageStubHandler(nil, routes))
	t.Cleanup(server.Close)
	return server
}

// countingImageStub is imageStub that also counts the requests it receives, so a scenario can prove
// that a rejected upload never started a download.
func countingImageStub(t *testing.T, routes map[string]stubResponse) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	hits := new(atomic.Int64)
	server := httptest.NewServer(imageStubHandler(hits, routes))
	t.Cleanup(server.Close)
	return server, hits
}

// imageDims decodes an image with the standard library and returns its size.
func imageDims(t *testing.T, body []byte, wantFormat string) (width, height int) {
	t.Helper()
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("response is not a decodable image: %v", err)
	}
	if format != wantFormat {
		t.Fatalf("image format %q, want %q", format, wantFormat)
	}
	return config.Width, config.Height
}

// isWebP reports whether the bytes are a RIFF/WEBP container and returns nothing else about it.
func isWebP(body []byte) bool {
	return len(body) > 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "WEBP"
}

// imageCacheDir is where upstream keeps resized author images.
func imageCacheDir(e *harness.Env) string { return filepath.Join(e.Metadata, "cache", "images") }

// cacheFiles lists the cache files of the author, sorted by name.
func cacheFiles(t *testing.T, e *harness.Env, id string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(imageCacheDir(e), id+"_*"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	return names
}

// fileExists reports whether the path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readFile reads a test file, failing the test on error.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// authorImagePath is where a downloaded author image is stored.
func authorImagePath(e *harness.Env, id, ext string) string {
	return filepath.Join(e.Metadata, "authors", id+"."+ext)
}

// bookCount returns the number of bookAuthors rows of the author.
func bookCount(t *testing.T, e *harness.Env, id string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM bookAuthors WHERE authorId = ?", id)
}

// bookAuthorNames returns the names of the authors linked to the library item's book, sorted.
func bookAuthorNames(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	rows := e.Rows(t, `SELECT a.name AS name FROM bookAuthors ba JOIN authors a ON a.id = ba.authorId
		JOIN libraryItems li ON li.mediaId = ba.bookId WHERE li.id = ? ORDER BY a.name`, itemID)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, fmt.Sprint(row["name"]))
	}
	return names
}

// metadataFileAuthors returns the "authors" of metadata/items/<id>/metadata.json.
func metadataFileAuthors(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	file := decode[struct {
		Authors []string `json:"authors"`
	}](t, readFile(t, filepath.Join(e.Metadata, "items", itemID, "metadata.json")))
	return file.Authors
}
