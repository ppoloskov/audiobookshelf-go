package item_cover

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// observer is a set of sockets by account name, opened before an action.
type observer struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

// observe opens one socket per name. An empty token stays unauthenticated.
func observe(t *testing.T, e *harness.Env, tokens map[string]string) *observer {
	t.Helper()
	o := &observer{sockets: map[string]*harness.Socket{}, marks: map[string]int{}}
	for name, token := range tokens {
		o.sockets[name] = e.Socket(t, token)
	}
	o.reset()
	return o
}

// reset records the current event position of every socket.
func (o *observer) reset() {
	for name, socket := range o.sockets {
		o.marks[name] = socket.Mark()
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

// anyMatch matches every event of a name (for payloads that are not an item).
func anyMatch([]json.RawMessage) bool { return true }

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

// itemUpdated waits for item_updated on each socket in want, requires its
// absence on each socket in not, and returns the payload every receiver got.
func (o *observer) itemUpdated(t *testing.T, e *harness.Env, itemID string, want, not []string) map[string]any {
	t.Helper()
	var first map[string]any
	for _, who := range want {
		if _, ok := o.sockets[who]; !ok {
			t.Fatalf("observer has no socket %q", who)
		}
		got := payload(t, o.sockets[who].Wait(t, o.marks[who], "item_updated", eventWait, idOf(itemID)))
		if got["id"] != itemID {
			t.Fatalf("%s: item_updated id %v", who, got["id"])
		}
		requireEqual(t, "payload updatedAt", got["updatedAt"], float64(updatedAt(t, e, itemID).UnixMilli()))
		if first == nil {
			first = got
		} else {
			requireEqual(t, "same item_updated payload for every receiver", got, first)
		}
	}
	for _, who := range not {
		socket, ok := o.sockets[who]
		if !ok {
			t.Fatalf("observer has no socket %q", who)
		}
		socket.None(t, o.marks[who], "item_updated", absentWindow, idOf(itemID))
	}
	if first == nil {
		t.Fatal("itemUpdated called without a receiver")
	}
	return first
}

// none requires that no connected socket receives the event.
func (o *observer) none(t *testing.T, name string, match func([]json.RawMessage) bool) {
	t.Helper()
	for who, socket := range o.sockets {
		socket.None(t, o.marks[who], name, absentWindow, match)
	}
}

// expectEvent waits for an event on one socket and returns it.
func (o *observer) expectEvent(t *testing.T, who, name string, match func([]json.RawMessage) bool) harness.Event {
	t.Helper()
	return o.sockets[who].Wait(t, o.marks[who], name, eventWait, match)
}
