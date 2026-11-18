package library_data

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	matchRoute = "server/routers/ApiRouter.js:90"
	matchSrc   = "server/controllers/LibraryController.js:1245-1252"
	matchScan  = "server/scanner/Scanner.js:472-544"
)

// metadataStub is a custom metadata provider on 127.0.0.1 that records its search requests.
type metadataStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []url.Values
}

func newMetadataStub(t *testing.T, matches string) *metadataStub {
	t.Helper()
	stub := &metadataStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.URL.Query())
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"matches":` + matches + `}`)) // a failed write only fails the request under test
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (m *metadataStub) seen() []url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

const stubMatch = `[{"title":"Hard Delete Me","author":"Zed Disposable","narrator":"Stub Narrator, Second","publisher":"Stub Press","publishedYear":"1999","description":"<p>Stub description</p>","genres":["Stub Genre","Other"],"tags":["stub"],"series":[{"series":"Stub Series","sequence":"4"}],"language":"English","isbn":"9781234567897","asin":"B0STUB0001"}]`

// taskJSON is a task event payload.
type taskJSON struct {
	ID         string         `json:"id"`
	Action     string         `json:"action"`
	Data       map[string]any `json:"data"`
	Title      string         `json:"title"`
	TitleKey   string         `json:"titleKey"`
	TitleSubs  []string       `json:"titleSubs"`
	Error      *string        `json:"error"`
	ErrorKey   *string        `json:"errorKey"`
	ShowOK     bool           `json:"showSuccess"`
	IsFailed   bool           `json:"isFailed"`
	IsFinished bool           `json:"isFinished"`
	StartedAt  int64          `json:"startedAt"`
	FinishedAt *int64         `json:"finishedAt"`
}

// tasks returns the payloads of a task event received by a socket for one library.
func (a *audience) tasks(t *testing.T, name, event, libraryID string) []taskJSON {
	t.Helper()
	var out []taskJSON
	for _, raw := range a.received(name, event) {
		var task taskJSON
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatalf("%s payload: %v", event, err)
		}
		if task.Data["libraryId"] == libraryID {
			out = append(out, task)
		}
	}
	return out
}

// matchAll calls the route as root and requires 200.
func matchAll(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(libraryID, "/matchall")), http.StatusOK, "matchall")
}

// bookRow is the stored metadata of a book the match changes.
type bookRow struct {
	Publisher, Year, Description, Language, ISBN, ASIN, Title string
	Narrators, Genres, Tags                                   []string
}

func readBook(t *testing.T, e *harness.Env, itemID string) bookRow {
	t.Helper()
	rows := e.Rows(t, "SELECT b.title, b.publisher, b.publishedYear, b.description, b.language, b.isbn, b.asin, b.narrators, b.genres, b.tags FROM books b JOIN libraryItems li ON li.mediaId = b.id WHERE li.id = ?", itemID)
	if len(rows) != 1 {
		t.Fatalf("no book for %s", itemID)
	}
	r := rows[0]
	str := func(key string) string {
		if r[key] == nil {
			return ""
		}
		return r[key].(string)
	}
	return bookRow{Title: str("title"), Publisher: str("publisher"), Year: str("publishedYear"), Description: str("description"), Language: str("language"),
		ISBN: str("isbn"), ASIN: str("asin"), Narrators: jsonStrings(t, r["narrators"]), Genres: jsonStrings(t, r["genres"]), Tags: jsonStrings(t, r["tags"])}
}

func matchScenarios(s *suite) []harness.Scenario {
	// every scenario builds its own fixture: the stub server lives as long as its scenario
	setup := newMatchFixture
	return []harness.Scenario{
		{
			Requirement: "route-r037", Invariant: "INV-ROUTE-R037-AUTH-001", Case: "error", State: "stateless", Assertion: "matchall-denied-changes-nothing",
			Sources: []string{matchRoute, matchSrc, middleware},
			Claim:   "GET matchall answers 401 without a token, 403 for a non-admin user (also with update permission) and without library access, 404 for an unknown library and 400 for an invalid limit before the admin check; no task starts, no provider is asked and no book changes.",
			Run: func(t *testing.T, e *harness.Env) {
				m := setup(t, e)
				updater := s.createUser(t, e, "data-match-updater", `"permissions":{"update":true}`)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before := []bookRow{readBook(t, e, m.items[0]), readBook(t, e, m.items[1])}
				path := libPath(m.lib.ID, "/matchall")
				requireStatus(t, e.Get(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, e.Token(t, "listener"), path), http.StatusForbidden, "listener")
				requireStatus(t, e.Get(t, updater.Token, path), http.StatusForbidden, "updater")
				requireStatus(t, e.Get(t, s.podcastOnly(t, e).Token, path), http.StatusForbidden, "limited")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/matchall")), http.StatusNotFound, "unknown")
				requireStatus(t, e.Get(t, e.Token(t, "listener"), path+"?limit=-1"), http.StatusBadRequest, "paging before admin")
				a.silent(t, "task_started", "root")
				requireEqual(t, "provider requests", len(m.stub.seen()), 0)
				requireEqual(t, "books", []bookRow{readBook(t, e, m.items[0]), readBook(t, e, m.items[1])}, before)
			},
		},
		{
			Requirement: "route-r037", Invariant: "INV-ROUTE-R037-TASK-001", Case: "edge", State: "stateless", Assertion: "matchall-empty-library-fails-its-task",
			Sources: []string{matchRoute, matchSrc, matchScan, "server/managers/TaskManager.js:17-60", "server/objects/Task.js:54-74"},
			Claim:   "GET matchall of a book library without items answers 200 and emits task_started (library-match-all with the library id, title, translation key and substitution) and task_finished marked failed with the error 'No items found' to every authenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "match-empty", nil, nil)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": s.podcastOnly(t, e).Token, "anonymous": ""})
				matchAll(t, e, lib.ID)
				for _, name := range []string{"root", "listener", "limited"} {
					harness.Eventually(t, eventWait, "task events for "+name, func() bool {
						return len(a.tasks(t, name, "task_started", lib.ID)) == 1 && len(a.tasks(t, name, "task_finished", lib.ID)) == 1
					})
					started, finished := a.tasks(t, name, "task_started", lib.ID)[0], a.tasks(t, name, "task_finished", lib.ID)[0]
					if started.Action != "library-match-all" || started.Title != `Matching books in "`+lib.Name+`"` || started.TitleKey != "MessageTaskMatchingBooksInLibrary" ||
						!slices.Equal(started.TitleSubs, []string{lib.Name}) || !started.ShowOK || started.IsFinished || started.IsFailed || started.StartedAt <= 0 {
						t.Fatalf("task_started: %+v", started)
					}
					if finished.ID != started.ID || !finished.IsFailed || !finished.IsFinished || finished.Error == nil || *finished.Error != "No items found" ||
						finished.ErrorKey == nil || *finished.ErrorKey != "MessageNoItemsFound" || finished.FinishedAt == nil || *finished.FinishedAt < started.StartedAt {
						t.Fatalf("task_finished: %+v", finished)
					}
				}
				a.silent(t, "task_started", "anonymous")
			},
		},
		{
			Requirement: "route-r037", Invariant: "INV-ROUTE-R037-PODCAST-001", Case: "edge", State: "stateless", Assertion: "matchall-podcast-library-does-nothing",
			Sources: []string{matchRoute, matchSrc, "server/scanner/Scanner.js:473-476"},
			Claim:   "GET matchall of a podcast library answers 200 and starts no task, so no task event and no change follow.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				rows := e.Rows(t, "SELECT id, updatedAt FROM podcasts")
				matchAll(t, e, e.IDs[podcastLib])
				a.silent(t, "task_started", "root")
				a.silent(t, "task_finished", "root")
				a.silent(t, "item_updated", "root")
				requireEqual(t, "podcasts", e.Rows(t, "SELECT id, updatedAt FROM podcasts"), rows)
			},
		},
		{
			Requirement: "route-r037", Invariant: "INV-ROUTE-R037-CONTRACT", Case: "normal", State: "stateless", Assertion: "matchall-applies-custom-provider-match",
			Sources: []string{matchRoute, matchSrc, matchScan, "server/scanner/Scanner.js:37-135", "server/scanner/Scanner.js:326-420", "server/providers/CustomProviderAdapter.js:19-150"},
			Claim:   "GET matchall of a library with a custom provider asks the provider once per item (mediaType, title, author), fills the metadata the books lack (narrators, publisher, year, description, genres, tags, language, isbn, asin, series) while keeping the existing title, writes the item metadata file, emits item_updated per item to users with library access, series_added, and a task_finished with updated 2.",
			Run: func(t *testing.T, e *harness.Env) {
				m := setup(t, e)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": s.podcastOnly(t, e).Token, "anonymous": ""})
				matchAll(t, e, m.lib.ID)
				harness.Eventually(t, 10*time.Second, "task_finished", func() bool { return len(a.tasks(t, "root", "task_finished", m.lib.ID)) == 1 })
				finished := a.tasks(t, "root", "task_finished", m.lib.ID)[0]
				results, _ := finished.Data["scanResults"].(map[string]any)
				if finished.IsFailed || !finished.IsFinished || results["updated"] != float64(2) || results["added"] != float64(0) || results["missing"] != float64(0) {
					t.Fatalf("task_finished: %+v", finished)
				}
				seen := m.stub.seen()
				if len(seen) != 2 {
					t.Fatalf("provider requests: %v", seen)
				}
				for _, q := range seen {
					if q.Get("mediaType") != "book" || q.Get("query") != "Hard Delete Me" || q.Get("author") != "Zed Disposable" {
						t.Fatalf("provider query: %v", q)
					}
				}
				want := bookRow{Title: "Hard Delete Me", Publisher: "Stub Press", Year: "1999", Description: "<p>Stub description</p>", Language: "English", ISBN: "9781234567897", ASIN: "B0STUB0001",
					Narrators: []string{"Stub Narrator", "Second"}, Genres: []string{"Stub Genre", "Other"}, Tags: []string{"stub"}}
				for _, id := range m.items {
					requireEqual(t, "book "+id, readBook(t, e, id), want)
					data, err := os.ReadFile(filepath.Join(e.Metadata, "items", id, "metadata.json"))
					if err != nil || !strings.Contains(string(data), "Stub Press") {
						t.Fatalf("item metadata file not updated: %v %s", err, data)
					}
				}
				series := e.Rows(t, "SELECT s.id, bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId = s.id WHERE s.libraryId = ? AND s.name = 'Stub Series'", m.lib.ID)
				if len(series) != 2 || series[0]["id"] != series[1]["id"] || series[0]["sequence"] != "4" || series[1]["sequence"] != "4" {
					t.Fatalf("series rows: %v", series)
				}
				for _, name := range []string{"root", "listener"} {
					harness.Eventually(t, eventWait, "item_updated for "+name, func() bool {
						return len(a.received(name, "item_updated")) == 2 && len(a.received(name, "series_added")) == 1
					})
					requireEqual(t, name+" item_updated", idsOnly(t, a.received(name, "item_updated")), sortedCopy(m.items))
				}
				a.silent(t, "item_updated", "limited", "anonymous")
			},
		},
		{
			Requirement: "route-r037", Invariant: "INV-ROUTE-R037-REPEAT-001", Case: "edge", State: "stateless", Assertion: "matchall-repeat-and-skip-settings",
			Sources: []string{matchRoute, matchScan, "server/scanner/Scanner.js:439-447", "server/scanner/Scanner.js:326-420"},
			Claim:   "A second matchall asks the provider again but changes nothing and emits no item_updated (details are only filled when missing); with skipMatchingMediaWithAsin or skipMatchingMediaWithIsbn the items that have an asin or isbn are not asked at all; every run ends with a finished task.",
			Run: func(t *testing.T, e *harness.Env) {
				m := setup(t, e)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				matchAll(t, e, m.lib.ID)
				harness.Eventually(t, eventWait, "first run", func() bool { return len(a.tasks(t, "root", "task_finished", m.lib.ID)) == 1 })
				a.rearm()
				before := []bookRow{readBook(t, e, m.items[0]), readBook(t, e, m.items[1])}
				if before[0].Publisher != "Stub Press" {
					t.Fatalf("the first run must have filled the books: %+v", before[0])
				}
				requests := len(m.stub.seen())
				run := func(what string, wantRequests int) taskJSON {
					a.rearm()
					matchAll(t, e, m.lib.ID)
					harness.Eventually(t, eventWait, what+" task", func() bool { return len(a.tasks(t, "root", "task_finished", m.lib.ID)) == 1 })
					requireEqual(t, what+" requests", len(m.stub.seen())-requests, wantRequests)
					requests = len(m.stub.seen())
					a.silent(t, "item_updated", "root")
					requireEqual(t, what+" books", []bookRow{readBook(t, e, m.items[0]), readBook(t, e, m.items[1])}, before)
					task := a.tasks(t, "root", "task_finished", m.lib.ID)[0]
					results, _ := task.Data["scanResults"].(map[string]any)
					if task.IsFailed || results["updated"] != float64(0) {
						t.Fatalf("%s task: %+v", what, task)
					}
					return task
				}
				run("repeat", 2)
				for _, setting := range []string{"skipMatchingMediaWithAsin", "skipMatchingMediaWithIsbn"} {
					resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+m.lib.ID, map[string]any{"settings": map[string]any{setting: true}})
					requireStatus(t, resp, http.StatusOK, "set "+setting)
					run(setting, 0)
					resp = e.Patch(t, e.Token(t, "root"), "/api/libraries/"+m.lib.ID, map[string]any{"settings": map[string]any{setting: false}})
					requireStatus(t, resp, http.StatusOK, "reset "+setting)
				}
			},
		},
	}
}

// matchFixture is a book library of two books whose provider is a recording stub.
type matchFixture struct {
	lib   scratchLibrary
	stub  *metadataStub
	items []string
}

func newMatchFixture(t *testing.T, e *harness.Env) *matchFixture {
	t.Helper()
	stub := newMetadataStub(t, stubMatch)
	resp := e.Post(t, e.Token(t, "root"), "/api/custom-metadata-providers", map[string]any{"name": unique("match-provider"), "url": stub.URL, "mediaType": "book"})
	var created struct {
		Provider struct {
			ID string `json:"id"`
		} `json:"provider"`
	}
	resp.JSON(t, &created)
	requireStatus(t, resp, http.StatusOK, "create provider")
	lib := newScratchLibrary(t, e, "match", nil, map[string]any{"provider": "custom-" + created.Provider.ID})
	for _, name := range []string{"One", "Two"} {
		copyDir(t, filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me"), filepath.Join(lib.Root, "Match Author", "Match "+name))
	}
	lib.scan(t, e, false, 2)
	m := &matchFixture{lib: lib, stub: stub}
	for _, item := range itemRows(t, e, lib.ID) {
		m.items = append(m.items, item.ID)
	}
	slices.Sort(m.items)
	return m
}

// idsOnly returns the sorted ids of event payloads (single items).
func idsOnly(t *testing.T, raws []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, raw := range raws {
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, payload.ID)
	}
	slices.Sort(out)
	return out
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
