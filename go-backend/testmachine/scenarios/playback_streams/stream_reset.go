package playback_streams

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// resetScenarios cover stream_reset: the global audience, the payload, the
// distant-segment trigger, the resetting guard and the silent missing-segment
// answers.
func resetScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-reset", "INV-SOCKET-OUTGOING-STREAM-RESET", "normal", "initial", "stream-reset-backward-segment",
			[]string{hlsRouterSrc, hlsStreamSrc, streamResetSrc, socketClientSrc, webSocketSrc},
			"A missing segment before the start offset answers 404 and broadcasts stream_reset {startTime 6, streamId} to every authorized socket (both owner sockets, the admin and another user), once.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "reset-initial-user")
				setProgress(t, e, user, e.IDs["novel"], 44, novelDuration)
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["novel"], "", "reset-initial")
				harness.Eventually(t, 30*time.Second, "the first produced segment", func() bool {
					return getHLS(t, e, doc.ID, "output-2.ts", nil).Status == http.StatusOK
				})
				a.reset()
				requireStatus(t, getHLS(t, e, doc.ID, "output-1.ts", nil), http.StatusNotFound, "segment before the start offset")
				payload := requireGlobal[resetDoc](t, a, "stream_reset")
				requireEqual(t, "stream_reset payload", payload, resetDoc{StartTime: segmentLength, StreamID: doc.ID})
				a.settleAll(t)
				for i, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
					if got := eventsNamed[resetDoc](t, s, a.marks[i], "stream_reset"); len(got) != 1 {
						t.Fatalf("stream_reset on socket %d reached %d times, want 1", i, len(got))
					}
				}
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-reset", "INV-SOCKET-OUTGOING-STREAM-RESET-TRIGGER-001", "edge", "changed", "stream-reset-at-zero-is-silent",
			[]string{hlsRouterSrc, hlsStreamSrc, streamResetSrc},
			"A missing segment 0 of a stream that started later restarts the transcode from the beginning but broadcasts no stream_reset, because the reset start time 0 is not a truthy payload.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "reset-zero-user")
				setProgress(t, e, user, e.IDs["novel"], 44, novelDuration)
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["novel"], "", "reset-zero")
				harness.Eventually(t, 30*time.Second, "the first produced segment", func() bool {
					return getHLS(t, e, doc.ID, "output-2.ts", nil).Status == http.StatusOK
				})
				a.reset()
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusNotFound, "segment 0 before the start offset")
				a.requireNobody(t, "stream_reset", 500*time.Millisecond)
				// The silent reset still restarts the transcode at segment 0.
				waitSegment(t, e, doc.ID, 0)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-reset", "INV-SOCKET-OUTGOING-STREAM-RESET-TRIGGER-001", "normal", "changed", "stream-reset-distant-segment-and-guard",
			[]string{hlsRouterSrc, hlsStreamSrc, streamResetSrc},
			"A segment far beyond the produced segments of a running transcode answers 404 and broadcasts stream_reset {startTime 240, streamId} to every authorized socket; a second missing segment while the stream is resetting answers 404 without a second reset.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: slowFFmpegEnv(t)})
				user := createUser(t, own, "reset-distant-user")
				a := watch(t, own, user, "finisher")
				doc := startStream(t, own, user, own.IDs["novel"], "", "reset-distant")
				harness.Eventually(t, 20*time.Second, "a segment-check progress payload", func() bool {
					for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
						if event.NumSegments != 0 {
							return true
						}
					}
					return false
				})
				a.reset()
				requireStatus(t, getHLS(t, own, doc.ID, "output-40.ts", nil), http.StatusNotFound, "distant segment")
				payload := requireGlobal[resetDoc](t, a, "stream_reset")
				requireEqual(t, "stream_reset payload", payload, resetDoc{StartTime: 40 * segmentLength, StreamID: doc.ID})
				requireStatus(t, getHLS(t, own, doc.ID, "output-41.ts", nil), http.StatusNotFound, "segment during the reset")
				a.settleAll(t)
				for i, s := range []*harness.Socket{a.owner1, a.owner2, a.admin, a.other} {
					if got := eventsNamed[resetDoc](t, s, a.marks[i], "stream_reset"); len(got) != 1 {
						t.Fatalf("stream_reset on socket %d reached %d times, want 1", i, len(got))
					}
				}
				closeQuietly(t, own, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-reset", "INV-SOCKET-OUTGOING-STREAM-RESET-SILENT-001", "error", "failure", "stream-reset-silent-after-completion-and-close",
			[]string{hlsStreamSrc, streamResetSrc},
			"A missing segment of a completed transcode and of a closed stream answers 404 without a stream_reset and without affecting the remaining segments.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "reset-silent-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "reset-silent")
				eventFirst[streamOpenDoc](t, a.owner1, a.marks[0], "stream_open", 15*time.Second)
				a.reset()
				requireStatus(t, getHLS(t, e, doc.ID, "output-11.ts", nil), http.StatusNotFound, "segment beyond a completed transcode")
				a.requireNobody(t, "stream_reset", 500*time.Millisecond)
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusOK, "existing segment after the miss")
				closeQuietly(t, e, user.Token, doc.ID)
				a.reset()
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), http.StatusNotFound, "segment after close")
				a.requireNobody(t, "stream_reset", 500*time.Millisecond)
			}),

		scenario("socket-outgoing-stream-reset", "INV-SOCKET-OUTGOING-STREAM-RESET-SILENT-001", "edge", "repeat", "stream-reset-repeat-is-silent",
			[]string{hlsStreamSrc, streamResetSrc},
			"Requesting the same missing segment of a completed transcode twice answers 404 twice without a stream_reset and keeps the existing segments served.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "reset-repeat-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "reset-repeat")
				eventFirst[streamOpenDoc](t, a.owner1, a.marks[0], "stream_open", 15*time.Second)
				a.reset()
				for i := 0; i < 2; i++ {
					requireStatus(t, getHLS(t, e, doc.ID, "output-11.ts", nil), http.StatusNotFound, "repeated distant segment")
				}
				a.requireNobody(t, "stream_reset", 500*time.Millisecond)
				requireStatus(t, getHLS(t, e, doc.ID, "output-10.ts", nil), http.StatusOK, "last produced segment")
				closeQuietly(t, e, user.Token, doc.ID)
			}),
	}
}
