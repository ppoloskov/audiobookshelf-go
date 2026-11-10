package catalog

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/wire"
)

func TestItemMatchEmptyAndDuplicateRelations(t *testing.T) {
	for _, test := range []struct {
		name  string
		match wire.BookMatch
	}{
		{"empty", wire.BookMatch{Author: raw([]string{}), Series: raw([]map[string]string{})}},
		{"duplicates", wire.BookMatch{
			Author: raw([]string{"ADA AUTHOR", "ada author"}),
			Series: raw([]map[string]string{{"series": "FIRST SERIES", "sequence": "1"}, {"series": "first series", "sequence": "1"}}),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{test.match}})
			var result quickMatchResult
			decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()), &result)
			if result.Updated == nil || *result.Updated {
				t.Fatalf("unchanged relations reported an update: %+v", result)
			}
			var authorID, seriesID, sequence string
			if err := rig.db.QueryRow(`SELECT (SELECT group_concat(authorId) FROM bookAuthors WHERE bookId='b1'), (SELECT group_concat(seriesId) FROM bookSeries WHERE bookId='b1'), (SELECT sequence FROM bookSeries WHERE bookId='b1')`).Scan(&authorID, &seriesID, &sequence); err != nil {
				t.Fatal(err)
			}
			if authorID != "a1" || seriesID != "s1" || sequence != "1" {
				t.Fatalf("relations changed: author=%q series=%q sequence=%q", authorID, seriesID, sequence)
			}
			if events := rig.takeEventNames(); len(events) != 0 {
				t.Fatalf("unchanged relations emitted %v", events)
			}
		})
	}
}

func TestItemMatchLinksExistingRelationsWithoutCreationEvents(t *testing.T) {
	rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{
		Author: raw([]string{"BEN WRITER", "ben writer"}),
		Series: raw([]map[string]string{{"series": "KNOWN SERIES", "sequence": "2"}, {"series": "known series", "sequence": "3"}}),
	}}})
	if _, err := rig.db.Exec(`INSERT INTO series(id,name,libraryId,createdAt,updatedAt) VALUES('s2','Known Series','l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	response := rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser())
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	var authorID, seriesID, sequence string
	if err := rig.db.QueryRow(`SELECT (SELECT group_concat(authorId) FROM bookAuthors WHERE bookId='b1'), (SELECT group_concat(seriesId) FROM bookSeries WHERE bookId='b1'), (SELECT sequence FROM bookSeries WHERE bookId='b1')`).Scan(&authorID, &seriesID, &sequence); err != nil {
		t.Fatal(err)
	}
	if authorID != "a2" || seriesID != "s2" || sequence != "3" {
		t.Fatalf("relations: author=%q series=%q sequence=%q", authorID, seriesID, sequence)
	}
	for _, event := range rig.takeEventNames() {
		if strings.HasSuffix(event, "_added") || event == "author_updated" {
			t.Fatalf("linking existing entities emitted %q", event)
		}
	}
}

func TestItemMatchDuplicateSeriesUpdatesSequentially(t *testing.T) {
	rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{
		Series: raw([]map[string]string{{"series": "First Series", "sequence": "2"}, {"series": "first series", "sequence": "1"}}),
	}}})
	var result quickMatchResult
	decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()), &result)
	if result.Updated == nil || !*result.Updated {
		t.Fatalf("intermediate sequence change was not reported: %+v", result)
	}
	var got sql.NullString
	var count int
	if err := rig.db.QueryRow(`SELECT sequence FROM bookSeries WHERE bookId='b1' AND seriesId='s1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM bookSeries WHERE bookId='b1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if got != (sql.NullString{String: "1", Valid: true}) || count != 1 {
		t.Fatalf("series sequence=%+v rows=%d, want final 1 and one relation", got, count)
	}
	if events := rig.takeEventNames(); strings.Join(events, ",") != "item_updated" {
		t.Fatalf("events=%v, want one item_updated", events)
	}
}

func TestItemMatchDuplicateNewSeriesCreatesOneRelation(t *testing.T) {
	rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{
		Series: raw([]map[string]string{{"series": "New Series", "sequence": "2"}, {"series": "new series", "sequence": "3"}}),
	}}})
	var result quickMatchResult
	decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()), &result)
	if result.Updated == nil || !*result.Updated {
		t.Fatalf("new duplicate relation was not reported as updated: %+v", result)
	}
	var name, sequence string
	var count int
	if err := rig.db.QueryRow(`SELECT s.name,bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1'`).Scan(&name, &sequence); err != nil {
		t.Fatal(err)
	}
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM bookSeries WHERE bookId='b1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if name != "New Series" || sequence != "3" || count != 1 {
		t.Fatalf("series=%q sequence=%q rows=%d, want one New Series at 3", name, sequence, count)
	}
	if events := rig.takeEventNames(); strings.Join(events, ",") != "series_removed,series_added,item_updated" {
		t.Fatalf("events=%v, want one add, one remove and one item_updated", events)
	}
}

func TestItemMatchMissingSequencePreservesExistingAndDefaultsNull(t *testing.T) {
	t.Run("existing series", func(t *testing.T) {
		rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{Series: raw([]map[string]string{{"series": "First Series"}})}}})
		var result quickMatchResult
		decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()), &result)
		var got sql.NullString
		if err := rig.db.QueryRow(`SELECT sequence FROM bookSeries WHERE bookId='b1' AND seriesId='s1'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != (sql.NullString{String: "1", Valid: true}) || result.Updated == nil || *result.Updated {
			t.Fatalf("sequence=%+v result=%+v, want existing 1 unchanged", got, result)
		}
		if events := rig.takeEventNames(); len(events) != 0 {
			t.Fatalf("events=%v, want none", events)
		}
	})

	t.Run("new series", func(t *testing.T) {
		rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{Series: raw([]map[string]string{{"series": "New Series"}})}}})
		var result quickMatchResult
		decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()), &result)
		var got sql.NullString
		if err := rig.db.QueryRow(`SELECT bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1' AND s.name='New Series'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.Valid || result.Updated == nil || !*result.Updated {
			t.Fatalf("sequence=%+v result=%+v, want new relation with SQL NULL", got, result)
		}
	})
}

func TestItemMatchRelationFailureRollsBackDatabaseAndEvents(t *testing.T) {
	rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{
		Author: raw([]string{"New Author"}),
		Series: raw([]map[string]string{{"series": "New Series", "sequence": "2"}}),
	}}})
	if _, err := rig.db.Exec(`CREATE TRIGGER fail_quickmatch_series BEFORE INSERT ON bookSeries BEGIN SELECT RAISE(ABORT, 'quickmatch test failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", w.Code, w.Body)
	}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM authors WHERE name='New Author'`, 0},
		{`SELECT COUNT(*) FROM series WHERE name='New Series'`, 0},
		{`SELECT COUNT(*) FROM bookAuthors WHERE bookId='b1' AND authorId='a1'`, 1},
		{`SELECT COUNT(*) FROM bookSeries WHERE bookId='b1' AND seriesId='s1'`, 1},
	} {
		var got int
		if err := rig.db.QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}
	if events := rig.takeEventNames(); len(events) != 0 {
		t.Fatalf("events after rolled back match: %v", events)
	}
}

func TestItemMatchSeriesSequenceNullEmptyAndNumbers(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence any
		want     sql.NullString
	}{
		{"null", nil, sql.NullString{}},
		{"empty", "", sql.NullString{Valid: true}},
		{"zero", 0, sql.NullString{String: "0", Valid: true}},
		{"number", 2, sql.NullString{String: "2", Valid: true}},
		{"text_zero", "0", sql.NullString{String: "0", Valid: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rig := matchRig(t, &fakeBooks{books: []wire.BookMatch{{Series: raw([]map[string]any{{"series": "First Series", "sequence": test.sequence}})}}})
			response := rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser())
			if response.Code != 200 {
				t.Fatalf("%d %s", response.Code, response.Body)
			}
			var got sql.NullString
			if err := rig.db.QueryRow(`SELECT sequence FROM bookSeries WHERE bookId='b1' AND seriesId='s1'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("sequence=%+v, want %+v", got, test.want)
			}
		})
	}
}
