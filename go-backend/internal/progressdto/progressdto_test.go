package progressdto_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/wire"

	_ "modernc.org/sqlite"
)

// The expectations below are MediaProgress.applyProgressUpdate
// (server/models/MediaProgress.js:190) and
// User.createUpdateMediaProgressFromPayload worked out by hand.

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(schemaassets.SchemaSQL)
	now := "2024-01-01 00:00:00.000 +00:00"
	exec(`INSERT INTO users(id,username,createdAt,updatedAt) VALUES ('u1','reader',?,?)`, now, now)
	exec(`INSERT INTO libraries(id,name,createdAt,updatedAt) VALUES ('lib1','Books',?,?),('lib2','Podcasts',?,?)`, now, now, now, now)
	exec(`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId,createdAt,updatedAt) VALUES
		('li-book','book1','book','lib1',?,?),('li-book2','book2','book','lib1',?,?),('li-pod','pod1','podcast','lib2',?,?)`, now, now, now, now, now, now)
	exec(`INSERT INTO podcastEpisodes(id,podcastId,createdAt,updatedAt) VALUES ('ep1','pod1',?,?)`, now, now)
	return db
}

func payload(libraryItemID string, fields map[string]any) progressdto.Payload {
	raw, _ := json.Marshal(fields)
	var p progressdto.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		panic(err)
	}
	p.LibraryItemID = libraryItemID
	return p
}

func apply(t *testing.T, db *sql.DB, p progressdto.Payload) wire.MediaProgress {
	t.Helper()
	got, err := progressdto.Apply(context.Background(), db, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFirstUpdateCreatesProgressForTheBook(t *testing.T) {
	db := newDB(t)
	got := apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 30, "progress": 0.3}))
	if got.MediaItemID != "book1" || got.MediaItemType != "book" || got.LibraryItemID == nil || *got.LibraryItemID != "li-book" {
		t.Fatalf("progress addresses %+v", got)
	}
	if got.IsFinished || got.Progress != 0.3 || *got.CurrentTime != 30 || *got.Duration != 100 || got.FinishedAt != nil {
		t.Fatalf("progress = %+v", got)
	}
	stored, err := progressdto.ReadOne(context.Background(), db, "u1", "li-book", "")
	if err != nil || stored == nil || stored.ID != got.ID || *stored.CurrentTime != 30 {
		t.Fatalf("ReadOne = %+v, %v", stored, err)
	}
}

func TestFinishingRules(t *testing.T) {
	t.Run("within ten seconds of the end finishes by default", func(t *testing.T) {
		db := newDB(t)
		apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 10, "progress": 0.1}))
		got := apply(t, db, payload("li-book", map[string]any{"currentTime": 95}))
		if !got.IsFinished || got.Progress != 1 || got.FinishedAt == nil {
			t.Fatalf("progress = %+v, want finished with progress 1", got)
		}
	})
	t.Run("markAsFinishedTimeRemaining overrides the ten seconds", func(t *testing.T) {
		db := newDB(t)
		apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 10}))
		if got := apply(t, db, payload("li-book", map[string]any{"currentTime": 85, "markAsFinishedTimeRemaining": 20})); !got.IsFinished {
			t.Fatalf("15s left with a 20s threshold must finish: %+v", got)
		}
		db = newDB(t)
		apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 10}))
		if got := apply(t, db, payload("li-book", map[string]any{"currentTime": 95, "markAsFinishedTimeRemaining": 2})); got.IsFinished {
			t.Fatalf("5s left with a 2s threshold must not finish: %+v", got)
		}
	})
	t.Run("markAsFinishedPercentComplete compares with progress", func(t *testing.T) {
		db := newDB(t)
		apply(t, db, payload("li-book", map[string]any{"duration": 1000, "currentTime": 10}))
		got := apply(t, db, payload("li-book", map[string]any{"currentTime": 950, "markAsFinishedPercentComplete": 99}))
		if got.IsFinished {
			t.Fatalf("95%% with a 99%% threshold must not finish: %+v", got)
		}
		got = apply(t, db, payload("li-book", map[string]any{"currentTime": 951, "markAsFinishedPercentComplete": 90}))
		if !got.IsFinished {
			t.Fatalf("95%% with a 90%% threshold must finish although 49s remain: %+v", got)
		}
	})
	t.Run("no duration never finishes", func(t *testing.T) {
		db := newDB(t)
		got := apply(t, db, payload("li-book", map[string]any{"currentTime": 5000}))
		if got.IsFinished {
			t.Fatalf("progress = %+v", got)
		}
	})
}

func TestUnfinishingAndRewinding(t *testing.T) {
	db := newDB(t)
	apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 10}))
	done := apply(t, db, payload("li-book", map[string]any{"currentTime": 99}))
	if !done.IsFinished || done.FinishedAt == nil {
		t.Fatalf("setup: %+v", done)
	}
	// isFinished=false on a finished row resets progress and position.
	reset := apply(t, db, payload("li-book", map[string]any{"isFinished": false, "currentTime": 40}))
	if reset.IsFinished || reset.FinishedAt != nil || reset.Progress != 0 || *reset.CurrentTime != 0 {
		t.Fatalf("explicit unfinish = %+v, want reset to 0 and the payload currentTime ignored", reset)
	}
	// Moving a finished book away from the end unfinishes it.
	apply(t, db, payload("li-book", map[string]any{"isFinished": true}))
	back := apply(t, db, payload("li-book", map[string]any{"currentTime": 20}))
	if back.IsFinished || back.FinishedAt != nil || *back.CurrentTime != 20 {
		t.Fatalf("rewind = %+v", back)
	}
}

func TestExplicitFinishUsesPayloadFinishedAt(t *testing.T) {
	db := newDB(t)
	apply(t, db, payload("li-book", map[string]any{"duration": 100, "currentTime": 10}))
	got := apply(t, db, payload("li-book", map[string]any{"isFinished": true, "finishedAt": 1700000000000}))
	if !got.IsFinished || got.Progress != 1 || got.FinishedAt == nil || *got.FinishedAt != 1700000000000 {
		t.Fatalf("progress = %+v", got)
	}
}

func TestProgressValueIsClampedAndHideFlagResets(t *testing.T) {
	db := newDB(t)
	apply(t, db, payload("li-book", map[string]any{"duration": 1000, "currentTime": 10, "hideFromContinueListening": true}))
	if got := apply(t, db, payload("li-book", map[string]any{"progress": 7})); got.Progress != 1 {
		t.Fatalf("progress above 1 must clamp, got %v", got.Progress)
	}
	if got := apply(t, db, payload("li-book", map[string]any{"progress": -3})); got.Progress != 0 {
		t.Fatalf("progress below 0 must clamp, got %v", got.Progress)
	}
	// A position change shows the book again unless the payload hides it.
	if got := apply(t, db, payload("li-book", map[string]any{"currentTime": 20})); got.HideFromContinueListening {
		t.Fatalf("moving must reset hideFromContinueListening: %+v", got)
	}
	if got := apply(t, db, payload("li-book", map[string]any{"currentTime": 30, "hideFromContinueListening": true})); !got.HideFromContinueListening {
		t.Fatalf("payload hide wins: %+v", got)
	}
}

func TestLastUpdateOverridesUpdatedAtAndUnchangedPayloadKeepsIt(t *testing.T) {
	db := newDB(t)
	const stamp = int64(1600000000000)
	apply(t, db, payload("li-book", map[string]any{"duration": 1000, "currentTime": 5}))
	first := apply(t, db, payload("li-book", map[string]any{"currentTime": 10, "lastUpdate": stamp}))
	if first.LastUpdate == nil || *first.LastUpdate != stamp {
		t.Fatalf("lastUpdate = %v, want %d", first.LastUpdate, stamp)
	}
	again := apply(t, db, payload("li-book", map[string]any{"currentTime": 10}))
	if *again.LastUpdate != stamp {
		t.Fatalf("a payload that changes nothing must not touch updatedAt: %d", *again.LastUpdate)
	}
	ms, ok, err := progressdto.LastUpdate(context.Background(), db, "u1", "book1")
	if err != nil || !ok || ms != stamp {
		t.Fatalf("LastUpdate = %d %v %v", ms, ok, err)
	}
	if _, ok, _ := progressdto.LastUpdate(context.Background(), db, "u1", "book2"); ok {
		t.Fatal("LastUpdate for an item without progress must be absent")
	}
	moved := apply(t, db, payload("li-book", map[string]any{"currentTime": 11}))
	if *moved.LastUpdate <= stamp || *moved.LastUpdate > time.Now().UnixMilli() {
		t.Fatalf("a change must set updatedAt to now, got %d", *moved.LastUpdate)
	}
}

func TestRefusals(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		p       progressdto.Payload
		allow   func(string) bool
		status  int
		message string
	}{
		{"unknown item", payload("nope", nil), nil, http.StatusNotFound, "Library item not found"},
		{"podcast without episode", payload("li-pod", nil), nil, http.StatusBadRequest, "Library item is not a book"},
		{"unknown episode", progressdto.Payload{LibraryItemID: "li-pod", EpisodeID: "nope"}, nil, http.StatusNotFound, "Episode not found"},
		{"library not accessible", payload("li-book", nil), func(id string) bool { return id != "lib1" }, http.StatusForbidden, "Forbidden"},
		{"episode library not accessible", progressdto.Payload{LibraryItemID: "li-pod", EpisodeID: "ep1"}, func(id string) bool { return id != "lib2" }, http.StatusForbidden, "Forbidden"},
	} {
		_, err := progressdto.ApplyChecked(ctx, db, "u1", tc.p, tc.allow)
		var se *progressdto.StatusError
		if !errors.As(err, &se) || se.Status != tc.status || se.Message != tc.message {
			t.Errorf("%s: err = %v, want %d %q", tc.name, err, tc.status, tc.message)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mediaProgresses`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused updates stored %d rows (%v)", n, err)
	}
	if got, err := progressdto.ApplyChecked(ctx, db, "u1", payload("li-book", nil), func(string) bool { return true }); err != nil || got.MediaItemID != "book1" {
		t.Fatalf("allowed library: %+v, %v", got, err)
	}
}

func TestEpisodeProgressAndReadOrder(t *testing.T) {
	db := newDB(t)
	ep := apply(t, db, progressdto.Payload{LibraryItemID: "li-pod", EpisodeID: "ep1", Duration: progressdto.Value(60), CurrentTime: progressdto.Value(5)})
	if ep.MediaItemType != "podcastEpisode" || ep.EpisodeID == nil || *ep.EpisodeID != "ep1" || ep.MediaItemID != "ep1" {
		t.Fatalf("episode progress = %+v", ep)
	}
	apply(t, db, payload("li-book2", map[string]any{"duration": 10, "currentTime": 1}))
	apply(t, db, payload("li-book", map[string]any{"duration": 10, "currentTime": 1}))
	all, err := progressdto.Read(context.Background(), db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, p := range all {
		order = append(order, p.MediaItemID)
	}
	// A freshly loaded upstream user lists progress by mediaItemId.
	if len(order) != 3 || order[0] != "book1" || order[1] != "book2" || order[2] != "ep1" {
		t.Fatalf("Read order = %v, want [book1 book2 ep1]", order)
	}
	one, err := progressdto.ReadOne(context.Background(), db, "u1", "li-pod", "ep1")
	if err != nil || one == nil || one.MediaItemID != "ep1" {
		t.Fatalf("ReadOne(episode) = %+v, %v", one, err)
	}
	if none, err := progressdto.ReadOne(context.Background(), db, "u1", "li-pod", "other"); err != nil || none != nil {
		t.Fatalf("ReadOne(other episode) = %+v, %v", none, err)
	}
	if none, err := progressdto.ReadOne(context.Background(), db, "other-user", "li-book", ""); err != nil || none != nil {
		t.Fatalf("ReadOne(other user) = %+v, %v", none, err)
	}
}
