package tools

import (
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
	"audiobookshelf-compat/internal/events"
)

const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	library    = "51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41"
	podcast    = "eeb6224f-1316-40c8-b8bf-00804cc90436"
)

type rig struct {
	t      *testing.T
	d      *db.DB
	h      *Handler
	mux    *http.ServeMux
	root   string
	mu     sync.Mutex
	events []events.Task
	names  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, d: dbtest.Fixture(t), mux: http.NewServeMux(), root: t.TempDir()}
	cfg := config.Config{MetadataPath: filepath.Join(r.root, "metadata")}
	r.h = New(r.d, cfg, func(_, event string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.names = append(r.names, event)
		if task, ok := payload.(events.Task); ok && event == "task_finished" {
			r.events = append(r.events, task)
		}
	})
	t.Cleanup(r.h.Close)
	r.h.Routes(r.mux)
	return r
}

func (r *rig) addBook(id, title string, tracks ...string) string {
	r.t.Helper()
	dir := filepath.Join(r.root, "lib", title)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	var files []map[string]any
	for i, name := range tracks {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			r.t.Fatal(err)
		}
		files = append(files, map[string]any{"ino": name, "index": i + 1, "duration": 10.0, "metadata": map[string]any{"filename": name, "path": p}})
	}
	audio, _ := json.Marshal(files)
	chapters, _ := json.Marshal([]map[string]any{{"id": 0, "start": 0, "end": 1.25, "title": "First"}, {"id": 1, "start": 1.25, "end": 2.5, "title": "Second"}})
	const now = "2020-01-01 00:00:00.000 +00:00"
	for query, args := range map[string][]any{
		`INSERT INTO books(id,title,duration,audioFiles,chapters,genres,narrators,createdAt,updatedAt) VALUES(?,?,?,?,?,'[]','[]',?,?)`:                                      {"book-" + id, title, 10.0 * float64(len(tracks)), string(audio), string(chapters), now, now},
		`INSERT INTO libraryItems(id,libraryId,path,relPath,mediaId,mediaType,isFile,authorNamesFirstLast,createdAt,updatedAt) VALUES(?,?,?,?,?,'book',0,'Writer\Name',?,?)`: {id, library, dir, title, "book-" + id, now, now},
	} {
		if _, err := r.d.Exec(r.t.Context(), query, args...); err != nil {
			r.t.Fatal(err)
		}
	}
	return dir
}

// call sends the request as the root user, or as the listener, or anonymously.
func (r *rig) call(who, method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if who != "" {
		var u db.User
		if err := r.d.Get(r.t.Context(), &u, "SELECT * FROM users WHERE id = ?", map[string]string{"root": rootID, "listener": listenerID}[who]); err != nil {
			r.t.Fatal(err)
		}
		req = req.WithContext(auth.WithUser(req.Context(), auth.NewPrincipal(u)))
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *rig) tasks(query string) (running []map[string]any, queued []any) {
	r.t.Helper()
	var body struct {
		Tasks  []map[string]any `json:"tasks"`
		Queued struct {
			Embed []any `json:"embedMetadata"`
		} `json:"queuedTaskData"`
	}
	if err := json.Unmarshal(r.call("root", "GET", "/api/tasks"+query, "").Body.Bytes(), &body); err != nil {
		r.t.Fatal(err)
	}
	return body.Tasks, body.Queued.Embed
}

func (r *rig) finished() []events.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Task(nil), r.events...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func exists(p string) func() bool { return func() bool { _, err := os.Stat(p); return err == nil } }

// fakeFFmpeg installs an ffmpeg that records each call (its arguments and a
// copy of its second input, the metadata file) in dir, then waits for the "go"
// file before it writes its output (the last argument).
func fakeFFmpeg(t *testing.T) (dir string, release func()) {
	t.Helper()
	dir = t.TempDir()
	script := "#!/bin/sh\nn=$(ls '" + dir + "' | grep -c '^call-')\necho \"$@\" > '" + dir + "/call-'$n\ni=0; for a; do if [ \"$prev\" = -i ]; then i=$((i+1)); [ $i = 2 ] && cp \"$a\" '" + dir + "/meta-'$n; fi; prev=$a; done\n" +
		"while [ ! -f '" + dir + "/go' ]; do sleep 0.02; done\nfor a; do last=$a; done\ntouch \"$last\"\n"
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", path)
	var once sync.Once
	release = func() { once.Do(func() { _ = os.WriteFile(filepath.Join(dir, "go"), nil, 0o644) }) }
	t.Cleanup(release)
	return dir, release
}

func TestRoutesAuthorisationAndUnfitItems(t *testing.T) {
	r := newRig(t)
	r.addBook("item-1", "One", "a.mp3")
	for _, route := range []string{"POST /api/tools/item/item-1/encode-m4b", "POST /api/tools/item/item-1/embed-metadata", "DELETE /api/tools/item/item-1/encode-m4b", "POST /api/tools/batch/embed-metadata"} {
		method, path, _ := strings.Cut(route, " ")
		for _, who := range []string{"", "listener"} {
			if w := r.call(who, method, path, `{"libraryItemIds":["item-1"]}`); w.Code != 403 {
				t.Errorf("%s as %q: %d, want 403", route, who, w.Code)
			}
		}
	}
	if w := r.call("listener", "GET", "/api/tasks", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"tasks":[]`) {
		t.Errorf("tasks as a user: %d %s", w.Code, w.Body)
	}
	for route, status := range map[string]int{
		"POST /api/tools/item/nope/encode-m4b":                404,
		"POST /api/tools/item/nope/embed-metadata":            404,
		"DELETE /api/tools/item/item-1/encode-m4b":            404,
		"POST /api/tools/item/" + podcast + "/encode-m4b":     400,
		"POST /api/tools/item/" + podcast + "/embed-metadata": 400,
	} {
		method, path, _ := strings.Cut(route, " ")
		if w := r.call("root", method, path, ""); w.Code != status {
			t.Errorf("%s: %d, want %d", route, w.Code, status)
		}
	}
}

// DELETE on a running encode ends it failed and canceled, kills ffmpeg, installs
// no output, and refuses a second encode meanwhile.
func TestCancelEncode(t *testing.T) {
	r := newRig(t)
	dir := r.addBook("item-1", "Cancel Me", "a.wav")
	calls, _ := fakeFFmpeg(t)
	if w := r.call("root", "POST", "/api/tools/item/item-1/encode-m4b", ""); w.Code != 200 {
		t.Fatalf("encode: %d %s", w.Code, w.Body)
	}
	waitFor(t, "ffmpeg", exists(filepath.Join(calls, "call-0")))
	if tasks, _ := r.tasks(""); len(tasks) != 1 || tasks[0]["action"] != "encode-m4b" {
		t.Fatalf("running %v", tasks)
	}
	if w := r.call("root", "POST", "/api/tools/item/item-1/encode-m4b", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "already processing") {
		t.Fatalf("second encode: %d %s", w.Code, w.Body)
	}
	if w := r.call("root", "DELETE", "/api/tools/item/item-1/encode-m4b", ""); w.Code != 200 {
		t.Fatalf("cancel: %d", w.Code)
	}
	if done := r.finished(); len(done) != 1 || !done[0].IsFailed || !done[0].IsFinished {
		t.Fatalf("finished %+v", done)
	}
	if tasks, _ := r.tasks(""); len(tasks) != 0 {
		t.Fatalf("tasks after cancel: %v", tasks)
	}
	time.Sleep(100 * time.Millisecond) // negative check: the killed ffmpeg must not finish the task again
	if _, err := os.Stat(filepath.Join(dir, "Cancel Me.m4b")); !os.IsNotExist(err) || len(r.finished()) != 1 {
		t.Fatalf("output %v, finishes %d", err, len(r.finished()))
	}
}

// Embeds run one at a time, later ones wait (listed with include=queue), and
// the metadata handed to ffmpeg is escaped and lists the chapters in order.
func TestEmbedQueueAndMetadata(t *testing.T) {
	r := newRig(t)
	one := r.addBook("item-1", "A; #book=one", "a.mp3")
	two := r.addBook("item-2", "Second", "b.mp3")
	calls, release := fakeFFmpeg(t)
	if w := r.call("root", "POST", "/api/tools/item/item-1/embed-metadata", ""); w.Code != 200 {
		t.Fatalf("embed 1: %d", w.Code)
	}
	waitFor(t, "ffmpeg", exists(filepath.Join(calls, "call-0")))
	if w := r.call("root", "POST", "/api/tools/item/item-2/embed-metadata", ""); w.Code != 200 {
		t.Fatalf("embed 2: %d", w.Code)
	}
	if w := r.call("root", "POST", "/api/tools/item/item-2/embed-metadata", ""); w.Code != 400 {
		t.Fatalf("duplicate: %d, want 400", w.Code)
	}
	if tasks, queued := r.tasks("?include=queue"); len(tasks) != 1 || len(queued) != 1 {
		t.Fatalf("running %d, queued %d", len(tasks), len(queued))
	}
	if _, queued := r.tasks(""); queued != nil {
		t.Fatalf("queue listed without include=queue: %v", queued)
	}
	if _, err := os.Stat(filepath.Join(calls, "call-1")); err == nil {
		t.Fatal("the second embed started while the first ran")
	}
	release()
	waitFor(t, "both embeds", func() bool { return len(r.finished()) == 2 })
	for _, task := range r.finished() {
		if task.IsFailed {
			t.Fatalf("failed: %+v", task)
		}
	}
	for _, p := range []string{filepath.Join(one, "a.mp3"), filepath.Join(two, "b.mp3")} {
		if st, err := os.Stat(p); err != nil || st.Size() != 0 {
			t.Fatalf("%s not replaced by the ffmpeg output: %v", p, err)
		}
	}
	meta, _ := os.ReadFile(filepath.Join(calls, "meta-0"))
	for _, want := range []string{`title=A\; \#book\=one`, `artist=Writer\\Name`, "START=0", "END=1250", "title=First", "START=1250", "END=2500", "title=Second"} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("metadata lacks %q:\n%s", want, meta)
		}
	}
	if strings.Index(string(meta), "title=First") > strings.Index(string(meta), "title=Second") {
		t.Fatalf("chapters out of order:\n%s", meta)
	}
}

// A failing embed ends failed and the next queued one still runs.
func TestEmbedFailureDoesNotBlockQueue(t *testing.T) {
	r := newRig(t)
	r.addBook("item-1", "Missing Audio", "a.mp3")
	r.addBook("item-2", "Fine", "b.mp3")
	if err := os.Remove(filepath.Join(r.root, "lib", "Missing Audio", "a.mp3")); err != nil {
		t.Fatal(err)
	}
	_, release := fakeFFmpeg(t)
	release()
	for _, id := range []string{"item-1", "item-2"} {
		r.call("root", "POST", "/api/tools/item/"+id+"/embed-metadata", "")
	}
	waitFor(t, "both embeds", func() bool { return len(r.finished()) == 2 })
	failed := 0
	for _, task := range r.finished() {
		if task.IsFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed tasks: %d, want 1", failed)
	}
}

// Close cancels the running task and starts no queued embed.
func TestCloseCancelsWork(t *testing.T) {
	r := newRig(t)
	r.addBook("item-1", "One", "a.mp3")
	r.addBook("item-2", "Two", "b.mp3")
	calls, _ := fakeFFmpeg(t)
	for _, id := range []string{"item-1", "item-2"} {
		r.call("root", "POST", "/api/tools/item/"+id+"/embed-metadata", "")
	}
	waitFor(t, "ffmpeg", exists(filepath.Join(calls, "call-0")))
	r.h.Close()
	waitFor(t, "the running embed to end", func() bool { return len(r.finished()) == 1 })
	if !r.finished()[0].IsFailed {
		t.Fatal("the closed task should end failed")
	}
	if _, err := os.Stat(filepath.Join(calls, "call-1")); err == nil {
		t.Fatal("a queued embed started after Close")
	}
}

// The batch route validates every item before it queues any.
func TestBatchEmbedIsAllOrNothing(t *testing.T) {
	r := newRig(t)
	r.addBook("item-1", "One", "a.mp3")
	r.addBook("item-2", "Two", "b.mp3")
	calls, release := fakeFFmpeg(t)
	post := func(body string) int { return r.call("root", "POST", "/api/tools/batch/embed-metadata", body).Code }
	for body, status := range map[string]int{`{"libraryItemIds":[]}`: 400, `x`: 400, `{"libraryItemIds":["item-1","nope"]}`: 404} {
		if got := post(body); got != status {
			t.Fatalf("%s: %d, want %d", body, got, status)
		}
	}
	if tasks, queued := r.tasks("?include=queue"); len(tasks) != 0 || len(queued) != 0 {
		t.Fatalf("a rejected batch left work: %v %v", tasks, queued)
	}
	if post(`{"libraryItemIds":["item-1","item-2"]}`) != 200 {
		t.Fatal("valid batch refused")
	}
	waitFor(t, "ffmpeg", exists(filepath.Join(calls, "call-0")))
	if post(`{"libraryItemIds":["item-2"]}`) != 400 {
		t.Fatal("an already queued item was accepted")
	}
	release()
	waitFor(t, "both embeds", func() bool { return len(r.finished()) == 2 })
}

func TestEncodeBuildsM4bBesideSources(t *testing.T) {
	r := newRig(t)
	dir := r.addBook("item-1", "Merged", "a.mp3", "b.mp3")
	calls, release := fakeFFmpeg(t)
	release()
	if w := r.call("root", "POST", "/api/tools/item/item-1/encode-m4b?bitrate=64k", ""); w.Code != 200 {
		t.Fatalf("encode: %d", w.Code)
	}
	waitFor(t, "the encode", func() bool { return len(r.finished()) == 1 })
	args, _ := os.ReadFile(filepath.Join(calls, "call-0"))
	if task := r.finished()[0]; task.IsFailed || !strings.Contains(string(args), "-b:a 64k") || !strings.Contains(string(args), "-f concat") {
		t.Fatalf("task %+v, ffmpeg args %s", task, args)
	}
	if _, err := os.Stat(filepath.Join(dir, "Merged.m4b")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "metadata", "cache", "items", "item-1", "a.mp3")); err != nil {
		t.Fatalf("source copy not kept: %v", err)
	}
	_ = fmt.Sprint
}
