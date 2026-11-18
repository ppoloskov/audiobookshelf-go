package browse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	eventWait   = 5 * time.Second
	eventAbsent = 400 * time.Millisecond
)

// audience is the set of sockets used to check who receives an event: every
// authenticated user (admin, regular, library-restricted) and one connection
// that never authenticated.
type audience struct {
	named map[string]*harness.Socket
	marks map[string]int
}

// connect opens the sockets of the audience.
func (s *suite) connect(t *testing.T, e *harness.Env) *audience {
	t.Helper()
	a := &audience{named: map[string]*harness.Socket{}, marks: map[string]int{}}
	a.named["root"] = e.Socket(t, e.Token(t, "root"))
	a.named["listener"] = e.Socket(t, e.Token(t, "listener"))
	a.named["podonly"] = e.Socket(t, s.restrictedToken(t, e, "browse-podonly"))
	a.named["anonymous"] = e.Socket(t, "")
	a.mark()
	return a
}

// mark records the position after the events received so far.
func (a *audience) mark() {
	for name, socket := range a.named {
		a.marks[name] = socket.Mark()
	}
}

// expect waits until every authenticated socket received the event and the
// anonymous one did not; it returns the first matching payload of root.
func (a *audience) expect(t *testing.T, name string, match func(args []json.RawMessage) bool) json.RawMessage {
	t.Helper()
	var payload json.RawMessage
	for who, socket := range a.named {
		if who == "anonymous" {
			continue
		}
		event := socket.Wait(t, a.marks[who], name, eventWait, match)
		if len(event.Args) != 1 {
			t.Fatalf("%s: %s has %d arguments", who, name, len(event.Args))
		}
		if who == "root" {
			payload = event.Args[0]
		}
	}
	a.named["anonymous"].None(t, a.marks["anonymous"], name, eventAbsent, nil)
	return payload
}

// expectNone fails if any socket receives the event (matching) after the mark.
func (a *audience) expectNone(t *testing.T, name string, match func(args []json.RawMessage) bool) {
	t.Helper()
	for who, socket := range a.named {
		socket.None(t, a.marks[who], name, eventAbsent, match)
	}
}

// idIs matches an event whose first argument has the id.
func idIs(id string) func(args []json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var payload struct {
			ID string `json:"id"`
		}
		return len(args) > 0 && json.Unmarshal(args[0], &payload) == nil && payload.ID == id
	}
}

// nameIs matches an event whose first argument has the name.
func nameIs(name string) func(args []json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var payload struct {
			Name string `json:"name"`
		}
		return len(args) > 0 && json.Unmarshal(args[0], &payload) == nil && payload.Name == name
	}
}

// metadataProvider is a local custom metadata provider the quick match asks.
type metadataProvider struct {
	server  *httptest.Server
	mu      sync.Mutex
	matches []map[string]any
	slug    string
}

// setMatches replaces what the provider answers to the next searches.
func (p *metadataProvider) setMatches(matches []map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.matches = matches
}

// provider starts the stub (once) and registers it as a custom provider.
func (s *suite) provider(t *testing.T, e *harness.Env) *metadataProvider {
	t.Helper()
	if s.metadata != nil {
		return s.metadata
	}
	p := &metadataProvider{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		matches := p.matches
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"matches": matches}); err != nil {
			t.Errorf("provider reply: %v", err)
		}
	}))
	// Not closed per scenario: later scenarios reuse the provider; the process exit ends it.
	resp := postJSON(t, e, e.Token(t, "root"), "/api/custom-metadata-providers", `{"name":"browse-stub","mediaType":"book","url":`+jsonString(p.server.URL)+`}`)
	mustOK(t, "register provider", resp)
	var created struct {
		Provider struct {
			ID string `json:"id"`
		} `json:"provider"`
	}
	resp.JSON(t, &created)
	p.slug = "custom-" + created.Provider.ID
	s.metadata = p
	return p
}

// jsonString renders a JSON string literal.
func jsonString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err) // a string always marshals
	}
	return string(raw)
}

// setBookSeries replaces the series list of a fixture book through the media route.
func setBookSeries(t *testing.T, e *harness.Env, key string, series string) {
	t.Helper()
	resp := patchJSON(t, e, e.Token(t, "root"), "/api/items/"+e.IDs[key]+"/media", `{"metadata":{"series":`+series+`}}`)
	mustOK(t, "set series of "+key, resp)
}

// match asks the quick match of a book with the provider.
func (s *suite) match(t *testing.T, e *harness.Env, token, key string, override bool) harness.Response {
	t.Helper()
	// The finder caches results per title, so every match uses a fresh title.
	s.matches++
	body := `{"provider":` + jsonString(s.metadata.slug) + `,"title":"browse match ` + strconv.Itoa(s.matches) + `","overrideDetails":`
	if override {
		body += "true}"
	} else {
		body += "false}"
	}
	return postJSON(t, e, token, "/api/items/"+e.IDs[key]+"/match", body)
}
