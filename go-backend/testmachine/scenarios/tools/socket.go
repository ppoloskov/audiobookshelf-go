package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const queueEvent = "metadata_embed_queue_update"

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
	IsFailed   bool `json:"isFailed"`
	IsFinished bool `json:"isFinished"`
}

func embedTask(event harness.Event) (taskMarker, bool) {
	var task taskMarker
	if len(event.Args) != 1 || json.Unmarshal(event.Args[0], &task) != nil || task.Action != "embed-metadata" {
		return task, false
	}
	return task, true
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

func (a *audience) waitFinished(t *testing.T, id string) {
	t.Helper()
	event := a.sockets["root"].Wait(t, a.marks["root"], "task_finished", 30*time.Second, func(args []json.RawMessage) bool {
		task, ok := embedTask(harness.Event{Args: args})
		return ok && task.Data.LibraryItemID == id
	})
	task, _ := embedTask(event)
	if task.ID == "" || task.IsFailed || !task.IsFinished {
		t.Fatalf("embed did not finish successfully: %s", event.Args)
	}
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

func (a *audience) requireTrace(t *testing.T, ids []string) {
	t.Helper()
	for name, socket := range a.sockets {
		a.requireSocketTrace(t, name, socket, ids)
	}
}

func (a *audience) requireSocketTrace(t *testing.T, name string, socket *harness.Socket, ids []string) {
	t.Helper()
	var got []string
	started := make(map[string]string)
	for _, event := range socket.Events()[a.marks[name]:] {
		switch event.Name {
		case queueEvent:
			update := decodeQueue(t, event)
			got = append(got, fmt.Sprintf("queue:%s:%t", update.LibraryItemID, update.Queued))
		case "task_started", "task_finished":
			task, ok := embedTask(event)
			if !ok {
				continue
			}
			if task.ID == "" || task.Data.LibraryItemID == "" || task.IsFailed {
				t.Fatalf("invalid/failed embed task: %s", event.Args)
			}
			if event.Name == "task_started" {
				started[task.Data.LibraryItemID] = task.ID
			} else if !task.IsFinished || started[task.Data.LibraryItemID] != task.ID {
				t.Fatalf("task finish does not match a successful started task: %s", event.Args)
			}
			got = append(got, event.Name+":"+task.Data.LibraryItemID)
		}
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
	if name == "anonymous" {
		want = nil
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s embed trace = %v, want %v", name, got, want)
	}
}
