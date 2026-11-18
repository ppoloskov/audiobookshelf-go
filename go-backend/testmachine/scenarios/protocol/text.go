package protocol

import (
	"bytes"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// libraryItemsPage is the paginated item list the clients sort.
type libraryItemsPage struct {
	Total   int `json:"total"`
	Results []struct {
		Media struct {
			Metadata struct {
				Title string `json:"title"`
			} `json:"metadata"`
		} `json:"media"`
	} `json:"results"`
}

// titles returns the results' media titles in response order.
func (p libraryItemsPage) titles() []string {
	out := make([]string, 0, len(p.Results))
	for _, item := range p.Results {
		out = append(out, item.Media.Metadata.Title)
	}
	return out
}

// libraryItems reads one sorted page.
func libraryItems(t *testing.T, e *harness.Env, token, libraryID, sort, desc string) libraryItemsPage {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+libraryID+"/items?sort="+sort+"&desc="+desc+"&limit=20")
	wantStatus(t, resp, http.StatusOK, "items sort "+sort)
	var page libraryItemsPage
	resp.JSON(t, &page)
	return page
}

// wantTitles compares an ordered title list.
func wantTitles(t *testing.T, got []string, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d titles %v, want %d %v", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: titles = %v, want %v", what, got, want)
		}
	}
}

// textOrderingScenarios covers the JS-derived sort keys and the item order
// clients observe.
func textOrderingScenarios() []harness.Scenario {
	const req = "text-client-ordering"
	const keyInv = "INV-TEXT-CLIENT-ORDERING"
	const sortInv = "INV-TEXT-CLIENT-ORDERING-SORT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: keyInv, Case: "normal", State: "stateless", Assertion: "stored-sort-keys-follow-the-prefix-and-last-first-rules",
			Sources: []string{
				"server/utils/index.js:181-212",
				"server/utils/parsers/parseNameString.js:31-36",
				"server/controllers/MiscController.js:183-260",
				"server/Database.js:845-900",
			},
			Claim: "The seeded sort keys are the JS-derived values: books and libraryItems store titleIgnorePrefix \"Beginning\" for \"The Beginning\" and \"Middle\" for \"The Middle\" (prefix \"the\" plus one space removed, case-insensitively) and leave an unprefixed title unchanged; authors.lastFirst is \"Disposable, Zed\", \"Example, Ada\" and \"Fixture, Terry\".",
			Run: func(t *testing.T, e *harness.Env) {
				wantPrefix := map[string]string{"The Beginning": "Beginning", "The Middle": "Middle", "Ebook Only": "Ebook Only"}
				for title, want := range wantPrefix {
					rows := e.Rows(t, "SELECT titleIgnorePrefix FROM books WHERE title = ?", title)
					if len(rows) != 1 {
						t.Fatalf("books %q: %d rows", title, len(rows))
					}
					if got, _ := rows[0]["titleIgnorePrefix"].(string); got != want {
						t.Fatalf("books.titleIgnorePrefix for %q = %q, want %q", title, got, want)
					}
					rows = e.Rows(t, "SELECT titleIgnorePrefix FROM libraryItems WHERE title = ?", title)
					if len(rows) != 1 {
						t.Fatalf("libraryItems %q: %d rows", title, len(rows))
					}
					if got, _ := rows[0]["titleIgnorePrefix"].(string); got != want {
						t.Fatalf("libraryItems.titleIgnorePrefix for %q = %q, want %q", title, got, want)
					}
				}
				wantLastFirst := map[string]string{"Zed Disposable": "Disposable, Zed", "Ada Example": "Example, Ada", "Terry Fixture": "Fixture, Terry"}
				for name, want := range wantLastFirst {
					rows := e.Rows(t, "SELECT lastFirst FROM authors WHERE name = ?", name)
					if len(rows) != 1 {
						t.Fatalf("authors %q: %d rows", name, len(rows))
					}
					if got, _ := rows[0]["lastFirst"].(string); got != want {
						t.Fatalf("authors.lastFirst for %q = %q, want %q", name, got, want)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: sortInv, Case: "edge", State: "stateless", Assertion: "item-sorts-use-the-stored-keys-and-the-prefix-setting",
			Sources: []string{
				"server/utils/queries/libraryItemsBookFilters.js:248-305",
				"server/controllers/LibraryController.js:604-680",
				"server/controllers/MiscController.js:139-175",
			},
			Claim: "With the default sortingIgnorePrefix=false, sort=media.metadata.title returns Ebook Only, Hard Delete Me, Single File Novel, The Beginning, The Middle, while sort=media.metadata.authorNameLF orders by lastFirst (Disposable, Zed; Example, Ada; Fixture, Terry) with title as the tiebreak (Ebook Only before Single File Novel); setting sortingIgnorePrefix=true reorders the title sort by the stored prefix-stripped key (The Beginning first, The Middle before Single File Novel, with the displayed titles unchanged), and restoring false returns the original order.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				library := e.IDs["library"]
				defaultOrder := []string{"Ebook Only", "Hard Delete Me", "Single File Novel", "The Beginning", "The Middle"}
				wantTitles(t, libraryItems(t, e, token, library, "media.metadata.title", "0").titles(), defaultOrder, "default title sort")
				wantTitles(t, libraryItems(t, e, token, library, "media.metadata.authorNameLF", "0").titles(),
					[]string{"Hard Delete Me", "Ebook Only", "Single File Novel", "The Beginning", "The Middle"}, "authorNameLF sort")

				wantStatus(t, e.Patch(t, token, "/api/settings", map[string]any{"sortingIgnorePrefix": true}), http.StatusOK, "enable sortingIgnorePrefix")
				t.Cleanup(func() {
					if resp := e.Patch(t, token, "/api/settings", map[string]any{"sortingIgnorePrefix": false}); resp.Status != http.StatusOK {
						t.Errorf("restore sortingIgnorePrefix: status %d", resp.Status)
					}
				})
				wantTitles(t, libraryItems(t, e, token, library, "media.metadata.title", "0").titles(),
					[]string{"The Beginning", "Ebook Only", "Hard Delete Me", "The Middle", "Single File Novel"}, "title sort with prefixes ignored")
				wantStatus(t, e.Patch(t, token, "/api/settings", map[string]any{"sortingIgnorePrefix": false}), http.StatusOK, "disable sortingIgnorePrefix")
				wantTitles(t, libraryItems(t, e, token, library, "media.metadata.title", "0").titles(), defaultOrder, "title sort after restore")
				reversed := []string{"The Middle", "The Beginning", "Single File Novel", "Hard Delete Me", "Ebook Only"}
				wantTitles(t, libraryItems(t, e, token, library, "media.metadata.title", "1").titles(), reversed, "descending title sort")
			},
		},
		{
			Requirement: req, Invariant: sortInv, Case: "error", State: "stateless", Assertion: "unknown-library-read-returns-no-ordering-data",
			Sources: []string{"server/controllers/LibraryController.js:604-615", "server/routers/ApiRouter.js:47-80"},
			Claim:   "An item list for an unknown library id answers 404 with no results page; an unknown sort key is ignored and answers 200 in the server's default order rather than failing.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				missing := e.Get(t, token, "/api/libraries/00000000-0000-4000-8000-000000000000/items?limit=20")
				wantStatus(t, missing, http.StatusNotFound, "unknown library")
				if bytes.Contains(missing.Body, []byte(`"results"`)) {
					t.Fatalf("404 body carried a results page: %.200s", missing.Body)
				}
				defaults := libraryItems(t, e, token, e.IDs["library"], "", "0")
				unknown := libraryItems(t, e, token, e.IDs["library"], "not.a.real.field", "0")
				wantTitles(t, unknown.titles(), defaults.titles(), "unknown sort key")
			},
		},
	}
}
