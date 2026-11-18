package collections

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

// libraryItem is the part of an expanded library item that collection
// payloads carry and clients read.
type libraryItem struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	MediaType string `json:"mediaType"`
	Media     struct {
		ID       string `json:"id"`
		Metadata struct {
			Title string `json:"title"`
		} `json:"metadata"`
	} `json:"media"`
}

// collection is the expanded collection JSON of every collection route and event.
type collection struct {
	ID          string        `json:"id"`
	LibraryID   string        `json:"libraryId"`
	Name        string        `json:"name"`
	Description *string       `json:"description"`
	Books       []libraryItem `json:"books"`
	LastUpdate  int64         `json:"lastUpdate"`
	CreatedAt   int64         `json:"createdAt"`
	RSSFeed     *struct {
		ID         string `json:"id"`
		Slug       string `json:"slug"`
		EntityID   string `json:"entityId"`
		EntityType string `json:"entityType"`
	} `json:"rssFeed"`
}

// itemIDs returns the library item ids of the books in payload order.
func (c collection) itemIDs() []string {
	ids := make([]string, 0, len(c.Books))
	for _, book := range c.Books {
		ids = append(ids, book.ID)
	}
	return ids
}

// items names the fixture library items the scenarios use.
type items struct{ saga1, saga2, novel, ebook, disposable, podcast string }

func fixtureItems(e *harness.Env) items {
	return items{e.IDs["saga1"], e.IDs["saga2"], e.IDs["novel"], e.IDs["ebook"], e.IDs["disposable"], e.IDs["podcast"]}
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

// decode decodes a 200 response into a collection.
func decode(t *testing.T, resp harness.Response) collection {
	t.Helper()
	wantStatus(t, resp, http.StatusOK)
	var c collection
	resp.JSON(t, &c)
	return c
}

// createAs creates a collection in the fixture library and returns it.
func createAs(t *testing.T, e *harness.Env, token, name string, books ...string) collection {
	t.Helper()
	return decode(t, call(t, e, token, http.MethodPost, "/api/collections", map[string]any{
		"libraryId": e.IDs["library"], "name": name, "books": books,
	}))
}

// create creates a collection as root.
func create(t *testing.T, e *harness.Env, name string, books ...string) collection {
	t.Helper()
	return createAs(t, e, e.Token(t, "root"), name, books...)
}

// fetch reads one collection as root.
func fetch(t *testing.T, e *harness.Env, id string) collection {
	t.Helper()
	return decode(t, e.Get(t, e.Token(t, "root"), "/api/collections/"+id))
}

func sameStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// links returns the library item ids and the order values of the collection's
// links in persisted order.
func links(t *testing.T, e *harness.Env, collectionID string) (itemIDs []string, orders []string) {
	t.Helper()
	rows := e.Rows(t, "SELECT li.id AS item, cb.`order` AS position FROM collectionBooks cb JOIN libraryItems li ON li.mediaId = cb.bookId WHERE cb.collectionId = ? ORDER BY cb.`order`", collectionID)
	for _, row := range rows {
		itemIDs = append(itemIDs, fmt.Sprint(row["item"]))
		orders = append(orders, fmt.Sprint(row["position"]))
	}
	return itemIDs, orders
}

// wantLinks asserts the persisted links are exactly itemIDs, numbered 1..n.
func wantLinks(t *testing.T, e *harness.Env, collectionID string, itemIDs ...string) {
	t.Helper()
	gotItems, gotOrders := links(t, e, collectionID)
	sameStrings(t, "persisted link items", gotItems, append([]string{}, itemIDs...))
	want := make([]string, len(itemIDs))
	for i := range want {
		want[i] = fmt.Sprint(i + 1)
	}
	sameStrings(t, "persisted link orders", gotOrders, want)
}

// collectionRow returns the persisted collections row.
func collectionRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, description, libraryId, createdAt, updatedAt FROM collections WHERE id = ?", id)
	if len(rows) != 1 {
		t.Fatalf("collections row %s: %d rows", id, len(rows))
	}
	return rows[0]
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

// afterMillis blocks until the wall clock is strictly past ms, so a change made
// next gets a strictly larger timestamp.
func afterMillis(t *testing.T, ms int64) {
	t.Helper()
	harness.Eventually(t, 5*time.Second, "clock to pass the timestamp", func() bool { return time.Now().UnixMilli() > ms })
}

// snapshot is every collection and link row; equal snapshots mean nothing changed.
func snapshot(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, description, libraryId, createdAt, updatedAt FROM collections ORDER BY id")
	linkRows := e.Rows(t, "SELECT id, collectionId, bookId, `order`, createdAt FROM collectionBooks ORDER BY id")
	encoded, err := json.Marshal([]any{rows, linkRows})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// unchanged runs act and asserts the collection tables are the same afterwards.
func unchanged(t *testing.T, e *harness.Env, act func()) {
	t.Helper()
	before := snapshot(t, e)
	act()
	if after := snapshot(t, e); after != before {
		t.Fatalf("a rejected request changed collection rows:\nbefore %s\nafter  %s", before, after)
	}
}

// users made through the admin API; created once per server.
var (
	userMu    sync.Mutex
	userToken = map[string]string{}
)

// extraUser creates (once) a user through POST /api/users and returns its access token.
func extraUser(t *testing.T, e *harness.Env, name string, permissions map[string]any) string {
	t.Helper()
	key := e.Install + "/" + name
	userMu.Lock()
	defer userMu.Unlock()
	if token, ok := userToken[key]; ok {
		return token
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", map[string]any{
		"username": name, "password": "collections-" + name, "type": "user", "isActive": true, "permissions": permissions,
	})
	wantStatus(t, resp, http.StatusOK)
	token, _ := e.Login(t, name, "collections-"+name)
	userToken[key] = token
	return token
}

// podcastOnly is a user who may update and delete but only sees the podcast library.
func podcastOnly(t *testing.T, e *harness.Env) string {
	return extraUser(t, e, "pod-editor", map[string]any{
		"download": true, "update": true, "delete": true, "accessAllLibraries": false,
		"librariesAccessible": []string{e.IDs["podcastLibrary"]}, "accessAllTags": true, "accessExplicitContent": true,
	})
}

// editor is a user with update but no delete permission and access to everything.
func editor(t *testing.T, e *harness.Env) string {
	return extraUser(t, e, "collection-editor", map[string]any{
		"download": true, "update": true, "delete": false, "accessAllLibraries": true,
		"accessAllTags": true, "accessExplicitContent": true,
	})
}

// audience is one socket per kind of recipient.
type audience struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

// connect opens an authenticated socket for root, listener, finisher, the
// podcast-only user and an editor, plus one that never authenticates.
func connect(t *testing.T, e *harness.Env) *audience {
	t.Helper()
	a := &audience{sockets: map[string]*harness.Socket{}, marks: map[string]int{}}
	a.sockets["root"] = e.Socket(t, e.Token(t, "root"))
	a.sockets["listener"] = e.Socket(t, e.Token(t, "listener"))
	a.sockets["finisher"] = e.Socket(t, e.Token(t, "finisher"))
	a.sockets["pod-editor"] = e.Socket(t, podcastOnly(t, e))
	a.sockets["editor"] = e.Socket(t, editor(t, e))
	a.sockets["anonymous"] = e.Socket(t, "")
	a.mark()
	return a
}

// mark records the current position of every socket.
func (a *audience) mark() {
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
}

func matchID(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var c collection
		return len(args) == 1 && json.Unmarshal(args[0], &c) == nil && c.ID == id
	}
}

const eventWait = 5 * time.Second
const quietWindow = 300 * time.Millisecond

// receive waits for the event on every authenticated socket and returns the
// payload each decoded. Exactly one argument is required.
func (a *audience) receive(t *testing.T, event, id string) collection {
	t.Helper()
	var first collection
	for name, socket := range a.sockets {
		if name == "anonymous" {
			continue
		}
		ev := socket.Wait(t, a.marks[name], event, eventWait, matchID(id))
		var c collection
		if err := json.Unmarshal(ev.Args[0], &c); err != nil {
			t.Fatalf("%s socket: %s payload: %v", name, event, err)
		}
		if name == "root" {
			first = c
		}
		if first.ID != "" && c.ID != first.ID {
			t.Fatalf("%s socket got a different collection", name)
		}
	}
	a.silent(t, event+" on unauthenticated socket", "anonymous", event)
	return first
}

// silent asserts the named socket got no such event since the mark.
func (a *audience) silent(t *testing.T, what, name, event string) {
	t.Helper()
	a.sockets[name].None(t, a.marks[name], event, quietWindow, nil)
}

// none asserts no socket got the event since the mark.
func (a *audience) none(t *testing.T, event string) {
	t.Helper()
	for name := range a.sockets {
		a.silent(t, event, name, event)
	}
}

// payloads returns every payload of the event the socket got since the mark.
func (a *audience) payloads(name, event string) []collection {
	var found []collection
	for _, ev := range a.sockets[name].Events()[a.marks[name]:] {
		var c collection
		if ev.Name == event && len(ev.Args) == 1 && json.Unmarshal(ev.Args[0], &c) == nil {
			found = append(found, c)
		}
	}
	return found
}

// openFeed opens an RSS feed for the collection and closes it when the scenario ends.
func openFeed(t *testing.T, e *harness.Env, collectionID, slug string) string {
	t.Helper()
	root := e.Token(t, "root")
	open := call(t, e, root, http.MethodPost, "/api/feeds/collection/"+collectionID+"/open", map[string]any{"serverAddress": e.URL, "slug": slug})
	wantStatus(t, open, http.StatusOK)
	feedID, _ := harness.Dig(open.Object(t), "feed", "id").(string)
	if feedID == "" {
		t.Fatalf("feed open response without id: %s", open.Body)
	}
	t.Cleanup(func() { call(t, e, root, http.MethodPost, "/api/feeds/"+feedID+"/close", nil) }) // already closed is fine
	return feedID
}
