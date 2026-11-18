package realtime

// The Socket.IO cover search: search_covers streams provider results to the
// requesting socket and cancel_cover_search aborts an in-flight search. The
// offline-deterministic provider is a custom metadata provider whose URL is a
// stub server on 127.0.0.1 (SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 is set by
// the harness); every built-in provider is a fixed external host.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// coverSearchHandler dispatches the two cover-search events.
	coverSearchHandler = "server/SocketAuthority.js:197,374-431"
	// coverCancelHandler dispatches cancel_cover_search.
	coverCancelHandler = "server/SocketAuthority.js:198,433-451"
	// coverSearchManager is the in-flight registry, abort and result extraction.
	coverSearchManager = "server/managers/CoverSearchManager.js:51-75,110-122,201-216"
	// coverProviderTimeout is the 10 s provider timeout that ends a hanging
	// search when nobody cancels it.
	coverProviderTimeout = "server/managers/CoverSearchManager.js:24-30,178-196"
	// coverAdapterSearch is the custom provider request and response mapping.
	coverAdapterSearch = "server/providers/CustomProviderAdapter.js:21-66,90-136"
	// coverClient is the web client that emits search_covers and listens to the
	// streamed events; its provider dropdown offers the custom providers
	// (server/controllers/SearchController.js:231).
	coverClient = "client/components/modals/item/tabs/Cover.vue:385,417"
)

const coverEventWindow = 15 * time.Second

// coverSearchResult is the cover_search_result payload the client filters by
// requestId (client/components/modals/item/tabs/Cover.vue).
type coverSearchResult struct {
	RequestID string   `json:"requestId"`
	Provider  string   `json:"provider"`
	Covers    []string `json:"covers"`
	Total     int      `json:"total"`
}

// coverSearchAnswer is any of the single-object cover_search_* payloads.
type coverSearchAnswer struct {
	RequestID string `json:"requestId"`
	Error     string `json:"error"`
	Provider  string `json:"provider"`
}

// coverProviderStub is a stub custom metadata provider: a local HTTP server
// plus the server-side provider row pointing at it.
type coverProviderStub struct {
	server *httptest.Server
	slug   string // custom-<id>

	mu    sync.Mutex
	calls []string
	hang  chan struct{} // non-nil while a hanging search is in flight
}

// newCoverProviderStub creates a custom metadata provider through the admin
// API pointing at a stub server that answers search with matches.
func newCoverProviderStub(t *testing.T, e *harness.Env, matches any) *coverProviderStub {
	t.Helper()
	return newCoverProviderStubHandler(t, e, func(stub *coverProviderStub, w http.ResponseWriter, _ *http.Request) {
		writeCoverMatches(w, matches)
	})
}

// newCoverProviderStubHandler creates the provider with an arbitrary search
// handler; hook may block to keep a search in flight.
func newCoverProviderStubHandler(t *testing.T, e *harness.Env, hook func(stub *coverProviderStub, w http.ResponseWriter, r *http.Request)) *coverProviderStub {
	t.Helper()
	var stub *coverProviderStub
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		stub.record(r.URL.RawQuery)
		if hook != nil {
			hook(stub, w, r)
		}
	}))
	t.Cleanup(server.Close)

	resp := e.Post(t, e.Token(t, "root"), "/api/custom-metadata-providers", map[string]any{
		"name": "rt-cover-provider", "url": server.URL, "mediaType": "book",
	})
	requireStatus(t, resp, http.StatusOK, "create custom metadata provider")
	var created struct {
		Provider struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"provider"`
	}
	resp.JSON(t, &created)
	if created.Provider.ID == "" {
		t.Fatalf("custom provider answer: %.300s", resp.Body)
	}
	stub = &coverProviderStub{server: server, slug: "custom-" + created.Provider.ID}
	t.Cleanup(stub.release) // never leave a handler blocking
	return stub
}

// writeCoverMatches answers a custom provider search body.
func writeCoverMatches(w http.ResponseWriter, matches any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"matches": matches}); err != nil {
		// the caller is in-process; a failed encode is a scenario bug
		panic(err)
	}
}

// record notes the RawQuery of a stub call.
func (stub *coverProviderStub) record(query string) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.calls = append(stub.calls, query)
}

// queries returns the recorded stub queries.
func (stub *coverProviderStub) queries() []string {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return slices.Clone(stub.calls)
}

// hangSearch makes /search block until release.
func (stub *coverProviderStub) hangSearch(t *testing.T) {
	t.Helper()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.hang = make(chan struct{})
}

// waitHang blocks while a hanging search is in flight.
func (stub *coverProviderStub) waitHang() {
	stub.mu.Lock()
	hang := stub.hang
	stub.mu.Unlock()
	if hang != nil {
		<-hang
	}
}

// release unblocks a hanging search; it is safe to call twice.
func (stub *coverProviderStub) release() {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.hang != nil {
		close(stub.hang)
		stub.hang = nil
	}
}

// waitCoverEvent waits for the next cover_search_* event with that name whose
// decoded requestId matches (empty requestId matches any).
func waitCoverEvent(t *testing.T, s *harness.Socket, mark int, name, requestID string) harness.Event {
	t.Helper()
	return s.Wait(t, mark, name, coverEventWindow, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		return requestID == "" || decodeRaw[coverSearchAnswer](t, args[0]).RequestID == requestID
	})
}

// coverEvents returns the cover events of that name since mark for the
// requestId (empty matches any).
func coverEvents(t *testing.T, s *harness.Socket, mark int, name, requestID string) []harness.Event {
	t.Helper()
	var found []harness.Event
	for i, event := range s.Events() {
		if i < mark || event.Name != name || len(event.Args) == 0 {
			continue
		}
		if requestID == "" || decodeRaw[coverSearchAnswer](t, event.Args[0]).RequestID == requestID {
			found = append(found, event)
		}
	}
	return found
}

// emitSearchCovers emits search_covers with the payload the client sends
// (client/components/modals/item/tabs/Cover.vue:417).
func emitSearchCovers(t *testing.T, s *harness.Socket, requestID, title, author, provider string, podcast bool) {
	t.Helper()
	s.Emit(t, "search_covers", map[string]any{
		"requestId": requestID, "title": title, "author": author, "provider": provider, "podcast": podcast,
	})
}

// waitInvalidCoverError waits for the next cover_search_error whose error is
// 'Invalid request parameters' and whose requestId matches wantID (empty when
// the answer must not carry one).
func waitInvalidCoverError(t *testing.T, s *harness.Socket, mark int, wantID string) harness.Event {
	t.Helper()
	return s.Wait(t, mark, "cover_search_error", coverEventWindow, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		answer := decodeRaw[coverSearchAnswer](t, args[0])
		return answer.Error == "Invalid request parameters" && answer.RequestID == wantID
	})
}

// searchCoversScenarios covers socket-incoming-search-covers.
func searchCoversScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-search-covers", Invariant: "INV-SOCKET-INCOMING-SEARCH-COVERS", Case: "normal", State: "initial", Assertion: "covers-custom-provider-streams-results",
			Sources: []string{coverSearchHandler, coverSearchManager, coverAdapterSearch, coverClient},
			Claim:   "A non-admin socket searching a custom provider receives cover_search_result with the provider slug and the covers of the provider answer before cover_search_complete, both carrying its requestId; the stub received the book query.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStub(t, e, []any{map[string]any{"title": "Stub Book", "cover": "http://127.0.0.1/stub-cover.jpg"}})
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-cover-1", "Stub Title", "Stub Author", stub.slug, false)

				result := decodeArg[coverSearchResult](t, waitCoverEvent(t, socket, mark, "cover_search_result", "rt-cover-1"))
				if result.RequestID != "rt-cover-1" || result.Provider != stub.slug || result.Total != 1 {
					t.Fatalf("cover_search_result: %+v", result)
				}
				if !reflect.DeepEqual(result.Covers, []string{"http://127.0.0.1/stub-cover.jpg"}) {
					t.Fatalf("cover_search_result covers: %+v", result.Covers)
				}
				complete := decodeArg[coverSearchAnswer](t, waitCoverEvent(t, socket, mark, "cover_search_complete", "rt-cover-1"))
				if complete.RequestID != "rt-cover-1" {
					t.Fatalf("cover_search_complete: %+v", complete)
				}
				flush(t, socket)
				for _, name := range []string{"cover_search_error", "cover_search_provider_error"} {
					if got := coverEvents(t, socket, mark, name, ""); len(got) != 0 {
						t.Fatalf("unexpected %s: %v", name, got)
					}
				}
				if got := countEvents(socket, mark, "cover_search_complete"); got != 1 {
					t.Fatalf("want one cover_search_complete, got %d", got)
				}

				// The stub received the book search query of the adapter
				// (server/providers/CustomProviderAdapter.js:31-42).
				harness.Eventually(t, 5*time.Second, "the provider stub to be called", func() bool {
					return len(stub.queries()) > 0
				})
				values, err := url.ParseQuery(stub.queries()[0])
				if err != nil {
					t.Fatalf("stub query %q: %v", stub.queries()[0], err)
				}
				if values.Get("mediaType") != "book" || values.Get("query") != "Stub Title" || values.Get("author") != "Stub Author" {
					t.Fatalf("stub query: %v", values)
				}
			},
		},
		{
			Requirement: "socket-incoming-search-covers", Invariant: "INV-SOCKET-INCOMING-SEARCH-COVERS", Case: "normal", State: "repeat", Assertion: "covers-concurrent-searches-carry-own-ids",
			Sources: []string{coverSearchHandler, coverSearchManager},
			Claim:   "Two concurrent searches from one socket, each with its own requestId, stream their results separately: each requestId gets its own result and complete events.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStub(t, e, []any{map[string]any{"title": "Repeat Book", "cover": "http://127.0.0.1/repeat-cover.jpg"}})
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-repeat-a", "Repeat Title", "", stub.slug, false)
				emitSearchCovers(t, socket, "rt-repeat-b", "Repeat Title", "", stub.slug, false)

				for _, id := range []string{"rt-repeat-a", "rt-repeat-b"} {
					result := decodeArg[coverSearchResult](t, waitCoverEvent(t, socket, mark, "cover_search_result", id))
					if result.RequestID != id || result.Total != 1 || len(result.Covers) != 1 {
						t.Fatalf("cover_search_result %s: %+v", id, result)
					}
					waitCoverEvent(t, socket, mark, "cover_search_complete", id)
				}
				if got := countEvents(socket, mark, "cover_search_result"); got != 2 {
					t.Fatalf("want two cover_search_result events, got %d", got)
				}
				if got := countEvents(socket, mark, "cover_search_complete"); got != 2 {
					t.Fatalf("want two cover_search_complete events, got %d", got)
				}
			},
		},
		{
			Requirement: "socket-incoming-search-covers", Invariant: "INV-SOCKET-INCOMING-SEARCH-COVERS", Case: "error", State: "failure", Assertion: "covers-unauthorized-gets-error",
			Sources: []string{coverSearchHandler},
			Claim:   "A socket without a user gets cover_search_error with its requestId echoed and error Unauthorized, and starts no search: the stub is never called.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStub(t, e, []any{map[string]any{"title": "Denied Book", "cover": "http://127.0.0.1/denied.jpg"}})
				anon := connectAnon(t, e)
				mark := anon.Mark()
				emitSearchCovers(t, anon, "rt-denied", "Denied Title", "", stub.slug, false)
				answer := decodeArg[coverSearchAnswer](t, waitCoverEvent(t, anon, mark, "cover_search_error", "rt-denied"))
				if answer.Error != "Unauthorized" {
					t.Fatalf("cover_search_error: %+v", answer)
				}
				flush(t, anon)
				for _, name := range []string{"cover_search_result", "cover_search_complete", "cover_search_provider_error"} {
					if got := coverEvents(t, anon, mark, name, ""); len(got) != 0 {
						t.Fatalf("unexpected %s: %v", name, got)
					}
				}
				if len(stub.queries()) != 0 {
					t.Fatalf("a rejected search must not reach the provider: %v", stub.queries())
				}
			},
		},
		{
			Requirement: "socket-incoming-search-covers", Invariant: "INV-SOCKET-INCOMING-SEARCH-COVERS", Case: "edge", State: "initial", Assertion: "covers-invalid-parameters",
			Sources: []string{coverSearchHandler},
			Claim:   "A search without requestId or title is answered with cover_search_error error 'Invalid request parameters', carrying the requestId only when the payload defined one; a non-object payload from an anonymous socket is answered Unauthorized without a requestId.",
			Run: func(t *testing.T, e *harness.Env) {
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()

				for _, invalid := range []struct {
					payload map[string]any
					wantID  string // the requestId key the answer must carry
				}{
					{map[string]any{"title": "No Id", "provider": "google", "podcast": false}, ""},
					{map[string]any{"requestId": "rt-half", "provider": "google", "podcast": false}, "rt-half"},
				} {
					socket.Emit(t, "search_covers", invalid.payload)
					event := waitInvalidCoverError(t, socket, mark, invalid.wantID)
					answer := argObject(t, event)
					if answer["error"] != "Invalid request parameters" {
						t.Fatalf("payload %v: error %v", invalid.payload, answer["error"])
					}
					if got, _ := answer["requestId"].(string); got != invalid.wantID {
						t.Fatalf("payload %v: want requestId %q in the answer, got %v", invalid.payload, invalid.wantID, answer["requestId"])
					}
				}
				flush(t, socket)
				requireQuiet(t, socket, mark, "cover_search_result", "cover_search_complete", "cover_search_provider_error")

				anon := connectAnon(t, e)
				anonMark := anon.Mark()
				anon.Emit(t, "search_covers", "not-an-object")
				answer := decodeArg[coverSearchAnswer](t, waitCoverEvent(t, anon, anonMark, "cover_search_error", ""))
				if answer.Error != "Unauthorized" || answer.RequestID != "" {
					t.Fatalf("string payload: %+v", answer)
				}
				flush(t, anon)
				// search_covers with no payload at all is not emitted here: upstream
				// fails on it with an unhandled rejection that exits the process
				// (server/SocketAuthority.js:379, server/Server.js:246-251), and no
				// client sends it.
			},
		},
		{
			Requirement: "socket-incoming-search-covers", Invariant: "INV-SOCKET-INCOMING-SEARCH-COVERS", Case: "edge", State: "changed", Assertion: "covers-empty-provider-answer-completes-quietly",
			Sources: []string{coverSearchHandler, coverSearchManager, coverAdapterSearch},
			Claim:   "After the provider answers without covers, the search completes with only cover_search_complete: no result event and no error.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStub(t, e, []any{})
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-empty", "Empty Title", "", stub.slug, false)
				complete := decodeArg[coverSearchAnswer](t, waitCoverEvent(t, socket, mark, "cover_search_complete", "rt-empty"))
				if complete.RequestID != "rt-empty" {
					t.Fatalf("cover_search_complete: %+v", complete)
				}
				flush(t, socket)
				for _, name := range []string{"cover_search_result", "cover_search_error", "cover_search_provider_error"} {
					if got := coverEvents(t, socket, mark, name, ""); len(got) != 0 {
						t.Fatalf("unexpected %s: %v", name, got)
					}
				}
			},
		},
	}
}

// cancelCoversScenarios covers socket-incoming-cancel-cover-search.
func cancelCoversScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-cancel-cover-search", Invariant: "INV-SOCKET-INCOMING-CANCEL-COVER-SEARCH", Case: "normal", State: "initial", Assertion: "cover-cancel-aborts-in-flight-search",
			Sources: []string{coverCancelHandler, coverSearchManager, coverClient},
			Claim:   "Cancelling an in-flight search answers cover_search_cancelled with the requestId on the cancelling socket and no complete, result or provider error follows.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStubHandler(t, e, func(stub *coverProviderStub, w http.ResponseWriter, _ *http.Request) {
					stub.waitHang()
					writeCoverMatches(w, []any{})
				})
				stub.hangSearch(t)
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-cancel-1", "Cancel Title", "", stub.slug, false)
				harness.Eventually(t, 5*time.Second, "the provider stub to be called", func() bool {
					return len(stub.queries()) > 0
				})

				socket.Emit(t, "cancel_cover_search", "rt-cancel-1")
				answer := decodeArg[coverSearchAnswer](t, waitCoverEvent(t, socket, mark, "cover_search_cancelled", "rt-cancel-1"))
				if answer.RequestID != "rt-cancel-1" {
					t.Fatalf("cover_search_cancelled: %+v", answer)
				}
				stub.release()
				flush(t, socket)
				for _, name := range []string{"cover_search_complete", "cover_search_result", "cover_search_provider_error"} {
					if got := coverEvents(t, socket, mark, name, ""); len(got) != 0 {
						t.Fatalf("a cancelled search must not emit %s: %s", name, got)
					}
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-cover-search", Invariant: "INV-SOCKET-INCOMING-CANCEL-COVER-SEARCH", Case: "normal", State: "repeat", Assertion: "cover-cancel-idempotent-single-event",
			Sources: []string{coverCancelHandler, coverSearchManager},
			Claim:   "Two cancel_cover_search events for one in-flight search produce exactly one cover_search_cancelled: the second cancellation finds no active search.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStubHandler(t, e, func(stub *coverProviderStub, w http.ResponseWriter, _ *http.Request) {
					stub.waitHang()
					writeCoverMatches(w, []any{})
				})
				stub.hangSearch(t)
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-cancel-2", "Cancel Title", "", stub.slug, false)
				harness.Eventually(t, 5*time.Second, "the provider stub to be called", func() bool {
					return len(stub.queries()) > 0
				})

				socket.Emit(t, "cancel_cover_search", "rt-cancel-2")
				socket.Emit(t, "cancel_cover_search", "rt-cancel-2")
				waitCoverEvent(t, socket, mark, "cover_search_cancelled", "rt-cancel-2")
				stub.release()
				flush(t, socket)
				if got := len(coverEvents(t, socket, mark, "cover_search_cancelled", "")); got != 1 {
					t.Fatalf("want exactly one cover_search_cancelled, got %d", got)
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-cover-search", Invariant: "INV-SOCKET-INCOMING-CANCEL-COVER-SEARCH", Case: "edge", State: "changed", Assertion: "cover-cancel-after-completion-noop",
			Sources: []string{coverCancelHandler, coverSearchManager},
			Claim:   "Cancelling after the search completed, or with an unknown requestId, emits nothing: only active searches can be cancelled.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStub(t, e, []any{map[string]any{"title": "Done Book", "cover": "http://127.0.0.1/done.jpg"}})
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				emitSearchCovers(t, socket, "rt-cancel-done", "Done Title", "", stub.slug, false)
				waitCoverEvent(t, socket, mark, "cover_search_complete", "rt-cancel-done")

				socket.Emit(t, "cancel_cover_search", "rt-cancel-done")
				socket.Emit(t, "cancel_cover_search", "rt-never-started")
				flush(t, socket)
				for _, requestID := range []string{"rt-cancel-done", "rt-never-started"} {
					if got := coverEvents(t, socket, mark, "cover_search_cancelled", requestID); len(got) != 0 {
						t.Fatalf("a cancel after completion must not emit cover_search_cancelled for %s: %v", requestID, got)
					}
				}
				// The completed search's own completion is the only cover event.
				if got := len(coverEvents(t, socket, mark, "cover_search_complete", "rt-cancel-done")); got != 1 {
					t.Fatalf("want only the search's own cover_search_complete, got %d", got)
				}
			},
		},
		{
			Requirement: "socket-incoming-cancel-cover-search", Invariant: "INV-SOCKET-INCOMING-CANCEL-COVER-SEARCH", Case: "error", State: "failure", Assertion: "cover-cancel-gated-by-auth",
			Sources: []string{coverCancelHandler, coverProviderTimeout},
			Claim:   "An unauthenticated cancel_cover_search emits nothing and does not abort another user's in-flight search, which then times out at the provider and completes.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newCoverProviderStubHandler(t, e, func(stub *coverProviderStub, w http.ResponseWriter, _ *http.Request) {
					stub.waitHang()
					writeCoverMatches(w, []any{})
				})
				stub.hangSearch(t)
				listener, _ := connectAuth(t, e, e.Token(t, "listener"))
				anon := connectAnon(t, e)
				listenerMark, anonMark := listener.Mark(), anon.Mark()
				emitSearchCovers(t, listener, "rt-cancel-gated", "Gated Title", "", stub.slug, false)
				harness.Eventually(t, 5*time.Second, "the provider stub to be called", func() bool {
					return len(stub.queries()) > 0
				})

				anon.Emit(t, "cancel_cover_search", "rt-cancel-gated")
				// The unauthorized cancellation is silent on the anonymous socket.
				flush(t, anon)
				if got := coverEvents(t, anon, anonMark, "cover_search_cancelled", ""); len(got) != 0 {
					t.Fatalf("an unauthorized cancel must not emit cover_search_cancelled: %s", got)
				}
				// The search is still in flight: it hits the 10 s provider timeout
				// and completes.
				providerError := waitCoverEvent(t, listener, listenerMark, "cover_search_provider_error", "rt-cancel-gated")
				if answer := argObject(t, providerError); answer["provider"] != stub.slug {
					t.Fatalf("cover_search_provider_error: %v", answer)
				}
				waitCoverEvent(t, listener, listenerMark, "cover_search_complete", "rt-cancel-gated")
				if got := coverEvents(t, listener, listenerMark, "cover_search_cancelled", ""); len(got) != 0 {
					t.Fatalf("the search must not have been cancelled: %s", got)
				}
				stub.release()
			},
		},
	}
}
