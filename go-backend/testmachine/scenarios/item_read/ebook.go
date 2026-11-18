package item_read

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const ebookSource = "server/controllers/LibraryItemController.js:1121-1155"

// ebookScenarios covers GET /api/items/:id/ebook/:fileid?.
func ebookScenarios(s *setup) []harness.Scenario {
	build := func(invariant, assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r069", Invariant: invariant, Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{ebookSource, middlewareSource}, Claim: claim, Run: run,
		}
	}
	const contract = "INV-ROUTE-R069-CONTRACT"
	route := func(t *testing.T, e *harness.Env, itemID string) string { return "/api/items/" + itemID + "/ebook" }
	scenarios := []harness.Scenario{
		build(contract, "ebook-primary-file", "normal",
			"Without a file id the primary ebook file is served byte for byte as application/epub+zip, also with a trailing slash and to a user without download permission.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["ebook"], "Ebook Only.epub")
				for _, c := range []struct{ token, path string }{
					{e.Token(t, "listener"), "/api/items/" + e.IDs["ebook"] + "/ebook"},
					{e.Token(t, "listener"), "/api/items/" + e.IDs["ebook"] + "/ebook/"},
					{s.token(t, e, "nodownload"), "/api/items/" + e.IDs["ebook"] + "/ebook"},
				} {
					wantFileResponse(t, get(t, e, c.token, c.path, nil), file, "application/epub+zip")
				}
			}),
		build(contract, "ebook-by-file-id", "normal",
			"With the inode of an ebook library file the same file is served.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["ebook"], "Ebook Only.epub")
				wantFileResponse(t, get(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs["ebook"]+"/ebook/"+file.Ino, nil), file, "application/epub+zip")
			}),
		build(contract, "ebook-byte-ranges", "edge",
			"The ebook honours Range requests with 206, the exact Content-Range and bytes.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["ebook"], "Ebook Only.epub")
				checkRanges(t, e, e.Token(t, "listener"), "/api/items/"+e.IDs["ebook"]+"/ebook", file)
			}),
		build(contract, "ebook-missing-or-invalid", "error",
			"An item without an ebook answers 404; a file id of a non-ebook file (audio, cover) answers 400; an unknown file id answers 404.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				for _, key := range []string{"saga1", "saga2", "podcast"} {
					wantStatus(t, key+" without ebook", e.Get(t, token, "/api/items/"+e.IDs[key]+"/ebook"), http.StatusNotFound)
				}
				for _, c := range []struct{ item, name string }{{"saga1", "01 - Part 1.mp3"}, {"saga1", "cover.jpg"}, {"saga2", "desc.txt"}} {
					file := fileNamed(t, e, e.IDs[c.item], c.name)
					wantStatus(t, c.name, e.Get(t, token, "/api/items/"+e.IDs[c.item]+"/ebook/"+file.Ino), http.StatusBadRequest)
				}
				wantStatus(t, "unknown file id", e.Get(t, token, "/api/items/"+e.IDs["ebook"]+"/ebook/0"), http.StatusNotFound)
				other := fileNamed(t, e, e.IDs["saga1"], "cover.jpg")
				wantStatus(t, "file of another item", e.Get(t, token, "/api/items/"+e.IDs["ebook"]+"/ebook/"+other.Ino), http.StatusNotFound)
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r069", invariant: "INV-ROUTE-R069-ACCESS-001", label: "ebook", path: route, sources: []string{ebookSource},
	})...)
}
