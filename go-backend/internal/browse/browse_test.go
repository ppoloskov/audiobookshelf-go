package browse

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/settings"

	"modernc.org/sqlite"
)

type sentEvent struct {
	Name    string
	Payload any
}

// rig is a Handler over the seeded database.
type rig struct {
	t      *testing.T
	d      *db.DB
	mux    *http.ServeMux
	store  *settings.Store
	mu     sync.Mutex
	events []sentEvent
}

func newRigOn(t *testing.T, d *db.DB) *rig {
	t.Helper()
	r := &rig{t: t, d: d, mux: http.NewServeMux()}
	var err error
	if r.store, err = settings.NewStore(context.Background(), d, func(string) string { return "" }, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	emit := events.Emitter(func(_ string, name string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, sentEvent{name, payload})
	})
	New(d, r.store, emit).Routes(r.mux)
	return r
}

func newRig(t *testing.T) *rig { return newRigOn(t, seeded(t)) }

func (r *rig) takeEvents() []sentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *rig) do(user *auth.Principal, method, target, body string) (int, []byte) {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), user)))
	return w.Code, w.Body.Bytes()
}

// get answers 200 with the decoded body of target.
func (r *rig) get(user *auth.Principal, target string, into any) {
	r.t.Helper()
	code, body := r.do(user, "GET", target, "")
	if code != 200 {
		r.t.Fatalf("GET %s = %d %s", target, code, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		r.t.Fatalf("GET %s: %v: %s", target, err, body)
	}
}

func (r *rig) status(user *auth.Principal, method, target, body string) int {
	code, _ := r.do(user, method, target, body)
	return code
}

func principal(id, typ string, perms db.Permissions, extra *db.UserExtra) *auth.Principal {
	u := db.User{ID: id, Username: db.Ptr(id), Type: db.Ptr(typ), IsActive: true}
	u.Permissions.V, u.ExtraData.V = perms, extra
	return auth.NewPrincipal(u)
}

func users() map[string]*auth.Principal {
	all := db.Permissions{AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: true, Update: true, Delete: true}
	return map[string]*auth.Principal{
		"root":  principal(rootID, "root", all, nil),
		"admin": principal(finisherID, "admin", all, &db.UserExtra{SeriesHideFromContinueListening: []string{"gs4"}}),
		// restricted sees only media tagged red, sci, x or tag, and no explicit content.
		"restricted": principal(rootID, "user", db.Permissions{LibrariesAccessible: []string{"l1", "l2", "l3", "l4"}, ItemTagsSelected: []string{"red", "sci", "x", "tag"}}, nil),
		// listener sees everything but "secret" media and explicit content.
		"listener": principal(listenerID, "user", db.Permissions{AccessAllLibraries: true, ItemTagsSelected: []string{"secret"}, SelectedTagsNotAccessible: true}, nil),
		// reader may see everything but not change it.
		"reader": principal(listenerID, "user", db.Permissions{AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: true}, nil),
	}
}

func b64(s string) string { return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(s))) }

type idList struct {
	Results []struct {
		ID string `json:"id"`
	} `json:"results"`
	Total int `json:"total"`
}

func (l idList) ids() []string {
	out := []string{}
	for _, r := range l.Results {
		out = append(out, r.ID)
	}
	return out
}

func (r *rig) items(user *auth.Principal, target string) ([]string, int) {
	r.t.Helper()
	var page idList
	r.get(user, target, &page)
	return page.ids(), page.Total
}

func TestFiltersSelectExactlyTheMatchingItems(t *testing.T) {
	r, root := newRig(t), users()["root"]
	// Order is not asserted for filters, only the set.
	for _, c := range []struct {
		name, filter string
		want         []string
	}{
		{"genre fiction", "genres." + b64("fiction"), []string{"i1", "i3", "i6"}},
		{"genre without match", "genres." + b64("none"), nil},
		{"tag red", "tags." + b64("red"), []string{"i1", "i2"}},
		{"narrator Nora", "narrators." + b64("Nora"), []string{"i1", "i2"}},
		{"language en", "languages." + b64("en"), []string{"i1", "i3", "i4"}},
		{"publisher Pub A", "publishers." + b64("Pub A"), []string{"i1", "i3"}},
		{"author a1", "authors." + b64("a1"), []string{"i1", "i3", "i5"}},
		{"series s1", "series." + b64("s1"), []string{"i1", "i2"}},
		{"no series", "series." + b64("no-series"), []string{"i6"}},
		{"explicit flag", "explicit", []string{"i2"}},
		{"abridged flag", "abridged", []string{"i2"}},
		{"missing items are issues", "issues", []string{"i3"}},
		{"decade 1990", "publishedDecades." + b64("1990"), []string{"i1", "i2"}},
		{"ebook present", "ebooks." + b64("ebook"), []string{"i2", "i3"}},
		{"supplementary files", "ebooks." + b64("supplementary"), []string{"i2"}},
		{"no audio tracks", "tracks." + b64("none"), []string{"i3", "i5"}},
		{"multiple tracks", "tracks." + b64("multi"), []string{"i2"}},
		{"single track", "tracks." + b64("single"), []string{"i1", "i4", "i6"}},
		{"missing authors", "missing." + b64("authors"), []string{"i6"}},
		{"missing asin", "missing." + b64("asin"), []string{"i2", "i4", "i6"}},
		// The root user's progress: b1 5s, b2 finished, b3 ebook 50%, b4 zero, b5 finished (the finisher's row is ignored).
		{"finished", "progress." + b64("finished"), []string{"i2", "i5"}},
		{"in progress", "progress." + b64("in-progress"), []string{"i1", "i3"}},
		// not-started: no time and not finished (an ebook-only progress row still counts as not started).
		{"not started", "progress." + b64("not-started"), []string{"i3", "i4", "i6"}},
		{"open feed", "feed-open", []string{"i2"}},
		{"shared", "share-open", []string{"i3"}},
		// URL-safe alphabet, no padding: what an app may send.
		{"url-safe unpadded", "publishers." + strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString([]byte("Pub A"))), "="), []string{"i1", "i3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ids, total := r.items(root, "/api/libraries/l1/items?filter="+c.filter)
			slices.Sort(ids)
			if !slices.Equal(ids, append([]string{}, c.want...)) || total != len(c.want) {
				t.Fatalf("ids %v total %d, want %v", ids, total, c.want)
			}
		})
	}
	for filter, status := range map[string]int{"bogus.x": 501, "ebooks." + b64("bogus"): 501, "missing." + b64("bogus"): 501, "publishedDecades." + b64("abc"): 501} {
		if got := r.status(root, "GET", "/api/libraries/l1/items?filter="+filter, ""); got != status {
			t.Errorf("filter %s: status %d, want %d", filter, got, status)
		}
	}
	if got := r.status(root, "GET", "/api/libraries/l1/items?sort=bogus", ""); got != 400 {
		t.Errorf("unknown book sort: status %d, want 400", got)
	}
}

func TestSortsOrderItemsByTheirKey(t *testing.T) {
	root := users()["root"]
	for _, tc := range []struct {
		name, query string
		ignore      bool
		want        []string
	}{
		{"added asc", "sort=addedAt&desc=0", false, []string{"i1", "i2", "i3", "i4", "i5", "i6"}},
		{"added desc", "sort=addedAt&desc=1", false, []string{"i6", "i5", "i4", "i3", "i2", "i1"}},
		// sizes: b1 100, b2 300, b3 200, b4 50, b5 400, b6 10
		{"size asc", "sort=size&desc=0", false, []string{"i6", "i4", "i1", "i3", "i2", "i5"}},
		// durations: b1 10, b2 20, b3 5, b4 30, b5 0, b6 15
		{"duration desc", "sort=media.duration&desc=1", false, []string{"i4", "i2", "i6", "i1", "i3", "i5"}},
		// Titles compare case-insensitively on the title column, or without the prefix when the server ignores prefixes.
		{"title", "sort=media.metadata.title&desc=0", false, []string{"i3", "i6", "i2", "i4", "i5", "i1"}},
		{"title ignoring prefix", "sort=media.metadata.title&desc=0", true, []string{"i1", "i6", "i2", "i4", "i5", "i3"}},
		// Series sequence within s2: b4 #1, b5 #2.5, b3 #3.
		{"sequence in series", "sort=sequence&desc=0&filter=series." + b64("s2"), false, []string{"i4", "i5", "i3"}},
		{"sequence in series desc", "sort=sequence&desc=1&filter=series." + b64("s2"), false, []string{"i3", "i5", "i4"}},
		{"page", "sort=addedAt&limit=2&page=1", false, []string{"i3", "i4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if tc.ignore {
				if _, err := r.store.UpdateServer(context.Background(), func(s *settings.Server) error { s.SortingIgnorePrefix = true; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			ids, _ := r.items(root, "/api/libraries/l1/items?"+tc.query)
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("order %v, want %v", ids, tc.want)
			}
		})
	}
}

// Whatever the filter, an item the user may not see never appears.
func TestListsNeverLeakInaccessibleItems(t *testing.T) {
	r, restricted := newRig(t), users()["restricted"]
	// Tag allow-list red/sci/x/tag and no explicit content: b1 (red) in l1, p1 (sci) in l2.
	for _, c := range []struct {
		target string
		want   []string
	}{
		{"/api/libraries/l1/items", []string{"i1"}},
		{"/api/libraries/l1/items?filter=explicit", nil},
		{"/api/libraries/l1/items?sort=size&desc=1&collapseseries=1", []string{"i1"}},
		{"/api/libraries/l2/items", []string{"pip1"}},
	} {
		ids, total := r.items(restricted, c.target)
		if !slices.Equal(ids, append([]string{}, c.want...)) || total != len(c.want) {
			t.Errorf("%s: ids %v total %d, want %v", c.target, ids, total, c.want)
		}
	}
	denied := principal(rootID, "user", db.Permissions{LibrariesAccessible: []string{"l2"}, AccessAllTags: true, AccessExplicitContent: true}, nil)
	for _, target := range []string{"/api/libraries/l1/items", "/api/libraries/l1/personalized", "/api/libraries/l1/search?q=a", "/api/libraries/l1/series"} {
		if got := r.status(denied, "GET", target, ""); got != 403 {
			t.Errorf("%s: a library the user cannot access answered %d", target, got)
		}
	}
	if got := r.status(users()["root"], "GET", "/api/libraries/nope/items", ""); got != 404 {
		t.Errorf("unknown library answered %d", got)
	}
	for _, q := range []string{"limit=-1", "page=1.5"} {
		if got := r.status(users()["root"], "GET", "/api/libraries/l1/items?"+q, ""); got != 400 {
			t.Errorf("%s answered %d", q, got)
		}
	}
}

func TestPodcastItemsIncludeNumEpisodesIncomplete(t *testing.T) {
	r, reader := newRig(t), users()["reader"]
	// Each cast has 3 episodes and the reader finished one of each.
	incomplete := func(target string) map[string]float64 {
		var page struct {
			Results []map[string]any `json:"results"`
		}
		r.get(reader, target, &page)
		got := map[string]float64{}
		for _, row := range page.Results {
			if v, ok := row["numEpisodesIncomplete"]; ok {
				got[row["id"].(string)] = v.(float64)
			}
		}
		return got
	}
	got := incomplete("/api/libraries/l4/items?include=numepisodesincomplete")
	if len(got) != 3 || got["igp1"] != 2 || got["igp2"] != 2 || got["igp3"] != 2 {
		t.Fatalf("with include: %v, want 2 for each", got)
	}
	if got := incomplete("/api/libraries/l4/items"); len(got) != 0 {
		t.Fatalf("without include: %v", got)
	}
}

func TestIncludeRSSFeedAndShareOnItemsAndShelves(t *testing.T) {
	r := newRig(t)
	type row struct {
		ID    string          `json:"id"`
		Feed  json.RawMessage `json:"rssFeed"`
		Share json.RawMessage `json:"mediaItemShare"`
	}
	marks := func(rows []row) map[string]string {
		out := map[string]string{}
		for _, it := range rows {
			if len(it.Feed) > 0 {
				out[it.ID] += "F"
			}
			if len(it.Share) > 0 {
				out[it.ID] += "S"
			}
		}
		return out
	}
	page := func(user *auth.Principal, q string) map[string]string {
		var body struct {
			Results []row `json:"results"`
		}
		r.get(user, "/api/libraries/l3/items?minified=1"+q, &body)
		return marks(body.Results)
	}
	// gi2 has a feed and an expired share, gi3 an open share.
	if got := page(users()["admin"], "&include=rssfeed,share"); got["gi2"] != "F" || got["gi3"] != "S" || got["gi1"] != "" {
		t.Errorf("admin page: %v", got)
	}
	if got := page(users()["reader"], "&include=rssfeed,share"); got["gi2"] != "F" || got["gi3"] != "" {
		t.Errorf("member page: %v", got)
	}
	if got := page(users()["admin"], ""); len(got) != 0 {
		t.Errorf("no include: %v", got)
	}
	var shelves []struct {
		ID       string `json:"id"`
		Entities []row  `json:"entities"`
	}
	r.get(users()["admin"], "/api/libraries/l3/personalized?minified=1&include=rssfeed,share", &shelves)
	for _, shelf := range shelves {
		if shelf.ID == "recently-added" {
			if got := marks(shelf.Entities); got["gi2"] != "F" || got["gi3"] != "S" {
				t.Errorf("recently-added: %v", got)
			}
			return
		}
	}
	t.Fatal("no recently-added shelf")
}

func shelfIDs(t *testing.T, r *rig, user *auth.Principal, path string) map[string][]string {
	t.Helper()
	var shelves []struct {
		ID       string `json:"id"`
		Entities []struct {
			ID string `json:"id"`
		} `json:"entities"`
	}
	r.get(user, path, &shelves)
	out := map[string][]string{}
	for _, s := range shelves {
		ids := []string{}
		for _, e := range s.Entities {
			ids = append(ids, e.ID)
		}
		out[s.ID] = ids
	}
	return out
}

func TestPersonalizedShelvesFollowProgressAndPermissionRules(t *testing.T) {
	r, us := newRig(t), users()
	l := shelfIDs(t, r, us["listener"], "/api/libraries/l3/personalized")
	// listener: g10 3s (02-06), g5 10s (02-03) unfinished audio; g4 ebook 50%; g1/g7 finished audio; g9 finished ebook.
	for shelf, want := range map[string][]string{
		"continue-listening": {"gi10", "gi5"}, "continue-reading": {"gi4"},
		"listen-again": {"gi1"}, // gi7 is finished but tagged secret
		"read-again":   {"gi9"},
	} {
		if !slices.Equal(l[shelf], want) {
			t.Errorf("%s %v, want %v", shelf, l[shelf], want)
		}
	}
	for id, ids := range l {
		for _, hidden := range []string{"gi6", "gi7"} { // explicit, secret
			if slices.Contains(ids, hidden) {
				t.Errorf("shelf %s leaks %s", id, hidden)
			}
		}
	}
	root := shelfIDs(t, r, us["root"], "/api/libraries/l3/personalized")
	if want := []string{"gi3"}; !slices.Equal(root["continue-listening"], want) { // root hid g5 from it
		t.Errorf("root continue-listening %v, want %v", root["continue-listening"], want)
	}
	if !slices.Contains(root["recently-added"], "gi6") || slices.Contains(root["recently-added"], "gi1") {
		t.Errorf("root recently-added %v", root["recently-added"])
	}
	for id, ids := range shelfIDs(t, r, us["root"], "/api/libraries/l3/personalized?limit=2") {
		if id != "recent-series" && len(ids) > 2 {
			t.Errorf("shelf %s has %d entities with limit=2", id, len(ids))
		}
	}
	if p := shelfIDs(t, r, us["listener"], "/api/libraries/l4/personalized"); !slices.Equal(p["continue-listening"], []string{"igp1"}) {
		t.Errorf("podcast continue-listening %v", p["continue-listening"])
	}
	// Continue series: the admin finished g1 and g2 (gs1 #1 and #2, gs2 #3), so the next books are g3 (gs1 #10) and g5 (gs2 #1).
	if c := slices.Sorted(slices.Values(shelfIDs(t, r, us["admin"], "/api/libraries/l3/personalized")["continue-series"])); !slices.Equal(c, []string{"gi3", "gi5"}) {
		t.Errorf("continue-series %v, want gi3 and gi5", c)
	}
}

// The shelves send the continued series and blank names the way the clients read them.
func TestShelfEntityShapes(t *testing.T) {
	r := newRig(t)
	var shelves []struct {
		ID       string            `json:"id"`
		Type     string            `json:"type"`
		Total    int               `json:"total"`
		Entities []json.RawMessage `json:"entities"`
	}
	r.get(users()["admin"], "/api/libraries/l3/personalized", &shelves)
	byID := map[string]int{}
	for i, s := range shelves {
		byID[s.ID] = i
	}
	var series []struct {
		ID    string `json:"id"`
		Books []struct {
			Media struct {
				Metadata struct{ AuthorName, SeriesName string } `json:"metadata"`
			} `json:"media"`
		} `json:"books"`
	}
	if i, ok := byID["recent-series"]; !ok {
		t.Fatal("no recent-series shelf")
	} else if err := json.Unmarshal(mustJSON(t, shelves[i].Entities), &series); err != nil || len(series) == 0 || len(series[0].Books) == 0 {
		t.Fatalf("recent-series %v %+v", err, series)
	} else if m := series[0].Books[0].Media.Metadata; m.AuthorName != "" || m.SeriesName != "" {
		t.Errorf("recent-series books keep names: %+v", m)
	}
	var cont []struct {
		Media struct {
			Metadata struct {
				SeriesName string `json:"seriesName"`
				Series     struct{ ID, Name string }
			} `json:"metadata"`
		} `json:"media"`
	}
	if i, ok := byID["continue-series"]; !ok {
		t.Fatal("no continue-series shelf")
	} else if err := json.Unmarshal(mustJSON(t, shelves[i].Entities), &cont); err != nil || cont[0].Media.Metadata.Series.ID == "" || cont[0].Media.Metadata.SeriesName != "" {
		t.Errorf("continue-series %v %+v", err, cont)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// countedConnector counts the queries that return rows.
type countedConnector struct {
	path  string
	count *atomic.Int64
}

func (c countedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	return countedConn{conn.(queryConn), c.count}, nil
}

func (c countedConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type queryConn interface {
	driver.Conn
	driver.QueryerContext
}

type countedConn struct {
	queryConn
	count *atomic.Int64
}

func (c countedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.count.Add(1)
	return c.queryConn.QueryContext(ctx, query, args)
}

// The item page and the series list load their rows in batches: the number
// of reads must not grow with the page size.
func TestPagesReadAConstantNumberOfQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	var count atomic.Int64
	sqlDB := sql.OpenDB(countedConnector{path, &count})
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(db.Schema); err != nil {
		t.Fatal(err)
	}
	d := db.New(sqlDB)
	seed(t, d)
	r, root := newRigOn(t, d), users()["root"]
	for _, target := range []string{"/api/libraries/l3/items", "/api/libraries/l3/series", "/api/libraries/l3/personalized", "/api/libraries/l3/search?q=g"} {
		counts := map[int]int64{}
		for _, limit := range []int{1, 8} {
			count.Store(0)
			if code, body := r.do(root, "GET", fmt.Sprintf("%s%slimit=%d", target, map[bool]string{true: "&", false: "?"}[strings.Contains(target, "?")], limit), ""); code != 200 {
				t.Fatalf("%s: %d %s", target, code, body)
			}
			counts[limit] = count.Load()
		}
		if counts[1] > counts[8] || counts[8] > 70 {
			t.Errorf("%s reads %v", target, counts)
		}
		if !strings.Contains(target, "personalized") && !strings.Contains(target, "search") && counts[1] != counts[8] {
			t.Errorf("%s reads grow with the page size: %v", target, counts)
		}
	}
}

func TestDecodeValueAcceptsWhatClientsSend(t *testing.T) {
	for in, want := range map[string]string{
		"bm8tc2VyaWVz": "no-series", "bm8tc2VyaWVz==": "no-series", "YWJj": "abc", "YWI=": "ab", "YWI": "ab", "YW": "a",
		"YW-_": "ao\xbf", "YW+/": "ao\xbf", "": "", "Y": "", "!!!": "",
	} {
		if got := decodeValue(in); got != want {
			t.Errorf("decodeValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// The expectations are the output of upstream's naturalSort + reduce code
// (libraryHelpers.js:236-257) run in Node on each input.
func TestSequenceListMatchesUpstream(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"1", "2", "3"}, "1-3"},
		{[]string{"3", "1", "2", "5"}, "1-3, 5"},
		{[]string{"1", "3"}, "1, 3"},
		{[]string{"1", "1.5", "2"}, "1, 1.5, 2"},
		{[]string{"2", "10", "9", "1"}, "1-2, 9-10"},
		{[]string{"1a", "1", "2", "b", "A"}, "1, 1a, 2, A, b"},
		{[]string{"01", "2", "03"}, "1-3"},
		{[]string{"1", "1"}, "1, 1"},
		{[]string{".5", "1.5", "2.5"}, "0.5-2.5"},
		{[]string{"1.", "2"}, "1-2"},
		{[]string{"Book 2", "Book 10", "Book 1"}, "Book 1, Book 2, Book 10"},
		{[]string{"0", "1", "2", "x", "4", "5"}, "0-2, 4-5, x"},
	} {
		c := &subSeries{filterSequence: map[string]*string{}}
		for i, s := range tc.in {
			b := &subBook{}
			b.it.ID = fmt.Sprint("i", i)
			c.books = append(c.books, b)
			c.filterSequence[b.it.ID] = &s
		}
		if got := c.sequenceList(); got != tc.want {
			t.Errorf("sequenceList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// subseriesRig has series "Mega" with books A (1), B (2), C (3) and D (no
// sequence), where A and B also belong to sub-series "Sub" (1, 2).
func subseriesRig(t *testing.T) *rig {
	t.Helper()
	d := dbtest.New(t)
	s := seeder{t, d}
	s.ins("libraries", "id", "lib1", "name", "Lib", "displayOrder", 0, "mediaType", "book", "provider", "google", "settings", "{}", "extraData", "{}", "createdAt", old, "updatedAt", old)
	s.ins("libraryFolders", "id", "f1", "path", "/books", "libraryId", "lib1", "createdAt", old, "updatedAt", old)
	s.series("mega", "Mega", "Mega", "lib1", old)
	s.series("sub", "The Sub", "Sub, The", "lib1", old)
	for i, title := range []string{"Alpha", "Bravo", "Charlie", "Delta"} {
		id := string(rune('A' + i))
		s.book(bookRow{id: id, itemID: "li" + id, title: title, titleIgnore: title, duration: float64(100-i*10) + 0.5, narrators: "[]", audio: "[]", ebook: "null", tags: "[]",
			genres: "[]", size: 10 * (i + 1), files: "[]", created: old, lib: "lib1", folder: "f1", mtimeDay: 1})
	}
	// Linked in this order: D before C, which decides ties.
	s.linkSeries("A", "mega", "1")
	s.linkSeries("B", "mega", "2")
	s.linkSeries("D", "mega", nil)
	s.linkSeries("C", "mega", "3")
	s.linkSeries("A", "sub", "1")
	s.linkSeries("B", "sub", "2")
	return newRigOn(t, d)
}

// A and B collapse behind the sub-series entry at A's place, the default order
// is sequence (a missing one compares as "null"), invalid sorts are dropped
// from the payload, and pagination slices the sorted list.
func TestCollapseSubseriesFilteredPage(t *testing.T) {
	r, root := subseriesRig(t), users()["root"]
	type page struct {
		Results []struct {
			ID              string `json:"id"`
			CollapsedSeries *struct {
				Name               string   `json:"name"`
				NameIgnorePrefix   string   `json:"nameIgnorePrefix"`
				LibraryItemIDs     []string `json:"libraryItemIds"`
				NumBooks           int      `json:"numBooks"`
				SeriesSequenceList string   `json:"seriesSequenceList"`
			} `json:"collapsedSeries"`
			Media struct {
				Metadata struct {
					Series struct {
						ID       string  `json:"id"`
						Sequence *string `json:"sequence"`
					} `json:"series"`
				} `json:"metadata"`
			} `json:"media"`
		} `json:"results"`
		Total  int     `json:"total"`
		SortBy *string `json:"sortBy"`
	}
	get := func(query string) (page, string) {
		var p page
		r.get(root, "/api/libraries/lib1/items?minified=1&collapseseries=1&filter=series."+b64("mega")+query, &p)
		var ids []string
		for _, res := range p.Results {
			ids = append(ids, res.ID)
		}
		return p, strings.Join(ids, ",")
	}
	if p, got := get(""); got != "liA,liC,liD" || p.Total != 3 { // A (collapsed, #1), C (#3), D (no sequence last)
		t.Errorf("default order %s total %d", got, p.Total)
	} else if c := p.Results[0].CollapsedSeries; c == nil || c.Name != "The Sub" || c.NameIgnorePrefix != "Sub, The" || c.NumBooks != 2 ||
		!slices.Equal(c.LibraryItemIDs, []string{"liA", "liB"}) || c.SeriesSequenceList != "1-2" {
		t.Errorf("collapsedSeries %+v", c)
	} else if m := p.Results[0].Media.Metadata.Series; m.ID != "mega" || m.Sequence == nil || *m.Sequence != "1" {
		t.Errorf("metadata.series %+v", m)
	}
	if p, got := get("&sort=media.metadata.title&desc=1"); got != "liA,liD,liC" || p.SortBy == nil {
		t.Errorf("title desc %s", got)
	}
	if _, got := get("&sort=media.duration&desc=1"); got != "liC,liD,liA" { // collapsed entries after plain books
		t.Errorf("duration desc %s", got)
	}
	// A sort that cannot tell items apart keeps the link order: D was linked before C.
	if _, got := get("&sort=addedAt"); got != "liD,liC,liA" {
		t.Errorf("addedAt (ties) %s", got)
	}
	if p, got := get("&sort=bogus"); got != "liA,liC,liD" || p.SortBy != nil {
		t.Errorf("invalid sort %s %v", got, p.SortBy)
	}
	if _, got := get("&limit=2&page=1"); got != "liD" {
		t.Errorf("page 1 %s", got)
	}
	// The sub-series itself: its books belong to Mega too, and a single collapsed entry stays uncollapsed.
	code, body := r.do(root, "GET", "/api/libraries/lib1/items?collapseseries=1&filter=series."+b64("sub"), "")
	if code != 200 || !strings.Contains(string(body), `"total":2`) || strings.Contains(string(body), `"collapsedSeries"`) {
		t.Errorf("sub-series alone: %d %s", code, body)
	}
}

// Collapsing series on the plain page hides all but the first book of a series.
func TestCollapseSeriesHidesLaterBooks(t *testing.T) {
	r, root := newRig(t), users()["root"]
	var page struct {
		Results []struct {
			ID              string `json:"id"`
			CollapsedSeries *struct {
				ID       string `json:"id"`
				NumBooks int    `json:"numBooks"`
			} `json:"collapsedSeries"`
		} `json:"results"`
		Total int `json:"total"`
	}
	r.get(root, "/api/libraries/l1/items?collapseseries=1&sort=addedAt", &page)
	// s1: b1 #1 first, b2 hidden; s2: b4 #1 first, b5 #2.5 and b3 #3 hidden; b6 has no series.
	var got []string
	for _, res := range page.Results {
		got = append(got, res.ID)
		if res.ID == "i4" && (res.CollapsedSeries == nil || res.CollapsedSeries.ID != "s2" || res.CollapsedSeries.NumBooks != 3) {
			t.Errorf("i4 collapsedSeries %+v", res.CollapsedSeries)
		}
	}
	if want := []string{"i1", "i4", "i6"}; !slices.Equal(got, want) || page.Total != 3 {
		t.Errorf("collapsed page %v total %d, want %v", got, page.Total, want)
	}
}

func TestSearch(t *testing.T) {
	r, root := newRig(t), users()["root"]
	var books struct {
		Book []struct {
			LibraryItem struct{ ID string } `json:"libraryItem"`
		} `json:"book"`
		Narrators []struct {
			Name     string
			NumBooks int
		} `json:"narrators"`
		Tags    []struct{ Name string } `json:"tags"`
		Authors []struct{ ID string }   `json:"authors"`
		Series  []struct {
			Series struct{ ID string }   `json:"series"`
			Books  []struct{ ID string } `json:"books"`
		} `json:"series"`
	}
	r.get(root, "/api/libraries/l1/search?q=ALPHA", &books)
	if len(books.Book) != 2 || len(books.Series) != 1 || books.Series[0].Series.ID != "s1" || len(books.Series[0].Books) != 2 {
		t.Errorf("alpha: %+v", books)
	}
	r.get(root, "/api/libraries/l1/search?q=nora", &books)
	if len(books.Narrators) != 1 || books.Narrators[0].NumBooks != 2 {
		t.Errorf("nora: %+v", books.Narrators)
	}
	r.get(root, "/api/libraries/l1/search?q=ada", &books)
	if len(books.Authors) != 1 || books.Authors[0].ID != "a1" {
		t.Errorf("ada: %+v", books.Authors)
	}
	var casts struct {
		Podcast []struct {
			LibraryItem struct{ ID string } `json:"libraryItem"`
		} `json:"podcast"`
		Episodes []struct {
			LibraryItem struct {
				ID            string
				RecentEpisode struct{ ID string } `json:"recentEpisode"`
				Media         struct{ Episodes []any }
			} `json:"libraryItem"`
		} `json:"episodes"`
	}
	r.get(root, "/api/libraries/l4/search?q=episode%20gp1-2", &casts)
	if len(casts.Podcast) != 0 || len(casts.Episodes) != 1 || casts.Episodes[0].LibraryItem.RecentEpisode.ID != "gegp12" || len(casts.Episodes[0].LibraryItem.Media.Episodes) != 0 {
		t.Errorf("episodes: %+v", casts)
	}
	for _, q := range []string{"", "%20"} {
		if got := r.status(root, "GET", "/api/libraries/l1/search?q="+q, ""); got != 400 {
			t.Errorf("empty query answered %d", got)
		}
	}
}
