package metadata

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// coverSearchCancelScenarios cover cover_search_cancelled: cancelling an active
// search emits the event to the cancelling socket alone, stops every further
// event for that search, and leaves the requestId reusable.
func coverSearchCancelScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-cover-search-cancelled"
	const invariant = "INV-SOCKET-OUTGOING-COVER-SEARCH-CANCELLED"
	return []harness.Scenario{
		scenario(requirement, invariant, "normal", "initial", "cover-cancel-active-search",
			[]string{coverCancelHandlerSrc, coverManagerCancelSrc, coverManagerBooksSrc, coverClientSearchSrc},
			"Cancelling an active search by its requestId emits cover_search_cancelled with that requestId to the cancelling socket alone, and when the slow provider's answer arrives afterwards no result, complete or provider error follows.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Delay: coverSlowResponse, Body: coverMatches("http://127.0.0.1/cover-slow.jpg")})
				provider := coverProvider(t, e, "Cover Cancel Provider", stub)
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}
				requestID := "cover-cancel-initial"
				mark := audience.requester.Mark()

				emitCoverSearch(t, audience.requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				stub.awaitRequest(t, 1)
				audience.requester.Emit(t, "cancel_cover_search", requestID)

				event := audience.requester.Wait(t, mark, "cover_search_cancelled", coverEventWait, nil)
				requireCoverEventFields(t, event, "requestId")
				requireEqual(t, "cover_search_cancelled payload", decodeArg[coverCancelledPayload](t, event), coverCancelledPayload{RequestID: requestID})
				audience.requireSilent(t, silentMarks, "cover_search_cancelled")

				// The provider answer still arrives after the cancellation; it must not
				// produce any further event for the search.
				lateMark := audience.requester.Mark()
				stub.awaitAnswer(t, 1)
				audience.requester.None(t, lateMark, "cover_search_result", 2*coverSilenceWait, nil)
				audience.requester.None(t, lateMark, "cover_search_complete", coverSilenceWait, nil)
				audience.requester.None(t, lateMark, "cover_search_provider_error", coverSilenceWait, nil)
			}),

		scenario(requirement, invariant, "normal", "repeat", "cover-cancel-completed-search-emits-none",
			[]string{coverManagerCancelSrc, coverCancelHandlerSrc},
			"Cancelling the requestId of a search that already completed emits no cover_search_cancelled, and a second cancellation of the same requestId emits none either.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				stub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-fast.jpg")})
				provider := coverProvider(t, e, "Cover Cancel Repeat Provider", stub)
				requestID := "cover-cancel-repeat"
				mark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				socket.Wait(t, mark, "cover_search_complete", coverEventWait, nil)

				socket.Emit(t, "cancel_cover_search", requestID)
				socket.None(t, mark, "cover_search_cancelled", coverSilenceWait, nil)
				socket.Emit(t, "cancel_cover_search", requestID)
				socket.None(t, mark, "cover_search_cancelled", coverSilenceWait, nil)
			}),

		scenario(requirement, invariant, "edge", "initial", "cover-cancel-unknown-request-id",
			[]string{coverManagerCancelSrc, coverCancelHandlerSrc},
			"Cancelling an unknown, empty or absent requestId emits no cover_search_cancelled and no error to the socket.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				for _, requestID := range []any{"cover-cancel-unknown", "", nil} {
					socket.Emit(t, "cancel_cover_search", requestID)
				}
				socket.Emit(t, "cancel_cover_search")
				socket.None(t, mark, "cover_search_cancelled", coverSilenceWait, nil)
				socket.None(t, mark, "cover_search_error", coverSilenceWait, nil)
			}),

		scenario(requirement, invariant, "error", "initial", "cover-cancel-unauthenticated-ignored",
			[]string{coverCancelHandlerSrc, coverSocketRegistrationSrc},
			"A cancellation from an unauthenticated socket emits no cover_search_cancelled to it and does not abort the authenticated socket's active search, which still delivers its result and complete.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Delay: coverSlowResponse, Body: coverMatches("http://127.0.0.1/cover-active.jpg")})
				provider := coverProvider(t, e, "Cover Cancel Anonymous Provider", stub)
				requestID := "cover-cancel-anonymous"
				mark := audience.requester.Mark()

				emitCoverSearch(t, audience.requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				stub.awaitRequest(t, 1)
				audience.anonymous.Emit(t, "cancel_cover_search", requestID)
				audience.anonymous.None(t, 0, "cover_search_cancelled", coverSilenceWait, nil)

				audience.requester.Wait(t, mark, "cover_search_result", coverEventWait, nil)
				audience.requester.Wait(t, mark, "cover_search_complete", coverEventWait, nil)
				audience.requester.None(t, mark, "cover_search_cancelled", coverSilenceWait, nil)
			}),

		scenario(requirement, invariant, "normal", "changed", "cover-cancel-frees-request-id",
			[]string{coverManagerCancelSrc, coverManagerBooksSrc, coverManagerStartSrc},
			"After a search is cancelled, the same requestId starts and completes a new search against another provider, and the cancelled provider's late answer still produces no result.",
			func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "root"))
				slowStub := newCoverStub(t, coverStubConfig{Delay: coverSlowResponse, Body: coverMatches("http://127.0.0.1/cover-cancelled.jpg")})
				slowProvider := coverProvider(t, e, "Cover Cancel Freed Slow Provider", slowStub)
				fastStub := newCoverStub(t, coverStubConfig{Body: coverMatches("http://127.0.0.1/cover-reused.jpg")})
				fastProvider := coverProvider(t, e, "Cover Cancel Freed Fast Provider", fastStub)
				requestID := "cover-cancel-reused"

				firstMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(slowProvider.ID), "Alpha"))
				slowStub.awaitRequest(t, 1)
				socket.Emit(t, "cancel_cover_search", requestID)
				socket.Wait(t, firstMark, "cover_search_cancelled", coverEventWait, nil)

				secondMark := socket.Mark()
				emitCoverSearch(t, socket, coverSearchPayload(requestID, customSlug(fastProvider.ID), "Alpha"))
				result := eventFirst[coverResultPayload](t, socket, secondMark, "cover_search_result", coverEventWait)
				requireEqual(t, "reused request result provider", result.Provider, customSlug(fastProvider.ID))
				requireEqual(t, "reused request result covers", result.Covers, []string{"http://127.0.0.1/cover-reused.jpg"})
				socket.Wait(t, secondMark, "cover_search_complete", coverEventWait, nil)
				afterComplete := socket.Mark()

				slowStub.awaitAnswer(t, 1)
				socket.None(t, secondMark, "cover_search_result", 2*coverSilenceWait, coverResultFromProvider(customSlug(slowProvider.ID)))
				socket.None(t, afterComplete, "cover_search_complete", coverSilenceWait, nil)
			}),

		scenario(requirement, invariant, "error", "failure", "cover-cancel-late-answer-emits-nothing",
			[]string{coverManagerCancelSrc, coverCancelHandlerSrc, coverManagerBooksSrc},
			"Cancelling an active search emits exactly one cover_search_cancelled and, after the slow provider's answer is written, no further event of any kind reaches any socket while the provider rows stay unchanged.",
			func(t *testing.T, e *harness.Env) {
				audience := openCoverAudience(t, e)
				stub := newCoverStub(t, coverStubConfig{Delay: coverSlowResponse, Body: coverMatches("http://127.0.0.1/cover-late.jpg")})
				provider := coverProvider(t, e, "Cover Cancel Late Provider", stub)
				before := e.Count(t, "SELECT COUNT(*) FROM customMetadataProviders")
				requestID := "cover-cancel-late"
				mark := audience.requester.Mark()
				silentMarks := []int{audience.sameUser.Mark(), audience.otherUser.Mark(), audience.anonymous.Mark()}

				emitCoverSearch(t, audience.requester, coverSearchPayload(requestID, customSlug(provider.ID), "Alpha"))
				stub.awaitRequest(t, 1)
				audience.requester.Emit(t, "cancel_cover_search", requestID)
				audience.requester.Wait(t, mark, "cover_search_cancelled", coverEventWait, nil)

				cancelled := eventsNamed[coverCancelledPayload](t, audience.requester, mark, "cover_search_cancelled")
				if len(cancelled) != 1 {
					t.Fatalf("cover_search_cancelled events = %d, want 1: %v", len(cancelled), cancelled)
				}
				lateMark := audience.requester.Mark()
				stub.awaitAnswer(t, 1)
				audience.requester.None(t, lateMark, "cover_search_result", 2*coverSilenceWait, nil)
				audience.requester.None(t, lateMark, "cover_search_complete", coverSilenceWait, nil)
				audience.requester.None(t, lateMark, "cover_search_provider_error", coverSilenceWait, nil)
				for i, socket := range audience.silent() {
					socket.None(t, silentMarks[i], "cover_search_result", coverSilenceWait, nil)
					socket.None(t, silentMarks[i], "cover_search_complete", coverSilenceWait, nil)
					socket.None(t, silentMarks[i], "cover_search_provider_error", coverSilenceWait, nil)
				}
				requireProviderCount(t, e, before)
			}),
	}
}
