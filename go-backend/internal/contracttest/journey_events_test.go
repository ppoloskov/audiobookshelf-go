package contracttest

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/contracttest/shape"
)

// The journey's Socket.IO events. The replacement trial (connectEventSocket in
// tools/parity/replacement-trial.cjs) connects one root client after login.root
// and records the events below; a step that declares events waits for them and
// the runner checks their names, recipient and payload shapes.

// journeySocketPath is the router-prefixed Socket.IO path the trial connects to.
// The Go server also serves the legacy /socket.io path.
const journeySocketPath = "/audiobookshelf/socket.io"

// journeyEventNames are the events the root client records. The trial's list,
// plus task_finished: the matchall step's completion event.
var journeyEventNames = map[string]bool{
	"rss_feed_open": true, "rss_feed_closed": true, "share_open": true, "share_closed": true,
	"author_updated": true, "author_removed": true, "author_added": true,
	"items_updated": true, "item_updated": true, "item_removed": true,
	"series_updated": true, "series_removed": true,
	"batch_quickmatch_complete": true, "task_finished": true,
}

// journeyMatchAllAction is the action of the one task the journey starts
// itself (matchall). The file watcher also finishes tasks during the journey
// (watcher-scan, about 10s after an earlier step's file write), so a
// task_finished counts only when it is this one.
const journeyMatchAllAction = "library-match-all"

// journeyEvent is one event the root client received during a step.
type journeyEvent struct {
	name    string
	payload any
}

// eventShapes maps each event a step emitted to the skeleton of its payloads.
// It is journey.json's "events" for a step.
type eventShapes map[string]*shape.Skeleton

// eventSocket is the root client's Socket.IO connection. It buffers the
// journey's events until a step takes them.
type eventSocket struct {
	client *sioClient
	mu     sync.Mutex
	events []journeyEvent
}

// connectEventSocket connects as the root user the way the trial does: the auth
// event with the token once the namespace is connected, then it waits for init.
func connectEventSocket(t testing.TB, baseURL, token string) *eventSocket {
	t.Helper()
	es := &eventSocket{}
	ready := make(chan struct{})
	var readyOnce sync.Once
	client, err := dialSocketIO(baseURL, journeySocketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("event socket: %v", err)
	}
	client.onEvent = func(name string, payload any) {
		switch {
		case name == "init":
			readyOnce.Do(func() { close(ready) })
		case journeyEventNames[name]:
			es.record(name, payload)
		}
	}
	es.client = client
	if err := client.emit("auth", token); err != nil {
		client.close()
		t.Fatalf("event socket: auth: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		client.close()
		t.Fatal("event socket: no init within 10s")
	}
	es.drain()
	return es
}

func (es *eventSocket) record(name string, payload any) {
	if name == "task_finished" {
		if task, _ := payload.(map[string]any); task["action"] != journeyMatchAllAction {
			return
		}
	}
	es.mu.Lock()
	es.events = append(es.events, journeyEvent{name: name, payload: payload})
	es.mu.Unlock()
}

// drain forgets the events received so far, so a step sees only its own.
func (es *eventSocket) drain() {
	es.mu.Lock()
	es.events = nil
	es.mu.Unlock()
}

// take waits up to 3s for the events a step expects, then 150ms more for
// stragglers, and returns and clears what arrived. It waits for every name in
// want when the recording names them; otherwise for count events.
func (es *eventSocket) take(want eventShapes, count int) []journeyEvent {
	deadline := time.Now().Add(3 * time.Second)
	for !es.complete(want, count) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	es.mu.Lock()
	defer es.mu.Unlock()
	out := es.events
	es.events = nil
	return out
}

func (es *eventSocket) complete(want eventShapes, count int) bool {
	es.mu.Lock()
	defer es.mu.Unlock()
	if len(want) == 0 {
		return len(es.events) >= count
	}
	seen := map[string]bool{}
	for _, ev := range es.events {
		seen[ev.name] = true
	}
	for name := range want {
		if !seen[name] {
			return false
		}
	}
	return true
}

func (es *eventSocket) close() {
	if es != nil && es.client != nil {
		es.client.close()
	}
}

// closeEvents disconnects the root client, if login.root connected one.
func (c *journeyCtx) closeEvents() { c.events.close() }

// checkJourneyEvents checks the events a step received against its recording:
// every event is one the step emitted, each recorded name arrived, and every
// payload conforms to its name's skeleton.
func checkJourneyEvents(t testing.TB, want eventShapes, got []journeyEvent) {
	t.Helper()
	seen := map[string]bool{}
	for _, ev := range got {
		skel, ok := want[ev.name]
		if !ok {
			t.Errorf("unexpected event %s: %s", ev.name, payloadSnippet(ev.payload))
			continue
		}
		seen[ev.name] = true
		for _, problem := range shape.Conforms(skel, ev.payload) {
			t.Errorf("event %s: %s", ev.name, problem)
		}
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			t.Errorf("missing event %s", name)
		}
	}
}

// eventShapesOf records the events a step received: one skeleton per name,
// inferred from all of that name's payloads.
func eventShapesOf(got []journeyEvent) eventShapes {
	samples := map[string][]any{}
	for _, ev := range got {
		samples[ev.name] = append(samples[ev.name], ev.payload)
	}
	out := eventShapes{}
	for name, payloads := range samples {
		out[name] = shape.Infer(payloads...)
	}
	return out
}

func payloadSnippet(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return snippet(raw)
}
