package metadata

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// eventScenarios cover the two Socket.IO events the custom provider routes
// emit. Both are broadcast to every authenticated client and nobody else.
const (
	eventWait     = 10 * time.Second
	eventNoneWait = 1 * time.Second
)

type eventAudience struct {
	admin, listener, finisher, anonymous *harness.Socket
}

// connectAudience opens one socket per role plus an unauthenticated socket.
func connectAudience(t *testing.T, e *harness.Env) *eventAudience {
	t.Helper()
	return &eventAudience{
		admin:     e.Socket(t, e.Token(t, "root")),
		listener:  e.Socket(t, e.Token(t, "listener")),
		finisher:  e.Socket(t, e.Token(t, "finisher")),
		anonymous: e.Socket(t, ""),
	}
}

// authorized returns the authenticated sockets.
func (a *eventAudience) authorized() []*harness.Socket {
	return []*harness.Socket{a.admin, a.listener, a.finisher}
}

// marks records the current end of every socket.
func (a *eventAudience) marks() []int {
	marks := make([]int, 0, 4)
	for _, socket := range append(a.authorized(), a.anonymous) {
		marks = append(marks, socket.Mark())
	}
	return marks
}

// providerEventsScenarios cover the custom_metadata_provider_added event.
func addedEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-custom-metadata-provider-added", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-ADDED", "normal", "initial", "added-event-authorized-audience-payload",
			[]string{customControllerSrc, socketEmitterSrc, customProviderModel},
			"Creating a custom metadata provider emits custom_metadata_provider_added with the provider's client JSON (id, name, mediaType, slug custom-<id>) exactly once to every authenticated socket, admin and normal user alike, and not to an unauthenticated socket.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				audience := connectAudience(t, e)
				marks := audience.marks()
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Event Added Provider", "url": stub.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, view.ID)

				for i, socket := range audience.authorized() {
					event := eventFirst[providerEvent](t, socket, marks[i], "custom_metadata_provider_added", eventWait)
					requireEventPayload(t, event, view)
					if got := eventsNamed[providerEvent](t, socket, marks[i], "custom_metadata_provider_added"); len(got) != 1 {
						t.Fatalf("socket received the added event %d times, want 1", len(got))
					}
				}
				audience.anonymous.None(t, marks[3], "custom_metadata_provider_added", eventNoneWait, nil)
			}),

		scenario("socket-outgoing-custom-metadata-provider-added", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-ADDED", "edge", "changed", "added-event-every-connection-of-a-user",
			[]string{socketEmitterSrc, customControllerSrc},
			"Two sockets of the same user each receive one custom_metadata_provider_added for a create while an unauthenticated socket receives none, so the audience is every authenticated connection.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				first := e.Socket(t, e.Token(t, "listener"))
				second := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				markFirst, markSecond, markAnonymous := first.Mark(), second.Mark(), anonymous.Mark()

				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Second Connection Provider", "url": stub.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, view.ID)

				requireEventPayload(t, eventFirst[providerEvent](t, first, markFirst, "custom_metadata_provider_added", eventWait), view)
				requireEventPayload(t, eventFirst[providerEvent](t, second, markSecond, "custom_metadata_provider_added", eventWait), view)
				anonymous.None(t, markAnonymous, "custom_metadata_provider_added", eventNoneWait, nil)
			}),

		scenario("socket-outgoing-custom-metadata-provider-added", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-ADDED", "normal", "repeat", "added-event-each-create",
			[]string{socketEmitterSrc, customControllerSrc},
			"Two successful creates emit two custom_metadata_provider_added events in request order, each carrying its own provider's id and slug.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				socket := e.Socket(t, token)
				mark := socket.Mark()
				stubA := newProviderStub(t, map[string]any{"matches": []any{}})
				stubB := newProviderStub(t, map[string]any{"matches": []any{}})
				first := createProvider(t, e, token, map[string]any{"name": "Repeat Added One", "url": stubA.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, first.ID)
				second := createProvider(t, e, token, map[string]any{"name": "Repeat Added Two", "url": stubB.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, second.ID)

				var events []providerEvent
				harness.Eventually(t, eventWait, "two added events", func() bool {
					events = eventsNamed[providerEvent](t, socket, mark, "custom_metadata_provider_added")
					return len(events) == 2
				})
				requireEventPayload(t, events[0], first)
				requireEventPayload(t, events[1], second)
			}),

		scenario("socket-outgoing-custom-metadata-provider-added", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-ADDED", "normal", "changed", "added-event-after-a-removal",
			[]string{socketEmitterSrc, customControllerSrc},
			"After one provider was created and removed, creating another still emits exactly one custom_metadata_provider_added for the new provider and none for the removed one.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				socket := e.Socket(t, token)
				stubA := newProviderStub(t, map[string]any{"matches": []any{}})
				removed := createProvider(t, e, token, map[string]any{"name": "Removed Then Added", "url": stubA.url(), "mediaType": "book"})
				deleteProvider(t, e, token, removed.ID)

				mark := socket.Mark()
				stubB := newProviderStub(t, map[string]any{"matches": []any{}})
				added := createProvider(t, e, token, map[string]any{"name": "Added After Removal", "url": stubB.url(), "mediaType": "book"})
				defer deleteProvider(t, e, token, added.ID)

				event := eventFirst[providerEvent](t, socket, mark, "custom_metadata_provider_added", eventWait)
				requireEventPayload(t, event, added)
				if got := eventsNamed[providerEvent](t, socket, mark, "custom_metadata_provider_added"); len(got) != 1 {
					t.Fatalf("added events after the removal = %d, want 1: %v", len(got), got)
				}
			}),

		scenario("socket-outgoing-custom-metadata-provider-added", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-ADDED", "error", "failure", "added-event-rejected-create-emits-none",
			[]string{socketEmitterSrc, customControllerSrc, apiAuthSrc},
			"A create rejected with 400 Invalid url or 403 for a non-admin emits no custom_metadata_provider_added to any socket and adds no row.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				audience := connectAudience(t, e)
				marks := audience.marks()
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")

				requireStatus(t, e.Post(t, token, "/api/custom-metadata-providers", map[string]any{"name": "Rejected Event Provider", "url": "not a url", "mediaType": "book"}), http.StatusBadRequest, "invalid url create")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/custom-metadata-providers", map[string]any{"name": "Rejected Event Provider", "url": "http://127.0.0.1:1", "mediaType": "book"}), http.StatusForbidden, "non-admin create")

				for i, socket := range audience.authorized() {
					socket.None(t, marks[i], "custom_metadata_provider_added", eventNoneWait, nil)
				}
				audience.anonymous.None(t, marks[3], "custom_metadata_provider_added", eventNoneWait, nil)
				if after := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders"); after != before {
					t.Fatalf("rejected creates changed the provider count from %d to %d", before, after)
				}
			}),
	}
}

// removedEventScenarios cover the custom_metadata_provider_removed event.
func removedEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-custom-metadata-provider-removed", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-REMOVED", "normal", "initial", "removed-event-authorized-audience-payload",
			[]string{customControllerSrc, socketEmitterSrc, customProviderModel},
			"Deleting a custom metadata provider emits custom_metadata_provider_removed with the provider's client JSON (id, name, mediaType, slug custom-<id>) exactly once to every authenticated socket, admin and normal user alike, and not to an unauthenticated socket.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Event Removed Provider", "url": stub.url(), "mediaType": "book"})

				audience := connectAudience(t, e)
				marks := audience.marks()
				deleteProvider(t, e, token, view.ID)

				for i, socket := range audience.authorized() {
					requireEventPayload(t, eventFirst[providerEvent](t, socket, marks[i], "custom_metadata_provider_removed", eventWait), view)
					if got := eventsNamed[providerEvent](t, socket, marks[i], "custom_metadata_provider_removed"); len(got) != 1 {
						t.Fatalf("socket received the removed event %d times, want 1", len(got))
					}
				}
				audience.anonymous.None(t, marks[3], "custom_metadata_provider_removed", eventNoneWait, nil)
			}),

		scenario("socket-outgoing-custom-metadata-provider-removed", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-REMOVED", "edge", "changed", "removed-event-every-connection-of-a-user",
			[]string{socketEmitterSrc, customControllerSrc},
			"Two sockets of the same user each receive one custom_metadata_provider_removed for a delete while an unauthenticated socket receives none.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Second Removal Provider", "url": stub.url(), "mediaType": "book"})

				first := e.Socket(t, e.Token(t, "listener"))
				second := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				markFirst, markSecond, markAnonymous := first.Mark(), second.Mark(), anonymous.Mark()
				deleteProvider(t, e, token, view.ID)

				requireEventPayload(t, eventFirst[providerEvent](t, first, markFirst, "custom_metadata_provider_removed", eventWait), view)
				requireEventPayload(t, eventFirst[providerEvent](t, second, markSecond, "custom_metadata_provider_removed", eventWait), view)
				anonymous.None(t, markAnonymous, "custom_metadata_provider_removed", eventNoneWait, nil)
			}),

		scenario("socket-outgoing-custom-metadata-provider-removed", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-REMOVED", "normal", "repeat", "removed-event-second-delete-emits-none",
			[]string{socketEmitterSrc, customControllerSrc},
			"The first delete emits one custom_metadata_provider_removed and the repeated delete of the same id answers 404 and emits no second event.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Repeated Removal Provider", "url": stub.url(), "mediaType": "book"})

				socket := e.Socket(t, token)
				mark := socket.Mark()
				deleteProvider(t, e, token, view.ID)
				requireEventPayload(t, eventFirst[providerEvent](t, socket, mark, "custom_metadata_provider_removed", eventWait), view)

				afterFirst := socket.Mark()
				requireStatus(t, e.Delete(t, token, "/api/custom-metadata-providers/"+view.ID), http.StatusNotFound, "second delete")
				socket.None(t, afterFirst, "custom_metadata_provider_removed", eventNoneWait, nil)
			}),

		scenario("socket-outgoing-custom-metadata-provider-removed", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-REMOVED", "normal", "changed", "removed-event-while-library-used",
			[]string{socketEmitterSrc, customControllerSrc},
			"Deleting a provider that a library uses still emits custom_metadata_provider_removed with the provider's original client JSON, and the library's provider column falls back to google.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				stub := newProviderStub(t, map[string]any{"matches": []any{}})
				view := createProvider(t, e, token, map[string]any{"name": "Library Event Provider", "url": stub.url(), "mediaType": "book"})
				library := e.IDs["library"]
				requireStatus(t, e.Patch(t, token, "/api/libraries/"+library, map[string]any{"provider": customSlug(view.ID)}), http.StatusOK, "set library provider")

				socket := e.Socket(t, token)
				mark := socket.Mark()
				deleteProvider(t, e, token, view.ID)
				requireEventPayload(t, eventFirst[providerEvent](t, socket, mark, "custom_metadata_provider_removed", eventWait), view)
				requireEqual(t, "library provider after the event delete", libraryProvider(t, e, library), fixtureLibraryProvider)
			}),

		scenario("socket-outgoing-custom-metadata-provider-removed", "INV-SOCKET-OUTGOING-CUSTOM-METADATA-PROVIDER-REMOVED", "error", "failure", "removed-event-rejected-delete-emits-none",
			[]string{socketEmitterSrc, customControllerSrc, apiAuthSrc},
			"A delete rejected with 403 for a non-admin or 404 for an unknown id emits no custom_metadata_provider_removed to any socket and leaves the provider rows unchanged.",
			func(t *testing.T, e *harness.Env) {
				root, listener := e.Token(t, "root"), e.Token(t, "listener")
				audience := connectAudience(t, e)
				marks := audience.marks()
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")

				requireStatus(t, e.Delete(t, listener, "/api/custom-metadata-providers/"+storedBookProvider.ID), http.StatusForbidden, "non-admin delete")
				requireStatus(t, e.Delete(t, root, "/api/custom-metadata-providers/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown delete")

				for i, socket := range audience.authorized() {
					socket.None(t, marks[i], "custom_metadata_provider_removed", eventNoneWait, nil)
				}
				audience.anonymous.None(t, marks[3], "custom_metadata_provider_removed", eventNoneWait, nil)
				if after := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders"); after != before {
					t.Fatalf("rejected deletes changed the provider count from %d to %d", before, after)
				}
				requireProviderRow(t, providerRow(t, e, storedBookProvider.ID), storedBookProvider)
			}),
	}
}
