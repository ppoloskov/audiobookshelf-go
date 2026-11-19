package metadata

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// coverSearchErrorScenarios cover cover_search_error: the two deliberate
// refusals of a search_covers request (unauthenticated socket, invalid
// parameters) reach the requesting socket only.
func coverSearchErrorScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-cover-search-error"
	const invariant = "INV-SOCKET-OUTGOING-COVER-SEARCH-ERROR"
	return []harness.Scenario{
		scenario(requirement, invariant, "normal", "initial", "cover-error-missing-title",
			[]string{coverSearchHandlerSrc, coverClientHandlersSrc},
			"A search_covers without a title from an authenticated socket emits exactly one cover_search_error to that socket with the requestId echoed and a non-empty error, no result, complete or provider error, and an uncontacted provider.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-never.jpg")})
				provider := coverProvider(t, e, "Cover Error Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}
				mark := audience.requester.Mark()

				requestID := "cover-error-no-title"
				payload := coverSearchPayload(requestID, customSlug(provider.ID), "")
				delete(payload, "title")
				emitCoverSearch(t, audience.requester, payload)

				event := audience.requester.Wait(t, mark, "cover_search_error", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId", "error")
				payloadError := decodeArg[coverErrorPayload](t, event)
				if payloadError.RequestID == nil {
					t.Fatalf("cover_search_error has no requestId: %s", event.Args[0])
				}
				requireEqual(t, "error requestId", *payloadError.RequestID, requestID)
				if payloadError.Error == "" {
					t.Fatalf("cover_search_error has an empty error: %s", event.Args[0])
				}

				audience.requester.None(t, mark, "cover_search_result", coverSilenceWait, nil)
				audience.requester.None(t, mark, "cover_search_complete", coverSilenceWait, nil)
				audience.requester.None(t, mark, "cover_search_provider_error", coverSilenceWait, nil)
				audience.requireSilent(t, silentMarks, "cover_search_error")
				requireNoCoverRequest(t, stub)
				requireProviderCount(t, e, before)
			}),

		scenario(requirement, invariant, "error", "initial", "cover-error-unauthenticated",
			[]string{coverSearchHandlerSrc, coverClientHandlersSrc},
			"A search_covers from an unauthenticated socket emits cover_search_error with the requestId echoed and a non-empty error to that socket alone, contacts no provider and reaches no authenticated socket.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-never.jpg")})
				provider := coverProvider(t, e, "Cover Error Anonymous Provider", stub)
				marks := []int{audience.requester.Mark(), audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				requestID := "cover-error-anonymous"
				emitCoverSearch(t, audience.anonymous, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))

				event := audience.anonymous.Wait(t, marks[3], "cover_search_error", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId", "error")
				payloadError := decodeArg[coverErrorPayload](t, event)
				if payloadError.RequestID == nil {
					t.Fatalf("cover_search_error has no requestId: %s", event.Args[0])
				}
				requireEqual(t, "error requestId", *payloadError.RequestID, requestID)
				if payloadError.Error == "" {
					t.Fatalf("cover_search_error has an empty error: %s", event.Args[0])
				}

				audience.requester.None(t, marks[0], "cover_search_error", coverSilenceWait, nil)
				audience.sameUser.None(t, marks[1], "cover_search_error", coverSilenceWait, nil)
				audience.otherUser.None(t, marks[2], "cover_search_error", coverSilenceWait, nil)
				requireNoCoverRequest(t, stub)
			}),

		scenario(requirement, invariant, "edge", "initial", "cover-error-request-id-echo",
			[]string{coverSearchHandlerSrc},
			"An absent requestId yields a cover_search_error whose only field is error, while an empty requestId or an empty title keeps the requestId field with its value, so the client's requestId comparison sees the refusal it asked for.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-never.jpg")})
				provider := coverProvider(t, e, "Cover Error Echo Provider", stub)
				slug := customSlug(provider.ID)
				mark := socket.Mark()

				emitCoverSearch(t, socket, map[string]any{"title": "Alpha", "provider": slug, "podcast": false})
				absent := socket.Wait(t, mark, "cover_search_error", coverEventWait, nil)
				requireCoverEventFields(t, absent, "error")

				emptyMark := socket.Mark()
				emptyID := map[string]any{"requestId": "", "title": "Alpha", "provider": slug, "podcast": false}
				emitCoverSearch(t, socket, emptyID)
				empty := eventFirst[coverErrorPayload](t, socket, emptyMark, "cover_search_error", coverEventWait)
				if empty.RequestID == nil || *empty.RequestID != "" {
					t.Fatalf("empty requestId was not echoed: %v", empty.RequestID)
				}

				emptyTitle := map[string]any{"requestId": "cover-error-empty-title", "title": "", "provider": slug, "podcast": false}
				markTitle := socket.Mark()
				emitCoverSearch(t, socket, emptyTitle)
				titleEvent := socket.Wait(t, markTitle, "cover_search_error", coverEventWait, nil)
				requireCoverEventFields(t, titleEvent, "requestId", "error")
				titleError := decodeArg[coverErrorPayload](t, titleEvent)
				if titleError.RequestID == nil || *titleError.RequestID != "cover-error-empty-title" {
					t.Fatalf("empty-title error requestId = %v, want the echoed id", titleError.RequestID)
				}

				errors := eventsNamed[coverErrorPayload](t, socket, mark, "cover_search_error")
				if len(errors) != 3 {
					t.Fatalf("cover_search_error events = %d, want 3: %v", len(errors), errors)
				}
				requireNoCoverRequest(t, stub)
			}),

		scenario(requirement, invariant, "normal", "repeat", "cover-error-repeat-same-request-id",
			[]string{coverSearchHandlerSrc},
			"Repeating the same invalid search_covers twice emits two cover_search_error events, both echoing the same requestId, so a refusal is never deduplicated.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				invalid := map[string]any{"requestId": "cover-error-repeat", "provider": "google", "podcast": false}
				for range 2 {
					searchMark := socket.Mark()
					emitCoverSearch(t, socket, invalid)
					socket.Wait(t, searchMark, "cover_search_error", coverEventWait, nil)
				}
				errors := eventsNamed[coverErrorPayload](t, socket, mark, "cover_search_error")
				if len(errors) != 2 {
					t.Fatalf("cover_search_error events = %d, want 2: %v", len(errors), errors)
				}
				for _, payloadError := range errors {
					if payloadError.RequestID == nil || *payloadError.RequestID != "cover-error-repeat" {
						t.Fatalf("repeated error requestId = %v, want %q", payloadError.RequestID, "cover-error-repeat")
					}
				}
			}),

		scenario(requirement, invariant, "normal", "changed", "cover-error-after-successful-search",
			[]string{coverSearchHandlerSrc, coverManagerBooksSrc},
			"After a successful search returned a result and completed, a later invalid search_covers answers cover_search_error with its own requestId, emits no result for it and leaves the provider query count at one.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-good.jpg")})
				provider := coverProvider(t, e, "Cover Error After Success Provider", stub)
				successMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-error-success", customSlug(provider.ID), "Alpha"))
				socket.Wait(t, successMark, "cover_search_complete", coverEventWait, nil)

				mark := socket.Mark()
				invalid := coverSearchPayload("cover-error-later", customSlug(provider.ID), "")
				delete(invalid, "title")
				emitCoverSearch(t, socket, invalid)
				event := socket.Wait(t, mark, "cover_search_error", coverEventWait, nil)
				payloadError := decodeArg[coverErrorPayload](t, event)
				if payloadError.RequestID == nil || *payloadError.RequestID != "cover-error-later" {
					t.Fatalf("error requestId = %v, want the later id", payloadError.RequestID)
				}
				socket.None(t, mark, "cover_search_result", coverSilenceWait, coverResultFromProvider(customSlug(provider.ID)))
				if requests := stub.requestCount(); requests != 1 {
					t.Fatalf("provider received %d requests, want 1", requests)
				}
			}),

		scenario(requirement, invariant, "error", "failure", "cover-error-rejected-search-changes-nothing",
			[]string{coverSearchHandlerSrc, coverManagerStartSrc},
			"A rejected search_covers leaves every persisted provider row, every in-flight event type and every later search untouched: no result, complete, provider error or cancelled reaches any socket, no provider is queried, and a following valid search still returns a result and completes.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-after.jpg")})
				provider := coverProvider(t, e, "Cover Error Failure Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				marks := []int{audience.requester.Mark(), audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				invalid := coverSearchPayload("cover-error-failure", customSlug(provider.ID), "")
				delete(invalid, "title")
				emitCoverSearch(t, audience.requester, invalid)
				audience.requester.Wait(t, marks[0], "cover_search_error", coverEventWait, nil)
				emitCoverSearch(t, audience.anonymous, coverSearchPayload("cover-error-failure-anonymous", customSlug(provider.ID), "Alpha"))
				audience.anonymous.Wait(t, marks[3], "cover_search_error", coverEventWait, nil)

				for i, socket := range []*harness.Socket{audience.requester, audience.sameUser, audience.otherUser, audience.anonymous} {
					socket.None(t, marks[i], "cover_search_result", coverSilenceWait, nil)
					socket.None(t, marks[i], "cover_search_complete", coverSilenceWait, nil)
					socket.None(t, marks[i], "cover_search_provider_error", coverSilenceWait, nil)
					socket.None(t, marks[i], "cover_search_cancelled", coverSilenceWait, nil)
				}
				requireNoCoverRequest(t, stub)
				requireProviderCount(t, e, before)

				afterMark := audience.requester.Mark()
				emitCoverSearch(t, audience.requester, coverSearchPayload("cover-error-recovery", customSlug(provider.ID), "Alpha"))
				audience.requester.Wait(t, afterMark, "cover_search_result", coverEventWait, nil)
				audience.requester.Wait(t, afterMark, "cover_search_complete", coverEventWait, nil)
				requireSingleCoverRequest(t, stub, "Alpha", "Ann Author")
			}),
	}
}

// coverProviderErrorScenarios cover cover_search_provider_error: a per-provider
// failure reported to the requesting socket, followed by completion.
func coverProviderErrorScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-cover-search-provider-error"
	const invariant = "INV-SOCKET-OUTGOING-COVER-SEARCH-PROVIDER-ERROR"
	return []harness.Scenario{
		scenario(requirement, invariant, "normal", "initial", "cover-provider-error-reports-failing-provider",
			[]string{coverSearchHandlerSrc, coverManagerBooksSrc, coverFinderSearchSrc, coverClientHandlersSrc},
			"A search whose finder call fails for one provider emits cover_search_provider_error to the requesting socket alone with the requestId echoed, that provider's name and a non-empty error, then completes without any result.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}
				mark := audience.requester.Mark()
				requestID := "cover-provider-error-initial"
				payload := coverSearchPayload(requestID, "google", "")
				// A non-string title reaches BookFinder.search's string handling and
				// throws before any provider request, which is the offline-reachable
				// finder failure the manager reports through onError.
				payload["title"] = 42
				emitCoverSearch(t, audience.requester, payload)

				event := audience.requester.Wait(t, mark, "cover_search_provider_error", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId", "provider", "error")
				providerError := decodeArg[coverProviderErrorPayload](t, event)
				if providerError.RequestID == nil || *providerError.RequestID != requestID {
					t.Fatalf("provider error requestId = %v, want %q", providerError.RequestID, requestID)
				}
				requireEqual(t, "provider error provider", providerError.Provider, "google")
				if providerError.Error == "" {
					t.Fatalf("cover_search_provider_error has an empty error: %s", event.Args[0])
				}
				requireEqual(t, "failing provider event order", coverEventOrder(t, audience.requester, mark, "cover_search_result", "cover_search_provider_error", "cover_search_complete"),
					[]string{"cover_search_provider_error", "cover_search_complete"})
				audience.requireSilent(t, silentMarks, "cover_search_provider_error")
			}),

		scenario(requirement, invariant, "error", "initial", "cover-provider-error-not-for-rejections",
			[]string{coverSearchHandlerSrc},
			"A search_covers rejected for invalid parameters or for an unauthenticated socket answers cover_search_error and never cover_search_provider_error on any socket.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				marks := []int{audience.requester.Mark(), audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				invalid := map[string]any{"requestId": "cover-provider-error-invalid", "provider": "google", "podcast": false}
				emitCoverSearch(t, audience.requester, invalid)
				audience.requester.Wait(t, marks[0], "cover_search_error", coverEventWait, nil)
				emitCoverSearch(t, audience.anonymous, coverSearchPayload("cover-provider-error-anonymous", "google", "Alpha"))
				audience.anonymous.Wait(t, marks[3], "cover_search_error", coverEventWait, nil)

				for i, socket := range []*harness.Socket{audience.requester, audience.sameUser, audience.otherUser, audience.anonymous} {
					socket.None(t, marks[i], "cover_search_provider_error", coverSilenceWait, nil)
				}
			}),

		scenario(requirement, invariant, "edge", "initial", "cover-provider-error-names-requested-provider",
			[]string{coverManagerBooksSrc, coverFinderSearchSrc},
			"Two failing searches name the provider string the client requested, an unknown slug and a regional audible provider, in their cover_search_provider_error events rather than a canonical provider.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				for _, provider := range []string{"bogus", "audible.de"} {
					payload := coverSearchPayload("cover-provider-error-"+provider, provider, "")
					payload["title"] = 42
					emitCoverSearch(t, socket, payload)
					socket.Wait(t, mark, "cover_search_provider_error", coverEventWait, coverProviderErrorFromProvider(provider))
				}
				errors := eventsNamed[coverProviderErrorPayload](t, socket, mark, "cover_search_provider_error")
				if len(errors) != 2 {
					t.Fatalf("cover_search_provider_error events = %d, want 2: %v", len(errors), errors)
				}
				requireEqual(t, "first failing provider", errors[0].Provider, "bogus")
				requireEqual(t, "second failing provider", errors[1].Provider, "audible.de")
			}),

		scenario(requirement, invariant, "normal", "repeat", "cover-provider-error-each-request",
			[]string{coverSearchHandlerSrc, coverManagerBooksSrc},
			"Two sequential failing searches emit two cover_search_provider_error events in request order with their own requestIds, each followed by its own complete.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				requestIDs := []string{"cover-provider-error-first", "cover-provider-error-second"}
				for _, requestID := range requestIDs {
					searchMark := socket.Mark()
					payload := coverSearchPayload(requestID, "google", "")
					payload["title"] = 42
					emitCoverSearch(t, socket, payload)
					socket.Wait(t, searchMark, "cover_search_complete", coverEventWait, nil)
				}
				errors := eventsNamed[coverProviderErrorPayload](t, socket, mark, "cover_search_provider_error")
				if len(errors) != len(requestIDs) {
					t.Fatalf("cover_search_provider_error events = %d, want %d: %v", len(errors), len(requestIDs), errors)
				}
				for i, requestID := range requestIDs {
					if errors[i].RequestID == nil || *errors[i].RequestID != requestID {
						t.Fatalf("provider error %d requestId = %v, want %q", i, errors[i].RequestID, requestID)
					}
				}
			}),

		scenario(requirement, invariant, "normal", "changed", "cover-provider-error-after-successful-search",
			[]string{coverSearchHandlerSrc, coverManagerBooksSrc},
			"After a successful custom-provider search, a failing search for another provider reports only its own provider error and requestId, emits no result and adds no provider request.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-ok.jpg")})
				provider := coverProvider(t, e, "Cover Provider Error Success Provider", stub)
				successMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-provider-error-success", customSlug(provider.ID), "Alpha"))
				socket.Wait(t, successMark, "cover_search_complete", coverEventWait, nil)

				mark := socket.Mark()
				payload := coverSearchPayload("cover-provider-error-after", "google", "")
				payload["title"] = 42
				emitCoverSearch(t, socket, payload)
				event := socket.Wait(t, mark, "cover_search_provider_error", coverEventWait, nil)
				failure := decodeArg[coverProviderErrorPayload](t, event)
				if failure.RequestID == nil || *failure.RequestID != "cover-provider-error-after" {
					t.Fatalf("provider error requestId = %v, want the failing id", failure.RequestID)
				}
				socket.None(t, mark, "cover_search_result", coverSilenceWait, nil)
				if requests := stub.requestCount(); requests != 1 {
					t.Fatalf("custom provider received %d requests, want 1", requests)
				}
			}),

		scenario(requirement, invariant, "error", "failure", "cover-provider-error-custom-failure-is-silent",
			[]string{coverFinderCustomSrc, coverAdapterRequestSrc, coverManagerBooksSrc},
			"A custom provider answering HTTP 500 emits no cover_search_provider_error and no result, still emits exactly one cover_search_complete, and leaves the provider rows unchanged, because the finder swallows a provider's own HTTP failure.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Status: http.StatusInternalServerError, Body: map[string]any{"error": "provider down"}})
				provider := coverProvider(t, e, "Cover Provider Error 500 Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				mark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-provider-error-500", customSlug(provider.ID), "Alpha"))
				socket.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "failing custom provider event order", coverEventOrder(t, socket, mark, "cover_search_result", "cover_search_provider_error", "cover_search_complete"),
					[]string{"cover_search_complete"})
				socket.None(t, mark, "cover_search_provider_error", coverSilenceWait, nil)
				if requests := stub.requestCount(); requests != 1 {
					t.Fatalf("custom provider received %d requests, want 1", requests)
				}
				requireProviderCount(t, e, before)
			}),
	}
}
