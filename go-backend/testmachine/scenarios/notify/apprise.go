package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// appriseCall is one recorded Apprise request. NotificationManager.sendNotification
// posts {urls, title, body} as JSON to Database.notificationSettings.appriseApiUrl
// (server/managers/NotificationManager.js:222-235).
type appriseCall struct {
	Method      string
	Path        string
	ContentType string
	Payload     map[string]any // decoded JSON body
	Raw         []byte
	Err         string // the body was not a JSON object
	At          time.Time
}

// appriseStub is a local Apprise endpoint on 127.0.0.1 that records every
// request and can answer with an error status or after a delay, so a scenario
// can prove success, provider failure and the six second client timeout without
// reaching the internet.
type appriseStub struct {
	server *httptest.Server
	done   chan struct{}

	mu     sync.Mutex
	calls  []appriseCall
	status int
	delay  time.Duration
}

// newAppriseStub starts the stub and registers its shutdown.
func newAppriseStub(t *testing.T) *appriseStub {
	t.Helper()
	stub := &appriseStub{done: make(chan struct{}), status: http.StatusOK}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(func() {
		close(stub.done)
		stub.server.Close()
	})
	return stub
}

// ServeHTTP records the request, then answers after the configured delay with
// the configured status.
func (a *appriseStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	call := appriseCall{Method: r.Method, Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), Raw: raw, At: time.Now()}
	switch {
	case err != nil:
		call.Err = err.Error()
	case len(bytes.TrimSpace(raw)) > 0:
		if err := json.Unmarshal(raw, &call.Payload); err != nil {
			call.Err = "body is not JSON: " + err.Error()
		}
	}
	a.mu.Lock()
	a.calls = append(a.calls, call)
	status, delay := a.status, a.delay
	a.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-a.done:
			return // the scenario ended; the client already gave up
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// URL returns the absolute URL of a path on the stub, the value the settings
// must carry as appriseApiUrl.
func (a *appriseStub) URL(path string) string { return a.server.URL + path }

// respond sets the status of later requests and the delay before answering.
func (a *appriseStub) respond(status int, delay time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.delay = status, delay
}

// Calls returns a snapshot of the recorded requests.
func (a *appriseStub) Calls() []appriseCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// Count returns how many requests the stub recorded.
func (a *appriseStub) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// wait waits until the stub recorded want requests within timeout.
func (a *appriseStub) wait(t *testing.T, want int, timeout time.Duration) []appriseCall {
	t.Helper()
	harness.Eventually(t, timeout, "the Apprise delivery", func() bool { return a.Count() >= want })
	return a.Calls()
}

// waitCalls waits until the stub recorded want requests and returns them.
func (a *appriseStub) waitCalls(t *testing.T, want int) []appriseCall {
	t.Helper()
	return a.wait(t, want, eventWait)
}

// requireCalls waits for want requests, then fails unless no further request
// follows within the quiet window.
func (a *appriseStub) requireExactly(t *testing.T, want int) []appriseCall {
	t.Helper()
	calls := a.waitCalls(t, want)
	time.Sleep(quietWindow) // a late delivery must show up in the count below
	if got := a.Count(); got != want {
		t.Fatalf("Apprise stub received %d requests, want %d", got, want)
	}
	return calls
}

// requireDelivery asserts the recorded call is the JSON payload of a successful
// Apprise notification.
func (c appriseCall) requireDelivery(t *testing.T, urls []string, title, body string) {
	t.Helper()
	if c.Method != http.MethodPost || c.Path != "/notify" {
		t.Fatalf("Apprise request = %s %s, want POST /notify", c.Method, c.Path)
	}
	if c.ContentType != "application/json" {
		t.Fatalf("Apprise Content-Type = %q, want application/json", c.ContentType)
	}
	if c.Err != "" {
		t.Fatalf("Apprise payload: %s (%.300s)", c.Err, c.Raw)
	}
	requireJSONEqual(t, "Apprise payload", c.Payload, map[string]any{
		"urls":  urls,
		"title": title,
		"body":  body,
	})
}

// requireNoDelivery fails when the stub received any request.
func (a *appriseStub) requireNoDelivery(t *testing.T, what string) {
	t.Helper()
	time.Sleep(quietWindow) // give a late delivery a chance to appear
	if got := a.Count(); got != 0 {
		t.Fatalf("%s: Apprise stub received %d requests, want none: %+v", what, got, a.Calls())
	}
}
