package playback_streams

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// progressScenarios cover stream_progress: the initial zero payload, the
// per-check payload with the percent, created-segment chunks and total segment
// count, its repetition while the transcode runs, and its absence after the
// stream finished or failed.
func progressScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS", "normal", "initial", "stream-progress-payload",
			[]string{streamStartSrc, streamCheckSrc, webPlayerSrc},
			"A transcoded book sends stream_progress first as {percent \"0%\", chunks [], numSegments 0} and, on the first segment check with segments, as {percent \"100.00%\", chunks [\"0-10\"], numSegments 11} on both owner sockets, and on neither the admin nor another user's socket.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: slowFFmpegEnv(t)})
				user := createUser(t, own, "progress-initial-user")
				a := watch(t, own, user, "finisher")
				doc := startStream(t, own, user, own.IDs["saga1"], "", "progress-initial")

				zero := eventFirst[progressDoc](t, a.owner1, a.marks[0], "stream_progress", 10*time.Second)
				requireEqual(t, "initial progress", zero, progressDoc{Stream: doc.ID, Percent: "0%", Chunks: []any{}, NumSegments: 0})
				zero2 := eventFirst[progressDoc](t, a.owner2, a.marks[1], "stream_progress", 10*time.Second)
				requireEqual(t, "initial progress on owner 2", zero2, zero)

				var real, real2 []progressDoc
				harness.Eventually(t, 15*time.Second, "a segment-check progress payload on both owners", func() bool {
					real = nil
					real2 = nil
					for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
						if event.NumSegments != 0 {
							real = append(real, event)
						}
					}
					for _, event := range eventsNamed[progressDoc](t, a.owner2, a.marks[1], "stream_progress") {
						if event.NumSegments != 0 {
							real2 = append(real2, event)
						}
					}
					return len(real) > 0 && len(real2) > 0
				})
				want := progressDoc{Stream: doc.ID, Percent: "100.00%", Chunks: []any{"0-10"}, NumSegments: saga1Segments}
				requireEqual(t, "segment progress", real[0], want)
				requireEqual(t, "segment progress on owner 2", real2[0], want)
				eventFirst[streamOpenDoc](t, a.owner2, a.marks[1], "stream_open", 10*time.Second)
				a.settleAll(t)
				if got := eventsNamed[streamOpenDoc](t, a.admin, a.marks[2], "stream_open"); len(got) != 0 {
					t.Fatalf("admin socket got stream_open: %+v", got)
				}
				if got := eventsNamed[progressDoc](t, a.admin, a.marks[2], "stream_progress"); len(got) != 0 {
					t.Fatalf("admin socket got stream_progress: %+v", got)
				}
				if got := eventsNamed[progressDoc](t, a.other, a.marks[3], "stream_progress"); len(got) != 0 {
					t.Fatalf("another user's socket got stream_progress: %+v", got)
				}
				closeQuietly(t, own, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS-REPEAT-001", "edge", "repeat", "stream-progress-repeats-identical",
			[]string{streamCheckSrc},
			"While the transcode runs the segment check repeats the identical stream_progress payload, so a client that misses one update catches up on the next.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: slowFFmpegEnv(t)})
				user := createUser(t, own, "progress-repeat-user")
				a := watch(t, own, user, "finisher")
				doc := startStream(t, own, user, own.IDs["saga1"], "", "progress-repeat")

				var real []progressDoc
				harness.Eventually(t, 15*time.Second, "two segment-check progress payloads", func() bool {
					real = real[:0]
					for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
						if event.NumSegments != 0 {
							real = append(real, event)
						}
					}
					return len(real) >= 2
				})
				for _, event := range real {
					requireEqual(t, "repeated progress", event, real[0])
				}
				var previous float64
				for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
					current := percentValue(t, event.Percent)
					if current < previous {
						t.Fatalf("progress went backwards: %v after %v", current, previous)
					}
					previous = current
				}
				closeQuietly(t, own, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS", "normal", "changed", "stream-progress-stops-after-completion",
			[]string{streamStartSrc, streamCheckSrc},
			"A transcode that completes before the first segment check emits only the initial zero progress and no further progress after stream_open.",
			func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "progress-changed-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, e.IDs["saga1"], "", "progress-changed")
				openIndex := waitEventIndex(t, a.owner1, a.marks[0], "stream_open", 15*time.Second)
				a.settleAll(t)
				after := a.owner1.Events()[openIndex+1:]
				for _, event := range after {
					if event.Name == "stream_progress" {
						t.Fatalf("progress after completion: %s", event.Args)
					}
				}
				progress := eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress")
				if len(progress) != 1 || progress[0].Percent != "0%" || progress[0].NumSegments != 0 {
					t.Fatalf("a fast transcode must emit only the initial progress, got %+v", progress)
				}
				closeQuietly(t, e, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS", "edge", "initial", "stream-progress-single-segment-chunk",
			[]string{streamCheckSrc, webPlayerSrc},
			"A one-segment item reports chunks as the single segment name (not a range), percent 100.00% and numSegments 1.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: slowFFmpegEnv(t)})
				user := createUser(t, own, "progress-single-user")
				a := watch(t, own, user, "finisher")
				doc := startStream(t, own, user, own.IDs["disposable"], "", "progress-single")
				var payload progressDoc
				harness.Eventually(t, 20*time.Second, "a segment-check progress payload", func() bool {
					for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
						if event.NumSegments != 0 {
							payload = event
							return true
						}
					}
					return false
				})
				requireEqual(t, "single-segment progress", payload, progressDoc{Stream: doc.ID, Percent: "100.00%", Chunks: []any{0}, NumSegments: 1})
				closeQuietly(t, own, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS", "edge", "changed", "stream-progress-chunk-gap",
			[]string{streamCheckSrc, webPlayerSrc},
			"When one produced segment file is missing, the chunk list compresses the created segments into two ranges, so the client sees the gap instead of one continuous run.",
			func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: slowFFmpegEnv(t)})
				user := createUser(t, own, "progress-gap-user")
				a := watch(t, own, user, "finisher")
				doc := startStream(t, own, user, own.IDs["saga1"], "", "progress-gap")
				waitSegment(t, own, doc.ID, saga1Segments-1)
				missing := filepath.Join(own.Install, "metadata", "streams", doc.ID, "output-5.ts")
				if err := os.Remove(missing); err != nil {
					t.Fatalf("remove a produced segment: %v", err)
				}
				var payload progressDoc
				harness.Eventually(t, 20*time.Second, "a progress payload with two chunks", func() bool {
					for _, event := range eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress") {
						if len(event.Chunks) == 2 {
							payload = event
							return true
						}
					}
					return false
				})
				requireEqual(t, "gap progress", payload, progressDoc{Stream: doc.ID, Percent: "90.91%", Chunks: []any{"0-4", "6-10"}, NumSegments: saga1Segments})
				closeQuietly(t, own, user.Token, doc.ID)
			}),

		scenario("socket-outgoing-stream-progress", "INV-SOCKET-OUTGOING-STREAM-PROGRESS-FAILURE-001", "error", "failure", "stream-progress-stops-after-error",
			[]string{streamStartSrc, streamCloseSrc},
			"A stream that fails on its input sends the initial zero progress, then stream_error, and no segment progress afterwards.",
			func(t *testing.T, e *harness.Env) {
				item := brokenItem(t, e, "Broken Progress")
				user := createUser(t, e, "progress-failure-user")
				a := watch(t, e, user, "finisher")
				doc := startStream(t, e, user, item, "", "progress-failure")
				errorIndex := waitEventIndex(t, a.owner1, a.marks[0], "stream_error", 15*time.Second)
				a.settleAll(t)
				for _, event := range a.owner1.Events()[errorIndex+1:] {
					if event.Name == "stream_progress" {
						t.Fatalf("progress after a stream error: %s", event.Args)
					}
				}
				progress := eventsNamed[progressDoc](t, a.owner1, a.marks[0], "stream_progress")
				if len(progress) != 1 || progress[0].NumSegments != 0 || progress[0].Percent != "0%" {
					t.Fatalf("a failed stream must emit only the initial progress, got %+v", progress)
				}
				closeQuietly(t, e, user.Token, doc.ID)
			}),
	}
}

// waitEventIndex waits for one event and returns its index in the socket's log.
func waitEventIndex(t *testing.T, s *harness.Socket, mark int, name string, timeout time.Duration) int {
	t.Helper()
	s.Wait(t, mark, name, timeout, nil)
	for i := mark; i < len(s.Events()); i++ {
		if s.Events()[i].Name == name {
			return i
		}
	}
	t.Fatalf("event %s not found in the socket log", name)
	return -1
}

// percentValue parses the "12.34%" percent text of a progress payload.
func percentValue(t *testing.T, percent string) float64 {
	t.Helper()
	if len(percent) < 2 || percent[len(percent)-1] != '%' {
		t.Fatalf("percent %q is not a percentage", percent)
	}
	var value float64
	if _, err := fmt.Sscanf(percent, "%f%%", &value); err != nil {
		t.Fatalf("percent %q: %v", percent, err)
	}
	return value
}
