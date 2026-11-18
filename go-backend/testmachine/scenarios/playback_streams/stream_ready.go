package playback_streams

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// readyScenarios cover stream_ready: a normal completion does not emit it, a
// reset whose retranscode finishes before the delayed segment check does, and a
// failed stream never does.
func readyScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-ready", "INV-SOCKET-OUTGOING-STREAM-READY", "normal", "initial", "stream-ready-not-emitted-on-normal-completion",
			[]string{streamStartSrc, streamCheckSrc, webPlayerSrc},
			"A normal completed transcode emits stream_open but no stream_ready on any socket; a client must not wait for it.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ready-normal-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "ready-normal")
				eventFirst[streamOpenDoc](t, a.owner1, a.marks[0], "stream_open", 15*time.Second)
				a.settleAll(t)
				a.requireNobody(t, "stream_ready", 1500*time.Millisecond)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-ready", "INV-SOCKET-OUTGOING-STREAM-READY-RESET-001", "edge", "changed", "stream-ready-after-fast-reset",
			[]string{streamResetSrc, streamStartSrc, streamCheckSrc, webSocketSrc},
			"After a reset whose retranscode finishes before the delayed segment check starts, stream_ready is emitted with a single null argument on both sockets of the owner and on no other socket.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ready-reset-user")
				setProgress(t, e, user, e.IDs["novel"], 44, novelDuration)
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["novel"], "", "ready-reset")
				harness.Eventually(t, 30*time.Second, "the first produced segment", func() bool {
					return getHLS(t, e, doc.ID, "output-2.ts", nil).Status == 200
				})
				a.reset()
				requireStatus(t, getHLS(t, e, doc.ID, "output-0.ts", nil), 404, "segment before the start offset")
				requireReady(t, a)
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-ready", "INV-SOCKET-OUTGOING-STREAM-READY-RESET-001", "normal", "repeat", "stream-ready-once-per-reset",
			[]string{streamResetSrc, streamStartSrc, streamCheckSrc},
			"Two resets of two sessions emit stream_ready once for each, never twice for the same stream.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ready-repeat-user")
				setProgress(t, e, user, e.IDs["novel"], 44, novelDuration)
				a := watch(t, e, user, "finisher")
				first := startStream(t, e, user, e.IDs["novel"], "", "ready-repeat-1")
				harness.Eventually(t, 30*time.Second, "the first segment of the first session", func() bool {
					return getHLS(t, e, first.ID, "output-2.ts", nil).Status == 200
				})
				a.reset()
				requireStatus(t, getHLS(t, e, first.ID, "output-0.ts", nil), 404, "first backward request")
				requireReady(t, a)
				closeQuietly(t, e, user.Token, first.ID)

				second := startStream(t, e, user, e.IDs["novel"], "", "ready-repeat-2")
				harness.Eventually(t, 30*time.Second, "the first segment of the second session", func() bool {
					return getHLS(t, e, second.ID, "output-2.ts", nil).Status == 200
				})
				a.reset()
				requireStatus(t, getHLS(t, e, second.ID, "output-0.ts", nil), 404, "second backward request")
				requireReady(t, a)
				closeQuietly(t, e, user.Token, second.ID)
			}),

		scenario("socket-outgoing-stream-ready", "INV-SOCKET-OUTGOING-STREAM-READY", "error", "failure", "stream-ready-not-emitted-for-failed-stream",
			[]string{streamStartSrc, streamCloseSrc},
			"A stream that fails on its input emits stream_error and no stream_ready.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Ready")
				user := createUser(t, e, "ready-failure-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, item, "", "ready-failure")
				eventFirst[errorDoc](t, a.owner1, a.marks[0], "stream_error", 15*time.Second)
				a.settleAll(t)
				a.requireNobody(t, "stream_ready", 500*time.Millisecond)
				closeQuietly(t, e, user.Token, doc.ID)
			}),
	}
}

// requireReady requires stream_ready with its single null argument on both owner
// sockets, once each, and on no admin or other-user socket.
func requireReady(t *testing.T, a *audience) {
	t.Helper()
	for i, s := range []*harness.Socket{a.owner1, a.owner2} {
		event := s.Wait(t, a.marks[i], "stream_ready", 15*time.Second, nil)
		if len(event.Args) != 1 || string(event.Args[0]) != "null" {
			t.Fatalf("stream_ready arguments %v, want one null", event.Args)
		}
		if got := eventsNamed[any](t, s, a.marks[i], "stream_ready"); len(got) != 1 {
			t.Fatalf("stream_ready reached an owner socket %d times, want 1", len(got))
		}
	}
	a.settleAll(t)
	if got := eventsNamed[any](t, a.admin, a.marks[2], "stream_ready"); len(got) != 0 {
		t.Fatalf("admin socket got stream_ready: %v", got)
	}
	if got := eventsNamed[any](t, a.other, a.marks[3], "stream_ready"); len(got) != 0 {
		t.Fatalf("another user's socket got stream_ready: %v", got)
	}
}
