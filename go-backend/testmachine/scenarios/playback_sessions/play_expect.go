package playback_sessions

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// saga1Tracks are the three direct-play tracks of "The Beginning" (inode,
// file name, offset); every track lasts 20.062041 s.
var saga1Tracks = []struct {
	Ino, File string
	Offset    float64
}{
	{"828693", "01 - Part 1.mp3", 0},
	{"828709", "02 - Part 2.mp3", 20.062041},
	{"828727", "03 - Part 3.mp3", 40.124082},
}

const saga1Folder = "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning"

// requireSaga1Session compares a play or read response for "The Beginning"
// with every deterministic field of the fixture.
func requireSaga1Session(t *testing.T, e *harness.Env, doc sessionDoc, userID string, startTime float64, method int, player string) {
	t.Helper()
	if doc.UserID != userID || doc.LibraryID != e.IDs["library"] || doc.LibraryItemID == nil || *doc.LibraryItemID != e.IDs["saga1"] {
		t.Fatalf("session ids wrong: %+v", doc)
	}
	if doc.BookID == nil || *doc.BookID != saga1BookID || doc.EpisodeID != nil || doc.MediaType != "book" {
		t.Fatalf("session media wrong: book %v episode %v type %s", doc.BookID, doc.EpisodeID, doc.MediaType)
	}
	requireEqual(t, "mediaMetadata", doc.MediaMetadata, map[string]any{
		"title": "The Beginning", "subtitle": nil,
		"authors":   []any{map[string]any{"id": "75e57a0b-a39d-4073-8501-e3b745ebd79d", "name": "Terry Fixture"}},
		"narrators": []any{}, "series": []any{map[string]any{"id": "e987a322-9111-4f79-8cb9-c25ee714e0d6", "name": "Long Saga", "sequence": "1"}},
		"genres": []any{}, "publishedYear": nil, "publishedDate": nil, "publisher": nil, "description": nil,
		"isbn": nil, "asin": nil, "language": nil, "explicit": false, "abridged": false,
	})
	requireEqual(t, "chapters", doc.Chapters, []chapterDoc{
		{0, 0, 20.062041, "Part 1"}, {1, 20.062041, 40.124082, "Part 2"}, {2, 40.124082, 60.186123, "Part 3"},
	})
	if doc.DisplayTitle != "The Beginning" || doc.DisplayAuthor != "Terry Fixture" {
		t.Fatalf("display fields: %q by %q", doc.DisplayTitle, doc.DisplayAuthor)
	}
	if doc.CoverPath == nil || *doc.CoverPath != mediaPath(e, saga1Folder+"/cover.jpg") {
		t.Fatalf("coverPath: %v", doc.CoverPath)
	}
	if doc.Duration == nil || !near(*doc.Duration, saga1Duration) || doc.PlayMethod == nil || *doc.PlayMethod != method {
		t.Fatalf("duration %v playMethod %v, want %v and %d", doc.Duration, doc.PlayMethod, saga1Duration, method)
	}
	if doc.MediaPlayer == nil || *doc.MediaPlayer != player {
		t.Fatalf("mediaPlayer %v, want %q", doc.MediaPlayer, player)
	}
	if doc.ServerVersion == nil || *doc.ServerVersion != serverVersion {
		t.Fatalf("serverVersion %v", doc.ServerVersion)
	}
	requireNear(t, "startTime", doc.StartTime, startTime)
	if doc.LibraryItem == nil || doc.LibraryItem.ID != e.IDs["saga1"] || doc.LibraryItem.MediaType != "book" || doc.LibraryItem.LibraryID != e.IDs["library"] ||
		doc.LibraryItem.Media.ID != saga1BookID || doc.LibraryItem.Media.Metadata.Title != "The Beginning" || doc.LibraryItem.Media.NumTracks != 3 ||
		!near(doc.LibraryItem.Media.Duration, saga1Duration) {
		t.Fatalf("expanded libraryItem wrong: %+v", doc.LibraryItem)
	}
}

// requireNewSessionTimes checks the time fields of a session that was just
// started: no listening yet, current time at the start time, dates of the
// start moment (UTC), and a start no earlier than the request.
func requireNewSessionTimes(t *testing.T, doc sessionDoc, requestedAt time.Time) {
	t.Helper()
	if doc.TimeListening == nil || *doc.TimeListening != 0 {
		t.Fatalf("timeListening of a new session: %v", doc.TimeListening)
	}
	requireNear(t, "currentTime", doc.CurrentTime, doc.StartTime)
	if doc.StartedAt < requestedAt.UnixMilli()-1000 || doc.UpdatedAt != doc.StartedAt {
		t.Fatalf("startedAt %d updatedAt %d, request at %d", doc.StartedAt, doc.UpdatedAt, requestedAt.UnixMilli())
	}
	started := time.UnixMilli(doc.StartedAt).UTC()
	if doc.Date == nil || *doc.Date != started.Format("2006-01-02") || doc.DayOfWeek == nil || *doc.DayOfWeek != started.Format("Monday") {
		t.Fatalf("date %v dayOfWeek %v, want %s %s", doc.Date, doc.DayOfWeek, started.Format("2006-01-02"), started.Format("Monday"))
	}
}

// requireDirectTracks compares the three direct-play tracks of saga1.
func requireDirectTracks(t *testing.T, e *harness.Env, tracks []trackDoc) {
	t.Helper()
	if len(tracks) != len(saga1Tracks) {
		t.Fatalf("direct play must list %d tracks, got %d", len(saga1Tracks), len(tracks))
	}
	for i, want := range saga1Tracks {
		track := tracks[i]
		if track.Index != i+1 || track.Ino != want.Ino || !near(track.Duration, 20.062041) || !near(track.Offset, want.Offset) ||
			track.MimeType != "audio/mpeg" || track.Codec == nil || *track.Codec != "mp3" || track.Title != want.File ||
			track.URL != "/api/items/"+e.IDs["saga1"]+"/file/"+want.Ino {
			t.Fatalf("track %d wrong: %+v", i+1, track)
		}
		if track.Metadata == nil || track.Metadata.Filename != want.File || track.Metadata.RelPath != want.File ||
			track.Metadata.Path != mediaPath(e, saga1Folder+"/"+want.File) {
			t.Fatalf("track %d metadata wrong: %+v", i+1, track.Metadata)
		}
	}
}

// requireHLSTrack compares the single transcode track of a session.
func requireHLSTrack(t *testing.T, doc sessionDoc, title string, duration float64) {
	t.Helper()
	if len(doc.AudioTracks) != 1 {
		t.Fatalf("transcode must list one track, got %d", len(doc.AudioTracks))
	}
	track := doc.AudioTracks[0]
	if track.Index != 1 || track.Offset != 0 || !near(track.Duration, duration) || track.Title != title ||
		track.URL != "/hls/"+doc.ID+"/output.m3u8" || track.MimeType != "application/vnd.apple.mpegurl" || track.Codec != nil || track.Metadata != nil {
		t.Fatalf("HLS track wrong: %+v", track)
	}
}

// deviceRow returns the stored devices row of a client device id.
func deviceRow(t *testing.T, e *harness.Env, deviceID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM devices WHERE deviceId = ?", deviceID)
	if len(rows) != 1 {
		t.Fatalf("device %q: want one row, got %d", deviceID, len(rows))
	}
	return rows[0]
}
