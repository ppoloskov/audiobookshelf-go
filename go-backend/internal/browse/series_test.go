package browse

import (
	"encoding/json"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filterdata"
)

type seriesResults struct {
	Results []struct {
		ID            string  `json:"id"`
		TotalDuration float64 `json:"totalDuration"`
		RSSFeed       *struct {
			FeedURL string `json:"feedUrl"`
		} `json:"rssFeed"`
		Books []struct {
			ID string `json:"id"`
		} `json:"books"`
	} `json:"results"`
	Total int `json:"total"`
}

func (s seriesResults) ids() []string {
	out := []string{}
	for _, r := range s.Results {
		out = append(out, r.ID)
	}
	return out
}

func TestSeriesDetailProgressAndFeed(t *testing.T) {
	r, us := newRig(t), users()
	type detail struct {
		ID               string  `json:"id"`
		NameIgnorePrefix *string `json:"nameIgnorePrefix"`
		Progress         *struct {
			LibraryItemIDs         []string `json:"libraryItemIds"`
			LibraryItemIDsFinished []string `json:"libraryItemIdsFinished"`
			IsFinished             bool     `json:"isFinished"`
		} `json:"progress"`
		RSSFeed json.RawMessage `json:"rssFeed"`
	}
	// The root user finished b2 of s1 (b1 #1, b2 #2).
	var d detail
	r.get(us["root"], "/api/series/s1?include=%20progress%20,%20rssfeed,,", &d)
	if d.ID != "s1" || d.Progress == nil || !slices.Equal(d.Progress.LibraryItemIDs, []string{"i1", "i2"}) || !slices.Equal(d.Progress.LibraryItemIDsFinished, []string{"i2"}) || d.Progress.IsFinished {
		t.Fatalf("detail %+v", d)
	}
	if string(d.RSSFeed) != "null" {
		t.Fatalf("rssFeed %s", d.RSSFeed)
	}
	if _, err := r.d.Exec(t.Context(), `INSERT INTO feeds (id, entityType, entityId, feedURL, title, preventIndexing, createdAt, updatedAt) VALUES ('fs1','series','s1','http://x/feed','S',1,?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	r.get(us["root"], "/api/libraries/l1/series/s1?include=rssfeed", &d)
	var feed struct {
		ID   string
		Meta struct{ PreventIndexing bool }
	}
	if err := json.Unmarshal(d.RSSFeed, &feed); err != nil || feed.ID != "fs1" || !feed.Meta.PreventIndexing {
		t.Fatalf("feed %s %v", d.RSSFeed, err)
	}
	var plain map[string]json.RawMessage
	r.get(us["root"], "/api/series/s1", &plain)
	if _, ok := plain["progress"]; ok {
		t.Error("progress without include")
	}
	if _, ok := plain["rssFeed"]; ok {
		t.Error("rssFeed without include")
	}
	if code := r.status(us["root"], "GET", "/api/libraries/l3/series/s1", ""); code != 404 {
		t.Errorf("a series of another library answered %d", code)
	}
}

func TestSeriesAccessRules(t *testing.T) {
	r, us := newRig(t), users()
	if code := r.status(us["root"], "GET", "/api/series/nope", ""); code != 404 {
		t.Fatalf("unknown series %d", code)
	}
	// s1's books are untagged or tagged red/blue, one explicit: the restricted user sees b1 only; no book of gs3 is visible to them.
	if code := r.status(us["restricted"], "GET", "/api/series/s1", ""); code != 200 {
		t.Fatalf("restricted s1: %d", code)
	}
	if code := r.status(us["restricted"], "GET", "/api/series/gs3", ""); code != 404 {
		t.Fatalf("no accessible books: %d", code)
	}
	if code := r.status(us["restricted"], "PATCH", "/api/series/gs3", `{"name":"x"}`); code != 404 {
		t.Fatalf("patch without accessible books: %d", code)
	}
	if code := r.status(us["reader"], "PATCH", "/api/series/s1", `{"name":"x"}`); code != 403 {
		t.Fatalf("update permission: %d", code)
	}
}

func TestSeriesOfAnotherLibraryIsNotFound(t *testing.T) {
	r := newRig(t)
	// s1 is in l1; this user may update but only access l2.
	other := principal(rootID, "user", db.Permissions{LibrariesAccessible: []string{"l2"}, AccessAllTags: true, AccessExplicitContent: true, Update: true}, nil)
	if code := r.status(other, "GET", "/api/series/s1", ""); code != 404 {
		t.Errorf("GET series of another library: %d", code)
	}
	if code := r.status(other, "PATCH", "/api/series/s1", `{"name":"x"}`); code != 404 {
		t.Errorf("PATCH series of another library: %d", code)
	}
	var name string
	if err := r.d.Get(t.Context(), &name, `SELECT name FROM series WHERE id='s1'`); err != nil || name != "Alpha Saga" {
		t.Errorf("series was changed: %q %v", name, err)
	}
	if code := r.status(users()["root"], "GET", "/api/series/s1", ""); code != 200 {
		t.Errorf("root: %d", code)
	}
}

func TestSeriesPatch(t *testing.T) {
	r, root := newRig(t), users()["root"]
	for _, body := range []string{`{}`, `{"name":null,"description":null}`} {
		if code, msg := r.do(root, "PATCH", "/api/series/s1", body); code != 400 || string(msg) != "No valid fields to update" {
			t.Fatalf("body %s: %d %q", body, code, msg)
		}
	}
	for _, body := range []string{`[1]`, `{"name":5}`, `nope`} {
		if code := r.status(root, "PATCH", "/api/series/s1", body); code != 400 {
			t.Fatalf("body %s: %d", body, code)
		}
	}
	var got seriesView
	code, raw := r.do(root, "PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`)
	if err := json.Unmarshal(raw, &got); code != 200 || err != nil {
		t.Fatalf("%d %s", code, raw)
	}
	if *got.Name != "The Renamed" || *got.NameIgnorePrefix != "Renamed, The" || *got.Description != "About" || got.UpdatedAt <= got.AddedAt {
		t.Fatalf("series %+v", got)
	}
	var column string
	if err := r.d.Get(t.Context(), &column, `SELECT nameIgnorePrefix FROM series WHERE id='s1'`); err != nil || column != "Alpha Saga" {
		t.Fatalf("the prefix column is not recomputed: %q %v", column, err)
	}
	if ev := r.takeEvents(); len(ev) != 1 || ev[0].Name != "series_updated" || ev[0].Payload.(seriesView).ID != "s1" {
		t.Fatalf("events %+v", ev)
	}
	var again seriesView
	_, raw = r.do(root, "PATCH", "/api/series/s1", `{"name":"The Renamed","description":"About"}`)
	if err := json.Unmarshal(raw, &again); err != nil || again.UpdatedAt != got.UpdatedAt || len(r.takeEvents()) != 0 {
		t.Fatalf("no-op changed state: %+v %v", again, err)
	}
	_, raw = r.do(root, "PATCH", "/api/series/s1", `{"description":""}`)
	if err := json.Unmarshal(raw, &again); err != nil || again.Description == nil || *again.Description != "" || again.Name == nil || *again.Name != "The Renamed" {
		t.Fatalf("description %+v %v", again, err)
	}
}

func TestSeriesList(t *testing.T) {
	r := newRig(t)
	list := func(user string, q string) seriesResults {
		var out seriesResults
		r.get(users()[user], "/api/libraries/l1/series?"+q, &out)
		return out
	}
	if got := list("root", "sort=numBooks&desc=1").ids(); !slices.Equal(got, []string{"s2", "s1"}) {
		t.Errorf("numBooks desc %v", got)
	}
	if got := list("root", "sort=name").ids(); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("name %v", got)
	}
	// s1 b1 10s + b2 20s; s2 b4 30 + b5 0 + b3 5.
	if l := list("root", "sort=totalDuration&desc=1"); !slices.Equal(l.ids(), []string{"s2", "s1"}) || l.Results[0].TotalDuration != 35 {
		t.Errorf("totalDuration %+v", l)
	}
	if got := list("root", "sort=name&limit=1&page=1"); !slices.Equal(got.ids(), []string{"s2"}) || got.Total != 2 {
		t.Errorf("page %+v", got)
	}
	// Books in sequence order: s2 is b4 #1, b5 #2.5, b3 #3.
	l := list("root", "filter=genres."+b64("fiction"))
	if !slices.Equal(l.ids(), []string{"s1", "s2"}) || len(l.Results[1].Books) != 3 || l.Results[1].Books[0].ID != "i4" || l.Results[1].Books[2].ID != "i3" {
		t.Errorf("genres %+v", l)
	}
	if got := list("root", "filter=genres."+b64("horror")).ids(); !slices.Equal(got, []string{"s2"}) {
		t.Errorf("horror %v", got)
	}
	// The restricted user sees b1 (red) only: s1 with that one book.
	if l := list("restricted", ""); !slices.Equal(l.ids(), []string{"s1"}) || len(l.Results[0].Books) != 1 {
		t.Errorf("restricted %+v", l)
	}
	if _, err := r.d.Exec(t.Context(), `INSERT INTO feeds (id, entityType, entityId, feedURL, createdAt, updatedAt) VALUES ('fs2','series','s2','https://example.test/rss',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	if l := list("root", "include=rssfeed&sort=name"); l.Results[1].RSSFeed == nil || l.Results[1].RSSFeed.FeedURL != "https://example.test/rss" || l.Results[0].RSSFeed != nil {
		t.Errorf("include=rssfeed %+v", l)
	}
	// Progress filters (the root user finished b2 and b5, started b1 and b3 (ebook)): upstream's
	// "finished" keeps series whose books are all finished, "not-started" those with no started book.
	for filter, want := range map[string][]string{
		"not-finished": {"s1", "s2"}, "in-progress": {"s1", "s2"}, "finished": nil, "not-started": nil,
	} {
		if got := list("root", "filter=progress."+b64(filter)).ids(); !slices.Equal(got, append([]string{}, want...)) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("progress %s: %v, want %v", filter, got, want)
		}
	}
	// A listener who finished everything of s1 and started nothing else.
	if _, err := r.d.Exec(t.Context(), `UPDATE mediaProgresses SET isFinished=1 WHERE userId=? AND mediaItemId IN ('b1')`, rootID); err != nil {
		t.Fatal(err)
	}
	if got := list("root", "filter=progress."+b64("finished")).ids(); !slices.Equal(got, []string{"s1"}) {
		t.Errorf("finished %v, want s1", got)
	}
}

func TestSeriesBooksUseTheMinifiedBookShape(t *testing.T) {
	r, root := newRig(t), users()["root"]
	var page struct {
		Results []struct {
			Books []map[string]any `json:"books"`
		} `json:"results"`
	}
	r.get(root, "/api/libraries/l1/series?sort=name", &page)
	media := page.Results[0].Books[0]["media"].(map[string]any)
	for _, key := range []string{"audioFiles", "chapters", "ebookFile", "tracks", "libraryItemId", "ebookFormat"} {
		if _, ok := media[key]; ok {
			t.Errorf("minified media has %q", key)
		}
	}
	metadata := media["metadata"].(map[string]any)
	for _, key := range []string{"titleIgnorePrefix", "authorName", "authorNameLF", "narratorName", "seriesName", "genres", "description"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("minified metadata lacks %q", key)
		}
	}
	if _, ok := metadata["descriptionPlain"]; ok {
		t.Error("minified metadata has descriptionPlain")
	}
}

func TestSeriesRenameInvalidatesFilterData(t *testing.T) {
	r, root := newRig(t), users()["root"]
	hasSeries := func(name string) bool {
		data, err := r.filters.Get(t.Context(), r.d, "l1", "book")
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(data.Series, func(n filterdata.Named) bool { return n.Name == name })
	}
	if !hasSeries("Alpha Saga") || hasSeries("Renamed Saga") { // also fills the cache
		t.Fatal("unexpected series menu before the rename")
	}
	if code := r.status(root, "PATCH", "/api/series/s1", `{"name":"Renamed Saga"}`); code != 200 {
		t.Fatalf("PATCH: %d", code)
	}
	if !hasSeries("Renamed Saga") || hasSeries("Alpha Saga") {
		t.Error("the filter menu still shows the old series name")
	}
}
