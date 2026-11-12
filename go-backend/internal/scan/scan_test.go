package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/settings"
)

func TestMain(m *testing.M) {
	watcherPendingDelay, watcherFilePoll = 300*time.Millisecond, 20*time.Millisecond
	os.Exit(m.Run())
}

type event struct {
	Audience, Name string
	Payload        json.RawMessage
}

type rig struct {
	t      *testing.T
	d      *db.DB
	s      *Scanner
	root   string
	meta   string
	mu     sync.Mutex
	events []event
}

func (r *rig) emit(audience, name string, payload any) {
	raw, _ := json.Marshal(payload)
	r.mu.Lock()
	r.events = append(r.events, event{audience, name, raw})
	r.mu.Unlock()
}

// take returns the events emitted so far and forgets them.
func (r *rig) take() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *rig) names() []string {
	var out []string
	for _, e := range r.take() {
		out = append(out, e.Name)
	}
	return out
}

const fakeProbe = "#!/bin/sh\nfor a; do f=\"$a\"; done\nn=$(wc -c < \"$f\" | tr -d '[:space:]')\nprintf '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"%s\",\"bit_rate\":\"64000\",\"tags\":{\"title\":\"T-%s\"}},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\",\"channels\":2}]}' \"$n\" \"$n\"\n"

func fakeFFprobe(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", path)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	fakeFFprobe(t, fakeProbe)
	r := &rig{t: t, d: dbtest.New(t), root: t.TempDir(), meta: t.TempDir()}
	for _, q := range []string{
		`INSERT INTO libraries(id,name,mediaType,settings,createdAt,updatedAt) VALUES('lib-1','Books','book','{}','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-1','` + r.root + `','lib-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := r.d.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	st, err := settings.NewStore(context.Background(), r.d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.s = New(r.d, st, r.emit, filterdata.NewCache(), config.Config{MetadataPath: r.meta})
	t.Cleanup(r.s.Close)
	return r
}

func (r *rig) write(rel, body string) string {
	r.t.Helper()
	p := filepath.Join(r.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) scan(force bool) Result {
	r.t.Helper()
	res, err := r.s.Scan(context.Background(), "lib-1", force)
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.d.Get(context.Background(), &n, query, args...); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) item(path string) db.LibraryItem {
	r.t.Helper()
	var it db.LibraryItem
	if err := r.d.Get(context.Background(), &it, `SELECT * FROM libraryItems WHERE path = ?`, filepath.Join(r.root, filepath.FromSlash(path))); err != nil {
		r.t.Fatalf("no item at %s: %v", path, err)
	}
	return it
}

func TestScanImportsBooksAndKeepsIdentityOnRename(t *testing.T) {
	r := newRig(t)
	r.write("Author/Book/01.mp3", "aaaa")
	r.write("Author/Book/02.mp3", "bbbbbb")
	r.write("Author/Book/cover.jpg", "img")
	if res := r.scan(false); res.Added != 1 {
		t.Fatalf("first scan = %+v", res)
	}
	it := r.item("Author/Book")
	if got := r.count(`SELECT COUNT(*) FROM bookAuthors`); got != 1 {
		t.Fatalf("author links = %d", got)
	}
	if res := r.scan(false); res != (Result{}) {
		t.Fatalf("unchanged rescan = %+v", res)
	}
	if err := os.Rename(filepath.Join(r.root, "Author/Book"), filepath.Join(r.root, "Author/Renamed")); err != nil {
		t.Fatal(err)
	}
	if res := r.scan(false); res.Updated != 1 || res.Added != 0 || res.Missing != 0 {
		t.Fatalf("rename scan = %+v", res)
	}
	if moved := r.item("Author/Renamed"); moved.ID != it.ID {
		t.Fatalf("renamed folder got a new item id %s, want %s", moved.ID, it.ID)
	}
}

func TestMissingIsStampedOnceAndAFolderComesBack(t *testing.T) {
	r := newRig(t)
	r.write("A/Book/a.mp3", "aaaa")
	r.scan(false)
	id := r.item("A/Book").ID
	if err := os.RemoveAll(filepath.Join(r.root, "A/Book")); err != nil {
		t.Fatal(err)
	}
	r.take()
	if res := r.scan(false); res.Missing != 1 {
		t.Fatalf("scan = %+v", res)
	}
	if !r.item("A/Book").IsMissing {
		t.Fatal("item not marked missing")
	}
	first := r.take()
	if res := r.scan(false); res.Missing != 1 {
		t.Fatalf("second scan = %+v", res)
	}
	for _, e := range r.take() {
		if e.Name == "items_updated" {
			t.Fatalf("a missing item announced again: %s", e.Payload)
		}
	}
	if len(first) == 0 {
		t.Fatal("no events for the first missing scan")
	}
	r.write("A/Book/a.mp3", "aaaa")
	if res := r.scan(false); res.Updated != 1 {
		t.Fatalf("scan after return = %+v", res)
	}
	if it := r.item("A/Book"); it.ID != id || it.IsMissing {
		t.Fatalf("returned item = %s missing=%v", it.ID, it.IsMissing)
	}
}

func TestScanEmitsItemsInChunksOfTen(t *testing.T) {
	r := newRig(t)
	for i := range 11 {
		r.write(fmt.Sprintf("Book %02d/track.mp3", i), fmt.Sprint("x", i))
	}
	r.scan(false)
	sizes := func(name string) (out []int) {
		for _, e := range r.take() {
			if e.Name == name {
				var items []json.RawMessage
				_ = json.Unmarshal(e.Payload, &items)
				out = append(out, len(items))
			}
		}
		return out
	}
	r.take()
	for i := range 11 {
		r.write(fmt.Sprintf("Book %02d/track.mp3", i), fmt.Sprint("changed-", i))
	}
	r.scan(false)
	if got := sizes("items_updated"); fmt.Sprint(got) != "[10 1]" {
		t.Fatalf("items_updated chunks = %v", got)
	}
}

func TestRescanSweepsAuthorsAndSeriesTheBookLeft(t *testing.T) {
	r := newRig(t)
	r.write("Who/Saga/Vol 1 - One/a.mp3", "aaa")
	r.scan(false)
	if r.count(`SELECT COUNT(*) FROM series`) != 1 || r.count(`SELECT COUNT(*) FROM authors WHERE name = 'Who'`) != 1 {
		t.Fatal("author and series not created")
	}
	r.take()
	r.write("Who/Saga/Vol 1 - One/metadata.json", `{"authors":["Jane Doe"],"series":["Other #3"]}`)
	r.scan(false)
	if got := r.count(`SELECT COUNT(*) FROM authors WHERE name = 'Who'`) + r.count(`SELECT COUNT(*) FROM series WHERE name = 'Saga'`); got != 0 {
		t.Fatalf("emptied author and series survived: %d", got)
	}
	names := strings.Join(r.names(), ",")
	for _, want := range []string{"author_added", "author_removed", "series_removed", "items_updated"} {
		if !strings.Contains(names, want) {
			t.Fatalf("events %s lack %s", names, want)
		}
	}
}

func TestMetadataJSONSeriesStringsAreRead(t *testing.T) {
	r := newRig(t)
	r.write("A/Book/a.mp3", "aaa")
	r.write("A/Book/metadata.json", `{"title":"From Sidecar","authors":["Jane Doe"],"series":["Saga #2","Other"],"chapters":[{"id":0,"start":0,"end":1,"title":"One"}]}`)
	r.scan(false)
	var rows []struct {
		Name     string  `db:"name"`
		Sequence *string `db:"sequence"`
	}
	if err := r.d.Select(context.Background(), &rows, `SELECT s.name AS name, x.sequence AS sequence FROM bookSeries x JOIN series s ON s.id = x.seriesId`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "Saga" || rows[0].Sequence == nil || *rows[0].Sequence != "2" {
		t.Fatalf("series = %+v", rows)
	}
	var title string
	if err := r.d.Get(context.Background(), &title, `SELECT title FROM libraryItems WHERE path = ?`, filepath.Join(r.root, "A/Book")); err != nil || title != "From Sidecar" {
		t.Fatalf("title = %q, %v", title, err)
	}
}

func TestWriteMetadataIsReadableAndRoundTrips(t *testing.T) {
	r := newRig(t)
	r.write("Jane Doe/Saga/Vol 1 - One/a.mp3", "aaa")
	r.scan(false)
	it := r.item("Jane Doe/Saga/Vol 1 - One")
	raw, err := os.ReadFile(filepath.Join(r.meta, "items", it.ID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := ParseMetadataJSON(raw)
	if len(doc.Authors) != 1 || doc.Authors[0] != "Jane Doe" || len(doc.Series) != 1 || doc.Series[0] != (SeriesEntry{"Saga", "1"}) {
		t.Fatalf("written sidecar = %s", raw)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if s, _ := generic["series"].([]any); len(s) != 1 || s[0] != "Saga #1" {
		t.Fatalf("series must be written as strings: %v", generic["series"])
	}
	for _, key := range []string{"explicit", "abridged", "tags", "chapters"} {
		if _, ok := generic[key]; !ok {
			t.Fatalf("sidecar lacks %q", key)
		}
	}
}

func TestPodcastEpisodesAreAddedAndRemoved(t *testing.T) {
	r := newRig(t)
	pod := t.TempDir()
	for _, q := range []string{
		`INSERT INTO libraries(id,name,mediaType,settings,createdAt,updatedAt) VALUES('lib-2','Pods','podcast','{}','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-2','` + pod + `','lib-2','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := r.d.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"Show/ep1.mp3": "one", "Show/ep2.mp3": "twotwo"} {
		if err := os.MkdirAll(filepath.Join(pod, "Show"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scan := func() Result {
		res, err := r.s.Scan(context.Background(), "lib-2", false)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := scan(); res.Added != 1 || r.count(`SELECT COUNT(*) FROM podcastEpisodes`) != 2 {
		t.Fatalf("scan = %+v episodes=%d", res, r.count(`SELECT COUNT(*) FROM podcastEpisodes`))
	}
	if err := os.Remove(filepath.Join(pod, "Show/ep1.mp3")); err != nil {
		t.Fatal(err)
	}
	scan()
	if got := r.count(`SELECT COUNT(*) FROM podcastEpisodes WHERE title = 'ep1'`); got != 0 {
		t.Fatal("episode of a deleted file survived")
	}
	if res := scan(); res.Missing != 0 {
		t.Fatalf("existing podcast folder reported %+v", res)
	}
}

func (r *rig) running() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return len(r.s.runs) > 0
}

// admin serves a request through the routes as an admin.
func (r *rig) admin(method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	r.s.Routes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	user := db.User{ID: "u1", Type: db.Ptr("root"), IsActive: true}
	req = req.WithContext(auth.WithUser(req.Context(), auth.NewPrincipal(user)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestRoutes(t *testing.T) {
	r := newRig(t)
	r.write("A/Book/a.mp3", "aaa")
	if w := r.admin("POST", "/api/libraries/nope/scan", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown library = %d", w.Code)
	}
	if w := r.admin("POST", "/api/libraries/lib-1/scan", ""); w.Code != http.StatusOK || w.Body.String() != "OK" {
		t.Fatalf("library scan = %d %q", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for r.count(`SELECT COUNT(*) FROM libraryItems`) == 0 || r.running() {
		if time.Now().After(deadline) {
			t.Fatal("route scan never imported the book")
		}
		time.Sleep(10 * time.Millisecond)
	}
	id := r.item("A/Book").ID
	if w := r.admin("POST", "/api/items/"+id+"/scan", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "UPTODATE") {
		t.Fatalf("item scan = %d %q", w.Code, w.Body.String())
	}
	if w := r.admin("POST", "/api/items/nope/scan", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown item = %d", w.Code)
	}
	if w := r.admin("POST", "/api/items/batch/scan", `{"libraryItemIds":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty batch = %d", w.Code)
	}
	if w := r.admin("POST", "/api/items/batch/scan", `{"libraryItemIds":["`+id+`"]}`); w.Code != http.StatusOK {
		t.Fatalf("batch = %d", w.Code)
	}
	plain := httptest.NewRecorder()
	mux := http.NewServeMux()
	r.s.Routes(mux)
	mux.ServeHTTP(plain, httptest.NewRequest("POST", "/api/libraries/lib-1/scan", nil))
	if plain.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated = %d", plain.Code)
	}
}

// gatedFFprobe holds every ffprobe call until release.
func gatedFFprobe(t *testing.T) (started, release func()) {
	t.Helper()
	dir := t.TempDir()
	startFile, goFile := filepath.Join(dir, "started"), filepath.Join(dir, "go")
	fakeFFprobe(t, "#!/bin/sh\ntouch '"+startFile+"'\nwhile [ ! -f '"+goFile+"' ]; do sleep 0.02; done\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n")
	var once sync.Once
	release = func() { once.Do(func() { _ = os.WriteFile(goFile, nil, 0o644) }) }
	t.Cleanup(release)
	return func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(startFile); err == nil {
				return
			}
		}
		t.Fatal("scan never reached ffprobe")
	}, release
}

func TestOneScanPerLibraryAndCancel(t *testing.T) {
	r := newRig(t)
	r.write("A/Book/a.mp3", "aaa")
	started, release := gatedFFprobe(t)
	done := make(chan Result, 1)
	go func() {
		res, _ := r.s.Scan(context.Background(), "lib-1", false)
		done <- res
	}()
	started()
	if _, err := r.s.Scan(context.Background(), "lib-1", false); err == nil {
		t.Fatal("a second scan of the same library ran")
	}
	r.s.Cancel("lib-1")
	release()
	if res := <-done; !res.Canceled {
		t.Fatalf("canceled scan = %+v", res)
	}
	if n := r.count(`SELECT COUNT(*) FROM libraryItems`); n != 0 {
		t.Fatalf("canceled scan imported %d items", n)
	}
	if res := r.scan(false); res.Added != 1 {
		t.Fatalf("scan after cancel = %+v", res)
	}
}

func TestStartSkipsABadCronAndKeepsTheRest(t *testing.T) {
	r := newRig(t)
	if _, err := r.d.Exec(context.Background(), `UPDATE libraries SET settings = ? WHERE id = 'lib-1'`, `{"autoScanCronExpression":"not a cron"}`); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Start(context.Background()); err != nil {
		t.Fatalf("Start failed on a bad library cron: %v", err)
	}
}

func TestWatcherBatchFollowsMovedFilesAndMarksRemovedFolders(t *testing.T) {
	r := newRig(t)
	r.write("Auth/Old Name/a.mp3", "aaaa")
	r.write("Auth/Doomed/d.mp3", "dddd")
	r.write("Auth/Mover/m.mp3", "mmmm")
	r.scan(false)
	ids := map[string]string{}
	for _, name := range []string{"Old Name", "Doomed", "Mover"} {
		ids[name] = r.item("Auth/" + name).ID
	}
	r.write("Auth/Fresh/f.mp3", "ffff")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Rename(filepath.Join(r.root, "Auth/Old Name"), filepath.Join(r.root, "Auth/New Name")))
	must(os.RemoveAll(filepath.Join(r.root, "Auth/Doomed")))
	must(os.Rename(filepath.Join(r.root, "Auth/Mover/m.mp3"), filepath.Join(r.root, "Auth/m.mp3")))
	must(os.Remove(filepath.Join(r.root, "Auth/Mover")))
	r.take()
	changes, err := r.s.scanFolderUpdates(context.Background(), "lib-1", "folder-1", []string{"Auth/Fresh/f.mp3", "Auth/Old Name/a.mp3", "Auth/New Name/a.mp3", "Auth/Doomed/d.mp3", "Auth/Mover/m.mp3", "Auth/m.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []change
	kinds = append(kinds, changes...)
	if r.item("Auth/New Name").ID != ids["Old Name"] {
		t.Fatal("renamed folder lost its item")
	}
	if !r.item("Auth/Doomed").IsMissing || r.item("Auth/Doomed").ID != ids["Doomed"] {
		t.Fatal("deleted folder not marked missing")
	}
	if r.item("Auth").ID != ids["Mover"] {
		t.Fatal("an item whose file moved up did not follow it")
	}
	if r.count(`SELECT COUNT(*) FROM libraryItems WHERE path LIKE '%/Fresh'`) != 1 {
		t.Fatalf("new folder not added; changes %v", kinds)
	}
}

func TestUpdateWatchedPathDebouncesIntoOneBatch(t *testing.T) {
	r := newRig(t)
	if err := r.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := r.write("A/Book/a.mp3", "aaa")
	r.s.UpdateWatchedPath("lib-1", p, "add")
	r.s.UpdateWatchedPath("lib-1", p, "add")
	r.s.UpdateWatchedPath("lib-1", "/elsewhere/x.mp3", "add")
	deadline := time.Now().Add(10 * time.Second)
	for r.count(`SELECT COUNT(*) FROM libraryItems`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the watcher batch never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var started, finished int
	for _, e := range r.take() {
		switch e.Name {
		case "task_started":
			started++
		case "task_finished":
			finished++
		}
	}
	if started != 1 {
		t.Fatalf("watcher-scan tasks started = %d", started)
	}
	_ = finished
}

func TestFilesDiffKeepsStoredEntryOrderAndAddedAt(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) fileEntry {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		return fileEntry{path: p, rel: name, info: info}
	}
	a, b := write("a.mp3"), write("b.epub")
	first := diffFiles(nil, []fileEntry{a, b}, 100)
	if len(first.added) != 2 || first.added[1].IsSupplementary == nil || !*first.added[1].IsSupplementary {
		t.Fatalf("added = %+v", first.added)
	}
	stored := first.files
	stored[0].AddedAt = 5
	second := diffFiles(stored, []fileEntry{b, a, write("c.jpg")}, 200)
	if second.files[0].AddedAt != 5 || second.files[0].Metadata.Filename != "a.mp3" || len(second.added) != 1 || second.changed() == false {
		t.Fatalf("diff = %+v", second)
	}
}

func TestPairPrefersPathThenFirstFreeInode(t *testing.T) {
	want := []keyed{{"/x", []string{"1"}}, {"/y", []string{"1", "2"}}, {"/z", nil}}
	found := []keyed{{"/q", []string{"1"}}, {"/x", nil}, {"/r", []string{"2"}}}
	got := pair(want, found)
	if fmt.Sprint(got) != "[1 0 -1]" {
		t.Fatalf("pair = %v", got)
	}
}
