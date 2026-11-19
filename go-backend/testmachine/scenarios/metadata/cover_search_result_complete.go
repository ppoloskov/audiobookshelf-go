package metadata

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// coverSearchResultScenarios cover cover_search_result and the search that
// produces it; every expectation is derived from SocketAuthority's
// handleCoverSearch callbacks and CoverSearchManager's book search.
func coverSearchResultScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-cover-search-result"
	const invariant = "INV-SOCKET-OUTGOING-COVER-SEARCH-RESULT"
	return []harness.Scenario{
		scenario(requirement, invariant, "normal", "initial", "cover-result-payload-and-audience",
			[]string{coverSocketRegistrationSrc, coverSearchHandlerSrc, coverManagerBooksSrc, coverManagerExtractSrc, coverClientHandlersSrc},
			"A search_covers from an authenticated socket for one custom provider emits exactly one cover_search_result to that socket alone with the requestId echoed, the provider slug, the provider's covers in match order with duplicates removed and total equal to the cover count, and the provider is queried once at /search with mediaType=book, the title and the author.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches(
					"http://127.0.0.1/cover-one.jpg",
					"http://127.0.0.1/cover-two.jpg",
					"http://127.0.0.1/cover-one.jpg",
				)})
				provider := coverProvider(t, e, "Cover Result Provider", stub)
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}
				mark := audience.requester.Mark()

				requestID := "cover-result-initial"
				emitCoverSearch(t, audience.requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))

				event := audience.requester.Wait(t, mark, "cover_search_result", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId", "provider", "covers", "total")
				requireEqual(t, "cover_search_result payload", decodeArg[coverResultPayload](t, event), coverResultPayload{
					RequestID: requestID,
					Provider:  customSlug(provider.ID),
					Covers:    []string{"http://127.0.0.1/cover-one.jpg", "http://127.0.0.1/cover-two.jpg"},
					Total:     2,
				})
				requireEqual(t, "cover_search_result count", coverEventOrder(t, audience.requester, mark, "cover_search_result"), []string{"cover_search_result"})
				audience.requireSilent(t, silentMarks, "cover_search_result")
				requireSingleCoverRequest(t, stub, "Alpha", "Ann Author")
			}),

		scenario(requirement, invariant, "normal", "changed", "cover-result-normal-user-allowed",
			[]string{coverSocketRegistrationSrc, coverSearchHandlerSrc, coverClientSearchSrc},
			"A normal (non-admin) user's authenticated socket may run a cover search and receives its cover_search_result and cover_search_complete, while the admin socket of another connection receives neither, because the handlers require authentication and not the admin role.",
			func(t *testing.T, e *harness.Env) {
				requester := e.Socket(t, e.Token(t, "listener"))
				admin := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-listener.jpg")})
				provider := coverProvider(t, e, "Cover Result Listener Provider", stub)
				adminMark := admin.Mark()
				mark := requester.Mark()

				requestID := "cover-result-listener"
				emitCoverSearch(t, requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				result := eventFirst[coverResultPayload](t, requester, mark, "cover_search_result", coverEventWait)
				requireEqual(t, "listener result", result, coverResultPayload{
					RequestID: requestID,
					Provider:  customSlug(provider.ID),
					Covers:    []string{"http://127.0.0.1/cover-listener.jpg"},
					Total:     1,
				})
				requester.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				admin.None(t, adminMark, "cover_search_result", coverSilenceWait, nil)
				admin.None(t, adminMark, "cover_search_complete", coverSilenceWait, nil)
				requireSingleCoverRequest(t, stub, "Alpha", "Ann Author")
			}),

		scenario(requirement, invariant, "normal", "repeat", "cover-result-repeat-request-id",
			[]string{coverSearchHandlerSrc, coverManagerStartSrc, coverManagerBooksSrc},
			"A second search_covers with the same requestId after the first search completed emits a second cover_search_result with that same requestId and the same covers, so a completed requestId can be reused and every search emits its own chunk.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-repeat.jpg")})
				provider := coverProvider(t, e, "Cover Result Repeat Provider", stub)
				requestID := "cover-result-repeat"
				mark := socket.Mark()

				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				socket.Wait(t, mark, "cover_search_result", coverEventWait, nil)
				socket.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				secondMark := socket.Mark()

				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				socket.Wait(t, secondMark, "cover_search_complete", coverEventWait, nil)

				results := eventsNamed[coverResultPayload](t, socket, mark, "cover_search_result")
				if len(results) != 2 {
					t.Fatalf("cover_search_result events = %d, want 2: %v", len(results), results)
				}
				for _, result := range results {
					requireEqual(t, "repeated result requestId", result.RequestID, requestID)
					requireEqual(t, "repeated result covers", result.Covers, []string{"http://127.0.0.1/cover-repeat.jpg"})
					requireEqual(t, "repeated result total", result.Total, 1)
				}
				if requests := stub.requestCount(); requests != 2 {
					t.Fatalf("provider received %d requests, want 2", requests)
				}
			}),

		scenario(requirement, invariant, "edge", "repeat", "cover-result-duplicate-active-request-id",
			[]string{coverManagerStartSrc, coverManagerBooksSrc},
			"A second search_covers with the requestId of a search that is still active is ignored: the active search emits its one result and one complete, the duplicate emits neither, and the provider is queried once.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Delay: coverSlowResponse, Body: coverMatches("http://127.0.0.1/cover-duplicate.jpg")})
				provider := coverProvider(t, e, "Cover Result Duplicate Provider", stub)
				requestID := "cover-result-duplicate"
				mark := socket.Mark()

				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				stub.awaitRequest(t, 1)
				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))

				result := eventFirst[coverResultPayload](t, socket, mark, "cover_search_result", coverEventWait)
				requireEqual(t, "duplicate search result", result, coverResultPayload{
					RequestID: requestID,
					Provider:  customSlug(provider.ID),
					Covers:    []string{"http://127.0.0.1/cover-duplicate.jpg"},
					Total:     1,
				})
				socket.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "duplicate search result count", len(eventsNamed[coverResultPayload](t, socket, mark, "cover_search_result")), 1)
				requireEqual(t, "duplicate search complete count", len(eventsNamed[coverCompletePayload](t, socket, mark, "cover_search_complete")), 1)
				if requests := stub.requestCount(); requests != 1 {
					t.Fatalf("provider received %d requests, want 1", requests)
				}
			}),

		scenario(requirement, invariant, "edge", "initial", "cover-result-empty-covers-emit-none",
			[]string{coverManagerBooksSrc, coverManagerExtractSrc, coverFinderCustomSrc, coverAdapterRequestSrc},
			"A provider whose matches carry no cover emits no cover_search_result at all, for both an answer with empty matches and an answer without a matches array, and the search still completes once per request.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				noCoverStub := newCoverStub(t, coverStubConfig{Body: map[string]any{"matches": []any{
					map[string]any{"title": "No Cover At All"},
				}}})
				noCover := coverProvider(t, e, "Cover Result Empty Provider", noCoverStub)
				malformedStub := newCoverStub(t, coverStubConfig{Body: map[string]any{"unexpected": true}})
				malformed := coverProvider(t, e, "Cover Result Malformed Provider", malformedStub)

				firstMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-result-empty", customSlug(noCover.ID), "Alpha"))
				socket.Wait(t, firstMark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "empty provider event order", coverEventOrder(t, socket, firstMark, "cover_search_result", "cover_search_complete"), []string{"cover_search_complete"})

				secondMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-result-malformed", customSlug(malformed.ID), "Alpha"))
				socket.Wait(t, secondMark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "malformed provider event order", coverEventOrder(t, socket, secondMark, "cover_search_result", "cover_search_complete"), []string{"cover_search_complete"})
				socket.None(t, secondMark, "cover_search_result", coverSilenceWait, nil)

				if requests := noCoverStub.requestCount(); requests != 1 {
					t.Fatalf("empty provider received %d requests, want 1", requests)
				}
				if requests := malformedStub.requestCount(); requests != 1 {
					t.Fatalf("malformed provider received %d requests, want 1", requests)
				}
			}),

		scenario(requirement, invariant, "normal", "changed", "cover-result-follows-provider-change",
			[]string{coverManagerBooksSrc, coverFinderCustomSrc, coverAdapterRequestSrc},
			"After the first provider is deleted and a second provider created, a search reports only the second provider's covers and none of the deleted provider's covers arrive again, so a result reflects the providers stored at request time.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				socket := e.Socket(t, token)
				stubA := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-old.jpg")})
				providerA := coverProvider(t, e, "Cover Result Old Provider", stubA)
				markA := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-result-old", customSlug(providerA.ID), "Alpha"))
				first := eventFirst[coverResultPayload](t, socket, markA, "cover_search_result", coverEventWait)
				requireEqual(t, "first provider covers", first.Covers, []string{"http://127.0.0.1/cover-old.jpg"})
				socket.Wait(t, markA, "cover_search_complete", coverEventWait, nil)

				deleteProvider(t, e, token, providerA.ID)
				requireNoProviderRow(t, e, providerA.ID)

				stubB := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-new.jpg")})
				providerB := coverProvider(t, e, "Cover Result New Provider", stubB)
				markB := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-result-new", customSlug(providerB.ID), "Alpha"))
				second := eventFirst[coverResultPayload](t, socket, markB, "cover_search_result", coverEventWait)
				requireEqual(t, "second provider slug", second.Provider, customSlug(providerB.ID))
				requireEqual(t, "second provider covers", second.Covers, []string{"http://127.0.0.1/cover-new.jpg"})
				socket.None(t, markB, "cover_search_result", coverSilenceWait, coverResultFromProvider(customSlug(providerA.ID)))
				requireEqual(t, "deleted provider requests", stubA.requestCount(), 1)
			}),

		scenario(requirement, invariant, "error", "failure", "cover-result-rejected-requests-emit-none",
			[]string{coverSearchHandlerSrc, coverManagerBooksSrc},
			"A search_covers with no title and a search_covers from an unauthenticated socket each answer cover_search_error and emit no cover_search_result to any socket, and neither contacts the provider nor changes the provider table.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-never.jpg")})
				provider := coverProvider(t, e, "Cover Result Rejected Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				marks := []int{audience.requester.Mark(), audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				invalid := coverSearchPayload("cover-result-invalid", customSlug(provider.ID), "")
				delete(invalid, "title")
				emitCoverSearch(t, audience.requester, invalid)
				audience.requester.Wait(t, marks[0], "cover_search_error", coverEventWait, nil)

				emitCoverSearch(t, audience.anonymous, coverSearchPayload("cover-result-anonymous", customSlug(provider.ID), "Alpha"))
				audience.anonymous.Wait(t, marks[3], "cover_search_error", coverEventWait, nil)

				audience.requester.None(t, marks[0], "cover_search_result", coverSilenceWait, nil)
				audience.sameUser.None(t, marks[1], "cover_search_result", coverSilenceWait, nil)
				audience.otherUser.None(t, marks[2], "cover_search_result", coverSilenceWait, nil)
				audience.anonymous.None(t, marks[3], "cover_search_result", coverSilenceWait, nil)
				requireNoCoverRequest(t, stub)
				requireProviderCount(t, e, before)
			}),
	}
}

// coverSearchCompleteScenarios cover cover_search_complete: one event per
// search that was not cancelled, after its provider results settled.
func coverSearchCompleteScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-cover-search-complete"
	const invariant = "INV-SOCKET-OUTGOING-COVER-SEARCH-COMPLETE"
	return []harness.Scenario{
		scenario(requirement, invariant, "normal", "initial", "cover-complete-payload-and-ordering",
			[]string{coverSearchHandlerSrc, coverManagerStartSrc, coverClientHandlersSrc},
			"A search over one custom provider emits one cover_search_complete to the requesting socket alone, after the cover_search_result, with exactly the requestId argument and no other field.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-complete.jpg")})
				provider := coverProvider(t, e, "Cover Complete Provider", stub)
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}
				mark := audience.requester.Mark()

				requestID := "cover-complete-initial"
				emitCoverSearch(t, audience.requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))

				event := audience.requester.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId")
				requireEqual(t, "cover_search_complete payload", decodeArg[coverCompletePayload](t, event), coverCompletePayload{RequestID: requestID})
				requireEqual(t, "event order", coverEventOrder(t, audience.requester, mark, "cover_search_result", "cover_search_complete"),
					[]string{"cover_search_result", "cover_search_complete"})
				audience.requireSilent(t, silentMarks, "cover_search_complete")
			}),

		scenario(requirement, invariant, "normal", "repeat", "cover-complete-each-search",
			[]string{coverManagerStartSrc, coverManagerBooksSrc, coverClientHandlersSrc},
			"Three searches, two with different requestIds and one reusing the first after it completed, emit three cover_search_complete events in request order with their own requestIds, each after that search's result.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-each.jpg")})
				provider := coverProvider(t, e, "Cover Complete Repeat Provider", stub)
				mark := socket.Mark()

				requestIDs := []string{"cover-complete-first", "cover-complete-second", "cover-complete-first"}
				for _, requestID := range requestIDs {
					searchMark := socket.Mark()
					emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
					socket.Wait(t, searchMark, "cover_search_complete", coverEventWait, nil)
					requireEqual(t, "search "+requestID+" event order", coverEventOrder(t, socket, searchMark, "cover_search_result", "cover_search_complete"),
						[]string{"cover_search_result", "cover_search_complete"})
				}

				completes := eventsNamed[coverCompletePayload](t, socket, mark, "cover_search_complete")
				if len(completes) != len(requestIDs) {
					t.Fatalf("cover_search_complete events = %d, want %d: %v", len(completes), len(requestIDs), completes)
				}
				for i, requestID := range requestIDs {
					requireEqual(t, "complete requestId", completes[i].RequestID, requestID)
				}
			}),

		scenario(requirement, invariant, "edge", "changed", "cover-complete-without-results",
			[]string{coverManagerStartSrc, coverManagerBooksSrc, coverFinderCustomSrc},
			"A search that produces no covers still emits exactly one cover_search_complete and no result, both for a provider answer without covers and for a provider slug whose row was deleted before the search.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				socket := e.Socket(t, token)
				stub := newCoverStub(t, coverStubConfig{Body: map[string]any{"matches": []any{}}})
				provider := coverProvider(t, e, "Cover Complete Empty Provider", stub)
				firstMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-complete-empty", customSlug(provider.ID), "Alpha"))
				socket.Wait(t, firstMark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "empty search order", coverEventOrder(t, socket, firstMark, "cover_search_result", "cover_search_complete"),
					[]string{"cover_search_complete"})

				deleteProvider(t, e, token, provider.ID)
				secondMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload("cover-complete-deleted", customSlug(provider.ID), "Alpha"))
				socket.Wait(t, secondMark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "deleted provider search order", coverEventOrder(t, socket, secondMark, "cover_search_result", "cover_search_complete"),
					[]string{"cover_search_complete"})
				if requests := stub.requestCount(); requests != 1 {
					t.Fatalf("provider received %d requests, want 1 (the deleted slug must not be queried)", requests)
				}
			}),

		scenario(requirement, invariant, "error", "failure", "cover-complete-not-after-rejection",
			[]string{coverSearchHandlerSrc, coverManagerStartSrc},
			"A search_covers rejected for a missing title and a search_covers from an unauthenticated socket emit cover_search_error and no cover_search_complete to any socket, and no provider is contacted.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-never.jpg")})
				provider := coverProvider(t, e, "Cover Complete Rejected Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				marks := []int{audience.requester.Mark(), audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				invalid := coverSearchPayload("cover-complete-invalid", customSlug(provider.ID), "")
				delete(invalid, "title")
				emitCoverSearch(t, audience.requester, invalid)
				audience.requester.Wait(t, marks[0], "cover_search_error", coverEventWait, nil)
				emitCoverSearch(t, audience.anonymous, coverSearchPayload("cover-complete-anonymous", customSlug(provider.ID), "Alpha"))
				audience.anonymous.Wait(t, marks[3], "cover_search_error", coverEventWait, nil)

				audience.requester.None(t, marks[0], "cover_search_complete", coverSilenceWait, nil)
				audience.sameUser.None(t, marks[1], "cover_search_complete", coverSilenceWait, nil)
				audience.otherUser.None(t, marks[2], "cover_search_complete", coverSilenceWait, nil)
				audience.anonymous.None(t, marks[3], "cover_search_complete", coverSilenceWait, nil)
				requireNoCoverRequest(t, stub)
				requireProviderCount(t, e, before)
			}),

		scenario(requirement, invariant, "edge", "changed", "cover-complete-after-provider-error",
			[]string{coverManagerBooksSrc, coverManagerStartSrc, coverFinderSearchSrc},
			"When the finder fails for a provider, the search emits cover_search_provider_error first and still emits exactly one cover_search_complete afterwards, with no cover_search_result.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				payload := coverSearchPayload("cover-complete-provider-error", "google", "")
				payload["title"] = 42
				emitCoverSearch(t, socket, payload)
				socket.Wait(t, mark, "cover_search_provider_error", coverEventWait, nil)
				socket.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				requireEqual(t, "failing provider event order", coverEventOrder(t, socket, mark, "cover_search_result", "cover_search_provider_error", "cover_search_complete"),
					[]string{"cover_search_provider_error", "cover_search_complete"})
			}),
	}
}
