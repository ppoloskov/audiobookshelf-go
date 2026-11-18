package user_progress

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func finishScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r111", Invariant: "INV-R111-FINISH-001", Case: "normal", State: "changed", Assertion: "finished-toggle",
			Sources: []string{applySource, "client/components/modals/ItemMoreMenuModal.vue:327"},
			Claim:   "{isFinished:true} marks an unfinished row finished (finishedAt now, progress 1, position kept), repeating it changes nothing, and {isFinished:false} clears finishedAt, progress and currentTime and keeps duration; repeating that changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				start := seedBook(t, e, who)
				nextMillisecond()
				before := time.Now()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true})
				after := time.Now()
				done := progressRow(t, e, who.id, saga1Book)
				requireRow(t, done, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 1, finished: true, finishedAt: "now", before: before, after: after})
				requireMoved(t, start, done)
				got := apiProgress(t, e, who, e.IDs["saga1"], "")
				if !got.IsFinished || got.Progress != 1 || got.FinishedAt == nil || got.CurrentTime != 12 {
					t.Fatalf("finished projection wrong: %+v", got)
				}
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true})
				sameRow(t, "finish again", done, progressRow(t, e, who.id, saga1Book))
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": false})
				reset := progressRow(t, e, who.id, saga1Book)
				requireRow(t, reset, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60})
				requireMoved(t, done, reset)
				got = apiProgress(t, e, who, e.IDs["saga1"], "")
				if got.IsFinished || got.Progress != 0 || got.FinishedAt != nil || got.CurrentTime != 0 || got.Duration != 60 {
					t.Fatalf("unfinished projection wrong: %+v", got)
				}
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": false})
				sameRow(t, "unfinish again", reset, progressRow(t, e, who.id, saga1Book))
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-FINISH-001", Case: "edge", State: "changed", Assertion: "finished-at-from-client",
			Sources: []string{applySource, "go-backend/testmachine/contracts/clients/absorb.md"},
			Claim:   "isFinished true with finishedAt stores that date; on an already finished row a new finishedAt replaces it; isFinished false drops finishedAt and the sent currentTime and sets progress 0.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true, "finishedAt": 1700000000000})
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 1, finished: true, finishedAt: "2023-11-14 22:13:20.000 +00:00"})
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true, "finishedAt": 1710000000000})
				row = progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 1, finished: true, finishedAt: "2024-03-09 16:00:00.000 +00:00"})
				if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.FinishedAt == nil || int64(*got.FinishedAt) != 1710000000000 {
					t.Fatalf("finishedAt projection: %+v", got)
				}
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": false, "finishedAt": 1720000000000, "currentTime": 50})
				requireRow(t, progressRow(t, e, who.id, saga1Book), who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60})
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-FINISH-001", Case: "edge", State: "changed", Assertion: "moving-position-unfinishes",
			Sources: []string{applySource},
			Claim:   "A PATCH without isFinished that moves currentTime of a finished row clears isFinished and finishedAt; one that keeps currentTime leaves it finished.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true, "finishedAt": 1700000000000})
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 12, "duration": 60})
				requireRow(t, progressRow(t, e, who.id, saga1Book), who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 1, finished: true, finishedAt: "2023-11-14 22:13:20.000 +00:00"})
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 5, "duration": 60, "progress": 5.0 / 60})
				requireRow(t, progressRow(t, e, who.id, saga1Book), who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 5, progress: 5.0 / 60})
				got := apiProgress(t, e, who, e.IDs["saga1"], "")
				if got.IsFinished || got.FinishedAt != nil {
					t.Fatalf("projection must be unfinished: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-FINISH-001", Case: "edge", State: "changed", Assertion: "auto-finish-rules",
			Sources: []string{applySource},
			Claim:   "An update finishes the row when the time remaining is below markAsFinishedTimeRemaining (default 10 s, strict) or, when markAsFinishedPercentComplete > 0 is sent, when progress exceeds that percent (which then replaces the time rule); duration 0 never auto-finishes.",
			Run: func(t *testing.T, e *harness.Env) {
				cases := []struct {
					name     string
					patch    map[string]any
					finished bool
				}{
					{"55 of 60, default 10 s", map[string]any{"currentTime": 55}, true},
					{"exactly 10 s remaining", map[string]any{"currentTime": 50}, false},
					{"46 of 60, 15 s rule", map[string]any{"currentTime": 46, "markAsFinishedTimeRemaining": 15}, true},
					{"55 of 60, 3 s rule", map[string]any{"currentTime": 55, "markAsFinishedTimeRemaining": 3}, false},
					{"59 of 60, 0 s rule", map[string]any{"currentTime": 59, "markAsFinishedTimeRemaining": 0}, false},
					{"40 of 60, 50 percent", map[string]any{"currentTime": 40, "markAsFinishedPercentComplete": 50}, true},
					{"exactly 50 percent", map[string]any{"currentTime": 30, "markAsFinishedPercentComplete": 50}, false},
					{"57 of 60, 99 percent replaces the time rule", map[string]any{"currentTime": 57, "markAsFinishedPercentComplete": 99}, false},
					{"55 of 60, percent 0 falls back to time", map[string]any{"currentTime": 55, "markAsFinishedPercentComplete": 0}, true},
				}
				for _, c := range cases {
					who := newActor(t, e)
					seedBook(t, e, who)
					before := time.Now()
					patchOK(t, e, who, e.IDs["saga1"], "", c.patch)
					row := progressRow(t, e, who.id, saga1Book)
					if (num(t, row, "isFinished") == 1) != c.finished {
						t.Fatalf("%s: isFinished = %v, want %v", c.name, row["isFinished"], c.finished)
					}
					if c.finished {
						requireBetween(t, c.name+" finishedAt", stamp(t, row, "finishedAt"), before, time.Now())
						if _, progress := extra(t, row); progress != 1 {
							t.Fatalf("%s: progress = %v, want 1", c.name, progress)
						}
					} else if _, has := text(row, "finishedAt"); has {
						t.Fatalf("%s: finishedAt must be NULL", c.name)
					}
					if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.IsFinished != c.finished {
						t.Fatalf("%s: projection isFinished = %v", c.name, got.IsFinished)
					}
				}
				who := newActor(t, e)
				patchOK(t, e, who, e.IDs["ebook"], "", map[string]any{"ebookProgress": 0.1})
				patchOK(t, e, who, e.IDs["ebook"], "", map[string]any{"currentTime": 100, "ebookProgress": 0.2})
				if num(t, progressRow(t, e, who.id, ebookBook), "isFinished") != 0 {
					t.Fatal("a row without duration must not auto-finish")
				}
			},
		},
	}
}
