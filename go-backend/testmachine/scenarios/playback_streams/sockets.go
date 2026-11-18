package playback_streams

import (
	"encoding/json"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// eventFirst returns the first event with name since mark and decodes its one
// argument into T.
func eventFirst[T any](t *testing.T, s *harness.Socket, mark int, name string, timeout time.Duration) T {
	t.Helper()
	ev := s.Wait(t, mark, name, timeout, nil)
	return decodeArg[T](t, ev)
}

// decodeArg decodes the single argument of an event.
func decodeArg[T any](t *testing.T, ev harness.Event) T {
	t.Helper()
	if len(ev.Args) != 1 {
		t.Fatalf("%s has %d arguments, want 1: %s", ev.Name, len(ev.Args), ev.Args)
	}
	var value T
	if err := json.Unmarshal(ev.Args[0], &value); err != nil {
		t.Fatalf("decode %s: %v: %s", ev.Name, err, ev.Args[0])
	}
	return value
}

// eventsNamed returns the decoded arguments of every event with that name since
// mark; each event must carry exactly one argument.
func eventsNamed[T any](t *testing.T, s *harness.Socket, mark int, name string) []T {
	t.Helper()
	var out []T
	for _, ev := range s.Events()[mark:] {
		if ev.Name != name {
			continue
		}
		out = append(out, decodeArg[T](t, ev))
	}
	return out
}

// audience is the sockets used to watch the stream events of one owner: two
// sockets of the owner, an admin and another user.
type audience struct {
	owner1, owner2, admin, other *harness.Socket
	marks                        [4]int
}

// watch connects the audience and marks the current end of every socket.
func watch(t *testing.T, e *harness.Env, owner actor, otherName string) *audience {
	t.Helper()
	a := &audience{
		owner1: e.Socket(t, owner.Token),
		owner2: e.Socket(t, owner.Token),
		admin:  e.Socket(t, e.Token(t, "root")),
		other:  e.Socket(t, e.Token(t, otherName)),
	}
	a.reset()
	return a
}

// reset moves every mark to the current end of the socket.
func (a *audience) reset() {
	a.marks = [4]int{a.owner1.Mark(), a.owner2.Mark(), a.admin.Mark(), a.other.Mark()}
}

// settle waits until everything emitted before the call reached the socket.
func settle(t *testing.T, s *harness.Socket) {
	t.Helper()
	mark := s.Mark()
	s.Emit(t, "ping")
	s.Wait(t, mark, "pong", 5*time.Second, nil)
}

// settleAll waits until everything emitted so far reached every socket.
func (a *audience) settleAll(t *testing.T) {
	t.Helper()
	for _, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
		settle(t, s)
	}
}

// requireOnlyOwner fails unless the event reached both owner sockets exactly
// once and no admin or other-user socket, and returns its payload.
func requireOnlyOwner[T any](t *testing.T, a *audience, name string) T {
	t.Helper()
	first := eventFirst[T](t, a.owner1, a.marks[0], name, 10*time.Second)
	eventFirst[T](t, a.owner2, a.marks[1], name, 10*time.Second)
	a.settleAll(t)
	if got := eventsNamed[T](t, a.owner1, a.marks[0], name); len(got) != 1 {
		t.Fatalf("%s reached owner socket 1 %d times, want 1", name, len(got))
	}
	if got := eventsNamed[T](t, a.owner2, a.marks[1], name); len(got) != 1 {
		t.Fatalf("%s reached owner socket 2 %d times, want 1", name, len(got))
	}
	if got := eventsNamed[T](t, a.admin, a.marks[2], name); len(got) != 0 {
		t.Fatalf("admin socket got %s: %v", name, got)
	}
	if got := eventsNamed[T](t, a.other, a.marks[3], name); len(got) != 0 {
		t.Fatalf("another user's socket got %s: %v", name, got)
	}
	return first
}

// requireNobody fails when any audience socket received the named event.
func (a *audience) requireNobody(t *testing.T, name string, window time.Duration) {
	t.Helper()
	for i, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
		s.None(t, a.marks[i], name, window, nil)
	}
}

// requireGlobal fails unless the event reached every authorized socket (both
// owner sockets, the admin and another user) and returns its payload.
func requireGlobal[T any](t *testing.T, a *audience, name string) T {
	t.Helper()
	first := eventFirst[T](t, a.owner1, a.marks[0], name, 10*time.Second)
	eventFirst[T](t, a.owner2, a.marks[1], name, 10*time.Second)
	eventFirst[T](t, a.admin, a.marks[2], name, 10*time.Second)
	eventFirst[T](t, a.other, a.marks[3], name, 10*time.Second)
	return first
}
