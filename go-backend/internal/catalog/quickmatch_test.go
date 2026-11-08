package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

type fakeBooks struct {
	books    []wire.BookMatch
	podcasts []wire.PodcastMatch
	queries  []wire.BookSearch
}

func (f *fakeBooks) SearchBooks(_ context.Context, q wire.BookSearch) []wire.BookMatch {
	f.queries = append(f.queries, q)
	return f.books
}

func (f *fakeBooks) SearchPodcasts(context.Context, string) []wire.PodcastMatch { return f.podcasts }

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func matchRig(t *testing.T, finder *fakeBooks) *authorRig {
	t.Helper()
	rig := newAuthorRig(t, nil)
	rig.h.finder = finder
	rig.mux = http.NewServeMux()
	rig.h.Routes(rig.mux, RouteDeps{Books: finder})
	return rig
}

func TestItemMatchValidationAndWarning(t *testing.T) {
	finder := &fakeBooks{}
	rig := matchRig(t, finder)
	if w := rig.do("POST", "/api/items/nope/match", "{}", rootUser()); w.Code != 404 {
		t.Fatalf("unknown item %d", w.Code)
	}
	if w := rig.do("POST", "/api/items/i1/match", "{}", readOnlyUser()); w.Code != 403 {
		t.Fatalf("no update permission %d", w.Code)
	}
	w := rig.do("POST", "/api/items/i1/match", `{"provider":"audible","title":"Query","author":"Someone"}`, rootUser())
	if w.Code != 200 || !sameJSON(w.Body.String(), `{"warning":"No audible match found"}`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	q := finder.queries[0]
	if q.Provider != "audible" || q.Title != "Query" || q.Author != "Someone" || q.MaxFuzzySearches != 2 {
		t.Fatalf("query %+v", q)
	}
	// Defaults: google, the book's title and author names.
	rig.do("POST", "/api/items/i1/match", "{}", rootUser())
	if q := finder.queries[1]; q.Provider != "google" || q.Title != "The Book" || q.Author != "Ada Author" {
		t.Fatalf("defaults %+v", q)
	}
}

func TestItemMatchAppliesDetails(t *testing.T) {
	finder := &fakeBooks{books: []wire.BookMatch{{
		Title: raw("Matched Title"), Subtitle: raw("Sub"), Author: raw("Ben Writer, New Person"), Narrator: raw("N1, N2"),
		Publisher: raw("Press"), PublishedYear: raw(2001), Genres: raw([]string{"G"}), Series: raw([]map[string]string{{"series": "First Series", "sequence": "5"}, {"series": "Fresh Series", "sequence": "1"}}),
	}}}
	rig := matchRig(t, finder)
	// Without overrides only empty fields are filled: the title stays, the
	// subtitle, publisher and narrators arrive; authors and series are kept
	// because the book already has them.
	var result quickMatchResult
	w := rig.do("POST", "/api/items/i1/match", `{"provider":"custom-1"}`, rootUser())
	decodeAuthorBody(t, w, &result)
	if result.Updated == nil || !*result.Updated || len(result.LibraryItem) == 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var title, subtitle, publisher, year, narrators string
	if err := rig.db.QueryRow(`SELECT title,COALESCE(subtitle,''),COALESCE(publisher,''),COALESCE(publishedYear,''),narrators FROM books WHERE id='b1'`).Scan(&title, &subtitle, &publisher, &year, &narrators); err != nil {
		t.Fatal(err)
	}
	if title != "The Book" || subtitle != "Sub" || publisher != "Press" || year != "2024" || narrators != `["Reader"]` {
		t.Fatalf("book %q %q %q %q %q", title, subtitle, publisher, year, narrators)
	}
	if names := strings.Join(rig.takeEventNames(), ","); names != "item_updated" {
		t.Fatalf("events %s", names)
	}
	// With overrideDetails everything is replaced and authors and series follow
	// the match: Ben stays, Ada Author goes (and is removed, being empty), a new
	// author is created, First Series changes its sequence, Fresh Series is new.
	w = rig.do("POST", "/api/items/i1/match", `{"provider":"custom-1","overrideDetails":true,"overrideCover":false}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if err := rig.db.QueryRow(`SELECT title,narrators,publishedYear FROM books WHERE id='b1'`).Scan(&title, &narrators, &year); err != nil {
		t.Fatal(err)
	}
	if title != "Matched Title" || narrators != `["N1","N2"]` || year != "2001" {
		t.Fatalf("overridden book %q %q %q", title, narrators, year)
	}
	rows, err := rig.db.Query(`SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId='b1' ORDER BY ba.createdAt`)
	if err != nil {
		t.Fatal(err)
	}
	var authors []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		authors = append(authors, n)
	}
	rows.Close()
	if strings.Join(authors, ",") != "Ben Writer,New Person" {
		t.Fatalf("authors %v", authors)
	}
	var seqs string
	if err := rig.db.QueryRow(`SELECT group_concat(s.name||'#'||bs.sequence) FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1'`).Scan(&seqs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seqs, "First Series#5") || !strings.Contains(seqs, "Fresh Series#1") {
		t.Fatalf("series %s", seqs)
	}
	names := rig.takeEventNames()
	joined := strings.Join(names, ",")
	for _, want := range []string{"author_added", "series_added", "author_removed", "item_updated"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("events %s lack %s", joined, want)
		}
	}
	// A match equal to the stored values changes nothing.
	finder.books = []wire.BookMatch{{Title: raw("Matched Title")}}
	var again quickMatchResult
	decodeAuthorBody(t, rig.do("POST", "/api/items/i1/match", `{"provider":"custom-1","overrideDetails":true,"overrideCover":false}`, rootUser()), &again)
	if again.Updated == nil || *again.Updated {
		t.Fatalf("second match updated: %v", again.Updated)
	}
}

func (r *authorRig) takeEventNames() []string {
	var names []string
	for _, e := range r.takeEvents() {
		names = append(names, e.event)
	}
	return names
}

func TestBatchQuickMatchAndMatchAll(t *testing.T) {
	finder := &fakeBooks{books: []wire.BookMatch{{Title: raw("T"), Publisher: raw("P")}}}
	rig := matchRig(t, finder)
	post := func(user store.User, body string) int {
		return rig.do("POST", "/api/items/batch/quickmatch", body, user).Code
	}
	if c := post(readOnlyUser(), `{"libraryItemIds":["i1"]}`); c != 403 {
		t.Fatalf("non-admin %d", c)
	}
	for _, body := range []string{`{}`, `{"libraryItemIds":[]}`, `{"libraryItemIds":["nope"]}`} {
		if c := post(rootUser(), body); c != 400 {
			t.Fatalf("%s: %d", body, c)
		}
	}
	done := make(chan struct{})
	rig.h.emit = func(user, event string, payload any) {
		rig.mu.Lock()
		rig.events = append(rig.events, recordedEvent{event, payload})
		rig.mu.Unlock()
		if event == "batch_quickmatch_complete" {
			if user != "u1" {
				t.Errorf("sent to %q", user)
			}
			close(done)
		}
	}
	if c := post(rootUser(), `{"libraryItemIds":["i1","i2"],"options":{"provider":"x"}}`); c != 200 {
		t.Fatalf("run %d", c)
	}
	<-done
	var complete batchQuickMatchResult
	for _, e := range rig.takeEvents() {
		if e.event == "batch_quickmatch_complete" {
			complete = e.payload.(batchQuickMatchResult)
		}
	}
	if !complete.Success || complete.Updates != 2 || complete.Unmatched != 0 {
		t.Fatalf("result %+v", complete)
	}
	if w := rig.do("GET", "/api/libraries/l1/matchall", "", readOnlyUser()); w.Code != 403 {
		t.Fatalf("matchall non-admin %d", w.Code)
	}
}
