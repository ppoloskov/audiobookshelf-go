package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/settings"
)

const (
	bookItem    = "b3c61dfc-dd4b-47ee-8998-0340c1228f52"
	bookItem2   = "7baea19e-3334-416f-b862-0cb7499e5006"
	podcastItem = "eeb6224f-1316-40c8-b8bf-00804cc90436"
	episode     = "c2b5b947-590f-4fe7-a9cd-ef18855a1ddb"
	series      = "e987a322-9111-4f79-8cb9-c25ee714e0d6"
	missingID   = "00000000-0000-4000-8000-000000000000"
)

type sentEvent struct {
	audience, name string
	payload        any
}

type rig struct {
	t      *testing.T
	db     *db.DB
	mux    *http.ServeMux
	events []sentEvent
	open   any
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d := dbtest.Fixture(t)
	r := &rig{t: t, db: d, mux: http.NewServeMux(), open: []string{}}
	emit := events.Emitter(func(audience, name string, payload any) {
		r.events = append(r.events, sentEvent{audience, name, dbtest.RoundTrip(t, payload)})
	})
	cfg := config.Config{JWTSecret: "user-test-secret"}
	st, err := settings.NewStore(context.Background(), d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(d, st, emit, cfg)
	if err != nil {
		t.Fatal(err)
	}
	New(d, a, st, emit, playback.New(d, st, cfg, emit), Deps{
		UserChanged: func(context.Context, string) error { return nil },
		UsersOnline: func() []map[string]any { return []map[string]any{{"id": "u1"}} },
	}).Routes(r.mux)
	return r
}

func (r *rig) id(username string) string {
	var id string
	if err := r.db.Get(context.Background(), &id, `SELECT id FROM users WHERE username = ?`, username); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// do sends the request as the named user and decodes a JSON answer.
func (r *rig) do(as, method, path, body string) (int, map[string]any) {
	r.t.Helper()
	req := httptest.NewRequest(method, strings.ReplaceAll(path, "{self}", r.id(as)), strings.NewReader(body))
	var row db.User
	if err := r.db.Get(req.Context(), &row, `SELECT * FROM users WHERE id = ?`, r.id(as)); err != nil {
		r.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), auth.NewPrincipal(row))))
	var doc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &doc) // text answers decode to nil
	return w.Code, doc
}

func (r *rig) status(as, method, path, body string) int {
	r.t.Helper()
	code, _ := r.do(as, method, path, body)
	return code
}

func TestStatuses(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		name, as, method, path, body string
		want                         int
	}{
		{"admin route for a user", "trial-listener", "GET", "/api/users", "", 403},
		{"online for a user", "trial-listener", "GET", "/api/users/online", "", 403},
		{"keys for a user", "trial-listener", "GET", "/api/api-keys", "", 403},
		{"user missing", "trial-root", "GET", "/api/users/" + missingID, "", 404},
		{"other user's stats", "trial-finisher", "GET", "/api/users/" + missingID + "/listening-stats", "", 403},
		{"missing user's stats", "trial-root", "GET", "/api/users/" + missingID + "/listening-stats", "", 404},
		{"session missing", "trial-listener", "DELETE", "/api/me/sessions/" + missingID, "", 404},
		{"session malformed", "trial-listener", "DELETE", "/api/me/sessions/not-a-uuid", "", 400},
		{"password keys absent", "trial-listener", "PATCH", "/api/me/password", `{}`, 400},
		{"password of wrong type", "trial-listener", "PATCH", "/api/me/password", `{"password":1,"newPassword":"x"}`, 400},
		{"password wrong", "trial-listener", "PATCH", "/api/me/password", `{"password":"wrong","newPassword":"x"}`, 400},
		{"progress of wrong type", "trial-root", "PATCH", "/api/me/progress/" + bookItem, `{"currentTime":"x"}`, 400},
		{"progress of unknown item", "trial-root", "PATCH", "/api/me/progress/" + missingID, `{}`, 404},
		{"batch empty", "trial-root", "PATCH", "/api/me/progress/batch/update", `[]`, 400},
		{"year too early", "trial-root", "GET", "/api/me/stats/year/1999", "", 400},
		{"bookmark without title", "trial-listener", "POST", "/api/me/item/" + bookItem2 + "/bookmark", `{"time":1}`, 400},
		{"bookmark of unknown item", "trial-listener", "POST", "/api/me/item/" + missingID + "/bookmark", `{"time":1,"title":"x"}`, 404},
		{"bookmark time malformed", "trial-listener", "DELETE", "/api/me/item/" + bookItem2 + "/bookmark/abc", "", 400},
		{"bookmark missing", "trial-listener", "PATCH", "/api/me/item/" + bookItem2 + "/bookmark", `{"time":99,"title":"x"}`, 404},
		{"series action unknown", "trial-listener", "GET", "/api/me/series/" + series + "/nope", "", 404},
		{"podcast progress needs an episode", "trial-listener", "PATCH", "/api/me/progress/" + podcastItem, `{"currentTime":1}`, 400},
		{"item sessions of a podcast need an episode", "trial-listener", "GET", "/api/me/item/listening-sessions/" + podcastItem, "", 404},
		{"huge page size", "trial-listener", "GET", "/api/me/listening-sessions?page=2&itemsPerPage=9223372036854775807", "", 200},
	} {
		if got := r.status(c.as, c.method, c.path, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOnlineUsersAndMe(t *testing.T) {
	r := newRig(t)
	_, doc := r.do("trial-root", "GET", "/api/users/online", "")
	if online, _ := doc["usersOnline"].([]any); len(online) != 1 {
		t.Errorf("online = %v", doc)
	}
	if sessions, ok := doc["openSessions"].([]any); !ok || len(sessions) != 0 {
		t.Errorf("open sessions = %#v, want []", doc["openSessions"])
	}
	_, me := r.do("trial-listener", "GET", "/api/me", "")
	if me["email"] != nil || me["username"] != "trial-listener" || len(me["bookmarks"].([]any)) != 2 {
		t.Errorf("me = %v", me)
	}
}

func TestUserAdministration(t *testing.T) {
	r := newRig(t)
	code, doc := r.do("trial-root", "POST", "/api/users", `{"username":"pat","password":"secret1","isActive":true,"permissions":{"download":false,"update":true},"librariesAccessible":["l1"]}`)
	perms, _ := doc["user"].(map[string]any)["permissions"].(map[string]any)
	if code != 200 || perms["download"] != false || perms["update"] != true {
		t.Fatalf("create: %d %v", code, doc)
	}
	if len(r.events) != 1 || r.events[0].audience != events.Admins || r.events[0].name != "user_added" {
		t.Errorf("events = %v", r.events)
	}
	if got := r.status("trial-root", "POST", "/api/users", `{"username":"pat","password":"x"}`); got != 400 {
		t.Errorf("duplicate name: %d", got)
	}
	if got := r.status("trial-root", "POST", "/api/users", `{"username":"z","password":"x","type":"root"}`); got != 400 {
		t.Errorf("root type: %d", got)
	}
	// An admin patch tells the acting admin, not the edited user; null permissions and email are no-ops.
	r.events = nil
	id := r.id("pat")
	if got := r.status("trial-root", "PATCH", "/api/users/"+id, `{"token":"x"}`); got != 400 {
		t.Errorf("read-only key: %d", got)
	}
	if got := r.status("trial-root", "PATCH", "/api/users/"+id, `{"bookmarks":null}`); got != 400 {
		t.Errorf("null read-only key: %d", got)
	}
	code, doc = r.do("trial-root", "PATCH", "/api/users/"+id, `{"email":null,"permissions":null,"isActive":false,"itemTagsSelected":["a"]}`)
	user, _ := doc["user"].(map[string]any)
	if code != 200 || user["isActive"] != false || user["email"] != nil || len(user["itemTagsSelected"].([]any)) != 1 || user["librariesAccessible"].([]any)[0] != "l1" {
		t.Errorf("update: %d %v", code, doc)
	}
	if len(r.events) != 1 || r.events[0].audience != r.id("trial-root") || r.events[0].name != "user_updated" {
		t.Errorf("update events = %v", r.events)
	}
	if got := r.status("trial-root", "PATCH", "/api/users/"+r.id("trial-root"), `{"username":"pat"}`); got != 400 {
		t.Errorf("taken name: %d", got)
	}
	admin := r.id("trial-root")
	if got := r.status("trial-root", "DELETE", "/api/users/"+admin, ""); got != 400 {
		t.Errorf("delete self: %d", got)
	}
	if got := r.status("trial-root", "DELETE", "/api/users/"+id, ""); got != 200 {
		t.Errorf("delete: %d", got)
	}
}

func TestDeleteUserDetachesSessionsAndRemovesPlaylists(t *testing.T) {
	r := newRig(t)
	if got := r.status("trial-root", "DELETE", "/api/users/{self}", ""); got != 400 {
		t.Fatalf("self: %d", got)
	}
	listener := r.id("trial-listener")
	if got := r.status("trial-root", "DELETE", "/api/users/"+listener, ""); got != 200 {
		t.Fatalf("delete: %d", got)
	}
	var playlists, owned, orphans int
	for query, dest := range map[string]*int{
		`SELECT COUNT(*) FROM playlists`:                                 &playlists,
		`SELECT COUNT(*) FROM playbackSessions WHERE userId IS NOT NULL`: &owned,
		`SELECT COUNT(*) FROM playbackSessions WHERE userId IS NULL`:     &orphans,
	} {
		if err := r.db.Get(context.Background(), dest, query); err != nil {
			t.Fatal(err)
		}
	}
	if playlists != 0 || owned != 0 || orphans != 7 {
		t.Errorf("playlists %d owned %d orphans %d", playlists, owned, orphans)
	}
}

func TestAPIKeys(t *testing.T) {
	r := newRig(t)
	listener := r.id("trial-listener")
	code, doc := r.do("trial-root", "POST", "/api/api-keys", `{"name":"k1","userId":"`+listener+`","isActive":true,"expiresIn":3600}`)
	key, _ := doc["apiKey"].(map[string]any)
	if code != 200 || key["apiKey"] == "" || key["isActive"] != true || key["expiresAt"] == nil || key["user"].(map[string]any)["username"] != "trial-listener" {
		t.Fatalf("create: %d %v", code, doc)
	}
	for _, body := range []string{`{"name":"k"}`, `{"name":"k","userId":"` + missingID + `"}`, `{"name":"k","userId":"` + listener + `","expiresIn":0}`} {
		if got := r.status("trial-root", "POST", "/api/api-keys", body); got != 400 {
			t.Errorf("%s: %d", body, got)
		}
	}
	id := key["id"].(string)
	code, doc = r.do("trial-root", "PATCH", "/api/api-keys/"+id, `{"isActive":false,"userId":"`+r.id("trial-finisher")+`"}`)
	if key = doc["apiKey"].(map[string]any); code != 200 || key["isActive"] != false || key["user"].(map[string]any)["username"] != "trial-finisher" {
		t.Errorf("update: %d %v", code, doc)
	}
	if _, list := r.do("trial-root", "GET", "/api/api-keys", ""); len(list["apiKeys"].([]any)) != 1 {
		t.Errorf("list = %v", list)
	}
	if r.status("trial-root", "DELETE", "/api/api-keys/"+id, "") != 200 || r.status("trial-root", "DELETE", "/api/api-keys/"+id, "") != 404 {
		t.Error("delete once, then 404")
	}
}

func TestProgressAndBookmarkEvents(t *testing.T) {
	r := newRig(t)
	body := `[{"libraryItemId":"` + bookItem2 + `","currentTime":5},{"libraryItemId":"` + missingID + `"}]`
	if got := r.status("trial-listener", "PATCH", "/api/me/progress/batch/update", body); got != 200 || len(r.events) != 1 {
		t.Errorf("batch: %d, events %v", got, r.events)
	}
	r.events = nil
	if got := r.status("trial-listener", "PATCH", "/api/me/progress/batch/update", `[{"libraryItemId":"`+missingID+`"}]`); got != 200 || len(r.events) != 0 {
		t.Errorf("batch of refused updates: %d, events %v", got, r.events)
	}
	code, saved := r.do("trial-listener", "POST", "/api/me/item/"+bookItem2+"/bookmark", `{"time":12.5,"title":"here"}`)
	if code != 200 || saved["title"] != "here" || saved["time"] != 12.5 {
		t.Fatalf("bookmark: %d %v", code, saved)
	}
	if _, again := r.do("trial-listener", "POST", "/api/me/item/"+bookItem2+"/bookmark", `{"time":12.5,"title":"there"}`); again["title"] != "there" || again["createdAt"] != saved["createdAt"] {
		t.Errorf("retitle = %v", again)
	}
	if _, doc := r.do("trial-listener", "GET", "/api/me/bookmarks/"+bookItem2, ""); len(doc["bookmarks"].([]any)) != 1 {
		t.Errorf("bookmarks = %v", doc)
	}
	if got := r.status("trial-listener", "DELETE", "/api/me/item/"+bookItem2+"/bookmark/12.5", ""); got != 200 {
		t.Errorf("delete bookmark: %d", got)
	}
	if last := r.events[len(r.events)-1]; last.name != "user_updated" || last.audience != r.id("trial-listener") {
		t.Errorf("last event = %v", last)
	}
}

func TestSeriesVisibility(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		action string
		hidden int
		events int
	}{
		{"remove-from-continue-listening", 1, 1}, {"remove-from-continue-listening", 1, 1},
		{"readd-to-continue-listening", 0, 2}, {"readd-to-continue-listening", 0, 2},
	} {
		_, doc := r.do("trial-listener", "GET", "/api/me/series/"+series+"/"+c.action, "")
		if got := len(doc["seriesHideFromContinueListening"].([]any)); got != c.hidden || len(r.events) != c.events {
			t.Errorf("%s: hidden %d events %d, want %d and %d", c.action, got, len(r.events), c.hidden, c.events)
		}
	}
}

func TestListeningStatsAndYear(t *testing.T) {
	r := newRig(t)
	_, stats := r.do("trial-listener", "GET", "/api/me/listening-stats", "")
	items := stats["items"].(map[string]any)
	if stats["totalTime"] == nil || len(items) == 0 || len(stats["recentSessions"].([]any)) != 7 {
		t.Errorf("stats = %v", stats)
	}
	if _, empty := r.do("trial-root", "GET", "/api/me/stats/year/2026", ""); empty["topAuthors"] == nil || empty["mostListenedMonth"] != nil || empty["totalListeningSessions"] != float64(0) {
		t.Errorf("empty year = %v", empty)
	}
	_, year := r.do("trial-listener", "GET", "/api/me/stats/year/2026", "")
	if year["totalListeningSessions"].(float64) < 3 || year["numBooksListened"].(float64) < 1 {
		t.Errorf("year = %v", year)
	}
}

func TestTopRanksRoundedSumsAndTiesByKey(t *testing.T) {
	got := top(totals[string]{"b": 10.4, "a": 10.4, "c": 10.6, "d": 1}, 3, "name")
	raw, _ := json.Marshal(got)
	if string(raw) != `[{"name":"c","time":11},{"name":"a","time":10},{"name":"b","time":10}]` {
		t.Errorf("top = %s", raw)
	}
	if raw, _ := json.Marshal(top(totals[int]{}, 1, "month")); string(raw) != `[]` {
		t.Errorf("empty top = %s", raw)
	}
}

func TestItemsInProgress(t *testing.T) {
	r := newRig(t)
	_, doc := r.do("trial-listener", "GET", "/api/me/items-in-progress", "")
	items := doc["libraryItems"].([]any)
	if len(items) < 3 { // two rows of the podcast and the books in progress stay distinct
		t.Fatalf("items = %d", len(items))
	}
	for _, it := range items {
		if it.(map[string]any)["progressLastUpdate"] == nil {
			t.Errorf("item without progressLastUpdate: %v", it)
		}
	}
	if _, one := r.do("trial-listener", "GET", "/api/me/items-in-progress?limit=1", ""); len(one["libraryItems"].([]any)) != 1 {
		t.Errorf("limit = %v", one)
	}
}

func TestEreaderDevices(t *testing.T) {
	r := newRig(t)
	self := r.id("trial-listener")
	device := `{"name":"k","email":"k@x.org","availabilityOption":"specificUsers","users":["` + self + `"]}`
	if got := r.status("trial-listener", "POST", "/api/me/ereader-devices", `{"ereaderDevices":[`+device+`,`+device+`]}`); got != 400 {
		t.Errorf("duplicate names: %d", got)
	}
	if got := r.status("trial-listener", "POST", "/api/me/ereader-devices", `{"ereaderDevices":[{"name":"k","email":"k@x.org","availabilityOption":"specificUsers","users":["other"]}]}`); got != 400 {
		t.Errorf("other user: %d", got)
	}
	code, doc := r.do("trial-listener", "POST", "/api/me/ereader-devices", `{"ereaderDevices":[`+device+`]}`)
	if code != 200 || len(doc["ereaderDevices"].([]any)) != 1 || r.events[len(r.events)-1].name != "ereader-devices-updated" {
		t.Errorf("save: %d %v", code, doc)
	}
}
