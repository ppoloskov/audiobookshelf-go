package podcast_episodes

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// audience is a set of sockets by name, opened before an action.
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
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
}

// receive waits for the event on each socket in want, requires its absence on
// each socket in not, and returns the events of the wanted sockets.
func (a *audience) receive(t *testing.T, name string, match func([]json.RawMessage) bool, want, not []string) map[string]harness.Event {
	t.Helper()
	got := map[string]harness.Event{}
	for _, who := range want {
		got[who] = a.sockets[who].Wait(t, a.marks[who], name, eventWait, match)
	}
	for _, who := range not {
		a.sockets[who].None(t, a.marks[who], name, quietWindow, match)
	}
	return got
}

// downloadMatcher matches a download event whose url is the given audio URL.
func downloadMatcher(audioURL string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var item downloadItem
		return json.Unmarshal(args[0], &item) == nil && item.URL == audioURL
	}
}

// episodeAddedMatcher matches an episode_added event whose episode title is the
// given title.
func episodeAddedMatcher(title string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var episode episodeJSON
		return json.Unmarshal(args[0], &episode) == nil && strPtrEquals(episode.Title, title)
	}
}

// payloadOf decodes the first argument of an event.
func payloadOf[T any](t *testing.T, event harness.Event) T {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("event %s has %d arguments, want 1", event.Name, len(event.Args))
	}
	var value T
	if err := json.Unmarshal(event.Args[0], &value); err != nil {
		t.Fatalf("event %s payload: %v: %.400s", event.Name, err, event.Args[0])
	}
	return value
}

// strPtrEquals compares a nullable string with a literal.
func strPtrEquals(value *string, want string) bool {
	return value != nil && *value == want
}
