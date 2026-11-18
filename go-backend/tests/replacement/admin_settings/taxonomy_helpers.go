package admin_settings

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// kind describes one of the two parallel resources, tags and genres.
type kind struct {
	name       string              // "tag" or "genre"
	field      string              // item column and metadata.json key: "tags" or "genres"
	oldKey     string              // rename body key
	newKey     string              // rename body key
	mergedKey  string              // rename response key
	listPath   string              // GET path
	renamePath string              // POST path
	deletePath string              // DELETE path prefix
	eventPath  []any               // path of the value list in an item_updated payload
	req        map[string]string   // role (list, rename, delete) to requirement id
	seed       map[string][]string // item key to the values written before the scenarios
	union      []string            // expected list after seeding
	pure       step                // rename without merge
	events     step                // rename observed on sockets
	merge      step                // rename onto an existing value
	dropped    step                // delete
	initial    []string            // expected list on the fixture
	afterMerge []string            // expected list after the rename steps
	unknown    string              // value that exists only in another letter case
	encoded    string              // value whose base64 contains + and /
	final      []string            // expected list after all steps
}

// step is one rename or delete with its expected result.
type step struct {
	from, to string
	merged   bool
	num      int
	after    map[string][]string // item key to its values afterwards
}

var tagKind = kind{
	name: "tag", field: "tags", oldKey: "tag", newKey: "newTag", mergedKey: "tagMerged",
	listPath: "/api/tags", renamePath: "/api/tags/rename", deletePath: "/api/tags/",
	eventPath: []any{"media", "tags"},
	req:       map[string]string{"list": "route-r208", "rename": "route-r209", "delete": "route-r210"},
	seed:      map[string][]string{"novel": {"Alpha", "beta"}, "saga1": {"Alpha"}, "podcast": {"Zulu", "alpine"}, "ebook": {"Zulu"}},
	union:     []string{"Alpha", "alpine", "beta", "Zulu"},
	pure:      step{from: "Zulu", to: "Yankee", num: 2, after: map[string][]string{"podcast": {"alpine", "Yankee"}, "ebook": {"Yankee"}}},
	events:    step{from: "Yankee", to: "Xray", num: 2, after: map[string][]string{"podcast": {"alpine", "Xray"}, "ebook": {"Xray"}}},
	merge:     step{from: "Alpha", to: "beta", merged: true, num: 2, after: map[string][]string{"novel": {"beta"}, "saga1": {"beta"}}},
	dropped:   step{from: "Xray", num: 2, after: map[string][]string{"podcast": {"alpine"}, "ebook": {}}},
	initial:   []string{}, afterMerge: []string{"alpine", "beta", "Xray"},
	unknown: "ALPINE", encoded: "???>>>", final: []string{"alpine", "beta"},
}

var genreKind = kind{
	name: "genre", field: "genres", oldKey: "genre", newKey: "newGenre", mergedKey: "genreMerged",
	listPath: "/api/genres", renamePath: "/api/genres/rename", deletePath: "/api/genres/",
	eventPath: []any{"media", "metadata", "genres"},
	req:       map[string]string{"list": "route-r211", "rename": "route-r212", "delete": "route-r213"},
	seed:      map[string][]string{"novel": {"Fiction", "Horror"}, "saga1": {"Horror"}, "podcast": {"Technology", "Horror"}},
	union:     []string{"Fiction", "Horror", "Technology"},
	pure:      step{from: "Technology", to: "Science", num: 1, after: map[string][]string{"podcast": {"Horror", "Science"}}},
	events:    step{from: "Science", to: "Physics", num: 1, after: map[string][]string{"podcast": {"Horror", "Physics"}}},
	merge:     step{from: "Horror", to: "Fiction", merged: true, num: 3, after: map[string][]string{"novel": {"Fiction"}, "saga1": {"Fiction"}, "podcast": {"Physics", "Fiction"}}},
	dropped:   step{from: "Fiction", num: 3, after: map[string][]string{"novel": {}, "saga1": {}, "podcast": {"Physics"}}},
	initial:   []string{"Fiction", "Technology"}, afterMerge: []string{"Fiction", "Physics"},
	unknown: "PHYSICS", encoded: "???>>>", final: []string{"Physics"},
}

// itemKeys are the fixture items the taxonomy scenarios touch.
var itemKeys = []string{"novel", "saga1", "saga2", "ebook", "podcast"}

// seedItems writes values (item key to list) through PATCH /api/items/:id/media
// (another packet's route, used only as setup); repeating it changes nothing.
func (k kind) seedItems(t *testing.T, e *harness.Env, values map[string][]string) {
	t.Helper()
	root := e.Token(t, "root")
	write := func(values map[string][]string) {
		for item, list := range values {
			body := map[string]any{k.field: list}
			if k.field == "genres" {
				body = map[string]any{"metadata": map[string]any{"genres": list}}
			}
			resp := e.Patch(t, root, "/api/items/"+e.IDs[item]+"/media", body)
			wantStatus(t, "seed "+item, resp, http.StatusOK)
		}
	}
	write(values)
}

// values reads an item's tags or genres column.
func (k kind) values(t *testing.T, e *harness.Env, item string) []string {
	t.Helper()
	rows := e.Rows(t, "SELECT mediaType, mediaId FROM libraryItems WHERE id = ?", e.IDs[item])
	if len(rows) != 1 {
		t.Fatalf("library item %s not found", item)
	}
	table := "books"
	if rows[0]["mediaType"] == "podcast" {
		table = "podcasts"
	}
	// field and table come from constants above, never from input
	data := e.Rows(t, "SELECT "+k.field+" AS v FROM "+table+" WHERE id = ?", rows[0]["mediaId"])
	var list []string
	if text, ok := data[0]["v"].(string); ok {
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			t.Fatalf("%s column of %s is not a JSON array: %v", k.field, item, err)
		}
	}
	if list == nil {
		list = []string{}
	}
	return list
}

// snapshot reads the values of every fixture item.
func (k kind) snapshot(t *testing.T, e *harness.Env) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, item := range itemKeys {
		out[item] = k.values(t, e, item)
	}
	return out
}

// wantRows checks the items named in after against the database and every other
// item against before.
func (k kind) wantRows(t *testing.T, e *harness.Env, before map[string][]string, after map[string][]string) {
	t.Helper()
	now := k.snapshot(t, e)
	for _, item := range itemKeys {
		want := before[item]
		if changed, ok := after[item]; ok {
			want = changed
		}
		if !sameJSON(now[item], want) {
			t.Errorf("%s of %s = %v, want %v", k.field, item, now[item], want)
		}
	}
}

// metadataFile reads the metadata.json of an item from the item folder
// (inItemFolder) or from metadata/items/<id>.
func (k kind) metadataFile(t *testing.T, e *harness.Env, item string, inItemFolder bool) []string {
	t.Helper()
	path := filepath.Join(e.Metadata, "items", e.IDs[item], "metadata.json")
	if inItemFolder {
		rows := e.Rows(t, "SELECT path FROM libraryItems WHERE id = ?", e.IDs[item])
		path = filepath.Join(rows[0]["path"].(string), "metadata.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("metadata.json of %s: %v", item, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("metadata.json of %s is not JSON: %v", item, err)
	}
	var list []string
	if err := json.Unmarshal(doc[k.field], &list); err != nil {
		t.Fatalf("metadata.json of %s has no %s list: %v", item, k.field, err)
	}
	return list
}

// wantFiles checks metadata.json of every item of after.
func (k kind) wantFiles(t *testing.T, e *harness.Env, after map[string][]string, inItemFolder bool) {
	t.Helper()
	for item, want := range after {
		if got := k.metadataFile(t, e, item, inItemFolder); !sameJSON(got, want) {
			t.Errorf("metadata.json %s of %s = %v, want %v", k.field, item, got, want)
		}
	}
}

func encodeValue(value string) string {
	return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(value)))
}

// rename posts a rename as root.
func (k kind) rename(t *testing.T, e *harness.Env, from, to string) harness.Response {
	t.Helper()
	return e.Post(t, e.Token(t, "root"), k.renamePath, map[string]any{k.oldKey: from, k.newKey: to})
}

// remove sends the DELETE request with the client's encoding (encodeURIComponent of base64).
func (k kind) remove(t *testing.T, e *harness.Env, value string) harness.Response {
	t.Helper()
	return e.Delete(t, e.Token(t, "root"), k.deletePath+encodeValue(value))
}

// wantRename checks a rename response.
func (k kind) wantRename(t *testing.T, resp harness.Response, s step) {
	t.Helper()
	wantStatus(t, k.name+" rename", resp, http.StatusOK)
	object := resp.Object(t)
	if !sameJSON(object["numItemsUpdated"], s.num) || object[k.mergedKey] != s.merged {
		t.Fatalf("rename response = %s, want numItemsUpdated %d and %s %v", resp.Body, s.num, k.mergedKey, s.merged)
	}
}

// wantDelete checks a delete response.
func wantDelete(t *testing.T, resp harness.Response, num int) {
	t.Helper()
	wantStatus(t, "delete", resp, http.StatusOK)
	if got := resp.Object(t)["numItemsUpdated"]; !sameJSON(got, num) {
		t.Fatalf("delete response = %s, want numItemsUpdated %d", resp.Body, num)
	}
}

// audienceUser is a user created for event audience checks, with the rule that
// decides which items it may see (SocketAuthority.libraryItemEmitter calls
// User.checkCanAccessLibraryItem: library, explicit content, item tags).
type audienceUser struct {
	name string
	body map[string]any
	// sees reports whether the user may see an item with these tags.
	sees func(item string, tags []string) bool
}

func hasTag(tags []string, tag string) bool { return containsAny(tags, tag) }

// audienceUsers are the restricted users; "alpine" is a tag the fixture podcast keeps throughout.
var audienceUsers = []audienceUser{
	{"podcast-only", map[string]any{"permissions": map[string]any{"accessAllLibraries": false}},
		nil}, // librariesAccessible is added at creation; rule below
	{"tag-allow", map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": false}, "itemTagsSelected": []string{"alpine"}},
		func(_ string, tags []string) bool { return hasTag(tags, "alpine") }},
	{"tag-deny", map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}, "itemTagsSelected": []string{"alpine"}},
		func(_ string, tags []string) bool { return !hasTag(tags, "alpine") }},
	{"no-explicit", map[string]any{"permissions": map[string]any{"accessExplicitContent": false}},
		func(item string, _ []string) bool { return item != explicitItem }},
}

// explicitItem is marked explicit by the audience setup.
const explicitItem = "saga2"

// userToken logs the audience user in, creating it on first use
// (POST /api/users is setup, not under test).
func userToken(t *testing.T, e *harness.Env, user audienceUser) string {
	t.Helper()
	key := e.URL + "|" + user.name
	tokenMu.Lock()
	cached := tokenCache[key]
	tokenMu.Unlock()
	if cached != "" {
		return cached
	}
	// Creating first (400 when the user exists) keeps failed logins, which the
	// login rate limiter counts, out of the packet.
	body := map[string]any{"username": user.name, "password": user.name + "-pw", "type": "user", "isActive": true}
	permissions := map[string]any{"accessExplicitContent": true} // new users cannot see explicit items by default
	for key, value := range user.body["permissions"].(map[string]any) {
		permissions[key] = value
	}
	for key, value := range user.body {
		body[key] = value
	}
	body["permissions"] = permissions
	if user.name == "podcast-only" {
		body["librariesAccessible"] = []string{e.IDs["podcastLibrary"]}
	}
	if resp := e.Post(t, e.Token(t, "root"), "/api/users", body); resp.Status != http.StatusOK && resp.Status != http.StatusBadRequest {
		t.Fatalf("create %s: %d %s", user.name, resp.Status, resp.Body)
	}
	resp := e.Post(t, "", "/login", map[string]string{"username": user.name, "password": user.name + "-pw"})
	wantStatus(t, "login "+user.name, resp, http.StatusOK)
	token, _ := harness.Dig(resp.Object(t), "user", "accessToken").(string)
	if token == "" {
		t.Fatalf("login of %s has no access token: %s", user.name, resp.Body)
	}
	tokenMu.Lock()
	tokenCache[key] = token
	tokenMu.Unlock()
	return token
}

var (
	tokenMu    sync.Mutex
	tokenCache = map[string]string{}
)

// audience is the set of fresh sockets (connected after setup, so every event
// they hold belongs to the scenario) used to check item_updated delivery.
type audience struct {
	root, listener *harness.Socket
	users          map[string]*harness.Socket
}

func (k kind) connect(t *testing.T, e *harness.Env) *audience {
	t.Helper()
	root := e.Token(t, "root")
	// one book is explicit so the explicit-content rule has an item to act on
	wantStatus(t, "mark explicit", e.Patch(t, root, "/api/items/"+e.IDs[explicitItem]+"/media",
		map[string]any{"metadata": map[string]any{"explicit": true}}), http.StatusOK)
	a := &audience{root: e.Socket(t, root), listener: e.Socket(t, e.Token(t, "listener")), users: map[string]*harness.Socket{}}
	for _, user := range audienceUsers {
		a.users[user.name] = e.Socket(t, userToken(t, e, user))
	}
	t.Cleanup(func() {
		a.root.Close()
		a.listener.Close()
		for _, socket := range a.users {
			socket.Close()
		}
	})
	return a
}

func itemMatch(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &payload) == nil && payload.ID == id
	}
}

// wantEvents checks that each touched item produced item_updated for root and
// listener (with the new values), that every restricted user received it exactly
// when its library, item-tag and explicit-content rules allow the item (judged
// by the item's tags after the request), and that untouched items produced none.
func (k kind) wantEvents(t *testing.T, e *harness.Env, a *audience, after map[string][]string) {
	t.Helper()
	tags := tagKind
	for item, want := range after {
		id := e.IDs[item]
		for _, socket := range []*harness.Socket{a.root, a.listener} {
			event := socket.Wait(t, 0, "item_updated", 5*time.Second, itemMatch(id))
			var payload any
			if err := json.Unmarshal(event.Args[0], &payload); err != nil {
				t.Fatalf("item_updated payload: %v", err)
			}
			if got := harness.Dig(payload, k.eventPath...); !sameJSON(got, want) {
				t.Errorf("item_updated %s %s = %v, want %v", item, k.field, got, want)
			}
		}
		itemTags := tags.values(t, e, item)
		for _, user := range audienceUsers {
			sees := user.sees
			if user.name == "podcast-only" {
				sees = func(item string, _ []string) bool { return item == "podcast" }
			}
			socket := a.users[user.name]
			if sees(item, itemTags) {
				socket.Wait(t, 0, "item_updated", 5*time.Second, itemMatch(id))
			} else {
				socket.None(t, 0, "item_updated", 150*time.Millisecond, itemMatch(id))
			}
		}
	}
	for _, item := range itemKeys {
		if _, touched := after[item]; !touched {
			a.root.None(t, 0, "item_updated", 100*time.Millisecond, itemMatch(e.IDs[item]))
		}
	}
}

// libraryValues reads the tags or genres of a library's filter data.
func (k kind) libraryValues(t *testing.T, e *harness.Env, libraryKey string) []string {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs[libraryKey]+"/filterdata")
	wantStatus(t, "filterdata", resp, http.StatusOK)
	return stringList(t, resp.Object(t)[k.field])
}

// stamps reads books/podcasts.updatedAt of every fixture item.
func (k kind) stamps(t *testing.T, e *harness.Env) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, item := range itemKeys {
		rows := e.Rows(t, "SELECT mediaType, mediaId FROM libraryItems WHERE id = ?", e.IDs[item])
		table := "books"
		if rows[0]["mediaType"] == "podcast" {
			table = "podcasts"
		}
		data := e.Rows(t, "SELECT updatedAt FROM "+table+" WHERE id = ?", rows[0]["mediaId"]) // table is a constant
		out[item], _ = data[0]["updatedAt"].(string)
	}
	return out
}

// wantStamps requires touched items to move forward and the others to stay.
func wantStamps(t *testing.T, before, after map[string]string, touched map[string][]string) {
	t.Helper()
	for item, old := range before {
		if _, moved := touched[item]; moved {
			wantMovedForward(t, item, old, after[item])
		} else {
			wantSame(t, item, old, after[item])
		}
	}
}
