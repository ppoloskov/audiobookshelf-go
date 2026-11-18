package library_manage

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventWait   = 5 * time.Second
	quietWindow = 400 * time.Millisecond
)

// audience is a set of connected sockets with the position after their last event.
type audience struct {
	sockets map[string]*harness.Socket
	marks   map[string]int
}

// connect opens one socket per named token ("" is an unauthenticated socket).
func connect(t *testing.T, e *harness.Env, tokens map[string]string) *audience {
	t.Helper()
	a := &audience{sockets: map[string]*harness.Socket{}, marks: map[string]int{}}
	for name, token := range tokens {
		a.sockets[name] = e.Socket(t, token)
	}
	a.rearm()
	return a
}

// rearm moves every mark after the events received so far.
func (a *audience) rearm() {
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
}

func (a *audience) close() {
	for _, socket := range a.sockets {
		socket.Close()
	}
}

func idMatch(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &payload) == nil && payload.ID == id
	}
}

// expect asserts that every receiver got the event for library id exactly once with equal payloads
// (returned decoded) and that no socket in absent got it.
func (a *audience) expect(t *testing.T, event, id string, receivers, absent []string) libraryJSON {
	t.Helper()
	var first libraryJSON
	for i, name := range receivers {
		ev := a.sockets[name].Wait(t, a.marks[name], event, eventWait, idMatch(id))
		if len(ev.Args) != 1 {
			t.Fatalf("%s to %s carries %d arguments, want 1", event, name, len(ev.Args))
		}
		var payload libraryJSON
		if err := json.Unmarshal(ev.Args[0], &payload); err != nil {
			t.Fatalf("%s payload to %s: %v", event, name, err)
		}
		if i == 0 {
			first = payload
		} else if !reflect.DeepEqual(first, payload) {
			t.Fatalf("%s payloads differ between sockets: %+v vs %+v", event, first, payload)
		}
	}
	for _, name := range absent {
		a.sockets[name].None(t, a.marks[name], event, quietWindow, idMatch(id))
	}
	for _, name := range receivers {
		count := 0
		for _, ev := range a.sockets[name].Events()[a.marks[name]:] {
			if ev.Name == event && idMatch(id)(ev.Args) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s for %s delivered %d times to %s, want once", event, id, count, name)
		}
	}
	return first
}

// silent asserts that no socket got the event (for any library) since the marks.
func (a *audience) silent(t *testing.T, event string) {
	t.Helper()
	for name, socket := range a.sockets {
		socket.None(t, a.marks[name], event, quietWindow, nil)
	}
}

// everyone connects the audiences of the library event scenarios: an admin, an all-access user, a user
// limited to the fixture podcast library, and an unauthenticated socket.
func everyone(t *testing.T, e *harness.Env) (*audience, accountUser) {
	t.Helper()
	limited := restrictedUser(t, e)
	return connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": limited.Token, "anonymous": ""}), limited
}
