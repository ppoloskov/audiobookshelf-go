package library_data

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	userPassword = "library-data-pw"
	eventWait    = 5 * time.Second
	quietWindow  = 400 * time.Millisecond
	missingID    = "00000000-0000-4000-8000-000000000000"
)

// Fixture keys of ids.json.
const (
	bookLib    = "library"
	podcastLib = "podcastLibrary"
)

var counter atomic.Int64

// unique returns a name that no earlier call of the same run produced.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, counter.Add(1))
}

// suite holds state the scenarios share on the packet server.
type suite struct {
	enriched bool
	users    map[string]accountUser
	meta     *metaLibrary
}

// accountUser is a user created through the API.
type accountUser struct{ ID, Token string }

func requireStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d (%s)", what, resp.Status, want, resp.Body)
	}
}

// libPath is the path of a library resource of a library id.
func libPath(id, suffix string) string { return "/api/libraries/" + id + suffix }

// idName is an {id, name} reference.
type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// filterData is the document of GET /api/libraries/:id/filterdata.
type filterData struct {
	Authors          []idName `json:"authors"`
	Genres           []string `json:"genres"`
	Tags             []string `json:"tags"`
	Series           []idName `json:"series"`
	Narrators        []string `json:"narrators"`
	Languages        []string `json:"languages"`
	Publishers       []string `json:"publishers"`
	PublishedDecades []string `json:"publishedDecades"`
	BookCount        int      `json:"bookCount"`
	AuthorCount      int      `json:"authorCount"`
	SeriesCount      int      `json:"seriesCount"`
	PodcastCount     int      `json:"podcastCount"`
	NumIssues        int      `json:"numIssues"`
	LoadedAt         int64    `json:"loadedAt"`
}

var filterArrayKeys = []string{"authors", "genres", "tags", "series", "narrators", "languages", "publishers", "publishedDecades"}

// getFilterData reads the filter data and checks every list is a JSON array (never null).
func getFilterData(t *testing.T, e *harness.Env, token, libraryID string) filterData {
	t.Helper()
	resp := e.Get(t, token, libPath(libraryID, "/filterdata"))
	requireStatus(t, resp, http.StatusOK, "filterdata")
	var raw map[string]json.RawMessage
	resp.JSON(t, &raw)
	for _, key := range filterArrayKeys {
		if value := strings.TrimSpace(string(raw[key])); !strings.HasPrefix(value, "[") {
			t.Fatalf("filterdata %q must be a JSON array, got %q", key, value)
		}
	}
	var data filterData
	resp.JSON(t, &data)
	return data
}

// encodeNarratorID builds the narrator id the web client uses: encodeURIComponent(base64(name)).
func encodeNarratorID(name string) string {
	return strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D").Replace(base64.StdEncoding.EncodeToString([]byte(name)))
}

// patchMedia patches an item's media as root and fails unless it succeeds.
func patchMedia(t *testing.T, e *harness.Env, itemID, body string) {
	t.Helper()
	resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/items/" + itemID + "/media", Token: e.Token(t, "root"), JSON: json.RawMessage(body)})
	requireStatus(t, resp, http.StatusOK, "patch media "+itemID)
}

// enrichment patches the fixture so authors, genres, tags, series, narrators, publishers, languages
// and decades have distinct values. The fixture ids are keys of ids.json.
var enrichment = []struct{ key, body string }{
	{"novel", `{"metadata":{"publisher":"Pub One","publishedYear":"1995","language":"English","narrators":["Nora Narrator","Vera Voice"],"genres":["Fiction","Mystery"],"series":[{"name":"Solo Series","sequence":"1"}]},"tags":["alpha","beta"]}`},
	{"saga1", `{"metadata":{"publisher":"Pub Two","publishedYear":"1985","language":"English","narrators":["Vera Voice"],"genres":["Fantasy"]},"tags":["alpha"]}`},
	{"saga2", `{"metadata":{"publisher":"Pub One","publishedYear":"2010","genres":["Fantasy","Mystery"]}}`},
	{"ebook", `{"metadata":{"explicit":true,"publishedYear":"2005","genres":["Mystery"],"series":[{"name":"Gone Series","sequence":"2"}]},"tags":["beta"]}`},
	{"disposable", `{"metadata":{"series":[{"name":"Gone Series","sequence":"10"}]},"tags":["gamma"]}`},
}

// enrich applies the enrichment once and restarts the server so the cached filter data is
// rebuilt from the database.
func (s *suite) enrich(t *testing.T, e *harness.Env) {
	t.Helper()
	if s.enriched {
		return
	}
	for _, step := range enrichment {
		patchMedia(t, e, e.IDs[step.key], step.body)
	}
	s.enriched = true
	e.Restart(t)
}

// createUser creates a regular user from a permissions fragment (JSON object members) and logs in.
func (s *suite) createUser(t *testing.T, e *harness.Env, name, fragment string) accountUser {
	t.Helper()
	key := e.Install + "/" + name // scenarios that start their own server must not reuse tokens
	if user, ok := s.users[key]; ok {
		return user
	}
	body := `{"username":"` + name + `","password":"` + userPassword + `","type":"user","isActive":true,` + fragment + `}`
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/users", Token: e.Token(t, "root"), JSON: json.RawMessage(body)})
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	requireStatus(t, resp, http.StatusOK, "create user "+name)
	token, _ := e.Login(t, name, userPassword)
	if s.users == nil {
		s.users = map[string]accountUser{}
	}
	user := accountUser{ID: created.User.ID, Token: token}
	s.users[key] = user
	return user
}

// podcastOnly is a user whose accessible libraries are only the fixture podcast library.
func (s *suite) podcastOnly(t *testing.T, e *harness.Env) accountUser {
	return s.createUser(t, e, "data-podcast-only", `"permissions":{"accessAllLibraries":false},"librariesAccessible":["`+e.IDs[podcastLib]+`"]`)
}

// noUpdate is a user that may download but not update (like the fixture listener, created apart so
// that the packet does not depend on fixture permissions).
func (s *suite) noDownload(t *testing.T, e *harness.Env) accountUser {
	return s.createUser(t, e, "data-no-download", `"permissions":{"download":false,"update":true}`)
}

// copyDir copies a directory tree.
func copyDir(t *testing.T, source, dest string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", source, err)
	}
}

func copyFile(source, dest string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scratchLibrary creates a book library over a new directory under the install with the watcher off,
// so only API calls change it.
type scratchLibrary struct {
	ID, Root, Name string
}

func newScratchLibrary(t *testing.T, e *harness.Env, prefix string, settings map[string]any, extra map[string]any) scratchLibrary {
	t.Helper()
	root := filepath.Join(e.Install, unique(prefix))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	name := unique(prefix)
	merged := map[string]any{"disableWatcher": true}
	for key, value := range settings {
		merged[key] = value
	}
	body := map[string]any{"name": name, "folders": []any{map[string]any{"fullPath": root}}, "settings": merged}
	for key, value := range extra {
		body[key] = value
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries", body)
	requireStatus(t, resp, http.StatusOK, "create scratch library")
	var lib struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &lib)
	return scratchLibrary{ID: lib.ID, Root: root, Name: name}
}

// scan starts a scan and waits until the library has want items, none missing.
func (l scratchLibrary) scan(t *testing.T, e *harness.Env, force bool, want int) {
	t.Helper()
	path := libPath(l.ID, "/scan")
	if force {
		path += "?force=1"
	}
	requireStatus(t, e.Post(t, e.Token(t, "root"), path, nil), http.StatusOK, "scan")
	harness.Eventually(t, 20*time.Second, fmt.Sprintf("library %s to hold %d items", l.Name, want), func() bool {
		return e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ? AND isMissing = 0", l.ID) == want
	})
}

// itemRow is the persisted part of a library item the packet reads.
type itemRow struct {
	ID, Path, MediaID string
	Missing, Invalid  bool
}

func itemRows(t *testing.T, e *harness.Env, libraryID string) []itemRow {
	t.Helper()
	var out []itemRow
	for _, row := range e.Rows(t, "SELECT id, path, mediaId, isMissing, isInvalid FROM libraryItems WHERE libraryId = ? ORDER BY path", libraryID) {
		out = append(out, itemRow{ID: fmt.Sprint(row["id"]), Path: fmt.Sprint(row["path"]), MediaID: fmt.Sprint(row["mediaId"]),
			Missing: fmt.Sprint(row["isMissing"]) == "1", Invalid: fmt.Sprint(row["isInvalid"]) == "1"})
	}
	return out
}

// jsonStrings decodes a JSON array column of strings.
func jsonStrings(t *testing.T, value any) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal([]byte(fmt.Sprint(value)), &out); err != nil {
		t.Fatalf("column is not a JSON string array: %v (%v)", err, value)
	}
	return out
}

func requireEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// audience is a set of sockets, each with the position after its last event.
type audience struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

// connect opens one socket per named token ("" is an unauthenticated socket).
func connect(t *testing.T, e *harness.Env, tokens map[string]string) *audience {
	t.Helper()
	a := &audience{sockets: map[string]*harness.Socket{}, marks: map[string]int{}}
	for name, token := range tokens {
		a.sockets[name] = e.Socket(t, token)
	}
	a.rearm()
	t.Cleanup(a.close)
	return a
}

func (a *audience) rearm() {
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
}

func (a *audience) close() {
	for _, socket := range a.sockets {
		socket.Close()
	}
}

// received returns the first argument of every event of the given name since the mark.
func (a *audience) received(name, event string) []json.RawMessage {
	var out []json.RawMessage
	for _, ev := range a.sockets[name].Events()[a.marks[name]:] {
		if ev.Name == event && len(ev.Args) > 0 {
			out = append(out, ev.Args[0])
		}
	}
	return out
}

// silent asserts that none of the named sockets got the event since the marks.
func (a *audience) silent(t *testing.T, event string, names ...string) {
	t.Helper()
	for _, name := range names {
		a.sockets[name].None(t, a.marks[name], event, quietWindow, nil)
	}
}
