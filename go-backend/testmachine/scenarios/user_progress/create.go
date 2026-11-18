package user_progress

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "normal", State: "initial", Assertion: "create-book-progress",
			Sources: []string{"server/routers/ApiRouter.js:185", patchSource, createSource},
			Claim:   "PATCH /api/me/progress/:libraryItemId creates the caller's progress row for a book with the sent currentTime, duration and progress, createdAt equal to updatedAt, and the API projection mirrors the row.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				before := time.Now()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 12, "duration": 60, "progress": 0.2})
				after := time.Now()
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 0.2})
				created, updated := stamp(t, row, "createdAt"), stamp(t, row, "updatedAt")
				if !created.Equal(updated) {
					t.Fatalf("a new row has createdAt == updatedAt, got %v and %v", row["createdAt"], row["updatedAt"])
				}
				requireBetween(t, "createdAt", created, before, after)
				requireAPIMatchesRow(t, apiProgress(t, e, who, e.IDs["saga1"], ""), row, who.id, e.IDs["saga1"], "")
				if n := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", who.id); n != 1 {
					t.Fatalf("want one row, got %d", n)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "initial", Assertion: "create-with-every-field",
			Sources: []string{patchSource, createSource},
			Claim:   "A new progress row stores hideFromContinueListening, ebookLocation, ebookProgress and a client createdAt (startedAt), and ignores finishedAt when isFinished is not true.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{
					"currentTime": 5, "duration": 60, "progress": 0.1, "isFinished": false, "hideFromContinueListening": true,
					"ebookLocation": "epubcfi(/6/4!/4/2)", "ebookProgress": 0.5, "finishedAt": 1700000000000, "createdAt": "2023-01-02T03:04:05.000Z",
				})
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 5, progress: 0.1, hide: true, ebookLocation: "epubcfi(/6/4!/4/2)", ebookProgress: 0.5})
				if got, _ := text(row, "createdAt"); got != "2023-01-02 03:04:05.000 +00:00" {
					t.Fatalf("createdAt = %q, want the client value", got)
				}
				got := apiProgress(t, e, who, e.IDs["saga1"], "")
				requireAPIMatchesRow(t, got, row, who.id, e.IDs["saga1"], "")
				if got.StartedAt != 1672628645000 || got.EbookLocation == nil || *got.EbookLocation != "epubcfi(/6/4!/4/2)" || !got.HideFromContinueListening {
					t.Fatalf("projection wrong: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-FINISH-001", Case: "edge", State: "initial", Assertion: "create-finished",
			Sources: []string{patchSource, "server/models/User.js:818-823"},
			Claim:   "A new row with isFinished true stores progress 1 and finishedAt (the client's finishedAt, else now) whatever progress was sent.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				before := time.Now()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"isFinished": true, "finishedAt": 1700000000000, "currentTime": 3, "duration": 60, "progress": 0.05})
				patchOK(t, e, who, e.IDs["novel"], "", map[string]any{"isFinished": true})
				after := time.Now()
				withDate := progressRow(t, e, who.id, saga1Book)
				requireRow(t, withDate, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 3, progress: 1, finished: true, finishedAt: "2023-11-14 22:13:20.000 +00:00"})
				requireRow(t, progressRow(t, e, who.id, novelBook), who.id, novelBook, wantRow{item: e.IDs["novel"], mediaType: "book", progress: 1, finished: true, finishedAt: "now", before: before, after: after})
				got := apiProgress(t, e, who, e.IDs["saga1"], "")
				if !got.IsFinished || got.FinishedAt == nil || int64(*got.FinishedAt) != 1700000000000 || got.Progress != 1 {
					t.Fatalf("finished projection wrong: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "normal", State: "initial", Assertion: "create-episode-progress",
			Sources: []string{"server/routers/ApiRouter.js:185", createSource, "server/models/MediaProgress.js:155-176"},
			Claim:   "PATCH /api/me/progress/:libraryItemId/:episodeId creates a podcastEpisode row keyed by the episode with the podcast id and the item id in extraData, and the projection carries episodeId.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				patchOK(t, e, who, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"currentTime": 4, "duration": 24, "progress": 4.0 / 24})
				row := progressRow(t, e, who.id, e.IDs["ep1"])
				requireRow(t, row, who.id, e.IDs["ep1"], wantRow{item: e.IDs["podcast"], mediaType: "podcastEpisode", podcastID: podcastRow, duration: 24, current: 4, progress: 4.0 / 24})
				requireAPIMatchesRow(t, apiProgress(t, e, who, e.IDs["podcast"], e.IDs["ep1"]), row, who.id, e.IDs["podcast"], e.IDs["ep1"])
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "initial", Assertion: "create-ebook-position",
			Sources: []string{patchSource, createSource},
			Claim:   "A client that sends only ebookLocation and ebookProgress creates a row with duration 0, the location and progress, and never auto-finishes it.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				patchOK(t, e, who, e.IDs["ebook"], "", map[string]any{"ebookLocation": "epubcfi(/6/2!/4/2/1:0)", "ebookProgress": 0.4})
				row := progressRow(t, e, who.id, ebookBook)
				requireRow(t, row, who.id, ebookBook, wantRow{item: e.IDs["ebook"], mediaType: "book", ebookLocation: "epubcfi(/6/2!/4/2/1:0)", ebookProgress: 0.4})
				patchOK(t, e, who, e.IDs["ebook"], "", map[string]any{"ebookLocation": "epubcfi(/6/8)", "ebookProgress": 0.9, "currentTime": 100})
				requireRow(t, progressRow(t, e, who.id, ebookBook), who.id, ebookBook, wantRow{item: e.IDs["ebook"], mediaType: "book", current: 100, ebookLocation: "epubcfi(/6/8)", ebookProgress: 0.9})
				got := apiProgress(t, e, who, e.IDs["ebook"], "")
				if got.EbookLocation == nil || *got.EbookLocation != "epubcfi(/6/8)" || got.EbookProgress != 0.9 || got.IsFinished {
					t.Fatalf("ebook projection wrong: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-LASTUPDATE-001", Case: "edge", State: "initial", Assertion: "create-ignores-last-update",
			Sources: []string{createSource, applySource},
			Claim:   "lastUpdate is applied only when an existing row is updated: a request that creates the row stores updatedAt as now.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				before := time.Now()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 8, "duration": 60, "progress": 8.0 / 60, "lastUpdate": 1700000000000})
				after := time.Now()
				row := progressRow(t, e, who.id, saga1Book)
				requireBetween(t, "updatedAt", stamp(t, row, "updatedAt"), before, after)
			},
		},
	}
}
