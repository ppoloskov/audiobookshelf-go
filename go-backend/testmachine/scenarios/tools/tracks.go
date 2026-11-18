package tools

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// trackLine reduces one track or progress event to a comparable unit.
type trackLine struct {
	kind   string // started | finished | track_progress | task_progress
	ino    string
	taskAt int // index into the transcript, for span membership
	value  float64
}

// collectTrackLines reduces a transcript slice to the track/progress events of
// one item, in arrival order.
func collectTrackLines(t *testing.T, events []harness.Event, itemID string) []trackLine {
	t.Helper()
	var lines []trackLine
	for i, event := range events {
		switch event.Name {
		case "track_started", "track_finished", "track_progress":
			payload := decodeTrack(t, event)
			if payload.LibraryItemID != itemID {
				continue
			}
			line := trackLine{kind: event.Name, ino: payload.Ino, taskAt: i}
			if payload.Progress != nil {
				line.value = *payload.Progress
			}
			lines = append(lines, line)
		case "task_progress":
			payload := decodeTaskProgress(t, event)
			if payload.LibraryItemID != itemID {
				continue
			}
			lines = append(lines, trackLine{kind: event.Name, taskAt: i, value: payload.Progress})
		}
	}
	return lines
}

// requireEmbedTrackSequence pins the per-file track transcript of an embed job:
// for each audio file, in task order, exactly one track_started and exactly one
// track_finished, with the next file starting only after the previous finish.
// Progress events are observed per file only when ffmpeg reports them; when
// they appear they must sit inside the file's span with task_progress first.
func requireEmbedTrackSequence(t *testing.T, lines []trackLine, inos []string) {
	t.Helper()
	cursor := 0
	for _, ino := range inos {
		start := indexAfter(lines, cursor, "track_started", ino)
		if start < 0 {
			t.Fatalf("no track_started for ino %s in %v", ino, lines)
		}
		finish := indexAfter(lines, start, "track_finished", ino)
		if finish < 0 {
			t.Fatalf("no track_finished for ino %s after its start in %v", ino, lines)
		}
		if countKind(lines, start+1, finish, "track_started", ino) != 0 {
			t.Fatalf("duplicate track_started for ino %s in %v", ino, lines)
		}
		if countKind(lines, start+1, finish, "track_finished", ino) != 0 {
			t.Fatalf("duplicate track_finished for ino %s in %v", ino, lines)
		}
		if firstKind(lines, start+1, finish, "track_progress", ino) < firstKind(lines, start+1, finish, "task_progress", "") {
			t.Fatalf("track_progress preceded task_progress while tagging ino %s in %v", ino, lines)
		}
		cursor = finish + 1
	}
	if indexAfter(lines, cursor, "track_started", "") >= 0 {
		t.Fatalf("track events continue after the last track_finished in %v", lines)
	}
}

// requireEmbedProgressPresence requires the pooled transcripts of the run's
// jobs to contain at least one task_progress and one track_progress event for
// the item: ffmpeg reports percent for at least one file across the jobs, and
// the client's progress display depends on it.
func requireEmbedProgressPresence(t *testing.T, linesList ...[]trackLine) {
	t.Helper()
	task, track := 0, 0
	for _, lines := range linesList {
		for _, line := range lines {
			switch line.kind {
			case "task_progress":
				task++
			case "track_progress":
				track++
			}
		}
	}
	if task == 0 || track == 0 {
		t.Fatalf("embed produced no progress events across %d jobs (task_progress=%d track_progress=%d)", len(linesList), task, track)
	}
}

// requireEmbedProgressValues pins the progress values of embed jobs: every
// value is a percentage and, for each file that ffmpeg reported, the last
// task_progress sits at the file's duration-weighted cumulative share
// (AudioMetadataManager.js:203-253) and the last track_progress reports the
// finished track.
func requireEmbedProgressValues(t *testing.T, lines []trackLine, durations []float64) {
	t.Helper()
	total := 0.0
	for _, duration := range durations {
		total += duration
	}
	if total <= 0 {
		t.Fatalf("task durations %v carry no total", durations)
	}
	for i, ino := range fileInos(lines) {
		start := indexAfter(lines, 0, "track_started", ino)
		finish := indexAfter(lines, start, "track_finished", ino)
		if start < 0 || finish < 0 {
			t.Fatalf("no started/finished span for ino %s in %v", ino, lines)
		}
		var taskValues, trackValues []float64
		for _, line := range linesInSpan(lines, start+1, finish) {
			switch line.kind {
			case "task_progress":
				if line.value < 0 || line.value > 101 {
					t.Fatalf("task_progress %v out of the percentage range", line.value)
				}
				taskValues = append(taskValues, line.value)
			case "track_progress":
				if line.value < 0 || line.value > 101 {
					t.Fatalf("track_progress %v out of the percentage range", line.value)
				}
				trackValues = append(trackValues, line.value)
			}
		}
		if len(taskValues) == 0 && len(trackValues) == 0 {
			continue // ffmpeg reported no percent for this file in this run
		}
		share := durations[i] / total
		want := (float64(i) + 1) * 100 * share
		if len(taskValues) > 0 {
			last := taskValues[len(taskValues)-1]
			if last < want-2 || last > want+3 {
				t.Fatalf("file %d final task_progress %v, want the cumulative share %v\u00b13", i, last, want)
			}
		}
		if len(trackValues) > 0 {
			if lastTrack := trackValues[len(trackValues)-1]; lastTrack < 99 {
				t.Fatalf("file %d final track_progress %v, want the finished track (~100)", i, lastTrack)
			}
		}
	}
}

// fileInos returns the distinct inos of the track events in first-seen order.
func fileInos(lines []trackLine) []string {
	var inos []string
	for _, line := range lines {
		if line.kind == "track_started" && !slices.Contains(inos, line.ino) {
			inos = append(inos, line.ino)
		}
	}
	return inos
}

func indexAfter(lines []trackLine, from int, kind, ino string) int {
	for i := from; i < len(lines); i++ {
		if lines[i].kind == kind && (ino == "" || lines[i].ino == ino) {
			return i
		}
	}
	return -1
}

func firstKind(lines []trackLine, from, to int, kind, ino string) int {
	for i := from; i < to && i < len(lines); i++ {
		if lines[i].kind == kind && (ino == "" || lines[i].ino == ino) {
			return i
		}
	}
	return math.MaxInt
}

func countKind(lines []trackLine, from, to int, kind, ino string) int {
	count := 0
	for i := from; i < to && i < len(lines); i++ {
		if lines[i].kind == kind && (ino == "" || lines[i].ino == ino) {
			count++
		}
	}
	return count
}

func linesInSpan(lines []trackLine, from, to int) []trackLine {
	if to > len(lines) {
		to = len(lines)
	}
	return lines[from:to]
}

// taskInos returns the audio file inos recorded in a task document, in task
// order.
func taskInos(task taskDocument) []string {
	inos := make([]string, len(task.Data.AudioFiles))
	for i, file := range task.Data.AudioFiles {
		inos[i] = file.Ino
	}
	return inos
}

// encodeTargetName is the merged file name the encode builds beside the
// sources (AbMergeManager.js:60-61).
func encodeTargetName(audioFilename string) string {
	ext := filepath.Ext(audioFilename)
	return audioFilename[:len(audioFilename)-len(ext)] + ".m4b"
}

// checkEncodeEvents runs one full successful encode of the grown novel item and
// pins the encode job's event transcript: task_started with the recorded query
// options, the merge-phase track_started/track_finished of the item's tracks
// with track_progress before task_progress, the metadata-embed phase's final
// task_progress of at least 95, and the terminal task document. The track and
// progress events reach admin sockets only.
func checkEncodeEvents(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["novel"]
	item := itemAt(t, e, root, id)
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	ino := item.Media.AudioFiles[0].Ino
	filename := item.Media.AudioFiles[0].Metadata.Filename
	growAudio(t, e, audioPath)
	before := itemSize(t, audioPath)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	started := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "encode-m4b", id)
	requireCommonTask(t, started, "encode-m4b", id, item.Media.Metadata.Title)
	if got := started.Data.Inos; len(got) != 1 || got[0] != ino {
		t.Fatalf("encode task inos = %v, want [%s]", got, ino)
	}
	if started.Data.TargetFilename != filename || started.Data.TargetFilepath != audioPath {
		t.Fatalf("encode task target = %q/%q, want %q/%q", started.Data.TargetFilename, started.Data.TargetFilepath, filename, audioPath)
	}
	if started.Data.EncodeOptions.Codec == nil || *started.Data.EncodeOptions.Codec != "aac" ||
		started.Data.EncodeOptions.Bitrate == nil || *started.Data.EncodeOptions.Bitrate != "128k" ||
		started.Data.EncodeOptions.Channels == nil || *started.Data.EncodeOptions.Channels != "2" {
		t.Fatalf("encode task options = %+v, want the query's codec=aac bitrate=128k channels=2 as strings", started.Data.EncodeOptions)
	}
	task := waitEncodeFinished(t, a.sockets["root"], a.marks["root"], id, false)
	requireSuccessfulEncodeTask(t, task)
	lines := collectTrackLines(t, a.sockets["root"].Events()[a.marks["root"]:], id)
	requireEncodeTrackSequence(t, lines, []string{ino})
	requireSuccessfulEncodeEvents(t, lines)
	requireEncodeCacheState(t, e, id, filename, true, backupFilename(filename), before)
	for name, socket := range a.sockets {
		switch name {
		case "root":
			continue
		case "admin":
			requireAnyTrackEvents(t, socket, a.marks[name], id, ino)
			waitEncodeFinished(t, socket, a.marks[name], id, false)
		case "anonymous":
			requireEncodeAudienceAnonymous(t, socket, a.marks[name], id)
		default:
			waitEncodeFinished(t, socket, a.marks[name], id, false)
		}
	}
	requireTaskEventsAdminOnly(t, a, a.marks, id)
	a.barrier(t)
}

// backupFilename is the cache name the merge gives the original track when the
// temp file shares its name (AbMergeManager.js:209-213): the base name with a
// .backup infix before the extension.
func backupFilename(filename string) string {
	ext := filepath.Ext(filename)
	return filename[:len(filename)-len(ext)] + ".backup" + ext
}

// requireSuccessfulEncodeTask pins the terminal encode projection.
func requireSuccessfulEncodeTask(t *testing.T, task encodeTask) {
	t.Helper()
	if task.IsFailed || !task.IsFinished {
		t.Fatalf("encode task flags isFailed=%t isFinished=%t", task.IsFailed, task.IsFinished)
	}
	if task.Error != nil || task.ErrorKey != nil || task.ErrorSubs != nil {
		t.Fatalf("successful encode error fields = %v/%v/%v", task.Error, task.ErrorKey, task.ErrorSubs)
	}
}

// requireEncodeTrackSequence pins the merge-phase track transcript of a
// single-track encode: one started, progress pairs with track_progress before
// task_progress, one finished, then the metadata-embed phase's final
// task_progress of at least 95 before the job finishes.
func requireEncodeTrackSequence(t *testing.T, lines []trackLine, inos []string) {
	t.Helper()
	cursor := 0
	for _, ino := range inos {
		start := indexAfter(lines, cursor, "track_started", ino)
		if start < 0 {
			t.Fatalf("no track_started for ino %s in %v", ino, lines)
		}
		finish := indexAfter(lines, start, "track_finished", ino)
		if finish < 0 {
			t.Fatalf("no track_finished for ino %s in %v", ino, lines)
		}
		if countKind(lines, start+1, finish, "track_started", ino) != 0 {
			t.Fatalf("duplicate track_started for ino %s in %v", ino, lines)
		}
		if countKind(lines, start, finish, "track_finished", ino) != 0 {
			t.Fatalf("duplicate track_finished for ino %s in %v", ino, lines)
		}
		if countKind(lines, start, finish, "track_progress", ino) == 0 {
			t.Fatalf("no merge-phase track_progress for ino %s in %v", ino, lines)
		}
		if firstKind(lines, start, finish, "track_progress", ino) > firstKind(lines, start, finish, "task_progress", "") {
			t.Fatalf("merge-phase task_progress preceded track_progress for ino %s in %v", ino, lines)
		}
		cursor = finish + 1
	}
}

// requireSuccessfulEncodeEvents pins the merge progress values and the
// metadata-embed phase: merge-phase task_progress values stay below the encode
// fraction of 95 and the final task_progress reports the finished embed phase.
func requireSuccessfulEncodeEvents(t *testing.T, lines []trackLine) {
	t.Helper()
	var merge, final []float64
	for _, line := range lines {
		if line.kind != "task_progress" {
			continue
		}
		if line.value < 0 || line.value > 100.5 {
			t.Fatalf("task_progress %v out of range", line.value)
		}
		if line.value < 95 {
			merge = append(merge, line.value)
		} else {
			final = append(final, line.value)
		}
	}
	if len(merge) == 0 {
		t.Fatalf("no merge-phase task_progress below 95 in %v", lines)
	}
	if len(final) == 0 || final[len(final)-1] < 95 {
		t.Fatalf("no metadata-embed task_progress of at least 95 in %v", lines)
	}
}

// requireAnyTrackEvents proves an additional admin socket saw the track events
// of the job without re-pinning the whole transcript.
func requireAnyTrackEvents(t *testing.T, socket *harness.Socket, from int, itemID, ino string) {
	t.Helper()
	socket.Wait(t, from, "track_started", finishedTimeout, trackMatcher(itemID, ino))
	socket.Wait(t, from, "track_progress", finishedTimeout, trackMatcher(itemID, ino))
	socket.Wait(t, from, "task_progress", finishedTimeout, taskProgressMatcher(itemID))
	socket.Wait(t, from, "track_finished", finishedTimeout, trackMatcher(itemID, ino))
}

// trackMatcher matches track_* payloads for the item and ino.
func trackMatcher(itemID, ino string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		return containsArg(args, `"libraryItemId":"`+itemID+`"`) && containsArg(args, `"ino":"`+ino+`"`)
	}
}

// requireEncodeAudienceAnonymous proves an anonymous socket recorded nothing at
// all about the encode job.
func requireEncodeAudienceAnonymous(t *testing.T, socket *harness.Socket, from int, itemID string) {
	t.Helper()
	for _, eventName := range []string{"task_started", "task_finished", "track_started", "track_finished", "track_progress", "task_progress"} {
		socket.None(t, from, eventName, 200*time.Millisecond, taskItemMatcher(itemID))
	}
}

// checkEmbedTrackSequence embeds a three-file book and pins its per-file track
// transcript and admin-only audience.
func checkEmbedTrackSequence(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga1"]
	a := connectAudiences(t, e, suffix)
	defer a.close()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	started := waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, false)
	lines := collectTrackLines(t, a.sockets["root"].Events()[a.marks["root"]:], id)
	requireEmbedTrackSequence(t, lines, taskInos(started))
	requireEmbedProgressValues(t, lines, taskDurations(t, started))
	requireTaskEventsAdminOnly(t, a, a.marks, id)
}

// itemInos returns the item's audio file inos in task order.
func itemInos(item itemDocument) []string {
	inos := make([]string, len(item.Media.AudioFiles))
	for i, file := range item.Media.AudioFiles {
		inos[i] = file.Ino
	}
	return inos
}

// taskDurations returns the per-file durations recorded in the task document.
func taskDurations(t *testing.T, task taskDocument) []float64 {
	t.Helper()
	durations := make([]float64, len(task.Data.AudioFiles))
	for i, file := range task.Data.AudioFiles {
		if file.Duration <= 0 {
			t.Fatalf("task audio file %d duration = %v, want positive", i, file.Duration)
		}
		durations[i] = file.Duration
	}
	return durations
}

// checkEmbedProgress embeds a three-file book twice and pins the progress
// payloads and their duration-weighted aggregation on admin sockets only. The
// two jobs pool their transcripts for the presence requirement: ffmpeg reports
// percent for at least one file across the jobs.
func checkEmbedProgress(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga1"]
	a := connectAudiences(t, e, suffix)
	defer a.close()
	var pooled [][]trackLine
	var started taskDocument
	for round := 1; round <= 2; round++ {
		a.reMark()
		wantStatus(t, submitItem(t, e, root, id, nil), 200)
		started = waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
		waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, false)
		pooled = append(pooled, collectTrackLines(t, a.sockets["root"].Events()[a.marks["root"]:], id))
	}
	requireEmbedProgressPresence(t, pooled...)
	for _, lines := range pooled {
		requireEmbedProgressValues(t, lines, taskDurations(t, started))
		requireEmbedTrackSequence(t, lines, taskInos(started))
	}
	requireTaskEventsAdminOnly(t, a, a.marks, id)
}

// checkEmbedTagFailure makes the second audio file unreadable and embeds a
// three-file book: the first file completes its full track cycle, the failing
// file gets a track_started but no track_progress, no task_progress and no
// track_finished, the task reports the per-file embed error, and the failed
// file's bytes stay untouched while the first file was rewritten.
func checkEmbedTagFailure(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga1"]
	item := itemAt(t, e, root, id)
	inos := itemInos(item)
	secondPath := item.Media.AudioFiles[1].Metadata.Path
	secondName := item.Media.AudioFiles[1].Metadata.Filename
	firstPath := item.Media.AudioFiles[0].Metadata.Path
	beforeFirst := modTime(t, firstPath)
	beforeSecond := modTime(t, secondPath)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	restore := makeUnreadable(t, secondPath)
	defer restore()
	wantStatus(t, submitItem(t, e, root, id, nil), 200)
	waitTaskStarted(t, a.sockets["root"], a.marks["root"], "embed-metadata", id)
	failed := waitEmbedTaskTerminal(t, a.sockets["root"], a.marks["root"], id, true)
	requireFailedDocument(t, failed, "MessageTaskFailedToEmbedMetadataInFile", []string{secondName})
	lines := collectTrackLines(t, a.sockets["root"].Events()[a.marks["root"]:], id)
	// First file completes fully (its progress events depend on ffmpeg's
	// reporting; its started/finished cycle does not).
	firstFinish := indexAfter(lines, 0, "track_finished", inos[0])
	if firstFinish < 0 {
		t.Fatalf("first file produced no track_finished in %v", lines)
	}
	if indexAfter(lines, 0, "track_started", inos[0]) < 0 {
		t.Fatalf("first file produced no track_started in %v", lines)
	}
	// Second file starts but never finishes.
	secondStart := indexAfter(lines, firstFinish, "track_started", inos[1])
	if secondStart < 0 {
		t.Fatalf("failing file produced no track_started in %v", lines)
	}
	if indexAfter(lines, secondStart, "track_finished", inos[1]) >= 0 {
		t.Fatalf("failing file produced a track_finished in %v", lines)
	}
	if countKind(lines, secondStart, len(lines), "track_progress", inos[1]) != 0 {
		t.Fatalf("failing file produced track_progress in %v", lines)
	}
	if countKind(lines, secondStart, len(lines), "task_progress", "") != 0 {
		t.Fatalf("failing file produced task_progress in %v", lines)
	}
	for name, socket := range a.sockets {
		if name == "root" || name == "anonymous" {
			continue
		}
		waitEmbedTaskTerminal(t, socket, a.marks[name], id, true)
	}
	requireTaskEventsAdminOnly(t, a, a.marks, id)
	a.barrier(t)
	if after := modTime(t, firstPath); !after.After(beforeFirst) {
		t.Fatalf("first audio file was not rewritten: %v stayed at %v", firstPath, beforeFirst)
	}
	if after := modTime(t, secondPath); !after.Equal(beforeSecond) {
		t.Fatalf("failing audio file moved from %v to %v", beforeSecond, after)
	}
	// The tag failure returns before the success cleanup, so upstream defines a
	// partial state: the item cache directory and its metadata file stay.
	cache := itemCachePath(e, id)
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("tag failure should leave the item cache directory: %v", err)
	}
	if _, err := os.Stat(cache + "/ffmetadata.txt"); err != nil {
		t.Fatalf("tag failure should leave the written metadata file: %v", err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatalf("remove the failed job's cache directory: %v", err)
	}
}

// modTime stats a file's modification time.
func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.ModTime()
}

// checkEncodeFailure makes the item's audio file unreadable and starts an
// encode: the route accepts with 200, the merge fails inside ffmpeg, the task
// reports the merge error, the track monitor still emitted the first
// track_started but no progress or finish events, the media is untouched and
// the cache keeps its directory but loses its metadata file.
func checkEncodeFailure(t *testing.T, e *harness.Env, suffix string) {
	t.Helper()
	root := e.Token(t, "root")
	id := e.IDs["saga2"]
	item := itemAt(t, e, root, id)
	audioPath := item.Media.AudioFiles[0].Metadata.Path
	ino := item.Media.AudioFiles[0].Ino
	before := audioModTime(t, item)
	a := connectAudiences(t, e, suffix)
	defer a.close()
	restore := makeUnreadable(t, audioPath)
	defer restore()
	wantStatus(t, e.Post(t, root, "/api/tools/item/"+id+"/encode-m4b?codec=aac&bitrate=128k&channels=2", nil), 200)
	waitTaskStarted(t, a.sockets["root"], a.marks["root"], "encode-m4b", id)
	task := waitEncodeFinished(t, a.sockets["root"], a.marks["root"], id, true)
	if task.Error == nil || *task.Error != "Failed to merge audio files" {
		t.Fatalf("failed merge error = %v, want 'Failed to merge audio files'", task.Error)
	}
	if task.ErrorKey == nil || *task.ErrorKey != "MessageTaskFailedToMergeAudioFiles" {
		t.Fatalf("failed merge errorKey = %v, want MessageTaskFailedToMergeAudioFiles", task.ErrorKey)
	}
	if task.ErrorSubs != nil {
		t.Fatalf("failed merge errorSubs = %v, want null", task.ErrorSubs)
	}
	requireCommonTask(t, encodeTaskProjection(t, task), "encode-m4b", id, item.Media.Metadata.Title)
	lines := collectTrackLines(t, a.sockets["root"].Events()[a.marks["root"]:], id)
	if countKind(lines, 0, len(lines), "track_started", ino) != 1 {
		t.Fatalf("failed merge track_started count = %d, want one (the monitor constructs before the merge): %v", countKind(lines, 0, len(lines), "track_started", ino), lines)
	}
	if countKind(lines, 0, len(lines), "track_progress", ino) != 0 || countKind(lines, 0, len(lines), "task_progress", "") != 0 || countKind(lines, 0, len(lines), "track_finished", ino) != 0 {
		t.Fatalf("failed merge produced progress events: %v", lines)
	}
	for name, socket := range a.sockets {
		if name == "root" || name == "anonymous" {
			continue
		}
		waitEncodeFinished(t, socket, a.marks[name], id, true)
	}
	requireTaskEventsAdminOnly(t, a, a.marks, id)
	a.barrier(t)
	if after := audioModTime(t, item); !after.Equal(before) {
		t.Fatalf("audio file moved from %v to %v", before, after)
	}
	// removeTask removes the merged temp file and the metadata file but keeps
	// the cache directory it created at job start.
	requireEncodeCacheState(t, e, id, encodeTargetName(item.Media.AudioFiles[0].Metadata.Filename), false, "", 0)
	if err := os.RemoveAll(itemCachePath(e, id)); err != nil {
		t.Fatalf("remove the failed job's cache directory: %v", err)
	}
}
