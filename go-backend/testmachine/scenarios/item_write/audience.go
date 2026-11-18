package item_write

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// audience is a set of authenticated sockets by name, opened before an action.
type audience struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

// connect opens one socket per name. An empty token stays unauthenticated.
func connect(t *testing.T, e *harness.Env, tokens map[string]string) *audience {
	t.Helper()
	a := &audience{sockets: map[string]*harness.Socket{}, marks: map[string]int{}}
	for name, token := range tokens {
		a.sockets[name] = e.Socket(t, token)
	}
	a.reset()
	return a
}

// reset records the current event position of every socket.
func (a *audience) reset() {
	for name, s := range a.sockets {
		a.marks[name] = s.Mark()
	}
}

// idOf matches an event whose first argument has the given id.
func idOf(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var payload struct {
			ID string `json:"id"`
		}
		return len(args) > 0 && json.Unmarshal(args[0], &payload) == nil && payload.ID == id
	}
}

// receive waits for the event on each socket in want and requires its absence on each socket in not.
func (a *audience) receive(t *testing.T, name string, match func([]json.RawMessage) bool, want, not []string) map[string]harness.Event {
	t.Helper()
	got := map[string]harness.Event{}
	for _, who := range want {
		got[who] = a.sockets[who].Wait(t, a.marks[who], name, eventWait, match)
	}
	for _, who := range not {
		a.sockets[who].None(t, a.marks[who], name, absentWindow, match)
	}
	return got
}

// payload decodes the first argument of an event.
func payload(t *testing.T, ev harness.Event) map[string]any {
	t.Helper()
	if len(ev.Args) != 1 {
		t.Fatalf("event %s has %d arguments, want 1", ev.Name, len(ev.Args))
	}
	var out map[string]any
	if err := json.Unmarshal(ev.Args[0], &out); err != nil {
		t.Fatalf("event %s payload: %v", ev.Name, err)
	}
	return out
}
