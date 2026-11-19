package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Upstream sources shared by the packet's scenarios. Every expectation below is
// derived from one of these files; remote provider hosts are only reached for
// the paths this packet marks not applicable.
const (
	searchRouterSrc      = "server/routers/ApiRouter.js:284-291"
	customRouterSrc      = "server/routers/ApiRouter.js:316-321"
	apiAuthSrc           = "server/Server.js:354"
	queryParamsSrc       = "server/utils/index.js:326-343"
	validateUrlSrc       = "server/utils/index.js:233-241"
	validASINSrc         = "server/utils/index.js:260-263"
	searchControllerSrc  = "server/controllers/SearchController.js:85-231"
	fetchItemSrc         = "server/controllers/SearchController.js:47-54"
	providersPayloadSrc  = "server/controllers/SearchController.js:220-231"
	customControllerSrc  = "server/controllers/CustomMetadataProviderController.js:24-122"
	customProviderModel  = "server/models/CustomMetadataProvider.js:96-103"
	customAdapterSrc     = "server/providers/CustomProviderAdapter.js:21-145"
	bookFinderSrc        = "server/finders/BookFinder.js:374-409"
	bookFinderCoversSrc  = "server/finders/BookFinder.js:608-640"
	bookFinderCustomSrc  = "server/finders/BookFinder.js:205-216"
	podcastFinderSrc     = "server/finders/PodcastFinder.js:15-25"
	authorFinderSrc      = "server/finders/AuthorFinder.js:25-35"
	chaptersFinderSrc    = "server/finders/BookFinder.js:642-644"
	audnexusChaptersSrc  = "server/providers/Audnexus.js:151-165"
	itunesSearchSrc      = "server/providers/iTunes.js:44-70"
	socketEmitterSrc     = "server/SocketAuthority.js:49-60"
	webProviderStoreSrc  = "client/store/scanners.js:26-57"
	webProviderPageSrc   = "client/pages/config/item-metadata-utils/custom-metadata-providers.vue:50-62"
	webProviderTableSrc  = "client/components/tables/CustomMetadataProviderTable.vue:20-70"
	webProviderModalSrc  = "client/components/modals/AddCustomMetadataProviderModal.vue:70-95"
	webMatchSrc          = "client/components/modals/item/tabs/Match.vue:400-430"
	webUploadSrc         = "client/components/cards/ItemUploadCard.vue:180-200"
	webChaptersSrc       = "client/pages/audiobook/_id/chapters.vue:781-790"
	webPodcastSearchSrc  = "client/pages/library/_library/podcast/search.vue:160-175"
	selfHostedCoverHost  = "127.0.0.1"
	customProviderPrefix = "custom-"
)

// fixtureLibraryProvider and fixturePodcastProvider are the seeded provider
// columns of the book and podcast libraries (fixture install).
const (
	fixtureLibraryProvider = "google"
	fixturePodcastProvider = "itunes"
)

// providerView is the provider object the create route returns and one entry of
// the admin list. The list serialises the model (no slug); create and the
// socket events serialise toClientJson (with slug).
type providerView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	MediaType       string  `json:"mediaType"`
	URL             string  `json:"url"`
	AuthHeaderValue *string `json:"authHeaderValue"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
	Slug            string  `json:"slug"`
}

// providerEvent is the custom_metadata_provider_added/removed payload
// (CustomMetadataProvider.toClientJson).
type providerEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Slug      string `json:"slug"`
}

// bookResult is one entry of GET /api/search/books as the web client reads it
// (Match.vue and ItemUploadCard.vue read title, author and series).
type bookResult struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	Author        string   `json:"author"`
	Narrator      string   `json:"narrator"`
	Publisher     string   `json:"publisher"`
	PublishedYear string   `json:"publishedYear"`
	Description   string   `json:"description"`
	Cover         string   `json:"cover"`
	ISBN          string   `json:"isbn"`
	ASIN          string   `json:"asin"`
	Genres        []string `json:"genres"`
	Tags          []string `json:"tags"`
	Series        []struct {
		Series   string `json:"series"`
		Sequence string `json:"sequence"`
	} `json:"series"`
	Language string   `json:"language"`
	Duration *float64 `json:"duration"`
}

// stubRequest is one request a local custom metadata provider received.
type stubRequest struct {
	Method        string
	Path          string
	Query         url.Values
	Authorization string
}

// providerStub is a local custom metadata provider served by httptest on
// 127.0.0.1: it answers every request with a fixed JSON body and records what
// the server under test asked for.
type providerStub struct {
	server *httptest.Server
	mu     sync.Mutex
	reqs   []stubRequest
}

func newProviderStub(t *testing.T, body any) *providerStub {
	t.Helper()
	stub := &providerStub{}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal stub payload: %v", err)
	}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.reqs = append(stub.reqs, stubRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.Query(),
			Authorization: r.Header.Get("Authorization"),
		})
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// A closed client connection is the client's concern, not the stub's.
		_, _ = w.Write(payload)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// url is the base URL stored as the provider url (the adapter appends /search).
func (s *providerStub) url() string { return s.server.URL }

// requests returns a copy of the recorded requests.
func (s *providerStub) requests() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.reqs...)
}

// scenario is the constructor used by the scenario tables.
func scenario(requirement, invariant, caseName, state, assertion string, sources []string, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement,
		Invariant:   invariant,
		Case:        caseName,
		State:       state,
		Assertion:   assertion,
		Sources:     sources,
		Claim:       claim,
		Run:         run,
	}
}

// requireStatus fails unless the response has the expected status.
func requireStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %.300s", what, status, resp.Status, resp.Body)
	}
}

// requireEqual compares two values by their JSON encoding.
func requireEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: encode got: %v", what, err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: encode want: %v", what, err)
	}
	if string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// createProvider posts a custom metadata provider as the admin and returns the
// decoded provider of the response.
func createProvider(t *testing.T, e *harness.Env, token string, body map[string]any) providerView {
	t.Helper()
	resp := e.Post(t, token, "/api/custom-metadata-providers", body)
	requireStatus(t, resp, http.StatusOK, "create custom metadata provider")
	var payload struct {
		Provider providerView `json:"provider"`
	}
	resp.JSON(t, &payload)
	if payload.Provider.ID == "" {
		t.Fatalf("create provider returned no id: %.300s", resp.Body)
	}
	return payload.Provider
}

// deleteProvider removes a provider as the admin and requires status 200.
func deleteProvider(t *testing.T, e *harness.Env, token, id string) {
	t.Helper()
	requireStatus(t, e.Delete(t, token, "/api/custom-metadata-providers/"+id), http.StatusOK, "delete custom metadata provider "+id)
}

// listProviders returns the admin provider list.
func listProviders(t *testing.T, e *harness.Env, token string) []providerView {
	t.Helper()
	resp := e.Get(t, token, "/api/custom-metadata-providers")
	requireStatus(t, resp, http.StatusOK, "list custom metadata providers")
	var payload struct {
		Providers []providerView `json:"providers"`
	}
	resp.JSON(t, &payload)
	return payload.Providers
}

// requireProviderCount fails unless the table holds exactly want rows.
func requireProviderCount(t *testing.T, e *harness.Env, want int) {
	t.Helper()
	if got := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders"); got != want {
		t.Fatalf("customMetadataProviders rows = %d, want %d", got, want)
	}
}

// providerRow returns the seeded row of one provider with the columns this
// packet asserts.
func providerRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, mediaType, url, authHeaderValue FROM customMetadataProviders WHERE id = ?", id)
	if len(rows) != 1 {
		t.Fatalf("customMetadataProviders row %s: got %d rows", id, len(rows))
	}
	return rows[0]
}

// customSlug is the provider value a client sends for a stored provider id.
func customSlug(id string) string { return customProviderPrefix + id }

// requireEventPayload asserts the added/removed event payload equals the
// provider's client JSON.
func requireEventPayload(t *testing.T, event providerEvent, view providerView) {
	t.Helper()
	requireEqual(t, "provider event payload", event, providerEvent{ID: view.ID, Name: view.Name, MediaType: view.MediaType, Slug: customSlug(view.ID)})
}

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

// stubMatches is the fixed payload of the reusable search stub: it exercises
// the adapter's type coercion, optional-field pruning, tag trimming/dedupe and
// series validation along with the order the source preserves.
func stubMatches() map[string]any {
	return map[string]any{
		"matches": []any{
			map[string]any{
				"title":         42,
				"subtitle":      "The Subtitle",
				"author":        []any{"Ann Author", "Bob Writer"},
				"narrator":      "Nia Narrator",
				"publisher":     "Packet Press",
				"publishedYear": 2001,
				"description":   "<b>Bold</b> text",
				"cover":         "http://127.0.0.1/cover-a.jpg",
				"isbn":          "9780000000001",
				"asin":          "B000000001",
				"genres":        "fantasy, science fiction, fantasy",
				"tags":          []any{" alpha ", "alpha", " beta "},
				"series": []any{
					map[string]any{"series": "Saga", "sequence": 2},
					map[string]any{"series": 7},
					map[string]any{"series": "Other"},
				},
				"language": "en",
				"duration": "12.5",
			},
			map[string]any{
				"title":    "Second Book",
				"author":   "Solo Author",
				"cover":    "http://127.0.0.1/cover-b.jpg",
				"duration": "not-a-number",
				"genres":   []any{"x", 1},
				"series":   []any{},
			},
			map[string]any{"title": "Third Book"},
		},
	}
}

// expectedStubBooks is the adapter's projection of stubMatches.
func expectedStubBooks() []bookResult {
	duration := 12.5
	return []bookResult{
		{
			Title:         "42",
			Subtitle:      "The Subtitle",
			Author:        "Ann Author,Bob Writer",
			Narrator:      "Nia Narrator",
			Publisher:     "Packet Press",
			PublishedYear: "2001",
			Description:   "<b>Bold</b> text",
			Cover:         "http://127.0.0.1/cover-a.jpg",
			ISBN:          "9780000000001",
			ASIN:          "B000000001",
			Genres:        []string{"fantasy", "science fiction"},
			Tags:          []string{"alpha", "beta"},
			Series: []struct {
				Series   string `json:"series"`
				Sequence string `json:"sequence"`
			}{{Series: "Saga", Sequence: "2"}, {Series: "Other"}},
			Language: "en",
			Duration: &duration,
		},
		{Title: "Second Book", Author: "Solo Author", Cover: "http://127.0.0.1/cover-b.jpg"},
		{Title: "Third Book"},
	}
}

// decodeBooks decodes a search/books body.
func decodeBooks(t *testing.T, resp harness.Response) []bookResult {
	t.Helper()
	var books []bookResult
	resp.JSON(t, &books)
	return books
}

// requireProviderRow validates a stored provider row against the created view.
func requireProviderRow(t *testing.T, row map[string]any, view providerView) {
	t.Helper()
	requireEqual(t, "stored provider id", row["id"], view.ID)
	requireEqual(t, "stored provider name", row["name"], view.Name)
	requireEqual(t, "stored provider mediaType", row["mediaType"], view.MediaType)
	requireEqual(t, "stored provider url", row["url"], view.URL)
}

// timestamp parses a provider timestamp, failing on a missing or malformed value.
func timestamp(t *testing.T, what, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("%s %q is not an RFC 3339 timestamp: %v", what, value, err)
	}
	return parsed
}

// providerNameList maps the provider list entries to their value/text pairs.
func providerNameList(t *testing.T, raw any) []map[string]string {
	t.Helper()
	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("provider list is %T, want an array", raw)
	}
	out := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("provider entry is %T, want an object", entry)
		}
		value, _ := object["value"].(string)
		text, _ := object["text"].(string)
		out = append(out, map[string]string{"value": value, "text": text})
	}
	return out
}

// requireNoBodyProviderRow fails when a provider row exists.
func requireNoProviderRow(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	if rows := e.Rows(t, "SELECT id FROM customMetadataProviders WHERE id = ?", id); len(rows) != 0 {
		t.Fatalf("provider %s still exists: %v", id, rows)
	}
}

// searchQuery builds the query of a cover/book search.
func searchQuery(provider, title, author string) url.Values {
	values := url.Values{"provider": {provider}, "title": {title}}
	if author != "" {
		values.Set("author", author)
	}
	return values
}

// errorMessage is the plain-text body of a rejected request.
func errorMessage(resp harness.Response) string { return string(resp.Body) }

// describe formats a provider for failure messages.
func describe(view providerView) string { return fmt.Sprintf("%s (%s)", view.Name, view.ID) }
