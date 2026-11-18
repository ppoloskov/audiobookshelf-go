package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const queueEvent = "metadata_embed_queue_update"

// finishedTimeout bounds the wait for a metadata embed task's completion.
const finishedTimeout = 30 * time.Second

type queueUpdate struct {
	LibraryItemID string
	Queued        bool
}

// decodeQueue requires the source's two typed fields and one argument. Other
// fields are not consumed by the clients and may be added independently.
func decodeQueue(t *testing.T, event harness.Event) queueUpdate {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s arguments = %d, want one", event.Name, len(event.Args))
	}
	// Extra keys are open-ended, but consumed keys are case-sensitive on the
	// wire. Struct decoding would incorrectly accept libraryitemid or QUEUED.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Args[0], &fields); err != nil {
		t.Fatalf("decode queue payload %s: %v", event.Args[0], err)
	}
	var id *string
	var queued *bool
	if raw, ok := fields["libraryItemId"]; !ok {
		t.Fatalf("queue payload lacks libraryItemId: %s", event.Args[0])
	} else if err := json.Unmarshal(raw, &id); err != nil {
		t.Fatalf("queue libraryItemId is not a string: %v", err)
	}
	if raw, ok := fields["queued"]; !ok {
		t.Fatalf("queue payload lacks queued: %s", event.Args[0])
	} else if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatalf("queue queued is not a boolean: %v", err)
	}
	if id == nil || *id == "" || queued == nil {
		t.Fatalf("queue payload lacks required ID/bool: %s", event.Args[0])
	}
	return queueUpdate{*id, *queued}
}

// taskMarker projects only fields needed to identify successful ordering
// barriers. Task data also contains media-specific documents outside this slice.
type taskMarker struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Data   struct {
		LibraryItemID string `json:"libraryItemId"`
	} `json:"data"`
	Error      *string  `json:"error"`
	ErrorKey   *string  `json:"errorKey"`
	ErrorSubs  []string `json:"errorSubs"`
	IsFailed   bool     `json:"isFailed"`
	IsFinished bool     `json:"isFinished"`
	StartedAt  *float64 `json:"startedAt"`
	FinishedAt *float64 `json:"finishedAt"`
}

func embedTask(event harness.Event) (taskMarker, bool) {
	var task taskMarker
	if len(event.Args) != 1 || json.Unmarshal(event.Args[0], &task) != nil || task.Action != "embed-metadata" {
		return task, false
	}
	return task, true
}

// numericTime requires a JS millisecond timestamp: a JSON number, not a string.
func numericTime(t *testing.T, field string, value *float64) float64 {
	t.Helper()
	if value == nil || *value <= 0 || math.IsInf(*value, 0) || math.IsNaN(*value) {
		t.Fatalf("%s = %v, want a positive numeric timestamp", field, value)
	}
	return *value
}

type audience struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

func (a *audience) close() {
	for _, socket := range a.sockets {
		socket.Close()
	}
}

// reMark moves every audience mark to the current end of that socket, so a
// later wait ignores events of an earlier phase.
func (a *audience) reMark() {
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
}

// markAll snapshots every socket's current position for a later requireRound.
func (a *audience) markAll() map[string]int {
	marks := make(map[string]int, len(a.sockets))
	for name, socket := range a.sockets {
		marks[name] = socket.Mark()
	}
	return marks
}

// waitEmbedFinished waits for the successful task_finished of the item's embed.
func waitEmbedFinished(t *testing.T, socket *harness.Socket, from int, id string) taskMarker {
	t.Helper()
	event := socket.Wait(t, from, "task_finished", finishedTimeout, func(args []json.RawMessage) bool {
		task, ok := embedTask(harness.Event{Args: args})
		return ok && task.Data.LibraryItemID == id
	})
	task, _ := embedTask(event)
	if task.ID == "" || task.IsFailed || !task.IsFinished {
		t.Fatalf("embed did not finish successfully: %s", event.Args)
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("task finishedAt %v precedes startedAt %v", finished, started)
	}
	return task
}

// waitEmbedFailed waits for the failed task_finished of the item's embed.
func waitEmbedFailed(t *testing.T, socket *harness.Socket, from int, id string) taskMarker {
	t.Helper()
	event := socket.Wait(t, from, "task_finished", finishedTimeout, func(args []json.RawMessage) bool {
		task, ok := embedTask(harness.Event{Args: args})
		return ok && task.Data.LibraryItemID == id && task.IsFailed
	})
	task, _ := embedTask(event)
	requireFailedTask(t, task, "", nil)
	return task
}

func (a *audience) waitFinished(t *testing.T, id string) taskMarker {
	t.Helper()
	return waitEmbedFinished(t, a.sockets["root"], a.marks["root"], id)
}

// barrier observes a later server response on each socket. All synchronous
// queue/task emissions before the last task's finish must precede its pong.
// This checks absence and duplicates without assuming a quiet sleep duration.
func (a *audience) barrier(t *testing.T) {
	t.Helper()
	for name, socket := range a.sockets {
		mark := socket.Mark()
		socket.Emit(t, "ping")
		event := socket.Wait(t, mark, "pong", 10*time.Second, nil)
		if len(event.Args) != 0 {
			t.Fatalf("%s pong has unexpected arguments: %s", name, event.Args)
		}
	}
}

// embedTrace reduces queue and embed-task events to comparable lines. Task
// events that fail abort the trace: a failing embed needs its own scenario.
func embedTrace(t *testing.T, events []harness.Event) []string {
	t.Helper()
	var lines []string
	started := make(map[string]string)
	for _, event := range events {
		switch event.Name {
		case queueEvent:
			update := decodeQueue(t, event)
			lines = append(lines, fmt.Sprintf("queue:%s:%t", update.LibraryItemID, update.Queued))
		case "task_started", "task_finished":
			task, ok := embedTask(event)
			if !ok {
				continue // other actions' tasks are another requirement's events
			}
			if task.ID == "" || task.Data.LibraryItemID == "" || task.IsFailed {
				t.Fatalf("invalid/failed embed task: %s", event.Args)
			}
			if event.Name == "task_started" {
				started[task.Data.LibraryItemID] = task.ID
			} else if !task.IsFinished || started[task.Data.LibraryItemID] != task.ID {
				t.Fatalf("task finish does not match a successful started task: %s", event.Args)
			}
			lines = append(lines, event.Name+":"+task.Data.LibraryItemID)
		}
	}
	return lines
}

// wantTrace is the source-defined transcript for one audience: the first item
// starts immediately, admins alone see each waiting admission, every
// authenticated socket sees each dequeue before that item's start, and
// anonymous sockets see no task or queue event at all.
func wantTrace(name string, ids []string) []string {
	if name == "anonymous" {
		return nil
	}
	want := []string{"task_started:" + ids[0]}
	if name == "root" || name == "admin" {
		for _, id := range ids[1:] {
			want = append(want, "queue:"+id+":true")
		}
	}
	want = append(want, "task_finished:"+ids[0])
	for _, id := range ids[1:] {
		want = append(want, "queue:"+id+":false", "task_started:"+id, "task_finished:"+id)
	}
	return want
}

func (a *audience) requireTrace(t *testing.T, ids []string) {
	t.Helper()
	from := make(map[string]int, len(a.sockets))
	for name := range a.sockets {
		from[name] = a.marks[name]
	}
	a.requireRound(t, from, ids)
}

// requireRound checks every socket's queue/task transcript since its mark.
func (a *audience) requireRound(t *testing.T, from map[string]int, ids []string) {
	t.Helper()
	for name, socket := range a.sockets {
		events := socket.Events()
		if from[name] > len(events) {
			from[name] = len(events)
		}
		got := embedTrace(t, events[from[name]:])
		if want := wantTrace(name, ids); !slices.Equal(got, want) {
			t.Errorf("%s embed trace = %v, want %v", name, got, want)
		}
	}
}

// requireFailureTrace checks the transcript when the first admission fails
// before touching media: the failure is reported as a failed task, produces no
// queue transition of its own, and each following waiter is still dequeued and
// run in admission order.
func (a *audience) requireFailureTrace(t *testing.T, failed string, waiting []string) {
	t.Helper()
	for name, socket := range a.sockets {
		events := socket.Events()
		from := a.marks[name]
		if from > len(events) {
			from = len(events)
		}
		lines, failure := failureTrace(t, events[from:])
		var want []string
		if name != "anonymous" {
			want = []string{"task_started:" + failed}
			if name == "root" || name == "admin" {
				for _, id := range waiting {
					want = append(want, "queue:"+id+":true")
				}
			}
			want = append(want, "task_finished:"+failed)
			for _, id := range waiting {
				want = append(want, "queue:"+id+":false", "task_started:"+id, "task_finished:"+id)
			}
		}
		if !slices.Equal(lines, want) {
			t.Errorf("%s failure trace = %v, want %v", name, lines, want)
		}
		if name != "root" {
			continue
		}
		if failure == nil {
			t.Fatalf("no embed task_finished for the failing item %s", failed)
		}
		requireFailedTask(t, *failure, "MessageTaskTargetDirectoryNotWritable", nil)
	}
}

// failureTrace is embedTrace for a transcript that legitimately contains one
// failed task; it returns that task's finish snapshot.
func failureTrace(t *testing.T, events []harness.Event) ([]string, *taskMarker) {
	t.Helper()
	var lines []string
	var failure *taskMarker
	started := make(map[string]string)
	for _, event := range events {
		switch event.Name {
		case queueEvent:
			update := decodeQueue(t, event)
			lines = append(lines, fmt.Sprintf("queue:%s:%t", update.LibraryItemID, update.Queued))
		case "task_started", "task_finished":
			task, ok := embedTask(event)
			if !ok {
				continue
			}
			if task.ID == "" || task.Data.LibraryItemID == "" {
				t.Fatalf("embed task without id/libraryItemId: %s", event.Args)
			}
			if event.Name == "task_started" {
				if task.IsFailed || task.IsFinished {
					t.Fatalf("task_started reports a terminal state: %s", event.Args)
				}
				started[task.Data.LibraryItemID] = task.ID
			} else if started[task.Data.LibraryItemID] != task.ID || !task.IsFinished {
				t.Fatalf("task_finished does not close a started task: %s", event.Args)
			} else if task.IsFailed {
				finished := task
				failure = &finished
			}
			lines = append(lines, event.Name+":"+task.Data.LibraryItemID)
		}
	}
	return lines, failure
}

// requireFailedTask pins the failure snapshot the client renders: the failed
// flag, the translated error key and subs the client looks up, and the terminal
// timestamps. An empty errorKey only checks that the failure is terminal.
func requireFailedTask(t *testing.T, task taskMarker, errorKey string, errorSubs []string) {
	t.Helper()
	if !task.IsFailed || !task.IsFinished {
		t.Fatalf("failed task reports isFailed=%t isFinished=%t", task.IsFailed, task.IsFinished)
	}
	if task.Error == nil || *task.Error == "" {
		t.Fatalf("failed task carries no error text")
	}
	if errorKey != "" {
		if task.ErrorKey == nil || *task.ErrorKey != errorKey {
			t.Fatalf("failed task errorKey = %v, want %s", task.ErrorKey, errorKey)
		}
		if !slices.Equal(task.ErrorSubs, errorSubs) {
			t.Fatalf("failed task errorSubs = %v, want %v", task.ErrorSubs, errorSubs)
		}
	}
	started := numericTime(t, "startedAt", task.StartedAt)
	if finished := numericTime(t, "finishedAt", task.FinishedAt); finished < started {
		t.Fatalf("failed task finishedAt %v precedes startedAt %v", finished, started)
	}
}

// requireFailedRound checks that one admission on an otherwise quiet queue ran
// and failed, with no queue transition for it on any socket.
func (a *audience) requireFailedRound(t *testing.T, from map[string]int, id string) {
	t.Helper()
	for name, socket := range a.sockets {
		events := socket.Events()
		start := from[name]
		if start > len(events) {
			start = len(events)
		}
		lines, failure := failureTrace(t, events[start:])
		var want []string
		if name != "anonymous" {
			want = []string{"task_started:" + id, "task_finished:" + id}
		}
		if !slices.Equal(lines, want) {
			t.Errorf("%s failed-task trace = %v, want %v", name, lines, want)
		}
		if name == "root" && failure == nil {
			t.Fatalf("no failed task_finished for %s", id)
		}
	}
}
