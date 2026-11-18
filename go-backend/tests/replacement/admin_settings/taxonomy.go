package admin_settings

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	listSource   = "server/controllers/MiscController.js:297-336"
	genreList    = "server/controllers/MiscController.js:436-471"
	renameSource = "server/controllers/MiscController.js:341-391"
	genreRename  = "server/controllers/MiscController.js:479-527"
	deleteSource = "server/controllers/MiscController.js:396-428"
	genreDelete  = "server/controllers/MiscController.js:534-566"
	filterSource = "server/utils/queries/libraryItemFilters.js:12-110"
	metadataFile = "server/models/LibraryItem.js:612-700"
)

// invariant builds an invariant id such as INV-ROUTE-R209-ROUTE from a requirement id.
func invariant(requirement, suffix string) string {
	return "INV-ROUTE-" + strings.ToUpper(strings.TrimPrefix(requirement, "route-")) + "-" + suffix
}

// sources picks the handler line range for the kind and role.
func (k kind) sources(role string, extra ...string) []string {
	ranges := map[string][2]string{"list": {listSource, genreList}, "rename": {renameSource, genreRename}, "delete": {deleteSource, genreDelete}}
	pick := ranges[role][0]
	if k.name == "genre" {
		pick = ranges[role][1]
	}
	return append([]string{pick}, extra...)
}

// listScenarios are the reads that must run before any item carries tags or genres.
func (k kind) listScenarios() []harness.Scenario {
	req := k.req["list"]
	plural := k.field
	return []harness.Scenario{
		scenario(req, invariant(req, "ROUTE"), "error", "stateless", plural+"-list-admin-only",
			"GET "+k.listPath+" answers 401 without a token and 403 for a non-admin user.",
			k.sources("list"),
			func(t *testing.T, e *harness.Env) { adminOnly(t, e, http.MethodGet, k.listPath, nil) }),
		scenario(req, invariant(req, "ROUTE"), "normal", "stateless", plural+"-list-initial",
			"GET "+k.listPath+" returns {"+plural+": [...]} with the values of the fixture books and podcasts.",
			k.sources("list"),
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), k.listPath)
				wantStatus(t, k.listPath, resp, http.StatusOK)
				if got := stringList(t, resp.Object(t)[plural]); !sameJSON(sortedCopy(got), sortedCopy(k.initial)) {
					t.Fatalf("%s = %v, want %v", plural, got, k.initial)
				}
			}),
	}
}

// editScenarios cover seeding, rename and delete, in dependency order.
func (k kind) editScenarios() []harness.Scenario {
	list, rename, del := k.req["list"], k.req["rename"], k.req["delete"]
	p := k.field
	var out []harness.Scenario
	out = append(out, scenario(list, invariant(list, "ROUTE"), "edge", "stateless", p+"-list-union-unique",
		"GET "+k.listPath+" returns each value once across books and podcasts"+map[bool]string{true: ", sorted case-insensitively", false: ""}[k.name == "tag"]+".",
		k.sources("list"),
		func(t *testing.T, e *harness.Env) {
			k.seedItems(t, e, k.seed)
			resp := e.Get(t, e.Token(t, "root"), k.listPath)
			wantStatus(t, k.listPath, resp, http.StatusOK)
			got := stringList(t, resp.Object(t)[p])
			if k.name == "tag" && !sameJSON(got, k.union) {
				t.Fatalf("tags = %v, want %v in case-insensitive order", got, k.union)
			}
			if !sameJSON(sortedCopy(got), sortedCopy(k.union)) {
				t.Fatalf("%s = %v, want %v", p, got, k.union)
			}
		}))
	out = append(out,
		scenario(rename, invariant(rename, "ROUTE"), "error", "initial", p+"-rename-admin-only",
			"POST "+k.renamePath+" answers 401 without a token and 403 for a non-admin and changes nothing.",
			k.sources("rename"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				adminOnly(t, e, http.MethodPost, k.renamePath, map[string]any{k.oldKey: k.pure.from, k.newKey: "Other"})
				k.wantRows(t, e, before, nil)
			}),
		scenario(rename, invariant(rename, "ROUTE"), "error", "failure", p+"-rename-invalid-body-rejected",
			"POST "+k.renamePath+" answers 400 when the old or new value is missing or empty and changes nothing.",
			k.sources("rename"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				root := e.Token(t, "root")
				bodies := []any{
					map[string]any{}, map[string]any{k.oldKey: k.pure.from}, map[string]any{k.newKey: "x"},
					map[string]any{k.oldKey: "", k.newKey: "x"}, map[string]any{k.oldKey: k.pure.from, k.newKey: ""}, `{bad`,
				}
				for _, body := range bodies {
					wantStatus(t, "invalid rename body", e.Post(t, root, k.renamePath, body), http.StatusBadRequest)
				}
				k.wantRows(t, e, before, nil)
			}),
		scenario(rename, invariant(rename, "PERSIST-001"), "normal", "initial", p+"-rename-rewrites-rows-and-files",
			"Renaming a value to a new name rewrites it in every book and podcast row and in each item's metadata.json, and reports tagMerged/genreMerged false with the item count.",
			k.sources("rename", metadataFile, filterSource),
			func(t *testing.T, e *harness.Env) {
				settingsPatch(t, e, map[string]any{"storeMetadataWithItem": true})
				before, stamps := k.snapshot(t, e), k.stamps(t, e)
				k.wantRename(t, k.rename(t, e, k.pure.from, k.pure.to), k.pure)
				k.wantRows(t, e, before, k.pure.after)
				wantStamps(t, stamps, k.stamps(t, e), k.pure.after)
				k.wantFiles(t, e, k.pure.after, true) // storeMetadataWithItem was enabled by the settings scenarios
			}),
		scenario(rename, invariant(rename, "EVENT-001"), "normal", "initial", p+"-rename-emits-item-updated",
			"A rename emits item_updated with the new values to every user who can access each changed item and to nobody else.",
			k.sources("rename", "server/SocketAuthority.js:104-110"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				a := k.connect(t, e)
				k.wantRename(t, k.rename(t, e, k.events.from, k.events.to), k.events)
				k.wantEvents(t, e, a, k.events.after)
				k.wantRows(t, e, before, k.events.after)
			}),
		scenario(rename, invariant(rename, "ROUTE"), "edge", "initial", p+"-rename-merges-into-existing-value",
			"Renaming onto a value some item already has merges: no duplicate is stored, the response says merged, and the count covers items that had the old value.",
			k.sources("rename"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				k.wantRename(t, k.rename(t, e, k.merge.from, k.merge.to), k.merge)
				k.wantRows(t, e, before, k.merge.after)
				k.wantFiles(t, e, k.merge.after, true)
			}),
		scenario(rename, invariant(rename, "REPEAT"), "edge", "repeat", p+"-rename-repeat-changes-nothing",
			"Repeating a finished rename updates no item and emits no event; the merged flag stays true because items already hold the new value.",
			k.sources("rename"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				a := k.connect(t, e)
				again := k.merge
				again.num, again.after = 0, nil
				k.wantRename(t, k.rename(t, e, k.merge.from, k.merge.to), again)
				k.wantRows(t, e, before, nil)
				a.root.None(t, 0, "item_updated", 300*time.Millisecond, nil)
			}),
		scenario(rename, invariant(rename, "ROUTE"), "edge", "failure", p+"-rename-unknown-or-other-case-changes-nothing",
			"Renaming a value that no item has (matching is case-sensitive) answers 200 with numItemsUpdated 0 and changes nothing.",
			k.sources("rename"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				k.wantRename(t, k.rename(t, e, k.unknown, "Other"), step{})
				k.wantRows(t, e, before, nil)
			}),
		scenario(rename, invariant(rename, "ROUTE"), "normal", "changed", p+"-rename-visible-in-list-and-filterdata",
			"After renames, GET "+k.listPath+" and the library filter data show the new values and no longer the old ones.",
			k.sources("rename", "server/Database.js:476-483"),
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), k.listPath)
				if got := stringList(t, resp.Object(t)[p]); !sameJSON(sortedCopy(got), sortedCopy(k.afterMerge)) {
					t.Fatalf("%s = %v, want %v", p, got, k.afterMerge)
				}
				filter := k.libraryValues(t, e, "library")
				if !containsAll(filter, k.merge.to) || containsAny(filter, k.merge.from, k.pure.from, k.events.from) {
					t.Fatalf("book library filter %s = %v", p, filter)
				}
			}),
		scenario(del, invariant(del, "ROUTE"), "error", "initial", p+"-delete-admin-only",
			"DELETE "+k.deletePath+":value answers 401 without a token and 403 for a non-admin and changes nothing.",
			k.sources("delete"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				adminOnly(t, e, http.MethodDelete, k.deletePath+encodeValue(k.dropped.from), nil)
				k.wantRows(t, e, before, nil)
			}),
		scenario(del, invariant(del, "PERSIST-001"), "normal", "initial", p+"-delete-removes-from-rows-and-files",
			"Deleting a base64-encoded value removes it from every book and podcast row and rewrites metadata.json under metadata/items when storeMetadataWithItem is off.",
			k.sources("delete", metadataFile),
			func(t *testing.T, e *harness.Env) {
				settingsPatch(t, e, map[string]any{"storeMetadataWithItem": false})
				before, stamps := k.snapshot(t, e), k.stamps(t, e)
				wantDelete(t, k.remove(t, e, k.dropped.from), k.dropped.num)
				k.wantRows(t, e, before, k.dropped.after)
				wantStamps(t, stamps, k.stamps(t, e), k.dropped.after)
				k.wantFiles(t, e, k.dropped.after, false)
			}),
		scenario(del, invariant(del, "EVENT-001"), "edge", "initial", p+"-delete-encoded-value-emits-item-updated",
			"A value whose base64 contains '+' and '/' is deleted through the client's URL encoding, and item_updated reaches only users who can access the item.",
			k.sources("delete", "server/SocketAuthority.js:104-110"),
			func(t *testing.T, e *harness.Env) {
				k.seedItems(t, e, map[string][]string{"saga2": {k.encoded}})
				before := k.snapshot(t, e)
				a := k.connect(t, e)
				wantDelete(t, k.remove(t, e, k.encoded), 1)
				after := map[string][]string{"saga2": {}}
				k.wantRows(t, e, before, after)
				k.wantEvents(t, e, a, after)
			}),
		scenario(del, invariant(del, "REPEAT"), "edge", "repeat", p+"-delete-repeat-changes-nothing",
			"Deleting an already deleted value answers 200 with numItemsUpdated 0 and emits no event.",
			k.sources("delete"),
			func(t *testing.T, e *harness.Env) {
				before, stamps := k.snapshot(t, e), k.stamps(t, e)
				a := k.connect(t, e)
				wantDelete(t, k.remove(t, e, k.dropped.from), 0)
				k.wantRows(t, e, before, nil)
				wantStamps(t, stamps, k.stamps(t, e), nil)
				a.root.None(t, 0, "item_updated", 300*time.Millisecond, nil)
			}),
		scenario(del, invariant(del, "ROUTE"), "edge", "failure", p+"-delete-other-case-changes-nothing",
			"Deleting a value that exists only in another letter case matches nothing and changes nothing.",
			k.sources("delete"),
			func(t *testing.T, e *harness.Env) {
				before := k.snapshot(t, e)
				wantDelete(t, k.remove(t, e, k.unknown), 0)
				k.wantRows(t, e, before, nil)
			}),
		scenario(del, invariant(del, "ROUTE"), "normal", "changed", p+"-delete-visible-in-list-and-filterdata",
			"After deletes, GET "+k.listPath+" and the library filter data no longer contain the deleted value.",
			k.sources("delete", "server/Database.js:485-489"),
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), k.listPath)
				if got := stringList(t, resp.Object(t)[p]); !sameJSON(sortedCopy(got), sortedCopy(k.final)) {
					t.Fatalf("%s = %v, want %v", p, got, k.final)
				}
				for _, library := range []string{"library", "podcastLibrary"} {
					if filter := k.libraryValues(t, e, library); containsAny(filter, k.dropped.from, k.encoded) {
						t.Fatalf("%s filter %s = %v still has a deleted value", library, p, filter)
					}
				}
			}),
	)
	return out
}

func containsAll(list []string, values ...string) bool {
	for _, value := range values {
		if !containsAny(list, value) {
			return false
		}
	}
	return true
}

func containsAny(list []string, values ...string) bool {
	for _, item := range list {
		for _, value := range values {
			if item == value {
				return true
			}
		}
	}
	return false
}
