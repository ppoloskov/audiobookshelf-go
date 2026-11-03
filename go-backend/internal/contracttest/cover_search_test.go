package contracttest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The cover tab's Socket.IO cover search (client/components/modals/item/tabs/
// Cover.vue; upstream SocketAuthority.handleCoverSearch). The providers are
// custom metadata providers on local servers, so no test reaches the network.

// coverEvent is one cover_search_* event the root socket received.
type coverEvent struct {
	name    string
	payload map[string]any
}

// coverSocket is a root socket that forwards the cover_search_* events.
type coverSocket struct {
	client *sioClient
	events chan coverEvent
}

// dialCoverSocket connects as the token's user and waits for init.
func dialCoverSocket(t *testing.T, in *Install, token string) *coverSocket {
	t.Helper()
	cs := &coverSocket{events: make(chan coverEvent, 64)}
	ready := make(chan struct{})
	var readyOnce sync.Once
	client, err := dialSocketIO(in.BaseURL, journeySocketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("cover socket: %v", err)
	}
	client.onEvent = func(name string, payload any) {
		switch {
		case name == "init":
			readyOnce.Do(func() { close(ready) })
		case strings.HasPrefix(name, "cover_search_"):
			body, _ := payload.(map[string]any)
			cs.events <- coverEvent{name: name, payload: body}
		}
	}
	cs.client = client
	t.Cleanup(client.close)
	if err := client.emit("auth", token); err != nil {
		t.Fatalf("cover socket: auth: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("cover socket: no init within 10s")
	}
	return cs
}

// emit sends one client event and fails the test if it cannot be written.
func (cs *coverSocket) emit(t *testing.T, name string, arg any) {
	t.Helper()
	if err := cs.client.emit(name, arg); err != nil {
		t.Fatalf("emit %s: %v", name, err)
	}
}

// waitCover returns the next event named name for requestID, skipping others.
func (cs *coverSocket) waitCover(t *testing.T, name, requestID string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev := <-cs.events:
			if ev.name == name && ev.payload["requestId"] == requestID {
				return ev.payload
			}
		case <-deadline:
			t.Fatalf("no %s for %s within %s", name, requestID, within)
		}
	}
}

// expectNoCover fails if an event named name arrives for requestID within
// within.
func (cs *coverSocket) expectNoCover(t *testing.T, name, requestID string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev := <-cs.events:
			if ev.name == name && ev.payload["requestId"] == requestID {
				t.Fatalf("unexpected %s for %s after cancel", name, requestID)
			}
		case <-deadline:
			return
		}
	}
}

// createCustomProvider adds a book custom provider whose search is at url and
// returns its id (the client's "custom-<id>" provider value is the slug).
func createCustomProvider(t *testing.T, in *Install, token, url string) string {
	t.Helper()
	resp, raw := in.Do(t, http.MethodPost, "/api/custom-metadata-providers", token, map[string]any{
		"name": "Cover Provider", "url": url, "mediaType": "book", "authHeaderValue": "",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create custom provider: status %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		Provider struct {
			ID string `json:"id"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Provider.ID == "" {
		t.Fatalf("create custom provider: no id in %s", snippet(raw))
	}
	return out.Provider.ID
}

// TestCoverSearchSocket: a search_covers request gets a cover_search_result
// with the provider's covers, then cover_search_complete.
func TestCoverSearchSocket(t *testing.T) {
	in := Boot(t)
	token := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	source := startJourneyImageSource(t)
	providerID := createCustomProvider(t, in, token, source+"/search")
	cs := dialCoverSocket(t, in, token)
	cs.emit(t, "search_covers", map[string]any{
		"requestId": "cover-1", "title": "Matched Book", "author": "", "provider": "custom-" + providerID, "podcast": false,
	})
	result := cs.waitCover(t, "cover_search_result", "cover-1", 10*time.Second)
	if result["provider"] != "custom-"+providerID {
		t.Fatalf("cover_search_result provider = %v", result["provider"])
	}
	covers, _ := result["covers"].([]any)
	if len(covers) != 1 || covers[0] != source+"/author.jpg" {
		t.Fatalf("cover_search_result covers = %v, want [%s/author.jpg]", result["covers"], source)
	}
	cs.waitCover(t, "cover_search_complete", "cover-1", 10*time.Second)
}

// TestCoverSearchCancel: a cancelled search answers cover_search_cancelled and
// sends nothing more, not even cover_search_complete.
func TestCoverSearchCancel(t *testing.T) {
	in := Boot(t)
	token := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	// The provider never answers until the test ends, so the search is still
	// running when the cancel arrives.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	providerID := createCustomProvider(t, in, token, slow.URL+"/search")
	cs := dialCoverSocket(t, in, token)
	cs.emit(t, "search_covers", map[string]any{
		"requestId": "cover-2", "title": "Slow Book", "author": "", "provider": "custom-" + providerID, "podcast": false,
	})
	// The socket reads its events in order, so the search is registered before
	// the cancel that follows it is handled.
	cs.emit(t, "cancel_cover_search", "cover-2")
	cs.waitCover(t, "cover_search_cancelled", "cover-2", 10*time.Second)
	cs.expectNoCover(t, "cover_search_complete", "cover-2", 1500*time.Millisecond)
}
