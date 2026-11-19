package metadata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Upstream sources of the cover-search socket flow. SocketAuthority registers
// search_covers and cancel_cover_search without requireAdminSocket; both handlers
// only require an authenticated socket and emit every event back to the
// requesting socket alone.
const (
	coverSocketRegistrationSrc = "server/SocketAuthority.js:195-198"
	coverSearchHandlerSrc      = "server/SocketAuthority.js:370-430"
	coverCancelHandlerSrc      = "server/SocketAuthority.js:432-451"
	coverAdminGateSrc          = "server/SocketAuthority.js:89-95"
	coverManagerStartSrc       = "server/managers/CoverSearchManager.js:34-73"
	coverManagerCancelSrc      = "server/managers/CoverSearchManager.js:78-88"
	coverManagerBooksSrc       = "server/managers/CoverSearchManager.js:117-160"
	coverManagerTimeoutSrc     = "server/managers/CoverSearchManager.js:165-217"
	coverManagerExtractSrc     = "server/managers/CoverSearchManager.js:222-241"
	coverFinderSearchSrc       = "server/finders/BookFinder.js:374-400"
	coverFinderCustomSrc       = "server/finders/BookFinder.js:205-216"
	coverAdapterRequestSrc     = "server/providers/CustomProviderAdapter.js:22-88"
	coverClientListenersSrc    = "client/components/modals/item/tabs/Cover.vue:317-330"
	coverClientHandlersSrc     = "client/components/modals/item/tabs/Cover.vue:339-375"
	coverClientSearchSrc       = "client/components/modals/item/tabs/Cover.vue:378-424"
)

const (
	// coverEventWait bounds an event the server must emit.
	coverEventWait = 15 * time.Second
	// coverSilenceWait is the window an event that must not arrive is observed for.
	coverSilenceWait = 1 * time.Second
	// coverSlowResponse is how long the slow provider stub holds its answer, so a
	// cancellation can land while the search is still active and the answer still
	// arrives afterwards inside the manager's 10 s provider budget.
	coverSlowResponse = 2 * time.Second
)

// coverResultPayload is the cover_search_result argument the web client reads
// (requestId and covers; provider and total are part of the payload).
type coverResultPayload struct {
	RequestID string   `json:"requestId"`
	Provider  string   `json:"provider"`
	Covers    []string `json:"covers"`
	Total     int      `json:"total"`
}

// coverCompletePayload is the cover_search_complete argument.
type coverCompletePayload struct {
	RequestID string `json:"requestId"`
}

// coverErrorPayload is the cover_search_error argument. requestId is optional:
// the Unauthorized and Invalid request parameters sites echo whatever the
// payload held, and JSON drops an undefined one.
type coverErrorPayload struct {
	RequestID *string `json:"requestId"`
	Error     string  `json:"error"`
}

// coverProviderErrorPayload is the cover_search_provider_error argument.
type coverProviderErrorPayload struct {
	RequestID *string `json:"requestId"`
	Provider  string  `json:"provider"`
	Error     string  `json:"error"`
}

// coverCancelledPayload is the cover_search_cancelled argument.
type coverCancelledPayload struct {
	RequestID string `json:"requestId"`
}

// coverAudience is the requesting socket plus the sockets that must never
// receive one of its cover-search events: a second connection of the same user,
// another user's socket and an unauthenticated socket.
type coverAudience struct {
	requester *harness.Socket
	sameUser  *harness.Socket
	otherUser *harness.Socket
	anonymous *harness.Socket
}

// openCoverAudience connects the four sockets of a cover-search audience.
func openCoverAudience(t *testing.T, e *harness.Env) *coverAudience {
	t.Helper()
	token := e.Token(t, "root")
	return &coverAudience{
		requester: e.Socket(t, token),
		sameUser:  e.Socket(t, token),
		otherUser: e.Socket(t, e.Token(t, "listener")),
		anonymous: e.Socket(t, ""),
	}
}

// silent returns the sockets that are not the requester.
func (a *coverAudience) silent() []*harness.Socket {
	return []*harness.Socket{a.sameUser, a.otherUser, a.anonymous}
}

// requireSilent fails when any non-requester socket received the event.
func (a *coverAudience) requireSilent(t *testing.T, marks []int, name string) {
	t.Helper()
	for i, socket := range a.silent() {
		socket.None(t, marks[i], name, coverSilenceWait, nil)
	}
}

// coverSearchPayload is the search_covers argument the web client emits.
func coverSearchPayload(requestID, provider, title string) map[string]any {
	return map[string]any{
		"requestId": requestID,
		"title":     title,
		"author":    "Ann Author",
		"provider":  provider,
		"podcast":   false,
	}
}

// emitCoverSearch sends one search_covers request.
func emitCoverSearch(t *testing.T, socket *harness.Socket, payload map[string]any) {
	t.Helper()
	socket.Emit(t, "search_covers", payload)
}

// requireCoverEventFields asserts an event carries one object argument whose
// keys are exactly want.
func requireCoverEventFields(t *testing.T, event harness.Event, want ...string) {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s has %d arguments, want 1: %s", event.Name, len(event.Args), event.Args)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(event.Args[0], &object); err != nil {
		t.Fatalf("decode %s object: %v: %s", event.Name, err, event.Args[0])
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	requireEqual(t, event.Name+" argument fields", got, sorted)
}

// coverEventOrder returns the names of the interesting events since mark in the
// order they arrived, so a scenario can pin ordering and exactly-once delivery.
func coverEventOrder(t *testing.T, socket *harness.Socket, mark int, names ...string) []string {
	t.Helper()
	interesting := make(map[string]bool, len(names))
	for _, name := range names {
		interesting[name] = true
	}
	var order []string
	for _, event := range socket.Events()[mark:] {
		if interesting[event.Name] {
			order = append(order, event.Name)
		}
	}
	return order
}

// coverResultFromProvider matches a cover_search_result for one provider.
func coverResultFromProvider(slug string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var payload coverResultPayload
		if err := json.Unmarshal(args[0], &payload); err != nil {
			return false
		}
		return payload.Provider == slug
	}
}

// coverProviderErrorFromProvider matches a cover_search_provider_error that names
// one provider.
func coverProviderErrorFromProvider(name string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var payload coverProviderErrorPayload
		if err := json.Unmarshal(args[0], &payload); err != nil {
			return false
		}
		return payload.Provider == name
	}
}

// coverStubConfig controls the local cover provider stub.
type coverStubConfig struct {
	Body   any           // marshalled JSON answer; nil means {"matches":[]}
	Raw    string        // verbatim answer body, wins over Body
	Status int           // HTTP status; 0 means 200
	Delay  time.Duration // how long the stub holds the answer
}

// coverStub is a deterministic custom metadata provider on 127.0.0.1. It
// records every request and how many answers it finished writing, so a
// cancellation scenario can prove the late answer really arrived.
type coverStub struct {
	server   *httptest.Server
	mu       sync.Mutex
	reqs     []stubRequest
	answered int
}

// newCoverStub starts the stub and closes it when the scenario ends.
func newCoverStub(t *testing.T, cfg coverStubConfig) *coverStub {
	t.Helper()
	body := []byte(cfg.Raw)
	if len(body) == 0 {
		payload := cfg.Body
		if payload == nil {
			payload = map[string]any{"matches": []any{}}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal cover stub body: %v", err)
		}
		body = encoded
	}
	stub := &coverStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.reqs = append(stub.reqs, stubRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()})
		stub.mu.Unlock()
		if cfg.Delay > 0 {
			timer := time.NewTimer(cfg.Delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		status := cfg.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		// A client that stopped reading is not the stub's concern.
		_, _ = w.Write(body)
		stub.mu.Lock()
		stub.answered++
		stub.mu.Unlock()
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// url is the base URL stored as the provider url.
func (s *coverStub) url() string { return s.server.URL }

// requestCount is the number of requests the stub received.
func (s *coverStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// requests returns a copy of the recorded requests.
func (s *coverStub) requests() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.reqs...)
}

// answeredCount is the number of answers the stub finished writing.
func (s *coverStub) answeredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answered
}

// awaitRequest blocks until the stub received want requests.
func (s *coverStub) awaitRequest(t *testing.T, want int) {
	t.Helper()
	harness.Eventually(t, coverEventWait, "cover stub requests", func() bool {
		return s.requestCount() >= want
	})
}

// awaitAnswer blocks until the stub wrote want answers.
func (s *coverStub) awaitAnswer(t *testing.T, want int) {
	t.Helper()
	harness.Eventually(t, coverEventWait, "cover stub answers", func() bool {
		return s.answeredCount() >= want
	})
}

// coverMatches builds a provider answer whose matches carry only covers; the
// adapter and the manager extract those cover strings.
func coverMatches(covers ...string) map[string]any {
	matches := make([]any, 0, len(covers))
	for _, cover := range covers {
		matches = append(matches, map[string]any{"cover": cover})
	}
	return map[string]any{"matches": matches}
}

// coverProvider creates a book custom provider that points at stub and deletes
// it when the scenario ends.
func coverProvider(t *testing.T, e *harness.Env, name string, stub *coverStub) providerView {
	t.Helper()
	view := createProvider(t, e, e.Token(t, "root"), map[string]any{"name": name, "url": stub.url(), "mediaType": "book"})
	t.Cleanup(func() { removeProvider(t, e, view.ID) })
	return view
}

// removeProvider deletes a provider without asserting the status: a scenario
// that already deleted the row leaves a 404 here, and cleanup only keeps the
// shared table tidy.
func removeProvider(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	_ = e.Delete(t, e.Token(t, "root"), "/api/custom-metadata-providers/"+id)
}

// requireSingleCoverRequest asserts the stub answered one book search with the
// query the client sent.
func requireSingleCoverRequest(t *testing.T, stub *coverStub, title, author string) {
	t.Helper()
	requests := stub.requests()
	if len(requests) != 1 {
		t.Fatalf("cover provider received %d requests, want 1: %v", len(requests), requests)
	}
	request := requests[0]
	requireEqual(t, "cover provider method", request.Method, http.MethodGet)
	requireEqual(t, "cover provider path", request.Path, "/search")
	requireEqual(t, "cover provider mediaType", request.Query.Get("mediaType"), "book")
	requireEqual(t, "cover provider query", request.Query.Get("query"), title)
	requireEqual(t, "cover provider author", request.Query.Get("author"), author)
}

// requireNoCoverRequest asserts the stub was not contacted at all.
func requireNoCoverRequest(t *testing.T, stub *coverStub) {
	t.Helper()
	if requests := stub.requests(); len(requests) != 0 {
		t.Fatalf("cover provider received %d requests, want none: %v", len(requests), requests)
	}
}
