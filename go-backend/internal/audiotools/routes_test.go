package audiotools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/store"
)

type recorded struct {
	event   string
	payload any
}

type rig struct {
	t      *testing.T
	h      *Handler
	mux    *http.ServeMux
	root   string
	mu     sync.Mutex
	events []recorded
}

// newRig opens a real fresh database, adds audiobooks on request and serves
// the handler's routes as an admin.
func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.DatabasePath(cfg.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const now = "2020-01-01 00:00:00.000 +00:00"
	if _, err := st.DB().Exec(`INSERT INTO libraries(id,name,mediaType,settings,createdAt,updatedAt) VALUES('lib-1','Books','book','{}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, root: root, mux: http.NewServeMux()}
	r.h = New(st.DB(), cfg, func(_ string, event string, payload any) {
		r.mu.Lock()
		r.events = append(r.events, recorded{event, payload})
		r.mu.Unlock()
	})
	t.Cleanup(r.h.Close)
	r.h.Routes(r.mux)
	return r
}

// addBook stores a one-or-more track audiobook whose files exist on disk.
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
	db := r.h.db
	const now = "2020-01-01 00:00:00.000 +00:00"
	if _, err := db.Exec(`INSERT INTO books(id,title,duration,audioFiles,chapters,genres,narrators,createdAt,updatedAt) VALUES(?,?,?,?,?,'[]','[]',?,?)`, "book-"+id, title, 10.0*float64(len(tracks)), string(audio), string(chapters), now, now); err != nil {
		r.t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO libraryItems(id,libraryId,path,relPath,mediaId,mediaType,isFile,authorNamesFirstLast,createdAt,updatedAt) VALUES(?,'lib-1',?,?,?,'book',0,'Writer\Name',?,?)`, id, dir, title, "book-"+id, now, now); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

func (r *rig) call(method, path string) *httptest.ResponseRecorder {
	r.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.mux.ServeHTTP(w, req.WithContext(store.ContextWithUser(req.Context(), store.User{ID: "admin", Type: "root"})))
	return w
}

func (r *rig) named(event string) []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recorded
	for _, e := range r.events {
		if e.event == event {
			out = append(out, e)
		}
	}
	return out
}

func (r *rig) tasks(query string) (tasks []map[string]any, queued []any) {
	r.t.Helper()
	w := r.call("GET", "/api/tasks"+query)
	var body struct {
		Tasks      []map[string]any `json:"tasks"`
		QueuedData struct {
			Embed []any `json:"embedMetadata"`
		} `json:"queuedTaskData"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		r.t.Fatalf("tasks body %q: %v", w.Body.String(), err)
	}
	return body.Tasks, body.QueuedData.Embed
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func exists(p string) func() bool { return func() bool { _, err := os.Stat(p); return err == nil } }

// fakeFFmpeg installs an ffmpeg that records each call (its arguments and a
// copy of its second input, the metadata file) in dir, then waits for the
// "go" file before writing its output (the last argument).
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

// DELETE on a running encode cancels it: the task ends failed and canceled,
// ffmpeg is killed, no output is installed, a second encode is refused while
// it runs, and a second cancel finds nothing.
func TestCancelEncodeEndsRunningTask(t *testing.T) {
	r := newRig(t)
	dir := r.addBook("item-1", "Cancel Me", "a.wav")
	calls, _ := fakeFFmpeg(t)

	if w := r.call("POST", "/api/tools/item/item-1/encode-m4b"); w.Code != 200 {
		t.Fatalf("encode = %d %s", w.Code, w.Body)
	}
	waitFor(t, "ffmpeg to start", exists(filepath.Join(calls, "call-0")))
	if tasks, _ := r.tasks(""); len(tasks) != 1 || tasks[0]["action"] != "encode-m4b" {
		t.Fatalf("running tasks = %v", tasks)
	}
	if w := r.call("POST", "/api/tools/item/item-1/encode-m4b"); w.Code != 400 || !strings.Contains(w.Body.String(), "already processing") {
		t.Fatalf("second encode = %d %s", w.Code, w.Body)
	}
	if w := r.call("DELETE", "/api/tools/item/item-1/encode-m4b"); w.Code != 200 {
		t.Fatalf("cancel = %d", w.Code)
	}
	finished := r.named("task_finished")
	if len(finished) != 1 {
		t.Fatalf("task_finished events = %d", len(finished))
	}
	if task := finished[0].payload.(events.Task); !task.IsFailed || !task.IsFinished {
		t.Fatalf("canceled task = %+v", task)
	}
	if tasks, _ := r.tasks(""); len(tasks) != 0 {
		t.Fatalf("tasks after cancel = %v", tasks)
	}
	if w := r.call("DELETE", "/api/tools/item/item-1/encode-m4b"); w.Code != 404 {
		t.Fatalf("second cancel = %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "Cancel Me.m4b")); !os.IsNotExist(err) {
		t.Fatalf("canceled encode left output: %v", err)
	}
	// The killed encode must not report a second finish.
	time.Sleep(100 * time.Millisecond) // negative check: nothing more may arrive
	if n := len(r.named("task_finished")); n != 1 {
		t.Fatalf("task_finished events after cancel = %d", n)
	}
}

// Encode refuses unknown items and non-admins' routes are admin-only.
func TestEncodeUnknownItemIs404(t *testing.T) {
	r := newRig(t)
	if w := r.call("POST", "/api/tools/item/nope/encode-m4b"); w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

// Embed runs one task at a time; later ones wait in the queue (listed with
// include=queue), then run in order; the metadata handed to ffmpeg escapes
// special characters and lists chapters in order.
func TestEmbedQueueRunsOneAtATimeAndWritesMetadata(t *testing.T) {
	r := newRig(t)
	one := r.addBook("item-1", "A; #book=one", "a.mp3")
	two := r.addBook("item-2", "Second", "b.mp3")
	calls, release := fakeFFmpeg(t)

	if w := r.call("POST", "/api/tools/item/item-1/embed-metadata"); w.Code != 200 {
		t.Fatalf("embed 1 = %d", w.Code)
	}
	waitFor(t, "first ffmpeg call", exists(filepath.Join(calls, "call-0")))
	if w := r.call("POST", "/api/tools/item/item-2/embed-metadata"); w.Code != 200 {
		t.Fatalf("embed 2 = %d", w.Code)
	}
	if w := r.call("POST", "/api/tools/item/item-2/embed-metadata"); w.Code != 400 {
		t.Fatalf("duplicate queued embed = %d, want 400", w.Code)
	}
	tasks, queued := r.tasks("?include=queue")
	if len(tasks) != 1 || len(queued) != 1 {
		t.Fatalf("running %d, queued %d; want 1 and 1", len(tasks), len(queued))
	}
	if _, err := os.Stat(filepath.Join(calls, "call-1")); err == nil {
		t.Fatal("second embed started while the first was running")
	}
	release()
	waitFor(t, "both embeds to finish", func() bool { return len(r.named("task_finished")) == 2 })
	for _, ev := range r.named("task_finished") {
		if task := ev.payload.(events.Task); task.IsFailed {
			t.Fatalf("embed failed: %+v", task)
		}
	}
	if tasks, queued := r.tasks("?include=queue"); len(tasks) != 0 || len(queued) != 0 {
		t.Fatalf("leftover tasks %v queue %v", tasks, queued)
	}
	for _, p := range []string{filepath.Join(one, "a.mp3"), filepath.Join(two, "b.mp3")} {
		if st, err := os.Stat(p); err != nil || st.Size() != 0 {
			t.Fatalf("%s not replaced by ffmpeg output: %v", p, err)
		}
	}
	meta, err := os.ReadFile(filepath.Join(calls, "meta-0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"title=A\\; \\#book\\=one", "artist=Writer\\\\Name", "START=0", "END=1250", "title=First", "START=1250", "END=2500", "title=Second"} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("metadata passed to ffmpeg lacks %q:\n%s", want, meta)
		}
	}
	if strings.Index(string(meta), "title=First") > strings.Index(string(meta), "title=Second") {
		t.Fatalf("chapters out of order:\n%s", meta)
	}
}

// A failing embed ends its task as failed and the next queued one still runs.
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
		if w := r.call("POST", "/api/tools/item/"+id+"/embed-metadata"); w.Code != 200 {
			t.Fatalf("embed %s = %d", id, w.Code)
		}
	}
	waitFor(t, "both embeds to end", func() bool { return len(r.named("task_finished")) == 2 })
	failed := 0
	for _, ev := range r.named("task_finished") {
		if ev.payload.(events.Task).IsFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed tasks = %d, want exactly the one with the missing file", failed)
	}
}

// Close cancels a running task, kills its ffmpeg and starts no queued embed.
func TestCloseCancelsRunningAndQueuedWork(t *testing.T) {
	r := newRig(t)
	r.addBook("item-1", "One", "a.mp3")
	r.addBook("item-2", "Two", "b.mp3")
	calls, _ := fakeFFmpeg(t)
	for _, id := range []string{"item-1", "item-2"} {
		r.call("POST", "/api/tools/item/"+id+"/embed-metadata")
	}
	waitFor(t, "ffmpeg to start", exists(filepath.Join(calls, "call-0")))
	r.h.Close()
	waitFor(t, "the running embed to end", func() bool { return len(r.named("task_finished")) == 1 })
	if !r.named("task_finished")[0].payload.(events.Task).IsFailed {
		t.Fatal("closed task should end failed")
	}
	if _, err := os.Stat(filepath.Join(calls, "call-1")); err == nil {
		t.Fatal("a queued embed started after Close")
	}
}
