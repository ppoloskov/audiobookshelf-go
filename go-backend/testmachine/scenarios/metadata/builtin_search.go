package metadata

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// staticBookProviders is BookFinder.providers without audiobookcovers, in the
// order getAllProviders emits it.
func staticBookProviders() []map[string]string {
	return []map[string]string{
		{"value": "google", "text": "Google Books"},
		{"value": "itunes", "text": "iTunes"},
		{"value": "openlibrary", "text": "Open Library"},
		{"value": "fantlab", "text": "FantLab.ru"},
		{"value": "audible", "text": "Audible.com"},
		{"value": "audible.ca", "text": "Audible.ca"},
		{"value": "audible.uk", "text": "Audible.co.uk"},
		{"value": "audible.au", "text": "Audible.com.au"},
		{"value": "audible.fr", "text": "Audible.fr"},
		{"value": "audible.de", "text": "Audible.de"},
		{"value": "audible.jp", "text": "Audible.co.jp"},
		{"value": "audible.it", "text": "Audible.it"},
		{"value": "audible.in", "text": "Audible.in"},
		{"value": "audible.es", "text": "Audible.es"},
	}
}

// staticCoverProviders is BookFinder.providers as booksCovers lists it.
func staticCoverProviders() []map[string]string {
	return []map[string]string{
		{"value": "google", "text": "Google Books"},
		{"value": "itunes", "text": "iTunes"},
		{"value": "openlibrary", "text": "Open Library"},
		{"value": "fantlab", "text": "FantLab.ru"},
		{"value": "audiobookcovers", "text": "AudiobookCovers.com"},
		{"value": "audible", "text": "Audible.com"},
		{"value": "audible.ca", "text": "Audible.ca"},
		{"value": "audible.uk", "text": "Audible.co.uk"},
		{"value": "audible.au", "text": "Audible.com.au"},
		{"value": "audible.fr", "text": "Audible.fr"},
		{"value": "audible.de", "text": "Audible.de"},
		{"value": "audible.jp", "text": "Audible.co.jp"},
		{"value": "audible.it", "text": "Audible.it"},
		{"value": "audible.in", "text": "Audible.in"},
		{"value": "audible.es", "text": "Audible.es"},
	}
}

// providersPayload decodes GET /api/search/providers.
func providersPayload(t *testing.T, resp harness.Response) map[string]any {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, "list search providers")
	providers, _ := resp.Object(t)["providers"].(map[string]any)
	if providers == nil {
		t.Fatalf("response has no providers object: %.300s", resp.Body)
	}
	return providers
}

// coverResults decodes GET /api/search/covers.
func coverResults(t *testing.T, resp harness.Response) ([]string, bool) {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, "cover search")
	object := resp.Object(t)
	raw, present := object["results"]
	if !present {
		t.Fatalf("cover search has no results field: %.300s", resp.Body)
	}
	if raw == nil {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("cover results is %T, want an array or null", raw)
	}
	covers := make([]string, 0, len(list))
	for _, entry := range list {
		cover, ok := entry.(string)
		if !ok {
			t.Fatalf("cover entry is %T, want a string", entry)
		}
		covers = append(covers, cover)
	}
	return covers, true
}

// providerBaseScenarios read the provider catalogue and the admin list before
// any scenario creates a custom provider: the fixture state.
func providerBaseScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r180", "INV-ROUTE-R180-CONTRACT", "normal", "stateless", "providers-static-catalogue",
			[]string{searchRouterSrc, providersPayloadSrc, webProviderStoreSrc},
			"GET /api/search/providers as the admin answers 200 with a providers object whose books array is the 14 built-in book providers in source order with their display names, whose booksCovers array is best, the 15 built-in providers including audiobookcovers, then all, and whose podcasts array is only itunes.",
			func(t *testing.T, e *harness.Env) {
				providers := providersPayload(t, e.Get(t, e.Token(t, "root"), "/api/search/providers"))
				if len(providers) != 3 {
					t.Fatalf("providers object has %d keys, want books, booksCovers, podcasts: %v", len(providers), providers)
				}
				requireEqual(t, "books", providerNameList(t, providers["books"]), staticBookProviders())

				booksCovers := append([]map[string]string{{"value": "best", "text": "Best"}}, staticCoverProviders()...)
				booksCovers = append(booksCovers, map[string]string{"value": "all", "text": "All"})
				requireEqual(t, "booksCovers", providerNameList(t, providers["booksCovers"]), booksCovers)

				requireEqual(t, "podcasts", providerNameList(t, providers["podcasts"]),
					[]map[string]string{{"value": "itunes", "text": "iTunes"}})
			}),

		scenario("route-r180", "INV-ROUTE-R180-CONTRACT", "error", "stateless", "providers-requires-auth",
			[]string{apiAuthSrc, searchRouterSrc},
			"GET /api/search/providers without a token answers 401 and returns no provider catalogue.",
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, "", "/api/search/providers")
				requireStatus(t, resp, http.StatusUnauthorized, "unauthenticated provider list")
			}),

		scenario("route-r192", "INV-ROUTE-R192-CONTRACT", "normal", "stateless", "custom-providers-list-empty",
			[]string{customRouterSrc, customControllerSrc, webProviderPageSrc},
			"GET /api/custom-metadata-providers as the admin on the fixture state answers 200 with an empty providers array because no custom provider exists yet.",
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/custom-metadata-providers")
				requireStatus(t, resp, http.StatusOK, "admin provider list")
				requireEqual(t, "providers", resp.Object(t)["providers"], []any{})
			}),

		scenario("route-r192", "INV-ROUTE-R192-CONTRACT", "error", "stateless", "custom-providers-list-auth",
			[]string{apiAuthSrc, customControllerSrc},
			"GET /api/custom-metadata-providers answers 401 without a token and 403 for both non-admin fixture users, so the list never reaches a normal user.",
			func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Get(t, "", "/api/custom-metadata-providers"), http.StatusUnauthorized, "unauthenticated list")
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/custom-metadata-providers"), http.StatusForbidden, "listener list")
				requireStatus(t, e.Get(t, e.Token(t, "finisher"), "/api/custom-metadata-providers"), http.StatusForbidden, "finisher list")
			}),
	}
}

// providersMergedScenarios run after the create group: the catalogue and the
// admin list must reflect the stored custom providers.
func providersMergedScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r180", "INV-ROUTE-R180-CUSTOM-001", "edge", "stateless", "providers-merge-custom-by-media-type",
			[]string{providersPayloadSrc, searchControllerSrc, customProviderModel},
			"After a book and a podcast custom provider exist, a non-admin GET /api/search/providers lists the book provider after the built-in books and after the built-in booksCovers entries but before all, keeps books at 14 built-ins plus both book providers, and lists the podcast provider after itunes in podcasts only.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				providers := providersPayload(t, e.Get(t, token, "/api/search/providers"))

				books := providerNameList(t, providers["books"])
				bookProviders := builtInValues(staticBookProviders())
				requireEqual(t, "built-in books prefix", builtInValues(books[:len(bookProviders)]), bookProviders)
				if len(books) != len(bookProviders)+2 {
					t.Fatalf("books has %d entries, want 14 built-ins plus the two book providers: %v", len(books), books)
				}
				requireBookCustomEntries(t, books[len(bookProviders):])

				covers := providerNameList(t, providers["booksCovers"])
				coverValues := builtInValues(staticCoverProviders())
				requireEqual(t, "booksCovers first entry", covers[0]["value"], "best")
				requireEqual(t, "booksCovers last entry", covers[len(covers)-1]["value"], "all")
				requireEqual(t, "booksCovers built-ins", builtInValues(covers[1:1+len(coverValues)]), coverValues)
				if len(covers) != len(coverValues)+4 {
					t.Fatalf("booksCovers has %d entries, want best, 15 built-ins, 2 book providers and all: %v", len(covers), covers)
				}
				requireBookCustomEntries(t, covers[1+len(coverValues):len(covers)-1])

				podcasts := providerNameList(t, providers["podcasts"])
				requireEqual(t, "podcasts", podcasts[0], map[string]string{"value": "itunes", "text": "iTunes"})
				if len(podcasts) != 2 {
					t.Fatalf("podcasts has %d entries, want itunes and the podcast provider: %v", len(podcasts), podcasts)
				}
				requireEqual(t, "podcast provider", podcasts[1], map[string]string{"value": customSlug(storedPodcastProvider.ID), "text": storedPodcastProvider.Name})
			}),

		scenario("route-r192", "INV-ROUTE-R192-CONTRACT", "edge", "stateless", "custom-providers-list-created-rows",
			[]string{customControllerSrc, customProviderModel, webProviderTableSrc},
			"GET /api/custom-metadata-providers returns the three created providers as full admin rows with their id, name, mediaType, stored url and authHeaderValue, and no slug.",
			func(t *testing.T, e *harness.Env) {
				providers := listProviders(t, e, e.Token(t, "root"))
				if len(providers) != 3 {
					t.Fatalf("provider list has %d rows, want 3: %v", len(providers), providers)
				}
				byID := make(map[string]providerView, len(providers))
				for _, provider := range providers {
					byID[provider.ID] = provider
				}
				for _, view := range []providerView{storedBookProvider, storedBookProviderRepeat, storedPodcastProvider} {
					got, ok := byID[view.ID]
					if !ok {
						t.Fatalf("provider list lacks %s: %v", describe(view), providers)
					}
					requireEqual(t, "list provider id", got.ID, view.ID)
					requireEqual(t, "list provider name", got.Name, view.Name)
					requireEqual(t, "list provider mediaType", got.MediaType, view.MediaType)
					requireEqual(t, "list provider url", got.URL, view.URL)
					requireEqual(t, "list provider authHeaderValue", got.AuthHeaderValue, view.AuthHeaderValue)
					requireEqual(t, "list provider has no slug field", got.Slug, "")
					timestamp(t, "list provider createdAt", got.CreatedAt)
					timestamp(t, "list provider updatedAt", got.UpdatedAt)
				}
			}),
	}
}

// builtInValues maps provider entries to their values.
func builtInValues(entries []map[string]string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry["value"])
	}
	return out
}

// requireBookCustomEntries checks the custom book provider entries of a list.
func requireBookCustomEntries(t *testing.T, entries []map[string]string) {
	t.Helper()
	if len(entries) != 2 {
		t.Fatalf("want two custom book provider entries, got %v", entries)
	}
	names := map[string]string{
		customSlug(storedBookProvider.ID):       storedBookProvider.Name,
		customSlug(storedBookProviderRepeat.ID): storedBookProviderRepeat.Name,
	}
	for _, entry := range entries {
		want, ok := names[entry["value"]]
		if !ok {
			t.Fatalf("unexpected custom provider entry %v", entry)
		}
		requireEqual(t, "custom provider text", entry["text"], want)
	}
}

// coverSearchScenarios cover GET /api/search/covers.
func coverSearchScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r175", "INV-ROUTE-R175-CONTRACT", "normal", "stateless", "covers-custom-provider-ordered-deduplicated",
			[]string{searchRouterSrc, searchControllerSrc, bookFinderCoversSrc, customAdapterSrc, webMatchSrc},
			"GET /api/search/covers?provider=custom-<id>&title=Alpha&author=Ann queries the configured provider at /search with mediaType=book, query=Alpha and author=Ann and answers 200 with results holding the matches' covers in match order with the duplicate removed and the match without a cover omitted.",
			func(t *testing.T, e *harness.Env) {
				stub := newProviderStub(t, map[string]any{"matches": []any{
					map[string]any{"title": "A", "cover": "http://127.0.0.1/cover-a.jpg"},
					map[string]any{"title": "B", "cover": "http://127.0.0.1/cover-b.jpg"},
					map[string]any{"title": "C", "cover": "http://127.0.0.1/cover-a.jpg"},
					map[string]any{"title": "D"},
				}})
				provider := createProvider(t, e, e.Token(t, "root"), map[string]any{
					"name": "Covers Provider", "url": stub.url(), "mediaType": "book",
				})
				defer deleteProvider(t, e, e.Token(t, "root"), provider.ID)

				resp := e.Do(t, harness.Request{Path: "/api/search/covers", Token: e.Token(t, "listener"), Query: searchQuery(customSlug(provider.ID), "Alpha", "Ann")})
				covers, present := coverResults(t, resp)
				if !present {
					t.Fatal("cover search returned null")
				}
				requireEqual(t, "covers", covers, []string{"http://127.0.0.1/cover-a.jpg", "http://127.0.0.1/cover-b.jpg"})

				requests := stub.requests()
				if len(requests) != 1 {
					t.Fatalf("provider received %d requests, want 1", len(requests))
				}
				request := requests[0]
				requireEqual(t, "provider method", request.Method, http.MethodGet)
				requireEqual(t, "provider path", request.Path, "/search")
				requireEqual(t, "provider mediaType", request.Query.Get("mediaType"), "book")
				requireEqual(t, "provider query", request.Query.Get("query"), "Alpha")
				requireEqual(t, "provider author", request.Query.Get("author"), "Ann")
			}),

		scenario("route-r175", "INV-ROUTE-R175-REJECT-001", "error", "stateless", "covers-auth-and-title-required",
			[]string{apiAuthSrc, searchControllerSrc, queryParamsSrc},
			"GET /api/search/covers answers 401 without a token, 400 when title is absent both for a book and for a podcast cover search, and 400 when provider is repeated as an array.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireStatus(t, e.Get(t, "", "/api/search/covers"), http.StatusUnauthorized, "unauthenticated covers")
				requireStatus(t, e.Get(t, token, "/api/search/covers"), http.StatusBadRequest, "covers without title")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/covers", Token: token, Query: url.Values{"podcast": {"1"}}}), http.StatusBadRequest, "podcast covers without title")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/covers", Token: token, Query: url.Values{"title": {"Alpha"}, "provider": {"google", "itunes"}}}), http.StatusBadRequest, "provider array")
			}),

		scenario("route-r175", "INV-ROUTE-R175-REJECT-001", "edge", "stateless", "covers-empty-title-chains-answer-empty",
			[]string{searchControllerSrc, bookFinderSrc, bookFinderCoversSrc, podcastFinderSrc},
			"With an empty title the all and best provider chains and an unknown provider answer 200 with an empty results array without contacting any provider, while a podcast cover search with an empty title answers 200 with results null.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				for _, provider := range []string{"all", "best", "bogus"} {
					resp := e.Do(t, harness.Request{Path: "/api/search/covers", Token: token, Query: url.Values{"title": {""}, "provider": {provider}}})
					covers, present := coverResults(t, resp)
					if !present || len(covers) != 0 {
						t.Fatalf("provider %s with an empty title: got results %v (present %v), want an empty array", provider, covers, present)
					}
				}
				resp := e.Do(t, harness.Request{Path: "/api/search/covers", Token: token, Query: url.Values{"title": {""}, "podcast": {"1"}}})
				requireStatus(t, resp, http.StatusOK, "podcast covers with an empty title")
				if raw, present := resp.Object(t)["results"]; !present || raw != nil {
					t.Fatalf("podcast covers with an empty title: results = %v (present %v), want null", raw, present)
				}
			}),
	}
}

// bookSearchScenarios cover GET /api/search/books.
func bookSearchScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r176", "INV-ROUTE-R176-CONTRACT", "normal", "stateless", "books-custom-provider-projection",
			[]string{searchRouterSrc, searchControllerSrc, bookFinderSrc, bookFinderCustomSrc, customAdapterSrc, webUploadSrc},
			"GET /api/search/books?provider=custom-<id>&title=Packet&author=Ann calls the configured provider once at /search with mediaType=book, query=Packet, author=Ann and the configured Authorization value, and answers 200 with the matches projected by the adapter in match order: numbers and arrays stringified, absent or unparsable optional fields omitted, tags trimmed and deduped, invalid series dropped and description sanitised.",
			func(t *testing.T, e *harness.Env) {
				stub := newProviderStub(t, stubMatches())
				authValue := "Token metadata-packet"
				provider := createProvider(t, e, e.Token(t, "root"), map[string]any{
					"name": "Books Provider", "url": stub.url(), "mediaType": "book", "authHeaderValue": authValue,
				})
				defer deleteProvider(t, e, e.Token(t, "root"), provider.ID)

				resp := e.Do(t, harness.Request{Path: "/api/search/books", Token: e.Token(t, "listener"), Query: searchQuery(customSlug(provider.ID), "Packet", "Ann")})
				requireStatus(t, resp, http.StatusOK, "book search")
				requireEqual(t, "books", decodeBooks(t, resp), expectedStubBooks())

				requests := stub.requests()
				if len(requests) != 1 {
					t.Fatalf("provider received %d requests, want 1", len(requests))
				}
				request := requests[0]
				requireEqual(t, "provider method", request.Method, http.MethodGet)
				requireEqual(t, "provider path", request.Path, "/search")
				requireEqual(t, "provider mediaType", request.Query.Get("mediaType"), "book")
				requireEqual(t, "provider query", request.Query.Get("query"), "Packet")
				requireEqual(t, "provider author", request.Query.Get("author"), "Ann")
				requireEqual(t, "provider authorization", request.Authorization, authValue)
			}),

		scenario("route-r176", "INV-ROUTE-R176-REJECT-001", "error", "stateless", "books-auth-not-found-and-validation-order",
			[]string{apiAuthSrc, searchControllerSrc, fetchItemSrc, queryParamsSrc},
			"GET /api/search/books answers 401 without a token, 404 with the not-found error when id names no library item, 400 before that lookup when title is longer than 1000 characters or repeated as an array, and 400 when provider is repeated.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				unknown := "00000000-0000-4000-8000-000000000000"
				requireStatus(t, e.Get(t, "", "/api/search/books"), http.StatusUnauthorized, "unauthenticated books")

				notFound := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: url.Values{"title": {"Packet"}, "id": {unknown}}})
				requireStatus(t, notFound, http.StatusNotFound, "unknown library item")
				if message, _ := notFound.Object(t)["error"].(string); message == "" {
					t.Fatalf("404 body has no error string: %.300s", notFound.Body)
				}

				long := make([]byte, 1001)
				for i := range long {
					long[i] = 'a'
				}
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: url.Values{"title": {string(long)}, "id": {unknown}}}), http.StatusBadRequest, "title too long with unknown id")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: url.Values{"title": {"a", "b"}}}), http.StatusBadRequest, "title array")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: url.Values{"title": {"Packet"}, "provider": {"google", "itunes"}}}), http.StatusBadRequest, "provider array")
			}),

		scenario("route-r176", "INV-ROUTE-R176-DEGRADE-001", "edge", "stateless", "books-empty-title-and-provider-degrades",
			[]string{bookFinderSrc, searchControllerSrc, customAdapterSrc},
			"Book searches that cannot produce a result answer 200 with an empty array and no error: an absent title under the default google provider, an absent title with a valid library item id (which is loaded first), a custom provider id that does not exist, a provider whose response has no matches array, and a provider URL whose server is closed.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireEqual(t, "empty query books", decodeBooks(t, e.Get(t, token, "/api/search/books")), []bookResult{})
				withItem := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: url.Values{"id": {e.IDs["novel"]}}})
				requireStatus(t, withItem, http.StatusOK, "empty title with a fixture item")
				requireEqual(t, "empty title books for a fixture item", decodeBooks(t, withItem), []bookResult{})

				missing := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: searchQuery(customSlug("00000000-0000-4000-8000-000000000000"), "Packet", "")})
				requireStatus(t, missing, http.StatusOK, "unknown custom provider")
				requireEqual(t, "unknown custom provider books", decodeBooks(t, missing), []bookResult{})

				malformed := newProviderStub(t, map[string]any{"unexpected": true})
				malformedProvider := createProvider(t, e, token, map[string]any{"name": "Malformed Provider", "url": malformed.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, malformedProvider.ID)
				bad := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: searchQuery(customSlug(malformedProvider.ID), "Packet", "")})
				requireStatus(t, bad, http.StatusOK, "malformed custom provider")
				requireEqual(t, "malformed custom provider books", decodeBooks(t, bad), []bookResult{})

				dead := newProviderStub(t, map[string]any{"matches": []any{}})
				deadProvider := createProvider(t, e, token, map[string]any{"name": "Dead Provider", "url": dead.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, deadProvider.ID)
				dead.server.Close()
				unreachable := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: searchQuery(customSlug(deadProvider.ID), "Packet", "")})
				requireStatus(t, unreachable, http.StatusOK, "unreachable custom provider")
				requireEqual(t, "unreachable custom provider books", decodeBooks(t, unreachable), []bookResult{})
			}),
	}
}

// podcastAndAuthorScenarios cover the two searches whose success path needs an
// external host.
func podcastAndAuthorScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r177", "INV-ROUTE-R177-CONTRACT", "error", "stateless", "podcast-auth-and-term-validation",
			[]string{apiAuthSrc, searchControllerSrc, queryParamsSrc, webPodcastSearchSrc},
			"GET /api/search/podcast answers 401 without a token, 400 when term is absent, and 400 when term is repeated as an array.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireStatus(t, e.Get(t, "", "/api/search/podcast"), http.StatusUnauthorized, "unauthenticated podcast search")
				requireStatus(t, e.Get(t, token, "/api/search/podcast"), http.StatusBadRequest, "podcast search without term")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/podcast", Token: token, Query: url.Values{"term": {"a", "b"}}}), http.StatusBadRequest, "term array")
			}),

		scenario("route-r177", "INV-ROUTE-R177-CONTRACT", "edge", "stateless", "podcast-empty-term-is-null",
			[]string{searchControllerSrc, podcastFinderSrc},
			"GET /api/search/podcast with an empty term and an optional country answers 200 with a null body without contacting iTunes.",
			func(t *testing.T, e *harness.Env) {
				resp := e.Do(t, harness.Request{Path: "/api/search/podcast", Token: e.Token(t, "root"), Query: url.Values{"term": {""}, "country": {"de"}}})
				requireStatus(t, resp, http.StatusOK, "empty podcast term")
				if string(resp.Body) != "null" {
					t.Fatalf("empty podcast term body = %.300s, want null", resp.Body)
				}
			}),

		scenario("route-r178", "INV-ROUTE-R178-CONTRACT", "error", "stateless", "authors-auth-and-q-validation",
			[]string{apiAuthSrc, searchControllerSrc, queryParamsSrc},
			"GET /api/search/authors answers 401 without a token, 400 when q is absent, and 400 when q is repeated as an array.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireStatus(t, e.Get(t, "", "/api/search/authors"), http.StatusUnauthorized, "unauthenticated author search")
				requireStatus(t, e.Get(t, token, "/api/search/authors"), http.StatusBadRequest, "author search without q")
				requireStatus(t, e.Do(t, harness.Request{Path: "/api/search/authors", Token: token, Query: url.Values{"q": {"a", "b"}}}), http.StatusBadRequest, "q array")
			}),

		scenario("route-r178", "INV-ROUTE-R178-CONTRACT", "edge", "stateless", "authors-empty-q-is-null",
			[]string{searchControllerSrc, authorFinderSrc},
			"GET /api/search/authors with an empty q answers 200 with a null body without contacting Audnexus.",
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/search/authors?q=")
				requireStatus(t, resp, http.StatusOK, "empty author query")
				if string(resp.Body) != "null" {
					t.Fatalf("empty author query body = %.300s, want null", resp.Body)
				}
			}),
	}
}

// chapterScenarios cover GET /api/search/chapters, whose success path needs
// Audnexus.
func chapterScenarios() []harness.Scenario {
	type chapterError struct {
		Error     string `json:"error"`
		StringKey string `json:"stringKey"`
	}
	requireInvalidASIN := func(t *testing.T, resp harness.Response, what string) {
		t.Helper()
		requireStatus(t, resp, http.StatusOK, what)
		var payload chapterError
		resp.JSON(t, &payload)
		requireEqual(t, what+" error", payload, chapterError{Error: "Invalid ASIN", StringKey: "MessageInvalidAsin"})
	}
	return []harness.Scenario{
		scenario("route-r179", "INV-ROUTE-R179-CONTRACT", "error", "stateless", "chapters-auth-and-asin-required",
			[]string{apiAuthSrc, searchControllerSrc, validASINSrc, webChaptersSrc},
			"GET /api/search/chapters answers 401 without a token and, for a missing asin, 200 with the Invalid ASIN body the web client turns into a string, never a 400.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireStatus(t, e.Get(t, "", "/api/search/chapters"), http.StatusUnauthorized, "unauthenticated chapters")
				requireInvalidASIN(t, e.Get(t, token, "/api/search/chapters"), "missing asin")
			}),

		scenario("route-r179", "INV-ROUTE-R179-CONTRACT", "edge", "stateless", "chapters-invalid-asin-formats",
			[]string{searchControllerSrc, validASINSrc, webChaptersSrc},
			"An empty, too-short or non-alphanumeric asin, and an asin repeated as an array, each answer 200 with error Invalid ASIN and stringKey MessageInvalidAsin without contacting Audnexus.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				for _, asin := range []string{"", "SHORT", "B00000000!", "abcdefghi"} {
					requireInvalidASIN(t, e.Do(t, harness.Request{Path: "/api/search/chapters", Token: token, Query: url.Values{"asin": {asin}}}), "asin "+asin)
				}
				requireInvalidASIN(t, e.Do(t, harness.Request{Path: "/api/search/chapters", Token: token, Query: url.Values{"asin": {"B000000001", "B000000002"}}}), "asin array")
			}),
	}
}
