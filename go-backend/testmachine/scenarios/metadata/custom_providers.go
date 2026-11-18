package metadata

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// The provider rows created by the create group and read by the later
// scenarios (one shared server, declaration order).
var (
	storedBookProvider       providerView
	storedBookProviderRepeat providerView
	storedPodcastProvider    providerView
)

// provider names of the rows the create group leaves in place.
const (
	bookProviderName       = "Packet Book Provider"
	podcastProviderName    = "Packet Podcast Provider"
	bookProviderAuthHeader = "Token provider-secret"
)

func stringPointer(value string) *string { return &value }

// createProviderScenarios cover POST /api/custom-metadata-providers.
func createProviderScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r193", "INV-ROUTE-R193-CONTRACT", "normal", "initial", "create-first-provider",
			[]string{customRouterSrc, customControllerSrc, customProviderModel, webProviderModalSrc},
			"POST /api/custom-metadata-providers as the admin with a name, an http URL, mediaType book and an Authorization value answers 200 with the stored provider (id, name, mediaType, the url verbatim, authHeaderValue and RFC 3339 timestamps at or after the request started; the response carries no slug), persists one row with those values and leaves the table with exactly that provider.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				started := time.Now().UTC()
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{
					"name": bookProviderName, "url": stub.url(), "mediaType": "book", "authHeaderValue": bookProviderAuthHeader,
				})
				storedBookProvider = view

				requireEqual(t, "provider name", view.Name, bookProviderName)
				requireEqual(t, "provider mediaType", view.MediaType, "book")
				requireEqual(t, "provider url", view.URL, stub.url())
				requireEqual(t, "create response has no slug", view.Slug, "")
				requireEqual(t, "provider authHeaderValue", view.AuthHeaderValue, stringPointer(bookProviderAuthHeader))
				created := timestamp(t, "provider createdAt", view.CreatedAt)
				if created.Before(started) {
					t.Fatalf("createdAt %s is before the request started at %s", created, started)
				}
				if updated := timestamp(t, "provider updatedAt", view.UpdatedAt); updated.Before(created) {
					t.Fatalf("updatedAt %s is before createdAt %s", updated, created)
				}

				row := providerRow(t, e, view.ID)
				requireProviderRow(t, row, view)
				requireEqual(t, "stored authHeaderValue", row["authHeaderValue"], bookProviderAuthHeader)
				requireProviderCount(t, e, 1)
			}),

		scenario("route-r193", "INV-ROUTE-R193-CONTRACT", "normal", "repeat", "create-repeat-adds-second-row",
			[]string{customControllerSrc, customProviderModel},
			"Posting the same provider body a second time answers 200 with a different id and persists a second row with the same name, mediaType, url and authHeaderValue, so creation is not idempotent and keeps two rows.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				view := createProvider(t, e, token, map[string]any{
					"name": bookProviderName, "url": storedBookProvider.URL, "mediaType": "book", "authHeaderValue": bookProviderAuthHeader,
				})
				storedBookProviderRepeat = view

				if view.ID == storedBookProvider.ID {
					t.Fatalf("repeated create returned the same id %s", view.ID)
				}
				requireEqual(t, "repeat provider url", view.URL, storedBookProvider.URL)
				requireEqual(t, "repeat provider authHeaderValue", view.AuthHeaderValue, storedBookProvider.AuthHeaderValue)
				requireProviderRow(t, providerRow(t, e, view.ID), view)
				requireProviderRow(t, providerRow(t, e, storedBookProvider.ID), storedBookProvider)
				requireProviderCount(t, e, 2)
			}),

		scenario("route-r193", "INV-ROUTE-R193-CONTRACT", "normal", "changed", "create-podcast-provider-and-empty-auth",
			[]string{customControllerSrc, customProviderModel, providersPayloadSrc},
			"After two book providers exist, creating one with mediaType podcast and an empty authHeaderValue answers 200 with mediaType podcast, authHeaderValue null and a third row whose stored authHeaderValue is NULL, so the media type is stored as sent and an empty Authorization value becomes null.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{
					"name": podcastProviderName, "url": stub.url(), "mediaType": "podcast", "authHeaderValue": "",
				})
				storedPodcastProvider = view

				requireEqual(t, "podcast provider mediaType", view.MediaType, "podcast")
				requireEqual(t, "podcast provider authHeaderValue", view.AuthHeaderValue, (*string)(nil))
				row := providerRow(t, e, view.ID)
				requireProviderRow(t, row, view)
				requireEqual(t, "stored podcast authHeaderValue", row["authHeaderValue"], nil)
				requireProviderCount(t, e, 3)
			}),

		scenario("route-r193", "INV-ROUTE-R193-REJECT-001", "error", "failure", "create-rejections-change-nothing",
			[]string{apiAuthSrc, customRouterSrc, customControllerSrc, validateUrlSrc, webProviderModalSrc},
			"POST /api/custom-metadata-providers answers 401 without a token, 403 for a non-admin, 400 Invalid request body when name, url or mediaType is missing (or url is empty) and 400 Invalid url for a string that is not a URL, and none of the rejected requests adds a row.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				valid := map[string]any{"name": "Rejected Provider", "url": stub.url(), "mediaType": "book"}

				requireStatus(t, e.Post(t, "", "/api/custom-metadata-providers", valid), http.StatusUnauthorized, "unauthenticated create")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/custom-metadata-providers", valid), http.StatusForbidden, "non-admin create")

				rejectBody := func(body map[string]any, want string, what string) {
					t.Helper()
					resp := e.Post(t, token, "/api/custom-metadata-providers", body)
					requireStatus(t, resp, http.StatusBadRequest, what)
					requireEqual(t, what+" body", errorMessage(resp), want)
				}
				rejectBody(map[string]any{"url": stub.url(), "mediaType": "book"}, "Invalid request body", "missing name")
				rejectBody(map[string]any{"name": "Rejected Provider", "mediaType": "book"}, "Invalid request body", "missing url")
				rejectBody(map[string]any{"name": "Rejected Provider", "url": stub.url()}, "Invalid request body", "missing mediaType")
				rejectBody(map[string]any{"name": "Rejected Provider", "url": "", "mediaType": "book"}, "Invalid request body", "empty url")
				rejectBody(map[string]any{"name": "Rejected Provider", "url": "not a url", "mediaType": "book"}, "Invalid url", "malformed url")

				requireProviderCount(t, e, 3)
				if rows := e.Rows(t, "SELECT id FROM customMetadataProviders WHERE name = ?", "Rejected Provider"); len(rows) != 0 {
					t.Fatalf("a rejected create persisted a row: %v", rows)
				}
			}),

		scenario("route-r193", "INV-ROUTE-R193-URL-001", "edge", "changed", "create-accepts-loopback-and-private-urls",
			[]string{customControllerSrc, validateUrlSrc},
			"validateUrl only parses the string: POST /api/custom-metadata-providers accepts and stores a loopback URL and a private-range URL verbatim with 200 (no SSRF or scheme filtering), and deleting the two rows restores the table.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				loopback := createProvider(t, e, token, map[string]any{"name": "Loopback Provider", "url": stub.url(), "mediaType": "book"})
				private := createProvider(t, e, token, map[string]any{"name": "Private Provider", "url": "http://192.168.1.10:8080", "mediaType": "book"})
				requireEqual(t, "loopback url stored verbatim", loopback.URL, stub.url())
				requireEqual(t, "private url stored verbatim", private.URL, "http://192.168.1.10:8080")
				requireProviderCount(t, e, 5)

				deleteProvider(t, e, token, loopback.ID)
				deleteProvider(t, e, token, private.ID)
				requireProviderCount(t, e, 3)
				requireNoProviderRow(t, e, loopback.ID)
				requireNoProviderRow(t, e, private.ID)
			}),
	}
}

// deleteProviderScenarios cover DELETE /api/custom-metadata-providers/:id.
func deleteProviderScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r194", "INV-ROUTE-R194-CONTRACT", "normal", "initial", "delete-provider-removes-row-and-search-result",
			[]string{customRouterSrc, customControllerSrc, customAdapterSrc},
			"DELETE /api/custom-metadata-providers/:id as the admin answers 200 OK, removes the row and its list entry, and a later book search with that slug answers 200 with an empty array, while the other providers remain.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, stubMatches())
				provider := createProvider(t, e, token, map[string]any{"name": "Doomed Provider", "url": stub.url(), "mediaType": "book"})
				requireProviderCount(t, e, 4)

				// The provider is queried successfully before its deletion.
				before := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: searchQuery(customSlug(provider.ID), "Packet", "Ann")})
				requireStatus(t, before, http.StatusOK, "search before delete")
				if books := decodeBooks(t, before); len(books) != 3 {
					t.Fatalf("search before delete returned %d books, want 3", len(books))
				}

				resp := e.Delete(t, token, "/api/custom-metadata-providers/"+provider.ID)
				requireStatus(t, resp, http.StatusOK, "delete provider")
				requireEqual(t, "delete body", string(resp.Body), "OK")
				requireNoProviderRow(t, e, provider.ID)
				requireProviderCount(t, e, 3)
				for _, listed := range listProviders(t, e, token) {
					if listed.ID == provider.ID {
						t.Fatalf("deleted provider is still listed: %v", listed)
					}
				}

				after := e.Do(t, harness.Request{Path: "/api/search/books", Token: token, Query: searchQuery(customSlug(provider.ID), "Packet", "Ann")})
				requireStatus(t, after, http.StatusOK, "search after delete")
				requireEqual(t, "search after delete", decodeBooks(t, after), []bookResult{})
			}),

		scenario("route-r194", "INV-ROUTE-R194-CONTRACT", "normal", "repeat", "delete-repeat-is-404",
			[]string{customRouterSrc, customControllerSrc},
			"Deleting the repeated provider answers 200 OK and removes it, and deleting the same id again answers 404 without changing the remaining rows.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				requireStatus(t, e.Delete(t, token, "/api/custom-metadata-providers/"+storedBookProviderRepeat.ID), http.StatusOK, "first delete of repeated provider")
				requireProviderCount(t, e, 2)
				requireStatus(t, e.Delete(t, token, "/api/custom-metadata-providers/"+storedBookProviderRepeat.ID), http.StatusNotFound, "second delete of repeated provider")
				requireProviderCount(t, e, 2)
				requireProviderRow(t, providerRow(t, e, storedBookProvider.ID), storedBookProvider)
				requireProviderRow(t, providerRow(t, e, storedPodcastProvider.ID), storedPodcastProvider)
			}),

		scenario("route-r194", "INV-ROUTE-R194-FALLBACK-001", "normal", "changed", "delete-provider-falls-libraries-back",
			[]string{customControllerSrc, customProviderModel, searchRouterSrc},
			"Deleting a book provider that a library uses as its provider sets that library's provider column back to google, and deleting a podcast provider sets its library back to itunes, without changing either library's name or media type.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				bookStub := newProviderStub(t, map[string]any{"matches": []any{}})
				podcastStub := newProviderStub(t, map[string]any{"matches": []any{}})
				book := createProvider(t, e, token, map[string]any{"name": "Library Book Provider", "url": bookStub.url(), "mediaType": "book"})
				podcast := createProvider(t, e, token, map[string]any{"name": "Library Podcast Provider", "url": podcastStub.url(), "mediaType": "podcast"})
				bookLibrary, podcastLibrary := e.IDs["library"], e.IDs["podcastLibrary"]

				requireStatus(t, e.Patch(t, token, "/api/libraries/"+bookLibrary, map[string]any{"provider": customSlug(book.ID)}), http.StatusOK, "set book library provider")
				requireStatus(t, e.Patch(t, token, "/api/libraries/"+podcastLibrary, map[string]any{"provider": customSlug(podcast.ID)}), http.StatusOK, "set podcast library provider")
				requireEqual(t, "book library provider before delete", libraryProvider(t, e, bookLibrary), customSlug(book.ID))
				requireEqual(t, "podcast library provider before delete", libraryProvider(t, e, podcastLibrary), customSlug(podcast.ID))

				deleteProvider(t, e, token, book.ID)
				deleteProvider(t, e, token, podcast.ID)

				requireEqual(t, "book library provider after delete", libraryProvider(t, e, bookLibrary), fixtureLibraryProvider)
				requireEqual(t, "podcast library provider after delete", libraryProvider(t, e, podcastLibrary), fixturePodcastProvider)
				bookRow := e.Rows(t, "SELECT name, mediaType FROM libraries WHERE id = ?", bookLibrary)
				requireEqual(t, "book library after fallback", bookRow, []map[string]any{{"name": "Audiobooks", "mediaType": "book"}})
				podcastRow := e.Rows(t, "SELECT name, mediaType FROM libraries WHERE id = ?", podcastLibrary)
				requireEqual(t, "podcast library after fallback", podcastRow, []map[string]any{{"name": "Podcasts", "mediaType": "podcast"}})
				requireProviderCount(t, e, 2)
			}),

		scenario("route-r194", "INV-ROUTE-R194-EDGE-001", "edge", "changed", "delete-last-provider-of-a-media-type",
			[]string{customControllerSrc, providersPayloadSrc},
			"Deleting the last custom podcast provider answers 200, removes it from the admin list, and leaves GET /api/search/providers podcasts holding only itunes while books keeps the remaining custom book provider.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				deleteProvider(t, e, token, storedPodcastProvider.ID)
				requireProviderCount(t, e, 1)
				for _, listed := range listProviders(t, e, token) {
					if listed.ID == storedPodcastProvider.ID {
						t.Fatalf("deleted podcast provider is still listed: %v", listed)
					}
				}
				providers := providersPayload(t, e.Get(t, token, "/api/search/providers"))
				requireEqual(t, "podcasts after removing the custom provider", providerNameList(t, providers["podcasts"]),
					[]map[string]string{{"value": "itunes", "text": "iTunes"}})
				if books := providerNameList(t, providers["books"]); len(books) != len(staticBookProviders())+1 {
					t.Fatalf("books after removing the podcast provider = %v, want the built-ins plus the remaining custom book provider", books)
				} else {
					requireEqual(t, "remaining custom book provider after the podcast delete", books[len(books)-1],
						map[string]string{"value": customSlug(storedBookProvider.ID), "text": storedBookProvider.Name})
				}
			}),

		scenario("route-r194", "INV-ROUTE-R194-REJECT-001", "error", "failure", "delete-rejections-change-nothing",
			[]string{apiAuthSrc, customRouterSrc, customControllerSrc},
			"DELETE /api/custom-metadata-providers/:id answers 401 without a token, 403 for a non-admin even when the id does not exist (the admin check precedes the lookup), 404 for an unknown id as the admin, and none of the rejected requests removes a row.",
			func(t *testing.T, e *harness.Env) {
				root, listener := e.Token(t, "root"), e.Token(t, "listener")
				unknown := "00000000-0000-4000-8000-000000000000"
				requireStatus(t, e.Delete(t, "", "/api/custom-metadata-providers/"+storedBookProvider.ID), http.StatusUnauthorized, "unauthenticated delete")
				requireStatus(t, e.Delete(t, listener, "/api/custom-metadata-providers/"+storedBookProvider.ID), http.StatusForbidden, "non-admin delete")
				requireStatus(t, e.Delete(t, listener, "/api/custom-metadata-providers/"+unknown), http.StatusForbidden, "non-admin delete of unknown id")
				requireStatus(t, e.Delete(t, root, "/api/custom-metadata-providers/"+unknown), http.StatusNotFound, "admin delete of unknown id")
				requireProviderCount(t, e, 1)
				requireProviderRow(t, providerRow(t, e, storedBookProvider.ID), storedBookProvider)
			}),
	}
}

// libraryProvider returns the provider column of one library.
func libraryProvider(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT provider FROM libraries WHERE id = ?", libraryID)
	if len(rows) != 1 {
		t.Fatalf("library %s: got %d rows", libraryID, len(rows))
	}
	provider, _ := rows[0]["provider"].(string)
	return provider
}
