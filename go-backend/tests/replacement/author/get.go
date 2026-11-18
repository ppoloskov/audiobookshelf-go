package author

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// minifiedItem is the part of a minified library item an author page reads.
type minifiedItem struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Media     struct {
		Metadata struct {
			Title  string `json:"title"`
			Series *struct {
				ID               string `json:"id"`
				Name             string `json:"name"`
				NameIgnorePrefix string `json:"nameIgnorePrefix"`
				Sequence         string `json:"sequence"`
			} `json:"series"`
		} `json:"metadata"`
	} `json:"media"`
}

// authorPage is GET /api/authors/:id with include=items,series.
type authorPage struct {
	authorJSON
	LibraryItems []minifiedItem `json:"libraryItems"`
	Series       []struct {
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Items []minifiedItem `json:"items"`
	} `json:"series"`
}

// dbTime parses a Sequelize SQLite timestamp column into epoch milliseconds.
func dbTime(t *testing.T, value any) int64 {
	t.Helper()
	text, _ := value.(string)
	parsed, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("timestamp %q: %v", text, err)
	}
	return parsed.UnixMilli()
}

func getScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-CONTRACT", Case: "normal", State: "stateless", Assertion: "get-author-fields",
			Sources: []string{"server/controllers/AuthorController.js:40-87", "server/models/Author.js:177-198"},
			Claim:   "GET /api/authors/:id returns the author with id, asin, name, description, imagePath, libraryId, addedAt and updatedAt (epoch ms equal to the row) and no numBooks, libraryItems or series.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Ada Example")
				resp := e.Get(t, e.Token(t, "root"), authorURL(id, ""))
				wantStatus(t, "get author", resp, http.StatusOK)
				body := resp.Object(t)
				for _, key := range []string{"id", "asin", "name", "description", "imagePath", "libraryId", "addedAt", "updatedAt"} {
					if _, ok := body[key]; !ok {
						t.Fatalf("author JSON lacks %q: %s", key, resp.Body)
					}
				}
				for _, key := range []string{"numBooks", "libraryItems", "series"} {
					if _, ok := body[key]; ok {
						t.Fatalf("plain author JSON must not have %q: %s", key, resp.Body)
					}
				}
				got := decode[authorJSON](t, resp.Body)
				row := mustRow(t, e, id)
				if got.ID != id || got.Name != "Ada Example" || got.LibraryID != e.IDs["library"] || got.ASIN != nil || got.Description != nil || got.ImagePath != nil {
					t.Fatalf("unexpected author fixture values: %s", resp.Body)
				}
				if got.AddedAt != dbTime(t, row["createdAt"]) || got.UpdatedAt != dbTime(t, row["updatedAt"]) {
					t.Fatalf("addedAt/updatedAt must be the row timestamps in epoch ms: %s vs %v %v", resp.Body, row["createdAt"], row["updatedAt"])
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ACCESS", Case: "normal", State: "stateless", Assertion: "get-author-needs-no-write-permission",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/AuthorController.js:427-442"},
			Claim:   "A user without update, delete or upload permission can read an author (the permission checks apply to DELETE, PATCH and POST only).",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				for _, who := range []string{"listener", "finisher"} {
					resp := e.Get(t, e.Token(t, who), authorURL(id, ""))
					wantStatus(t, who+" get author", resp, http.StatusOK)
					if got := decode[authorJSON](t, resp.Body); got.ID != id || got.Name != "Terry Fixture" {
						t.Fatalf("%s got %s", who, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "normal", State: "stateless", Assertion: "get-author-include-items",
			Sources: []string{"server/controllers/AuthorController.js:40-87"},
			Claim:   "include=items adds libraryItems holding the minified items of every book of the author and no series key.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Ada Example")
				resp := e.Do(t, harness.Request{Path: authorURL(id, ""), Token: e.Token(t, "root"), Query: map[string][]string{"include": {"items"}}})
				wantStatus(t, "get author include=items", resp, http.StatusOK)
				page := decode[authorPage](t, resp.Body)
				if _, ok := resp.Object(t)["series"]; ok {
					t.Fatalf("include=items alone must not add series: %s", resp.Body)
				}
				var titles []string
				for _, item := range page.LibraryItems {
					if item.LibraryID != e.IDs["library"] {
						t.Fatalf("item with wrong library: %+v", item)
					}
					titles = append(titles, item.ID+"="+item.Media.Metadata.Title)
				}
				slices.Sort(titles)
				want := []string{e.IDs["ebook"] + "=Ebook Only", e.IDs["novel"] + "=Single File Novel"}
				slices.Sort(want)
				if !slices.Equal(titles, want) {
					t.Fatalf("libraryItems = %v, want %v", titles, want)
				}
				if page.ID != id || page.Name != "Ada Example" {
					t.Fatalf("author fields are still returned with include: %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "normal", State: "stateless", Assertion: "get-author-include-items-series",
			Sources: []string{"server/controllers/AuthorController.js:40-87"},
			Claim:   "include=items,series adds series groups whose items carry metadata.series {id,name,nameIgnorePrefix,sequence} and are ordered by sequence.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				resp := e.Do(t, harness.Request{Path: authorURL(id, ""), Token: e.Token(t, "listener"), Query: map[string][]string{"include": {"items,series"}}})
				wantStatus(t, "get author include=items,series", resp, http.StatusOK)
				page := decode[authorPage](t, resp.Body)
				if len(page.LibraryItems) != 2 || len(page.Series) != 1 {
					t.Fatalf("want 2 items and 1 series: %s", resp.Body)
				}
				group := page.Series[0]
				seriesID := e.Rows(t, "SELECT id FROM series WHERE name = 'Long Saga'")[0]["id"]
				if group.ID != seriesID || group.Name != "Long Saga" || len(group.Items) != 2 {
					t.Fatalf("series group = %+v", group)
				}
				for i, want := range []struct{ item, title, sequence string }{{e.IDs["saga1"], "The Beginning", "1"}, {e.IDs["saga2"], "The Middle", "2"}} {
					got := group.Items[i]
					series := got.Media.Metadata.Series
					if got.ID != want.item || got.Media.Metadata.Title != want.title || series == nil || series.ID != seriesID || series.Name != "Long Saga" || series.NameIgnorePrefix != "Long Saga" || series.Sequence != want.sequence {
						t.Fatalf("series item %d = %+v, want %v in series %v", i, got, want, seriesID)
					}
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "edge", State: "stateless", Assertion: "get-author-include-without-items",
			Sources: []string{"server/controllers/AuthorController.js:40-87"},
			Claim:   "include=series without items, an empty include and an unknown include return the plain author JSON.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				for _, include := range []string{"series", "", "nothing,else"} {
					resp := e.Do(t, harness.Request{Path: authorURL(id, ""), Token: e.Token(t, "root"), Query: map[string][]string{"include": {include}}})
					wantStatus(t, "include="+include, resp, http.StatusOK)
					body := resp.Object(t)
					if _, ok := body["libraryItems"]; ok {
						t.Fatalf("include=%q must not add libraryItems: %s", include, resp.Body)
					}
					if _, ok := body["series"]; ok {
						t.Fatalf("include=%q must not add series: %s", include, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ACCESS", Case: "error", State: "stateless", Assertion: "get-author-unauthenticated",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/AuthorController.js:427-442"},
			Claim:   "GET /api/authors/:id without a token or with an invalid token is rejected with 401.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Ada Example")
				wantStatus(t, "no token", e.Get(t, "", authorURL(id, "")), http.StatusUnauthorized)
				wantStatus(t, "bad token", e.Get(t, "not-a-token", authorURL(id, "")), http.StatusUnauthorized)
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ACCESS", Case: "error", State: "stateless", Assertion: "get-author-not-found",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/AuthorController.js:427-442"},
			Claim:   "GET /api/authors/:id answers 404 for an id that is not an author, with a UUID or any other string.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, id := range []string{"00000000-0000-4000-8000-000000000000", "no-such-author"} {
					wantStatus(t, "id "+id, e.Get(t, e.Token(t, "root"), authorURL(id, "")), http.StatusNotFound)
					wantStatus(t, "id "+id+" with items", e.Get(t, e.Token(t, "root"), authorURL(id, "?include=items")), http.StatusNotFound)
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ACCESS", Case: "error", State: "stateless", Assertion: "get-author-inaccessible-library",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/AuthorController.js:427-442"},
			Claim:   "A user who cannot access the author's library gets 404, not the author.",
			Run: func(t *testing.T, e *harness.Env) {
				restricted := ensureUser(t, e, "author-podcast-only", map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
				id := authorID(t, e, "Ada Example")
				wantStatus(t, "restricted get", e.Get(t, restricted.token, authorURL(id, "")), http.StatusNotFound)
				wantStatus(t, "restricted get with items", e.Get(t, restricted.token, authorURL(id, "?include=items")), http.StatusNotFound)
				if !strings.Contains(string(e.Get(t, e.Token(t, "root"), authorURL(id, "")).Body), id) {
					t.Fatal("the author must stay readable for users with access")
				}
			},
		},
	}
}
