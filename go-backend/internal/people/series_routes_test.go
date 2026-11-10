package people

import (
	"encoding/json"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
)

func seriesRig(t *testing.T) *authorRig {
	t.Helper()
	rig := newAuthorRig(t, nil)
	for _, q := range []string{
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,isFinished BOOLEAN)`,
		`CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT,feedURL TEXT,title TEXT,description TEXT,preventIndexing BOOLEAN,ownerName TEXT,ownerEmail TEXT)`,
		// A second book in s1 with sequence 2 (b1 has 1) and one without.
		`INSERT INTO bookSeries VALUES ('bs2','2','b2','s1','2024-01-02T00:00:00Z')`,
	} {
		if _, err := rig.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func TestSeriesFindOneProgressAndFeed(t *testing.T) {
	rig := seriesRig(t)
	if _, err := rig.db.Exec(`INSERT INTO mediaProgresses VALUES ('p1','u2','b1',1)`); err != nil {
		t.Fatal(err)
	}
	reader := readOnlyUser()
	reader.ID = "u2"
	w := rig.do("GET", "/api/series/s1?include=%20progress%20,%20rssfeed,,", "", reader)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		SeriesView
		Progress *SeriesProgress `json:"progress"`
		RSSFeed  json.RawMessage `json:"rssFeed"`
	}
	decodeAuthorBody(t, w, &body)
	if body.ID != "s1" || body.NameIgnorePrefix == nil || *body.NameIgnorePrefix != "First Series" {
		t.Fatalf("series %+v", body.SeriesView)
	}
	if body.Progress == nil || strings.Join(body.Progress.LibraryItemIDs, ",") != "i1,i2" || strings.Join(body.Progress.LibraryItemIDsFinished, ",") != "i1" || body.Progress.IsFinished {
		t.Fatalf("progress %+v", body.Progress)
	}
	if string(body.RSSFeed) != "null" {
		t.Fatalf("rssFeed %s", body.RSSFeed)
	}
	if _, err := rig.db.Exec(`INSERT INTO mediaProgresses VALUES ('p2','u2','b2',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`INSERT INTO feeds VALUES ('f1','series','s1','http://x/feed','First Series',NULL,1,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	decodeAuthorBody(t, rig.do("GET", "/api/series/s1?include=progress,rssfeed", "", reader), &body)
	if !body.Progress.IsFinished || len(body.Progress.LibraryItemIDsFinished) != 2 {
		t.Fatalf("progress %+v", body.Progress)
	}
	var feed struct {
		ID, EntityType string
		Meta           struct{ PreventIndexing bool }
	}
	if err := json.Unmarshal(body.RSSFeed, &feed); err != nil || feed.ID != "f1" || !feed.Meta.PreventIndexing {
		t.Fatalf("feed %s %v", body.RSSFeed, err)
	}
	// Neither include: no progress or rssFeed keys.
	var plain map[string]json.RawMessage
	decodeAuthorBody(t, rig.do("GET", "/api/series/s1", "", reader), &plain)
	if _, ok := plain["progress"]; ok {
		t.Fatal("progress without include")
	}
	if _, ok := plain["rssFeed"]; ok {
		t.Fatal("rssFeed without include")
	}
}

func TestSeriesAccessRules(t *testing.T) {
	rig := seriesRig(t)
	if w := rig.do("GET", "/api/series/nope", "", rootUser()); w.Code != 404 {
		t.Fatalf("unknown series %d", w.Code)
	}
	// Every book of the series is explicit: a user without explicit access
	// sees a 404, not the series.
	if _, err := rig.db.Exec(`UPDATE books SET explicit=1`); err != nil {
		t.Fatal(err)
	}
	clean := store.User{ID: "u9", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)}
	if w := rig.do("GET", "/api/series/s1", "", clean); w.Code != 404 {
		t.Fatalf("no accessible books: %d", w.Code)
	}
	if w := rig.do("PATCH", "/api/series/s1", `{"name":"x"}`, clean); w.Code != 404 {
		t.Fatalf("patch without accessible books: %d", w.Code)
	}
	if w := rig.do("PATCH", "/api/series/s1", `{"name":"x"}`, readOnlyUser()); w.Code != 403 {
		t.Fatalf("update permission: %d", w.Code)
	}
}

func TestSeriesUpdate(t *testing.T) {
	rig := seriesRig(t)
	for _, body := range []string{`{}`, `{"name":5,"description":null}`, `[1]`} {
		if w := rig.do("PATCH", "/api/series/s1", body, rootUser()); w.Code != 400 || w.Body.String() != "No valid fields to update" {
			t.Fatalf("body %s: %d %q", body, w.Code, w.Body)
		}
	}
	w := rig.do("PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got SeriesView
	decodeAuthorBody(t, w, &got)
	if got.Name == nil || *got.Name != "The Renamed" || got.NameIgnorePrefix == nil || *got.NameIgnorePrefix != "Renamed, The" || got.Description == nil || *got.Description != "About" || got.UpdatedAt <= got.AddedAt {
		t.Fatalf("series %+v", got)
	}
	var column string
	if err := rig.db.QueryRow(`SELECT nameIgnorePrefix FROM series WHERE id='s1'`).Scan(&column); err != nil || column != "First Series" {
		t.Fatalf("the prefix column is not recomputed upstream: %q %v", column, err)
	}
	events := rig.takeEvents()
	if len(events) != 1 || events[0].event != "series_updated" {
		t.Fatalf("events %+v", events)
	}
	if payload, ok := events[0].payload.(SeriesView); !ok || payload.ID != "s1" {
		t.Fatalf("payload %+v", events[0].payload)
	}
	// The same values again: answered, saved nothing, announced nothing.
	var again SeriesView
	decodeAuthorBody(t, rig.do("PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`, rootUser()), &again)
	if again.UpdatedAt != got.UpdatedAt || len(rig.takeEvents()) != 0 {
		t.Fatalf("no-op changed state: %+v", again)
	}
	// An empty string is a valid description.
	decodeAuthorBody(t, rig.do("PATCH", "/api/series/s1", `{"description":""}`, rootUser()), &again)
	if again.Description == nil || *again.Description != "" {
		t.Fatalf("description %v", again.Description)
	}
}
