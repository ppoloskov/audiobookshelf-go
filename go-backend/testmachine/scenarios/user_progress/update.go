package user_progress

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// seedBook creates a progress row on saga1 (current 12 of 60) and returns its row.
func seedBook(t *testing.T, e *harness.Env, who actor) map[string]any {
	t.Helper()
	patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 12, "duration": 60, "progress": 0.2})
	return progressRow(t, e, who.id, saga1Book)
}

// requireMoved requires updatedAt strictly after the previous row's.
func requireMoved(t *testing.T, previous, next map[string]any) {
	t.Helper()
	if !stamp(t, next, "updatedAt").After(stamp(t, previous, "updatedAt")) {
		t.Fatalf("updatedAt must move forward: %v then %v", previous["updatedAt"], next["updatedAt"])
	}
}

// sameRow compares two row snapshots column by column.
func sameRow(t *testing.T, what string, a, b map[string]any) {
	t.Helper()
	for column, value := range a {
		if b[column] != value {
			t.Fatalf("%s: column %s changed from %#v to %#v", what, column, value, b[column])
		}
	}
}

func updateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "normal", State: "changed", Assertion: "update-moves-position",
			Sources: []string{patchSource, applySource, "server/models/User.js:796-799"},
			Claim:   "A second PATCH updates the same row: currentTime and progress change, id and createdAt stay, updatedAt moves strictly forward and the projection shows the new position.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := seedBook(t, e, who)
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 30, "duration": 60, "progress": 0.5})
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 30, progress: 0.5})
				requireMoved(t, first, row)
				if row["id"] != first["id"] || row["createdAt"] != first["createdAt"] {
					t.Fatalf("id and createdAt must stay: %v vs %v", first, row)
				}
				requireAPIMatchesRow(t, apiProgress(t, e, who, e.IDs["saga1"], ""), row, who.id, e.IDs["saga1"], "")
				if n := len(progressRows(t, e, who.id)); n != 1 {
					t.Fatalf("want one row, got %d", n)
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "repeat", Assertion: "update-same-payload-changes-nothing",
			Sources: []string{applySource},
			Claim:   "Repeating a PATCH with identical values leaves every column of the row, including updatedAt, unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				body := map[string]any{"currentTime": 30, "duration": 60, "progress": 0.5}
				patchOK(t, e, who, e.IDs["saga1"], "", body)
				first := progressRow(t, e, who.id, saga1Book)
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", body)
				sameRow(t, "repeat", first, progressRow(t, e, who.id, saga1Book))
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "changed", Assertion: "update-progress-clamped",
			Sources: []string{applySource},
			Claim:   "A PATCH that carries only progress stores it clamped to 0..1 in the row's extraData and leaves currentTime and duration alone.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				previous := seedBook(t, e, who)
				for _, c := range []struct{ sent, stored float64 }{{2, 1}, {-1, 0}, {0.75, 0.75}} {
					nextMillisecond()
					patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"progress": c.sent})
					row := progressRow(t, e, who.id, saga1Book)
					requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: c.stored})
					requireMoved(t, previous, row)
					previous = row
					if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.Progress != c.stored {
						t.Fatalf("progress %v sent: projection shows %v, want %v", c.sent, got.Progress, c.stored)
					}
				}
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "changed", Assertion: "update-partial-ebook-fields",
			Sources: []string{applySource},
			Claim:   "A PATCH with only ebookLocation and ebookProgress changes those columns and keeps the audio position, duration and finished state.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := seedBook(t, e, who)
				nextMillisecond()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"ebookLocation": "epubcfi(/6/10)", "ebookProgress": 0.33})
				row := progressRow(t, e, who.id, saga1Book)
				requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: 12, progress: 0.2, ebookLocation: "epubcfi(/6/10)", ebookProgress: 0.33})
				requireMoved(t, first, row)
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-SAMPLED", Case: "edge", State: "changed", Assertion: "hide-from-continue-listening-resets-on-progress",
			Sources: []string{applySource},
			Claim:   "hideFromContinueListening is stored when sent, survives a PATCH that does not move currentTime, is reset by a PATCH that moves currentTime without it, and is kept when the same PATCH sends it again.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				hidden := func(want bool, step string, current float64, progress float64) {
					row := progressRow(t, e, who.id, saga1Book)
					requireRow(t, row, who.id, saga1Book, wantRow{item: e.IDs["saga1"], mediaType: "book", duration: 60, current: current, progress: progress, hide: want})
					if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.HideFromContinueListening != want {
						t.Fatalf("%s: projection hide = %v, want %v", step, got.HideFromContinueListening, want)
					}
				}
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"hideFromContinueListening": true})
				hidden(true, "set", 12, 0.2)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 12, "ebookProgress": 0})
				hidden(true, "same position", 12, 0.2)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 20, "progress": 20.0 / 60})
				hidden(false, "moved without the flag", 20, 20.0/60)
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"hideFromContinueListening": true})
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 25, "progress": 25.0 / 60, "hideFromContinueListening": true})
				hidden(true, "moved with the flag", 25, 25.0/60)
			},
		},
		{
			Requirement: "route-r111", Invariant: "INV-R111-LASTUPDATE-001", Case: "edge", State: "changed", Assertion: "update-applies-last-update",
			Sources: []string{applySource, "go-backend/testmachine/contracts/clients/absorb.md"},
			Claim:   "On an existing row lastUpdate (number or ISO string) becomes updatedAt exactly and the projection's lastUpdate; an unparsable lastUpdate is ignored and updatedAt is the time of the write.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				for _, c := range []struct {
					sent   any
					stored string
					millis int64
				}{
					{1700000000000, "2023-11-14 22:13:20.000 +00:00", 1700000000000},
					{"2024-02-03T04:05:06.789Z", "2024-02-03 04:05:06.789 +00:00", 1706933106789},
				} {
					patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 40, "lastUpdate": c.sent})
					patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 41, "lastUpdate": c.sent})
					row := progressRow(t, e, who.id, saga1Book)
					if got, _ := text(row, "updatedAt"); got != c.stored {
						t.Fatalf("lastUpdate %v: updatedAt = %q, want %q", c.sent, got, c.stored)
					}
					if got := apiProgress(t, e, who, e.IDs["saga1"], ""); got.LastUpdate != c.millis {
						t.Fatalf("lastUpdate %v: projection lastUpdate = %d, want %d", c.sent, got.LastUpdate, c.millis)
					}
				}
				before := time.Now()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 45, "lastUpdate": "not a date"})
				requireBetween(t, "updatedAt", stamp(t, progressRow(t, e, who.id, saga1Book), "updatedAt"), before, time.Now())
			},
		},
	}
}
