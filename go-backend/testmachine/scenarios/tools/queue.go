package tools

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// batchBody is the body POST /api/tools/batch/embed-metadata accepts.
type batchBody struct {
	LibraryItemIDs []string `json:"libraryItemIds"`
}

func submitBatch(t *testing.T, e *harness.Env, token string, ids []string, query url.Values) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: "POST", Path: "/api/tools/batch/embed-metadata", Token: token, Query: query, JSON: batchBody{ids}})
}

func submitItem(t *testing.T, e *harness.Env, token, id string, query url.Values) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: "POST", Path: "/api/tools/item/" + id + "/embed-metadata", Token: token, Query: query})
}

// forceChapters is the query the client sends for the force mode.
func forceChapters() url.Values {
	return url.Values{"forceEmbedChapters": {"1"}}
}

// taskData is the open task document the client reads libraryItemId from and
// the queue exposes verbatim.
type taskData struct {
	LibraryItemID string  `json:"libraryItemId"`
	Duration      float64 `json:"duration"`
	MimeType      string  `json:"mimeType"`
	ItemCachePath string  `json:"itemCachePath"`
	Options       struct {
		ForceEmbedChapters bool `json:"forceEmbedChapters"`
		BackupFiles        bool `json:"backupFiles"`
	} `json:"options"`
	AudioFiles []struct {
		Index     int     `json:"index"`
		Filename  string  `json:"filename"`
		Path      string  `json:"path"`
		CachePath string  `json:"cachePath"`
		Duration  float64 `json:"duration"`
	} `json:"audioFiles"`
}

// taskDocument is the projection GET /api/tasks returns for a running task.
type taskDocument struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	Data            taskData `json:"data"`
	Title           string   `json:"title"`
	TitleKey        *string  `json:"titleKey"`
	TitleSubs       []string `json:"titleSubs"`
	Description     *string  `json:"description"`
	DescriptionKey  *string  `json:"descriptionKey"`
	DescriptionSubs []string `json:"descriptionSubs"`
	Error           *string  `json:"error"`
	ErrorKey        *string  `json:"errorKey"`
	ErrorSubs       []string `json:"errorSubs"`
	ShowSuccess     bool     `json:"showSuccess"`
	IsFailed        bool     `json:"isFailed"`
	IsFinished      bool     `json:"isFinished"`
	StartedAt       *float64 `json:"startedAt"`
	FinishedAt      *float64 `json:"finishedAt"`
}

// tasksDocument is the GET /api/tasks body. A nil QueuedTaskData means the
// request did not ask for the queue; a nil EmbedMetadata slice means the
// response carried null instead of the empty array.
type tasksDocument struct {
	Tasks          []taskDocument `json:"tasks"`
	QueuedTaskData *struct {
		EmbedMetadata *[]taskData `json:"embedMetadata"`
	} `json:"queuedTaskData"`
}

func tasksAt(t *testing.T, e *harness.Env, token, query string) tasksDocument {
	t.Helper()
	response := e.Get(t, token, "/api/tasks"+query)
	wantStatus(t, response, 200)
	var doc tasksDocument
	response.JSON(t, &doc)
	if doc.Tasks == nil {
		t.Fatalf("tasks is null, want an array: %s", response.Body)
	}
	return doc
}

// embedTasks filters the task list to metadata embeds, so an unrelated library
// scan task cannot change a queue assertion.
func embedTasks(doc tasksDocument) []taskDocument {
	var tasks []taskDocument
	for _, task := range doc.Tasks {
		if task.Action == "embed-metadata" {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// taskIDs lists the returned task ids in response order.
func taskIDs(doc tasksDocument) []string {
	ids := make([]string, len(doc.Tasks))
	for i, task := range doc.Tasks {
		ids[i] = task.ID
	}
	return ids
}

// queuedIDs returns the waiting embed item ids in queue order.
func queuedIDs(t *testing.T, doc tasksDocument) []string {
	t.Helper()
	if doc.QueuedTaskData == nil || doc.QueuedTaskData.EmbedMetadata == nil {
		t.Fatalf("response has no queuedTaskData.embedMetadata array")
	}
	ids := make([]string, len(*doc.QueuedTaskData.EmbedMetadata))
	for i, data := range *doc.QueuedTaskData.EmbedMetadata {
		if data.LibraryItemID == "" {
			t.Fatalf("queued task data %d has no libraryItemId", i)
		}
		ids[i] = data.LibraryItemID
	}
	return ids
}

// requireQueuedOrder pins the FIFO queue snapshot.
func requireQueuedOrder(t *testing.T, e *harness.Env, token string, want []string) {
	t.Helper()
	got := queuedIDs(t, tasksAt(t, e, token, "?include=queue"))
	if !slices.Equal(got, want) {
		t.Fatalf("queued embed ids = %v, want %v", got, want)
	}
}

func requireQueuedEmpty(t *testing.T, e *harness.Env, token string) {
	t.Helper()
	requireQueuedOrder(t, e, token, nil)
}

// itemDocument is the part of GET /api/items/:id these scenarios read: the
// item's location, title and audio tracks.
type itemDocument struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Media struct {
		Metadata struct {
			Title string `json:"title"`
		} `json:"metadata"`
		AudioFiles []struct {
			Index    int `json:"index"`
			Metadata struct {
				Path     string `json:"path"`
				Filename string `json:"filename"`
			} `json:"metadata"`
		} `json:"audioFiles"`
	} `json:"media"`
}

func itemAt(t *testing.T, e *harness.Env, token, id string) itemDocument {
	t.Helper()
	response := e.Get(t, token, "/api/items/"+id)
	wantStatus(t, response, 200)
	var item itemDocument
	response.JSON(t, &item)
	if item.ID != id || item.Media.Metadata.Title == "" || len(item.Media.AudioFiles) == 0 {
		t.Fatalf("item %s is not the expected book: %s", id, response.Body)
	}
	return item
}

// libraryItemDir is the directory the metadata manager tests for writability.
func (item itemDocument) libraryItemDir(t *testing.T) string {
	t.Helper()
	info, err := os.Stat(item.Path)
	if err != nil {
		t.Fatalf("stat item path %q: %v", item.Path, err)
	}
	if info.IsDir() {
		return item.Path
	}
	return filepath.Dir(item.Path)
}

func (item itemDocument) audioPaths() []string {
	paths := make([]string, len(item.Media.AudioFiles))
	for i, file := range item.Media.AudioFiles {
		paths[i] = file.Metadata.Path
	}
	return paths
}

func audioModTime(t *testing.T, item itemDocument) time.Time {
	t.Helper()
	path := item.Media.AudioFiles[0].Metadata.Path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat audio file %q: %v", path, err)
	}
	return info.ModTime()
}

// requireRewritten proves the finished embed actually rewrote the audio file.
func requireRewritten(t *testing.T, before time.Time, item itemDocument) {
	t.Helper()
	if after := audioModTime(t, item); !after.After(before) {
		t.Fatalf("audio file modification time %v did not move past %v", after, before)
	}
}

// requireUnchangedAudio proves a rejected or failed embed touched no media.
func requireUnchangedAudio(t *testing.T, before time.Time, item itemDocument) {
	t.Helper()
	if after := audioModTime(t, item); !after.Equal(before) {
		t.Fatalf("audio file modification time moved from %v to %v", before, after)
	}
}

// itemCachePath returns the per-item cache directory of an install.
func itemCachePath(e *harness.Env, id string) string {
	return filepath.Join(e.Metadata, "cache", "items", id)
}

// itemCachePathJoin is the cache path of one audio file of an item.
func itemCachePathJoin(e *harness.Env, id, filename string) string {
	return filepath.Join(itemCachePath(e, id), filename)
}

// requireNoItemCache mirrors the manager's cleanup: a successful embed that is
// not backing the files up leaves no item cache directory behind, and a failure
// before the cache is created never creates it. A failure inside FFmpeg is the
// defined partial state that keeps it (see checkFailureAdvance).
func requireNoItemCache(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	if _, err := os.Stat(itemCachePath(e, id)); err == nil {
		t.Fatalf("item cache directory %s survived the embed", itemCachePath(e, id))
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat item cache directory: %v", err)
	}
}

// makeUnwritable removes the owner's write permission from a path, so the
// manager's writability checks fail before any media change. The returned
// function restores the original mode.
func makeUnwritable(t *testing.T, path string) func() {
	t.Helper()
	return setPermissions(t, path, 0o222)
}

// makeUnreadable removes the owner's read permission from a file, so FFmpeg
// fails when it reads the source rather than in an earlier precondition.
func makeUnreadable(t *testing.T, path string) func() {
	t.Helper()
	return setPermissions(t, path, 0o444)
}

func setPermissions(t *testing.T, path string, clear os.FileMode) func() {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	mode := info.Mode().Perm()
	if err := os.Chmod(path, mode&^clear); err != nil {
		t.Fatalf("change mode of %q: %v", path, err)
	}
	return func() {
		if err := os.Chmod(path, mode); err != nil {
			t.Errorf("restore mode of %q: %v", path, err)
		}
	}
}

// requireSingleEmbedTask requires exactly one running metadata embed whose
// submitted items are still running-plus-waiting in submission order, and
// returns the running task.
func requireSingleEmbedTask(t *testing.T, doc tasksDocument, submitted []string) taskDocument {
	t.Helper()
	embeds := embedTasks(doc)
	if len(embeds) != 1 {
		t.Fatalf("running embed tasks = %d, want one: %s", len(embeds), rawTasks(t, doc))
	}
	waiting := queuedIDs(t, doc)
	running := embeds[0].Data.LibraryItemID
	if !slices.Equal(append([]string{running}, waiting...), submitted) {
		t.Fatalf("running + queued = %v, want the submission order %v", append([]string{running}, waiting...), submitted)
	}
	if len(waiting) == 0 {
		t.Fatalf("no waiting item was queued behind the running one")
	}
	return embeds[0]
}

func rawTasks(t *testing.T, doc tasksDocument) string {
	t.Helper()
	raw, err := json.Marshal(doc.Tasks)
	if err != nil {
		t.Fatalf("re-encode tasks: %v", err)
	}
	return string(raw)
}
