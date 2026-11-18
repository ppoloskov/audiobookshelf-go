package playback_sessions

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// requireProgressRow compares a progress document (event payload or API
// answer) with the stored mediaProgresses row.
func requireProgressRow(t *testing.T, doc progressDoc, row map[string]any) {
	t.Helper()
	if doc.ID != row["id"] || doc.UserID != row["userId"] || doc.MediaItemID != row["mediaItemId"] || doc.MediaItemType != row["mediaItemType"] {
		t.Fatalf("progress ids differ: %+v vs %v", doc, row)
	}
	if !near(doc.Duration, number(t, row["duration"])) || !near(doc.CurrentTime, number(t, row["currentTime"])) || !near(doc.Progress, progressFraction(t, row)) {
		t.Fatalf("progress numbers differ: %+v vs %v", doc, row)
	}
	if doc.IsFinished != (row["isFinished"].(int64) == 1) || doc.LastUpdate != dbTime(t, row["updatedAt"]) || doc.StartedAt != dbTime(t, row["createdAt"]) {
		t.Fatalf("progress flags or times differ: %+v vs %v", doc, row)
	}
	if row["finishedAt"] == nil {
		if doc.FinishedAt != nil {
			t.Fatalf("finishedAt must be null: %+v", doc)
		}
	} else if doc.FinishedAt == nil || int64(*doc.FinishedAt) != dbTime(t, row["finishedAt"]) {
		t.Fatalf("finishedAt differs: %+v vs %v", doc, row)
	}
}

// syncAndRead syncs a session and returns the stored progress row for its item.
func syncAndRead(t *testing.T, e *harness.Env, who actor, session sessionDoc, body map[string]any) map[string]any {
	t.Helper()
	syncSession(t, e, who.Token, session.ID, body)
	mediaID := saga1BookID
	if session.EpisodeID != nil {
		mediaID = *session.EpisodeID
	} else if session.BookID != nil {
		mediaID = *session.BookID
	}
	row := progressRow(t, e, who.ID, mediaID)
	if row == nil {
		t.Fatalf("no progress row for %s after sync", mediaID)
	}
	return row
}

// requireSessionRow compares the stored row of a session with expectations.
func requireSessionRow(t *testing.T, row map[string]any, doc sessionDoc, listening, currentTime float64) {
	t.Helper()
	if row["userId"] != doc.UserID || row["libraryId"] != doc.LibraryID || row["displayTitle"] != doc.DisplayTitle || row["displayAuthor"] != doc.DisplayAuthor {
		t.Fatalf("stored session identity: %v", row)
	}
	mediaID, mediaType := "", "book"
	if doc.EpisodeID != nil {
		mediaID, mediaType = *doc.EpisodeID, "podcastEpisode"
	} else if doc.BookID != nil {
		mediaID = *doc.BookID
	}
	if row["mediaItemId"] != mediaID || row["mediaItemType"] != mediaType || row["serverVersion"] != serverVersion {
		t.Fatalf("stored session media: %v", row)
	}
	if doc.MediaPlayer == nil || row["mediaPlayer"] != *doc.MediaPlayer || number(t, row["playMethod"]) != float64(*doc.PlayMethod) || !near(number(t, row["duration"]), *doc.Duration) {
		t.Fatalf("stored session playback: %v", row)
	}
	if doc.DeviceInfo["id"] != row["deviceId"] || row["date"] != *doc.Date || row["dayOfWeek"] != *doc.DayOfWeek {
		t.Fatalf("stored session device or date: %v", row)
	}
	if !near(number(t, row["timeListening"]), listening) || !near(number(t, row["currentTime"]), currentTime) || !near(number(t, row["startTime"]), doc.StartTime) {
		t.Fatalf("stored session times: %v, want listening %v current %v", row, listening, currentTime)
	}
	if dbTime(t, row["createdAt"]) != doc.StartedAt {
		t.Fatalf("createdAt %v must be startedAt %d", row["createdAt"], doc.StartedAt)
	}
	if doc.LibraryItemID == nil || row["extraData"] != `{"libraryItemId":"`+*doc.LibraryItemID+`"}` {
		t.Fatalf("stored extraData: %v", row["extraData"])
	}
	if row["coverPath"] == nil || doc.CoverPath == nil || row["coverPath"] != *doc.CoverPath {
		t.Fatalf("stored coverPath: %v", row["coverPath"])
	}
}
