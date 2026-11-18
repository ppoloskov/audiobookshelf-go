package library_data

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	narratorsRoute = "server/routers/ApiRouter.js:87-89"
	narratorsSrc   = "server/controllers/LibraryController.js:1110-1240"
)

type narratorJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NumBooks int    `json:"numBooks"`
}

func getNarrators(t *testing.T, e *harness.Env, token, libraryID string) []narratorJSON {
	t.Helper()
	resp := e.Get(t, token, libPath(libraryID, "/narrators"))
	requireStatus(t, resp, http.StatusOK, "narrators")
	var body struct {
		Narrators []narratorJSON `json:"narrators"`
	}
	resp.JSON(t, &body)
	if body.Narrators == nil {
		t.Fatalf("narrators must be an array: %s", resp.Body)
	}
	return body.Narrators
}

// narratorTable is the narrators list as name -> numBooks.
func narratorTable(list []narratorJSON) map[string]int {
	out := map[string]int{}
	for _, n := range list {
		out[n.Name] = n.NumBooks
	}
	return out
}

// bookNarrators returns the stored narrators of the book behind a library item id.
func bookNarrators(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	rows := e.Rows(t, "SELECT b.narrators FROM books b JOIN libraryItems li ON li.mediaId = b.id WHERE li.id = ?", itemID)
	if len(rows) != 1 {
		t.Fatalf("no book for item %s", itemID)
	}
	return jsonStrings(t, rows[0]["narrators"])
}

// timestamps returns books.updatedAt and libraryItems.updatedAt of an item.
func timestamps(t *testing.T, e *harness.Env, itemID string) (book, item string) {
	t.Helper()
	rows := e.Rows(t, "SELECT b.updatedAt AS bookAt, li.updatedAt AS itemAt FROM books b JOIN libraryItems li ON li.mediaId = b.id WHERE li.id = ?", itemID)
	if len(rows) != 1 {
		t.Fatalf("no row for item %s", itemID)
	}
	return fmt.Sprint(rows[0]["bookAt"]), fmt.Sprint(rows[0]["itemAt"])
}

// itemMetadataDigest hashes metadata/items/<id>/metadata.json; it fails when the file is missing.
func itemMetadataDigest(t *testing.T, e *harness.Env, itemID string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Metadata, "items", itemID, "metadata.json"))
	if err != nil {
		t.Fatalf("item metadata file: %v", err)
	}
	return sha256.Sum256(data)
}

// updatedItem is the part of an items_updated entry clients read.
type updatedItem struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Media     struct {
		Metadata struct {
			Narrators    []string `json:"narrators"`
			NarratorName string   `json:"narratorName"`
			Title        string   `json:"title"`
		} `json:"metadata"`
	} `json:"media"`
}

// itemsUpdated returns the entries of every items_updated event a socket received since its mark.
func (a *audience) itemsUpdated(t *testing.T, name string) []updatedItem {
	t.Helper()
	var all []updatedItem
	for _, raw := range a.received(name, "items_updated") {
		var items []updatedItem
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("items_updated payload is not an array of items: %v (%s)", err, raw)
		}
		all = append(all, items...)
	}
	return all
}

// expectItemsUpdated waits for items_updated to carry want (item id -> narrators) on the socket and checks
// the payload fields, then that the other sockets stay silent.
func (a *audience) expectItemsUpdated(t *testing.T, libraryID string, want map[string][]string, receivers, absent []string) {
	t.Helper()
	for _, name := range receivers {
		harness.Eventually(t, eventWait, "items_updated for "+name, func() bool { return len(a.itemsUpdated(t, name)) >= len(want) })
		got := a.itemsUpdated(t, name)
		if len(got) != len(want) {
			t.Fatalf("%s got %d updated items, want %d", name, len(got), len(want))
		}
		for _, item := range got {
			narrators, ok := want[item.ID]
			if !ok || item.LibraryID != libraryID || !slices.Equal(item.Media.Metadata.Narrators, narrators) || item.Media.Metadata.NarratorName != joinNames(narrators) || item.Media.Metadata.Title == "" {
				t.Fatalf("%s: unexpected updated item %+v (want %v)", name, item, want)
			}
		}
	}
	a.silent(t, "items_updated", absent...)
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}

func narratorPath(libraryID, name string) string {
	return libPath(libraryID, "/narrators/"+encodeNarratorID(name))
}

// itemSnapshot is what a narrator write must (not) change for a set of items.
type itemSnapshot struct {
	Book, Item string
	Narrators  []string
}

func snapshotItems(t *testing.T, e *harness.Env, keys ...string) map[string]itemSnapshot {
	out := map[string]itemSnapshot{}
	for _, key := range keys {
		book, item := timestamps(t, e, e.IDs[key])
		out[key] = itemSnapshot{Book: book, Item: item, Narrators: bookNarrators(t, e, e.IDs[key])}
	}
	return out
}

func narratorScenarios(s *suite) []harness.Scenario {
	allKeys := []string{"saga1", "saga2", "novel", "ebook", "disposable"}
	return []harness.Scenario{
		{
			Requirement: "route-r034", Invariant: "INV-ROUTE-R034-CONTRACT", Case: "normal", State: "stateless", Assertion: "narrators-list-fixture",
			Sources: []string{narratorsRoute, narratorsSrc},
			Claim:   "GET /api/libraries/:id/narrators of the fixture book library answers {narrators} with the one narrator: id encodeURIComponent(base64(name)), name and numBooks 1; a podcast library answers an empty array.",
			Run: func(t *testing.T, e *harness.Env) {
				got := getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib])
				requireEqual(t, "narrators", got, []narratorJSON{{ID: "Tm9yYSBOYXJyYXRvcg%3D%3D", Name: "Nora Narrator", NumBooks: 1}})
				requireEqual(t, "podcast narrators", len(getNarrators(t, e, e.Token(t, "root"), e.IDs[podcastLib])), 0)
				requireEqual(t, "listener sees the same", getNarrators(t, e, e.Token(t, "listener"), e.IDs[bookLib]), got)
			},
		},
		{
			Requirement: "route-r034", Invariant: "INV-ROUTE-R034-SORT-001", Case: "edge", State: "stateless", Assertion: "narrators-counts-natural-sort-and-ids",
			Sources: []string{narratorsRoute, "server/controllers/LibraryController.js:1110-1148"},
			Claim:   "The narrators list counts the books of each narrator, sorts names naturally (Narrator 2 before Narrator 10), and encodes ids whose base64 contains + and = as %2B and %3D.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				patchMedia(t, e, e.IDs["disposable"], `{"metadata":{"narrators":["Narrator 10","Narrator 2","Q?>>"]}}`)
				got := getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib])
				want := []narratorJSON{
					{encodeNarratorID("Narrator 2"), "Narrator 2", 1}, {encodeNarratorID("Narrator 10"), "Narrator 10", 1},
					{encodeNarratorID("Nora Narrator"), "Nora Narrator", 2}, {encodeNarratorID("Q?>>"), "Q?>>", 1}, {encodeNarratorID("Vera Voice"), "Vera Voice", 2},
				}
				requireEqual(t, "narrators", got, want)
				for name, token := range map[string]string{"listener": e.Token(t, "listener"), "finisher": e.Token(t, "finisher")} {
					requireEqual(t, name+" sees the same list", getNarrators(t, e, token, e.IDs[bookLib]), got)
				}
				tagged := s.createUser(t, e, "data-tag-allow", `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":false,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`)
				requireEqual(t, "tag-restricted user sees the same list", getNarrators(t, e, tagged.Token, e.IDs[bookLib]), got)
				if got[3].ID != "UT8%2BPg%3D%3D" {
					t.Fatalf("id of Q?>> is %q", got[3].ID)
				}
			},
		},
		{
			Requirement: "route-r034", Invariant: "INV-ROUTE-R034-AUTH-001", Case: "error", State: "stateless", Assertion: "narrators-list-denied-and-unknown",
			Sources: []string{narratorsRoute, middleware},
			Claim:   "GET narrators answers 401 without a token, 403 without library access (also for an unknown id), 404 for an unknown id and 400 for an invalid page.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.podcastOnly(t, e)
				path := libPath(e.IDs[bookLib], "/narrators")
				requireStatus(t, e.Get(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, limited.Token, path), http.StatusForbidden, "limited")
				requireStatus(t, e.Get(t, limited.Token, libPath(missingID, "/narrators")), http.StatusForbidden, "limited unknown")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/narrators")), http.StatusNotFound, "unknown")
				requireStatus(t, e.Get(t, e.Token(t, "root"), path+"?page=-3"), http.StatusBadRequest, "bad page")
			},
		},
		{
			Requirement: "route-r035", Invariant: "INV-ROUTE-R035-CONTRACT", Case: "normal", State: "initial", Assertion: "narrator-rename-moves-name-to-end",
			Sources: []string{"server/routers/ApiRouter.js:88", "server/controllers/LibraryController.js:1157-1199", "server/utils/queries/libraryItemFilters.js:139-175", "server/SocketAuthority.js:119-131", "server/Database.js:524-531"},
			Claim:   "PATCH narrators/:id renames the narrator in every book of the library (the new name is appended after the others), answers {updated: n}, bumps only those books' updatedAt, leaves other libraries and the item metadata file alone, emits items_updated with those items to users who can access the library, and updates the cached filter data.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				other := newScratchLibrary(t, e, "narr-other", nil, nil)
				copyDir(t, filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me"), filepath.Join(other.Root, "Other", "Other Book"))
				other.scan(t, e, false, 1)
				otherItem := itemRows(t, e, other.ID)[0].ID
				patchMedia(t, e, otherItem, `{"metadata":{"narrators":["Nora Narrator"]}}`)
				before := snapshotItems(t, e, allKeys...)
				otherBefore := bookNarrators(t, e, otherItem)
				digests := map[string][32]byte{}
				for _, key := range allKeys {
					digests[key] = itemMetadataDigest(t, e, e.IDs[key])
				}
				requireEqual(t, "novel before", before["novel"].Narrators, []string{"Nora Narrator", "Vera Voice"})
				requireEqual(t, "saga2 before", before["saga2"].Narrators, []string{"Nora Narrator"})
				filterBefore := getFilterData(t, e, e.Token(t, "root"), e.IDs[bookLib])
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": s.podcastOnly(t, e).Token, "anonymous": ""})
				resp := e.Patch(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Nora Narrator"), map[string]any{"name": "Nora Renamed"})
				requireStatus(t, resp, http.StatusOK, "rename")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "answer", body, map[string]any{"updated": float64(2)})
				after := snapshotItems(t, e, allKeys...)
				requireEqual(t, "novel", after["novel"].Narrators, []string{"Vera Voice", "Nora Renamed"})
				requireEqual(t, "saga2", after["saga2"].Narrators, []string{"Nora Renamed"})
				for _, key := range allKeys {
					changed := key == "novel" || key == "saga2"
					if changed && after[key].Book <= before[key].Book {
						t.Fatalf("%s: books.updatedAt must move forward: %s -> %s", key, before[key].Book, after[key].Book)
					}
					if !changed && (after[key].Book != before[key].Book || !slices.Equal(after[key].Narrators, before[key].Narrators)) {
						t.Fatalf("%s must not change: %+v -> %+v", key, before[key], after[key])
					}
					if after[key].Item != before[key].Item {
						t.Fatalf("%s: libraryItems.updatedAt must not change", key)
					}
					if itemMetadataDigest(t, e, e.IDs[key]) != digests[key] {
						t.Fatalf("%s: metadata.json must not be rewritten", key)
					}
				}
				requireEqual(t, "other library untouched", bookNarrators(t, e, otherItem), otherBefore)
				a.expectItemsUpdated(t, e.IDs[bookLib], map[string][]string{e.IDs["novel"]: {"Vera Voice", "Nora Renamed"}, e.IDs["saga2"]: {"Nora Renamed"}}, []string{"root", "listener"}, []string{"limited", "anonymous"})
				requireEqual(t, "list", narratorTable(getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib])), map[string]int{"Narrator 2": 1, "Narrator 10": 1, "Nora Renamed": 2, "Q?>>": 1, "Vera Voice": 2})
				requireEqual(t, "other library list", narratorTable(getNarrators(t, e, e.Token(t, "root"), other.ID)), map[string]int{"Nora Narrator": 1})
				filterAfter := getFilterData(t, e, e.Token(t, "root"), e.IDs[bookLib])
				if !slices.Contains(filterAfter.Narrators, "Nora Renamed") || slices.Contains(filterAfter.Narrators, "Nora Narrator") || len(filterAfter.Narrators) != len(filterBefore.Narrators) {
					t.Fatalf("filterdata narrators %v -> %v", filterBefore.Narrators, filterAfter.Narrators)
				}
			},
		},
		{
			Requirement: "route-r035", Invariant: "INV-ROUTE-R035-MERGE-001", Case: "edge", State: "changed", Assertion: "narrator-rename-to-existing-name-merges",
			Sources: []string{"server/controllers/LibraryController.js:1176-1186"},
			Claim:   "Renaming a narrator to a name the book already has removes the old name without adding a duplicate, and books that only had the old name get the new one.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				resp := e.Patch(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Vera Voice"), map[string]any{"name": "Nora Renamed"})
				requireStatus(t, resp, http.StatusOK, "merge")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "answer", body, map[string]any{"updated": float64(2)})
				requireEqual(t, "novel", bookNarrators(t, e, e.IDs["novel"]), []string{"Nora Renamed"})
				requireEqual(t, "saga1", bookNarrators(t, e, e.IDs["saga1"]), []string{"Nora Renamed"})
				a.expectItemsUpdated(t, e.IDs[bookLib], map[string][]string{e.IDs["novel"]: {"Nora Renamed"}, e.IDs["saga1"]: {"Nora Renamed"}}, []string{"root"}, nil)
				requireEqual(t, "list", narratorTable(getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib]))["Nora Renamed"], 3)
			},
		},
		{
			Requirement: "route-r035", Invariant: "INV-ROUTE-R035-ENCODING-001", Case: "edge", State: "repeat", Assertion: "narrator-rename-encoded-id-and-unknown-name",
			Sources: []string{"server/controllers/LibraryController.js:1157-1199", "server/utils/queries/libraryFilters.js:14-21"},
			Claim:   "A narrator whose id contains percent-encoded + and = is renamed to a Unicode name; renaming a name no book has answers {updated: 0}, changes nothing and emits no event.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				resp := e.Patch(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Q?>>"), map[string]any{"name": "Ünï Çödé"})
				requireStatus(t, resp, http.StatusOK, "rename encoded")
				requireEqual(t, "disposable", bookNarrators(t, e, e.IDs["disposable"]), []string{"Narrator 10", "Narrator 2", "Ünï Çödé"})
				a.expectItemsUpdated(t, e.IDs[bookLib], map[string][]string{e.IDs["disposable"]: {"Narrator 10", "Narrator 2", "Ünï Çödé"}}, []string{"root"}, nil)
				a.rearm()
				before := snapshotItems(t, e, allKeys...)
				resp = e.Patch(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Nora Narrator"), map[string]any{"name": "Anything"})
				requireStatus(t, resp, http.StatusOK, "unknown narrator")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "answer", body, map[string]any{"updated": float64(0)})
				requireEqual(t, "unchanged", snapshotItems(t, e, allKeys...), before)
				a.silent(t, "items_updated", "root")
			},
		},
		{
			Requirement: "route-r035", Invariant: "INV-ROUTE-R035-AUTH-001", Case: "error", State: "failure", Assertion: "narrator-rename-denied-and-invalid",
			Sources: []string{"server/routers/ApiRouter.js:88", "server/controllers/LibraryController.js:1157-1170", middleware},
			Claim:   "PATCH narrators/:id answers 401 without a token, 403 for a user without update permission, 403 without library access, 404 for an unknown library, 400 for a missing, empty or null name and for an invalid page (before the permission check), and changes and emits nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.createUser(t, e, "data-limited-updater", `"permissions":{"accessAllLibraries":false,"update":true},"librariesAccessible":["`+e.IDs[podcastLib]+`"]`)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before := snapshotItems(t, e, allKeys...)
				path := narratorPath(e.IDs[bookLib], "Nora Renamed")
				body := map[string]any{"name": "Blocked"}
				requireStatus(t, e.Patch(t, "", path, body), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), path, body), http.StatusForbidden, "no update permission")
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), path, map[string]any{}), http.StatusForbidden, "permission before name")
				requireStatus(t, e.Patch(t, limited.Token, path, body), http.StatusForbidden, "no library access")
				requireStatus(t, e.Patch(t, e.Token(t, "root"), narratorPath(missingID, "Nora Renamed"), body), http.StatusNotFound, "unknown library")
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), path+"?limit=-1", body), http.StatusBadRequest, "paging before permission")
				for name, payload := range map[string]any{"missing": map[string]any{}, "empty": map[string]any{"name": ""}, "null": map[string]any{"name": nil}} {
					requireStatus(t, e.Patch(t, e.Token(t, "root"), path, payload), http.StatusBadRequest, name+" name")
				}
				requireEqual(t, "unchanged", snapshotItems(t, e, allKeys...), before)
				a.silent(t, "items_updated", "root")
			},
		},
		{
			Requirement: "route-r036", Invariant: "INV-ROUTE-R036-CONTRACT", Case: "normal", State: "initial", Assertion: "narrator-remove-from-books",
			Sources: []string{"server/routers/ApiRouter.js:89", "server/controllers/LibraryController.js:1205-1240", "server/Database.js:532-536", "server/SocketAuthority.js:119-131"},
			Claim:   "DELETE narrators/:id removes the narrator from every book of the library keeping the others in order, answers {updated: n}, bumps only those books' updatedAt, leaves the item metadata file alone, emits items_updated to users who can access the library, and drops the name from the cached filter data and the narrators list.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e, allKeys...)
				digest := itemMetadataDigest(t, e, e.IDs["disposable"])
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": s.podcastOnly(t, e).Token, "anonymous": ""})
				resp := e.Delete(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Narrator 2"))
				requireStatus(t, resp, http.StatusOK, "remove")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "answer", body, map[string]any{"updated": float64(1)})
				after := snapshotItems(t, e, allKeys...)
				requireEqual(t, "disposable", after["disposable"].Narrators, []string{"Narrator 10", "Ünï Çödé"})
				if after["disposable"].Book <= before["disposable"].Book || after["disposable"].Item != before["disposable"].Item {
					t.Fatalf("timestamps: %+v -> %+v", before["disposable"], after["disposable"])
				}
				for _, key := range []string{"saga1", "saga2", "novel", "ebook"} {
					requireEqual(t, key+" unchanged", after[key], before[key])
				}
				if itemMetadataDigest(t, e, e.IDs["disposable"]) != digest {
					t.Fatal("metadata.json must not be rewritten")
				}
				a.expectItemsUpdated(t, e.IDs[bookLib], map[string][]string{e.IDs["disposable"]: {"Narrator 10", "Ünï Çödé"}}, []string{"root", "listener"}, []string{"limited", "anonymous"})
				if _, ok := narratorTable(getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib]))["Narrator 2"]; ok {
					t.Fatal("narrator still listed")
				}
				if slices.Contains(getFilterData(t, e, e.Token(t, "root"), e.IDs[bookLib]).Narrators, "Narrator 2") {
					t.Fatal("narrator still in filterdata")
				}
			},
		},
		{
			Requirement: "route-r036", Invariant: "INV-ROUTE-R036-MANY-001", Case: "edge", State: "changed", Assertion: "narrator-remove-renamed-name-from-many-books",
			Sources: []string{"server/controllers/LibraryController.js:1205-1240", "server/controllers/LibraryController.js:1157-1199"},
			Claim:   "After renames merged three books under one name, removing that name leaves each book's other narrators (an empty list for books with none), answers {updated: 3}, emits items_updated for the three and empties the narrators list of it.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				resp := e.Delete(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Nora Renamed"))
				requireStatus(t, resp, http.StatusOK, "remove")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "answer", body, map[string]any{"updated": float64(3)})
				for _, key := range []string{"novel", "saga1", "saga2"} {
					requireEqual(t, key, bookNarrators(t, e, e.IDs[key]), []string{})
				}
				a.expectItemsUpdated(t, e.IDs[bookLib], map[string][]string{e.IDs["novel"]: {}, e.IDs["saga1"]: {}, e.IDs["saga2"]: {}}, []string{"root"}, nil)
				requireEqual(t, "list", narratorTable(getNarrators(t, e, e.Token(t, "root"), e.IDs[bookLib])), map[string]int{"Narrator 10": 1, "Ünï Çödé": 1})
			},
		},
		{
			Requirement: "route-r036", Invariant: "INV-ROUTE-R036-REPEAT-001", Case: "edge", State: "repeat", Assertion: "narrator-remove-repeat-is-a-noop",
			Sources: []string{"server/controllers/LibraryController.js:1205-1240"},
			Claim:   "Removing a narrator that no book has any more answers {updated: 0}, changes nothing and emits no event.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before := snapshotItems(t, e, allKeys...)
				resp := e.Delete(t, e.Token(t, "root"), narratorPath(e.IDs[bookLib], "Nora Renamed"))
				requireStatus(t, resp, http.StatusOK, "repeat")
				var body map[string]any
				resp.JSON(t, &body)
				requireEqual(t, "repeat answer", body, map[string]any{"updated": float64(0)})
				requireEqual(t, "unchanged", snapshotItems(t, e, allKeys...), before)
				a.silent(t, "items_updated", "root")
			},
		},
		{
			Requirement: "route-r036", Invariant: "INV-ROUTE-R036-AUTH-001", Case: "error", State: "failure", Assertion: "narrator-remove-denied-and-unknown-library",
			Sources: []string{"server/routers/ApiRouter.js:89", "server/controllers/LibraryController.js:1205-1212", middleware},
			Claim:   "DELETE narrators/:id answers 401 without a token, 403 for a user without update permission and for one without library access, 404 for an unknown library, 400 for an invalid page, and changes and emits nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.createUser(t, e, "data-limited-updater", `"permissions":{"accessAllLibraries":false,"update":true},"librariesAccessible":["`+e.IDs[podcastLib]+`"]`)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before := snapshotItems(t, e, allKeys...)
				path := narratorPath(e.IDs[bookLib], "Narrator 10")
				requireStatus(t, e.Delete(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden, "no update permission")
				requireStatus(t, e.Delete(t, limited.Token, path), http.StatusForbidden, "no library access")
				requireStatus(t, e.Delete(t, e.Token(t, "root"), narratorPath(missingID, "Narrator 10")), http.StatusNotFound, "unknown library")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path+"?page=x1.5"), http.StatusForbidden, "non numeric page is 0")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path+"?limit=-1"), http.StatusBadRequest, "paging before permission")
				requireEqual(t, "unchanged", snapshotItems(t, e, allKeys...), before)
				a.silent(t, "items_updated", "root")
			},
		},
	}
}
