package item_read

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const downloadItemSource = "server/controllers/LibraryItemController.js:169-198"

// wantItemZip checks the zip answer of GET /api/items/:id/download for a folder item.
func wantItemZip(t *testing.T, e *harness.Env, resp harness.Response, itemID string) {
	t.Helper()
	wantStatus(t, "item download", resp, http.StatusOK)
	if got := contentType(t, resp); got != "application/zip" {
		t.Fatalf("Content-Type %q, want application/zip", got)
	}
	title, _ := itemRow(t, e, itemID)["title"].(string)
	if kind, name := attachment(t, resp); kind != "attachment" || name != title+".zip" {
		t.Fatalf("Content-Disposition %q filename %q, want attachment %q", kind, name, title+".zip")
	}
	wantZipOfDirectory(t, resp.Body, itemPath(t, e, itemID))
}

// itemDownloadScenarios covers GET /api/items/:id/download.
func itemDownloadScenarios(s *setup) []harness.Scenario {
	build := func(invariant, assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r052", Invariant: invariant, Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{downloadItemSource, middlewareSource, "server/utils/zipHelpers.js:6-55"}, Claim: claim, Run: run,
		}
	}
	route := func(t *testing.T, e *harness.Env, itemID string) string { return "/api/items/" + itemID + "/download" }
	scenarios := []harness.Scenario{
		build("INV-R052-ZIP-001", "download-zip-of-multifile-item", "normal",
			"The download of a multi-file item is a readable zip, named after the item title, whose entries are exactly the files of the item folder with unchanged bytes.",
			func(t *testing.T, e *harness.Env) {
				wantItemZip(t, e, get(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs["saga1"]+"/download", nil), e.IDs["saga1"])
			}),
		build("INV-R052-ZIP-001", "download-zip-of-other-folders", "edge",
			"A folder item with one audio file, one with sidecar text files, the ebook item and the podcast (cover and episodes) are also zipped completely.",
			func(t *testing.T, e *harness.Env) {
				for _, key := range []string{"novel", "saga2", "ebook", "podcast"} {
					wantItemZip(t, e, get(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs[key]+"/download", nil), e.IDs[key])
				}
			}),
		build("INV-R052-ZIP-001", "download-zip-without-update-permission", "edge",
			"The download needs only the download permission: a user without update and delete rights and a token query parameter both succeed.",
			func(t *testing.T, e *harness.Env) {
				wantItemZip(t, e, get(t, e, s.token(t, e, "plain"), "/api/items/"+e.IDs["saga2"]+"/download", nil), e.IDs["saga2"])
				resp := e.Do(t, harness.Request{Path: "/api/items/" + e.IDs["saga2"] + "/download", Query: map[string][]string{"token": {e.Token(t, "listener")}}})
				wantItemZip(t, e, resp, e.IDs["saga2"])
			}),
		build("INV-R052-SAMPLED", "download-without-permission", "error",
			"A user without the download permission gets 403 and no zip; the request changes nothing.",
			func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e)
				resp := get(t, e, s.token(t, e, "nodownload"), "/api/items/"+e.IDs["saga1"]+"/download", nil)
				wantStatus(t, "no download permission", resp, http.StatusForbidden)
				if contentType(t, resp) == "application/zip" || len(resp.Body) > 64 {
					t.Fatalf("403 response looks like content: %s %d bytes", resp.Header.Get("Content-Type"), len(resp.Body))
				}
				if snapshotItems(t, e) != before {
					t.Fatal("a refused download changed stored rows")
				}
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r052", invariant: "INV-R052-ACCESS-001", label: "download", path: route, sources: []string{downloadItemSource},
	})...)
}
