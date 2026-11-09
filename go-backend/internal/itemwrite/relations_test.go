package itemwrite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/filterdata"
)

// withTx runs fn in a transaction that is committed, like the callers do.
func withTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func column(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v.String)
	}
	return out
}

func eventNames(c Changes) []string {
	var out []string
	for _, e := range c.Events {
		out = append(out, e.Name)
	}
	return out
}

func TestLinkAuthorsCreatesLinksAndDerivesLastFirst(t *testing.T) {
	db := openDB(t)
	var c Changes
	var changed bool
	withTx(t, db, func(tx *sql.Tx) {
		var err error
		changed, err = c.LinkAuthors(context.Background(), tx, "l1", "b1", []string{" Ada Example ", "", "ada example", "Bob Roe"}, Rules{})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !changed {
		t.Fatal("linking two new authors reported no change")
	}
	// Case-insensitive duplicates collapse; lastFirst is derived like nameToLastFirst.
	got := column(t, db, `SELECT a.name||'|'||a.lastFirst FROM bookAuthors j JOIN authors a ON a.id=j.authorId WHERE j.bookId='b1' ORDER BY j.createdAt`)
	if want := []string{"Ada Example|Example, Ada", "Bob Roe|Roe, Bob"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authors %v, want %v", got, want)
	}
	if want := []string{"author_added", "author_added"}; !reflect.DeepEqual(eventNames(c), want) || len(c.AuthorsAdded) != 2 || len(c.AuthorsRelinked) != 0 {
		t.Fatalf("changes %+v", c)
	}
	// A new author's payload has no asin/description/imagePath keys (Author.js:177-199).
	payload := c.Events[0].Payload.(map[string]any)
	for _, key := range []string{"asin", "description", "imagePath", "numBooks"} {
		if _, has := payload[key]; has {
			t.Errorf("author_added payload carries %q", key)
		}
	}
}

func TestLinkAuthorsReplaceUnlinksAndKeepsExistingLinksStable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	withTx(t, db, func(tx *sql.Tx) {
		var c Changes
		if _, err := c.LinkAuthors(ctx, tx, "l1", "b1", []string{"Ada Example", "Bob Roe"}, Rules{}); err != nil {
			t.Fatal(err)
		}
	})
	before := column(t, db, `SELECT id FROM bookAuthors WHERE bookId='b1' ORDER BY createdAt`)
	var c Changes
	var changed bool
	withTx(t, db, func(tx *sql.Tx) {
		var err error
		changed, err = c.LinkAuthors(ctx, tx, "l1", "b1", []string{"BOB ROE"}, Rules{Replace: true, AuthorUpdates: true})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !changed || len(c.AuthorsUnlinked) != 1 {
		t.Fatalf("changed=%v unlinked=%v", changed, c.AuthorsUnlinked)
	}
	after := column(t, db, `SELECT id FROM bookAuthors WHERE bookId='b1'`)
	if len(after) != 1 || after[0] != before[1] {
		t.Fatalf("the kept author's link was recreated: before %v after %v", before, after)
	}
	// Ada has no other book: she is unlinked but not announced as updated (only the sweep removes her).
	if len(c.Events) != 0 {
		t.Fatalf("events %v", eventNames(c))
	}

	// An author that keeps another book after the unlink is announced as updated (PATCH rules).
	mustExec(t, db, `INSERT INTO books(id,title,createdAt,updatedAt) VALUES('b2','Other',?,?)`, stamp, stamp)
	withTx(t, db, func(tx *sql.Tx) {
		var c2 Changes
		if _, err := c2.LinkAuthors(ctx, tx, "l1", "b2", []string{"Bob Roe"}, Rules{AuthorUpdates: true}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"author_updated"}; !reflect.DeepEqual(eventNames(c2), want) || len(c2.AuthorsRelinked) != 1 {
			t.Fatalf("relink changes %+v", c2)
		}
		if n := c2.Events[0].Payload.(map[string]any)["numBooks"]; n != 2 {
			t.Fatalf("numBooks=%v, want 2", n)
		}
		var c3 Changes
		if _, err := c3.LinkAuthors(ctx, tx, "l1", "b1", []string{"Someone Else"}, Rules{Replace: true, AuthorUpdates: true}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"author_updated", "author_added"}; !reflect.DeepEqual(eventNames(c3), want) {
			t.Fatalf("unlink of an author who keeps a book: events %v, want %v", eventNames(c3), want)
		}
	})
}

func TestLinkAuthorsExactRuleTreatsCaseAsDifferentNames(t *testing.T) {
	db := openDB(t)
	withTx(t, db, func(tx *sql.Tx) {
		var c Changes
		if _, err := c.LinkAuthors(context.Background(), tx, "l1", "b1", []string{"ada example", "Ada Example"}, Rules{Exact: true}); err != nil {
			t.Fatal(err)
		}
	})
	if n := len(column(t, db, `SELECT id FROM authors`)); n != 2 {
		t.Fatalf("exact rules created %d authors, want 2", n)
	}
}

func TestLinkSeriesSequenceRules(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	prefixes := []string{"the"}
	seqs := func() string {
		return strings.Join(column(t, db, `SELECT s.name||'#'||COALESCE(j.sequence,'NULL')||'#'||s.nameIgnorePrefix FROM bookSeries j JOIN series s ON s.id=j.seriesId WHERE j.bookId='b1' ORDER BY j.createdAt`), ",")
	}
	link := func(r Rules, series ...Series) (Changes, bool) {
		var c Changes
		var changed bool
		withTx(t, db, func(tx *sql.Tx) {
			var err error
			changed, err = c.LinkSeries(ctx, tx, "l1", "b1", series, prefixes, r)
			if err != nil {
				t.Fatal(err)
			}
		})
		return c, changed
	}

	c, changed := link(Rules{SeriesAdded: true}, Series{"The Saga", "1"}, Series{"Side", ""})
	if !changed || seqs() != "The Saga#1#Saga,Side#NULL#Side" {
		t.Fatalf("created: changed=%v %s", changed, seqs())
	}
	if want := []string{"series_added", "series_added"}; !reflect.DeepEqual(eventNames(c), want) {
		t.Fatalf("events %v", eventNames(c))
	}
	// SeriesAdded is off for PATCH: the series exist but no event is sent.
	if c, _ := link(Rules{}, Series{"Third", "3"}); len(c.Events) != 0 || len(c.SeriesAdded) != 1 {
		t.Fatalf("without SeriesAdded: %+v", c)
	}
	// A blank sequence clears a stored one unless KeepSequence (scanner).
	if _, changed := link(Rules{KeepSequence: true}, Series{"the saga", ""}); changed || !strings.Contains(seqs(), "The Saga#1#") {
		t.Fatalf("KeepSequence changed the sequence: %s", seqs())
	}
	if _, changed := link(Rules{}, Series{"the saga", ""}); !changed || !strings.Contains(seqs(), "The Saga#NULL#") {
		t.Fatalf("blank sequence did not clear: %s", seqs())
	}
	// An identical sequence is not a change.
	if _, changed := link(Rules{}, Series{"Side", ""}); changed {
		t.Fatal("unchanged sequence reported a change")
	}
	// Replace unlinks series the list leaves out.
	c, changed = link(Rules{Replace: true}, Series{"Side", ""})
	if !changed || len(c.SeriesUnlinked) != 2 || seqs() != "Side#NULL#Side" {
		t.Fatalf("replace: changed=%v unlinked=%v %s", changed, c.SeriesUnlinked, seqs())
	}
}

func TestAnnouncePatchesFilterDataAndEmitsEvents(t *testing.T) {
	filters := filterdata.NewCache()
	filters.Store("l1", filterdata.Data{LoadedAt: time.Now().UnixMilli()})
	c := Changes{
		Events:       []Event{{"author_added", map[string]any{"id": "a1"}}},
		AuthorsAdded: []filterdata.Named{{ID: "a1", Name: "Ada"}},
		SeriesAdded:  []filterdata.Named{{ID: "s1", Name: "Saga"}},
	}
	var r recorder
	c.Announce("l1", filters, r.emit)
	d, _ := filters.Fresh("l1")
	if len(d.Authors) != 1 || len(d.Series) != 1 || fmt.Sprint(r.events) != "[author_added a1]" {
		t.Fatalf("filters %+v events %v", d, r.events)
	}
}

func TestSweepSeriesClosesItsFeeds(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `INSERT INTO series(id,name,libraryId,createdAt,updatedAt) VALUES ('s1','S','l1',?,?)`, stamp, stamp)
	mustExec(t, db, `INSERT INTO feeds(id,slug,entityType,entityId,feedURL,createdAt,updatedAt) VALUES ('f1','slug','series','s1','http://x/feed/slug',?,?)`, stamp, stamp)
	var r recorder
	Env{DB: db, Filters: filterdata.NewCache(), Emit: r.emit}.SweepSeries(context.Background(), []string{"s1"})
	if n := len(column(t, db, `SELECT id FROM feeds`)); n != 0 {
		t.Fatal("the removed series' feed row survived")
	}
	if len(r.names) != 2 || r.names[0] != "series_removed" || r.names[1] != "rss_feed_closed" {
		t.Fatalf("events %v, want series_removed then rss_feed_closed", r.names)
	}
}
