package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// trackPayload is the wire shape of track_started, track_finished and
// track_progress: one argument object with two string fields and, for
// track_progress, a numeric progress field. Extra keys are open-ended; the
// consumed keys are case-sensitive on the wire.
type trackPayload struct {
	LibraryItemID string
	Ino           string
	Progress      *float64
}

func decodeTrack(t *testing.T, event harness.Event) trackPayload {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s arguments = %d, want one", event.Name, len(event.Args))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Args[0], &fields); err != nil {
		t.Fatalf("decode %s payload: %v", event.Name, err)
	}
	var payload trackPayload
	if raw, ok := fields["libraryItemId"]; !ok {
		t.Fatalf("%s payload lacks libraryItemId: %s", event.Name, event.Args[0])
	} else if err := json.Unmarshal(raw, &payload.LibraryItemID); err != nil {
		t.Fatalf("%s libraryItemId is not a string: %v", event.Name, err)
	}
	if raw, ok := fields["ino"]; !ok {
		t.Fatalf("%s payload lacks ino: %s", event.Name, event.Args[0])
	} else if err := json.Unmarshal(raw, &payload.Ino); err != nil {
		t.Fatalf("%s ino is not a string: %v", event.Name, err)
	}
	if payload.LibraryItemID == "" || payload.Ino == "" {
		t.Fatalf("%s payload has an empty id/ino: %s", event.Name, event.Args[0])
	}
	if raw, ok := fields["progress"]; ok && event.Name == "track_progress" {
		if err := json.Unmarshal(raw, &payload.Progress); err != nil {
			t.Fatalf("%s progress is not a number: %v", event.Name, err)
		}
		if payload.Progress == nil || math.IsNaN(*payload.Progress) || math.IsInf(*payload.Progress, 0) {
			t.Fatalf("%s progress is not a finite number: %s", event.Name, event.Args[0])
		}
	}
	return payload
}

// taskProgressPayload is the wire shape of task_progress.
type taskProgressPayload struct {
	LibraryItemID string
	Progress      float64
}

func decodeTaskProgress(t *testing.T, event harness.Event) taskProgressPayload {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s arguments = %d, want one", event.Name, len(event.Args))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Args[0], &fields); err != nil {
		t.Fatalf("decode %s payload: %v", event.Name, err)
	}
	var payload taskProgressPayload
	if raw, ok := fields["libraryItemId"]; !ok {
		t.Fatalf("%s payload lacks libraryItemId: %s", event.Name, event.Args[0])
	} else if err := json.Unmarshal(raw, &payload.LibraryItemID); err != nil {
		t.Fatalf("%s libraryItemId is not a string: %v", event.Name, err)
	}
	if raw, ok := fields["progress"]; !ok {
		t.Fatalf("%s payload lacks progress: %s", event.Name, event.Args[0])
	} else if err := json.Unmarshal(raw, &payload.Progress); err != nil {
		t.Fatalf("%s progress is not a number: %v", event.Name, err)
	}
	if payload.LibraryItemID == "" || math.IsNaN(payload.Progress) || math.IsInf(payload.Progress, 0) {
		t.Fatalf("%s payload is not an id and finite progress: %s", event.Name, event.Args[0])
	}
	return payload
}

// encodeTaskData is the part of an encode-m4b task document the bundled client
// and this packet read: the item link, the recorded query options and the file
// targets. Remaining keys (metadata objects, the killed ffmpeg handle) are
// open-ended server state.
type encodeTaskData struct {
	LibraryItemID  string   `json:"libraryItemId"`
	UserID         string   `json:"userId"`
	Inos           []string `json:"inos"`
	TargetFilename string   `json:"targetFilename"`
	TargetFilepath string   `json:"targetFilepath"`
	TempFilepath   string   `json:"tempFilepath"`
	ItemCachePath  string   `json:"itemCachePath"`
	EncodeOptions  struct {
		Codec    *string `json:"codec"`
		Bitrate  *string `json:"bitrate"`
		Channels *string `json:"channels"`
	} `json:"encodeOptions"`
}

// encodeTask is the task_started/task_finished payload of an encode-m4b job.
type encodeTask struct {
	ID              string         `json:"id"`
	Action          string         `json:"action"`
	Data            encodeTaskData `json:"data"`
	Title           string         `json:"title"`
	TitleKey        *string        `json:"titleKey"`
	TitleSubs       []string       `json:"titleSubs"`
	Description     *string        `json:"description"`
	DescriptionKey  *string        `json:"descriptionKey"`
	DescriptionSubs []string       `json:"descriptionSubs"`
	Error           *string        `json:"error"`
	ErrorKey        *string        `json:"errorKey"`
	ErrorSubs       []string       `json:"errorSubs"`
	ShowSuccess     bool           `json:"showSuccess"`
	IsFailed        bool           `json:"isFailed"`
	IsFinished      bool           `json:"isFinished"`
	StartedAt       *float64       `json:"startedAt"`
	FinishedAt      *float64       `json:"finishedAt"`
}

func decodeEncodeTask(t *testing.T, event harness.Event) encodeTask {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s arguments = %d, want one", event.Name, len(event.Args))
	}
	var task encodeTask
	if err := json.Unmarshal(event.Args[0], &task); err != nil {
		t.Fatalf("decode %s payload: %v", event.Name, err)
	}
	if task.ID == "" || task.Action != "encode-m4b" {
		t.Fatalf("%s is not an encode-m4b task document: %s", event.Name, event.Args[0])
	}
	return task
}

// waitEmbedTaskTerminal waits for the embed-metadata task_finished of the item
// and returns the full task document, requiring the expected failed flag.
func waitEmbedTaskTerminal(t *testing.T, socket *harness.Socket, from int, id string, failed bool) taskDocument {
	t.Helper()
	event := socket.Wait(t, from, "task_finished", finishedTimeout, embedItemMatcher(id))
	var task taskDocument
	if err := json.Unmarshal(event.Args[0], &task); err != nil {
		t.Fatalf("decode task_finished: %v", err)
	}
	if task.IsFailed != failed || !task.IsFinished {
		t.Fatalf("embed task_finished isFailed=%t isFinished=%t, want failed=%t: %s", task.IsFailed, task.IsFinished, failed, event.Args)
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("embed task finishedAt %v precedes startedAt %v", finished, started)
	}
	return task
}

// requireFailedDocument pins the failure snapshot the client renders: the
// failed flag, the translated error key and subs the client looks up, and the
// terminal timestamps.
func requireFailedDocument(t *testing.T, task taskDocument, errorKey string, errorSubs []string) {
	t.Helper()
	if !task.IsFailed || !task.IsFinished {
		t.Fatalf("failed task reports isFailed=%t isFinished=%t", task.IsFailed, task.IsFinished)
	}
	if task.Error == nil || *task.Error == "" {
		t.Fatalf("failed task carries no error text")
	}
	if task.ErrorKey == nil || *task.ErrorKey != errorKey {
		t.Fatalf("failed task errorKey = %v, want %s", task.ErrorKey, errorKey)
	}
	if !slices.Equal(task.ErrorSubs, errorSubs) {
		t.Fatalf("failed task errorSubs = %v, want %v", task.ErrorSubs, errorSubs)
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("failed task finishedAt %v precedes startedAt %v", finished, started)
	}
}

// requireCommonTask pins the projection fields every tools task carries and
// that the bundled client renders (Task.js:54-74): the action-specific
// title/description strings and keys, the substitutions the client looks up,
// the success flag and the start timestamp shape.
func requireCommonTask(t *testing.T, task taskDocument, action, itemID, itemTitle string) {
	t.Helper()
	title, titleKey, description, descriptionKey := "Embedding Metadata", "MessageTaskEmbeddingMetadata", embedDescription(itemTitle), "MessageTaskEmbeddingMetadataDescription"
	if action == "encode-m4b" {
		title, titleKey, description, descriptionKey = "Encoding M4b", "MessageTaskEncodingM4b", encodeDescription(itemTitle), "MessageTaskEncodingM4bDescription"
	}
	if task.ID == "" || task.Action != action {
		t.Fatalf("task id/action = %q/%q, want a %s task for %s", task.ID, task.Action, action, itemID)
	}
	if task.Data.LibraryItemID != itemID {
		t.Fatalf("task data.libraryItemId = %q, want %q", task.Data.LibraryItemID, itemID)
	}
	if task.Title != title {
		t.Fatalf("task title = %q, want %q", task.Title, title)
	}
	if task.TitleKey == nil || *task.TitleKey != titleKey {
		t.Fatalf("task titleKey = %v, want %q", task.TitleKey, titleKey)
	}
	if task.TitleSubs != nil {
		t.Fatalf("task titleSubs = %v, want null", task.TitleSubs)
	}
	if task.Description == nil || *task.Description != description {
		t.Fatalf("task description = %v, want %q", task.Description, description)
	}
	if task.DescriptionKey == nil || *task.DescriptionKey != descriptionKey {
		t.Fatalf("task descriptionKey = %v, want %q", task.DescriptionKey, descriptionKey)
	}
	if !slices.Equal(task.DescriptionSubs, []string{itemTitle}) {
		t.Fatalf("task descriptionSubs = %v, want [%q]", task.DescriptionSubs, itemTitle)
	}
	if task.ShowSuccess {
		t.Fatalf("task showSuccess = true, want false")
	}
}

// requireSuccessfulTask pins the terminal projection of a successful job.
func requireSuccessfulTask(t *testing.T, task taskDocument) {
	t.Helper()
	if task.IsFailed || !task.IsFinished {
		t.Fatalf("successful task flags isFailed=%t isFinished=%t", task.IsFailed, task.IsFinished)
	}
	if task.Error != nil || task.ErrorKey != nil || task.ErrorSubs != nil {
		t.Fatalf("successful task error fields = %v/%v/%v, want null", task.Error, task.ErrorKey, task.ErrorSubs)
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("task finishedAt %v precedes startedAt %v", finished, started)
	}
}

// waitTaskStarted waits for the task_started of one action/item pair and
// returns it.
func waitTaskStarted(t *testing.T, socket *harness.Socket, from int, action, itemID string) taskDocument {
	t.Helper()
	event := socket.Wait(t, from, "task_started", finishedTimeout, func(args []json.RawMessage) bool {
		var task taskDocument
		if json.Unmarshal(args[0], &task) != nil {
			return false
		}
		return task.Action == action && task.Data.LibraryItemID == itemID
	})
	var task taskDocument
	if err := json.Unmarshal(event.Args[0], &task); err != nil {
		t.Fatalf("decode task_started: %v", err)
	}
	if task.IsFailed || task.IsFinished {
		t.Fatalf("task_started reports a terminal state: %s", event.Args)
	}
	numericTime(t, "startedAt", task.StartedAt)
	if task.FinishedAt != nil {
		t.Fatalf("task_started carries finishedAt %v, want null", *task.FinishedAt)
	}
	if task.Error != nil || task.ErrorKey != nil || task.ErrorSubs != nil {
		t.Fatalf("task_started carries error fields %v/%v/%v, want null", task.Error, task.ErrorKey, task.ErrorSubs)
	}
	return task
}

// waitEncodeFinished waits for the terminal encode-m4b task_finished and
// returns it with isFailed reported by the caller's expectations.
func waitEncodeFinished(t *testing.T, socket *harness.Socket, from int, itemID string, failed bool) encodeTask {
	t.Helper()
	event := socket.Wait(t, from, "task_finished", 120*time.Second, func(args []json.RawMessage) bool {
		var task encodeTask
		if json.Unmarshal(args[0], &task) != nil {
			return false
		}
		return task.Data.LibraryItemID == itemID
	})
	task := decodeEncodeTask(t, event)
	if task.IsFailed != failed || !task.IsFinished {
		t.Fatalf("encode task_finished isFailed=%t isFinished=%t, want failed=%t finished: %s", task.IsFailed, task.IsFinished, failed, event.Args)
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("encode task finishedAt %v precedes startedAt %v", finished, started)
	}
	return task
}

// requireTaskEventsAdminOnly proves a non-admin or anonymous socket recorded no
// track or progress events for the item since its mark, after the job ended
// and a pong barrier froze the transcript.
func requireTaskEventsAdminOnly(t *testing.T, a *audience, from map[string]int, itemID string) {
	t.Helper()
	for _, name := range []string{"listener", "restricted", "revoked", "anonymous"} {
		socket, ok := a.sockets[name]
		if !ok {
			continue
		}
		for _, eventName := range []string{"track_started", "track_finished", "track_progress", "task_progress"} {
			socket.None(t, from[name], eventName, 200*time.Millisecond, func(args []json.RawMessage) bool {
				return containsArg(args, `"libraryItemId":"`+itemID+`"`)
			})
		}
	}
}

// containsArg reports whether any event argument's JSON contains needle. The
// ids this packet matches on are UUIDs, so a substring search cannot cross a
// field boundary.
func containsArg(args []json.RawMessage, needle string) bool {
	for _, arg := range args {
		if containsJSON(string(arg), needle) {
			return true
		}
	}
	return false
}

func containsJSON(s, needle string) bool {
	return len(s) >= len(needle) && indexOf(s, needle) >= 0
}

func indexOf(s, needle string) int {
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// embedDescription is the source-defined description template of an embed task
// (AudioMetadataManager.js:110-114).
func embedDescription(title string) string {
	return fmt.Sprintf("Embedding metadata in audiobook %q.", title)
}

// encodeDescription is the source-defined description template of an encode
// task (AbMergeManager.js:88-92).
func encodeDescription(title string) string {
	return fmt.Sprintf("Encoding audiobook %q into a single m4b file.", title)
}

// itemSize stats a path and fails on error.
func itemSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.Size()
}

// growAudio replaces the content of an item's audio file with 180s of
// generated AAC audio, writing through the existing path so the inode (and the
// database ino) stays the same. The generated file makes the FFmpeg merge run
// for several seconds, which gives a pending task a stable window. Generated
// with the harness-provided ffmpeg; skipped when the file is already large.
func growAudio(t *testing.T, e *harness.Env, path string) {
	t.Helper()
	if info, err := os.Stat(path); err == nil && info.Size() >= 1<<20 {
		return
	}
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	grown := filepath.Join(e.Install, "grown-audio.m4b")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "180", "-c:a", "aac", "-b:a", "48k", grown)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate audio with %s: %v: %s", ffmpeg, err, out)
	}
	data, err := os.ReadFile(grown)
	if err != nil {
		t.Fatalf("read generated audio: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("overwrite %s: %v", path, err)
	}
	if err := os.Remove(grown); err != nil {
		t.Fatalf("remove generated audio: %v", err)
	}
}

// requireEncodeCacheState pins the item cache after a terminal encode job: the
// merged temp file and the ffmetadata file are gone, the item cache directory
// itself stays, and (on success) the original audio is backed up there.
func requireEncodeCacheState(t *testing.T, e *harness.Env, id, tempName string, withBackup bool, backupName string, backupSize int64) {
	t.Helper()
	cache := itemCachePath(e, id)
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("encode cache directory %s must exist: %v", cache, err)
	}
	for _, name := range []string{tempName, "ffmetadata.txt"} {
		if _, err := os.Stat(filepath.Join(cache, name)); err == nil {
			t.Fatalf("cache file %s survived the encode", name)
		}
	}
	if !withBackup {
		return
	}
	info, err := os.Stat(filepath.Join(cache, backupName))
	if err != nil {
		t.Fatalf("encode backup file %s: %v", backupName, err)
	}
	if info.Size() != backupSize {
		t.Fatalf("backup file size %d, want the original %d", info.Size(), backupSize)
	}
}
