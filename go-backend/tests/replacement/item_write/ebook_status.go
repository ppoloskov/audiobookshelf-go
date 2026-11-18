package item_write

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r070       = "route-r070"
	ebookSrc   = controllerSrc + ":1155-1215"
	ebookName  = "Ebook Only.epub"
	extraEbook = "Extra.epub"
)

func ebookStatus(t *testing.T, e *harness.Env, token, itemID, fileID string) harness.Response {
	t.Helper()
	return e.Patch(t, token, "/api/items/"+itemID+"/ebook/"+fileID+"/status", nil)
}

// supplementaryOf reports isSupplementary of the stored library file with the name.
func supplementaryOf(t *testing.T, e *harness.Env, itemID, filename string) any {
	t.Helper()
	for _, file := range libraryFileList(t, e, itemID) {
		if harness.Dig(file, "metadata", "filename") == filename {
			return file["isSupplementary"]
		}
	}
	t.Fatalf("no library file %q", filename)
	return nil
}

// primaryName returns the file name of books.ebookFile, or "" when null.
func primaryName(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	file, _ := column(t, mediaRow(t, e, itemID), "ebookFile").(map[string]any)
	name, _ := harness.Dig(file, "metadata", "filename").(string)
	return name
}

func ebookStatusScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R070-EBOOK-STATUS-001"
	const errInv = "INV-ROUTE-R070-CONTRACT"

	return []harness.Scenario{
		scenario(r070, inv, "normal", "initial", "ebook-status-primary-becomes-supplementary",
			"PATCH ebook status of the primary ebook file clears books.ebookFile, marks the library file supplementary, flags the item missing (no media files remain), moves updatedAt and emits item_updated.",
			srcs(ebookSrc, "client/components/tables/EbookFilesTableRow.vue:93-103"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				ino := inoOf(t, e, id, ebookName)
				if primaryName(t, e, id) != ebookName || supplementaryOf(t, e, id, ebookName) != false || itemRow(t, e, id)["isMissing"] != int64(0) {
					t.Fatal("fixture ebook is not the primary file of a present item")
				}
				a := itemAudience(t, e, s)
				before := updatedAt(t, e, id)
				wantStatus(t, "toggle", ebookStatus(t, e, e.Token(t, "root"), id, ino), http.StatusOK)
				if column(t, mediaRow(t, e, id), "ebookFile") != nil || supplementaryOf(t, e, id, ebookName) != true || itemRow(t, e, id)["isMissing"] != int64(1) {
					t.Fatalf("ebookFile %v supplementary %v missing %v", mediaRow(t, e, id)["ebookFile"], supplementaryOf(t, e, id, ebookName), itemRow(t, e, id)["isMissing"])
				}
				wantMoved(t, e, id, before)
				events := a.receive(t, "item_updated", idOf(id), []string{"root", "listener", "tagallow"}, []string{"tagdeny", "podcastonly", "anonymous"})
				got := payload(t, events["root"])
				if got["isMissing"] != true || harness.Dig(got, "media", "ebookFile") != nil {
					t.Fatalf("payload isMissing/ebookFile = %v/%v", got["isMissing"], harness.Dig(got, "media", "ebookFile"))
				}
			}),
		scenario(r070, inv, "normal", "changed", "ebook-status-supplementary-becomes-primary",
			"Toggling a supplementary ebook file makes it the primary ebookFile (without isSupplementary/fileType, with ebookFormat), marks it not supplementary and clears the missing flag.",
			srcs(ebookSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				ino := inoOf(t, e, id, ebookName)
				before := updatedAt(t, e, id)
				wantStatus(t, "toggle back", ebookStatus(t, e, e.Token(t, "root"), id, ino), http.StatusOK)
				file, _ := column(t, mediaRow(t, e, id), "ebookFile").(map[string]any)
				if file["ino"] != ino || file["ebookFormat"] != "epub" || harness.Dig(file, "metadata", "filename") != ebookName {
					t.Fatalf("ebookFile %v", file)
				}
				if _, has := file["isSupplementary"]; has {
					t.Fatalf("ebookFile keeps isSupplementary: %v", file)
				}
				if _, has := file["fileType"]; has {
					t.Fatalf("ebookFile keeps fileType: %v", file)
				}
				if supplementaryOf(t, e, id, ebookName) != false || itemRow(t, e, id)["isMissing"] != int64(0) {
					t.Fatal("library file still supplementary or item still missing")
				}
				wantMoved(t, e, id, before)
				if got := harness.Dig(itemDocument(t, e, id), "media", "ebookFormat"); got != "epub" {
					t.Fatalf("ebookFormat = %v", got)
				}
			}),
		scenario(r070, inv, "edge", "changed", "ebook-status-switches-the-primary-among-several",
			"With two ebook files, toggling the supplementary one makes it primary and the previous primary supplementary.",
			srcs(ebookSrc, "server/controllers/LibraryItemController.js:1182-1200"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				source := filepath.Join(itemPath(t, e, id), ebookName)
				data, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(itemPath(t, e, id), extraEbook), data, 0o644); err != nil {
					t.Fatal(err)
				}
				wantStatus(t, "item scan", e.Post(t, e.Token(t, "root"), "/api/items/"+id+"/scan", nil), http.StatusOK)
				harness.Eventually(t, 30*time.Second, "the scan to add "+extraEbook, func() bool {
					return len(libraryFileList(t, e, id)) == 2
				})
				if supplementaryOf(t, e, id, extraEbook) != true || primaryName(t, e, id) != ebookName {
					t.Fatalf("scan did not leave %s as the supplementary file", extraEbook)
				}
				extra, original := inoOf(t, e, id, extraEbook), inoOf(t, e, id, ebookName)
				wantStatus(t, "make extra primary", ebookStatus(t, e, e.Token(t, "root"), id, extra), http.StatusOK)
				if primaryName(t, e, id) != extraEbook || supplementaryOf(t, e, id, extraEbook) != false || supplementaryOf(t, e, id, ebookName) != true {
					t.Fatalf("primary %q, supplementary extra=%v original=%v", primaryName(t, e, id), supplementaryOf(t, e, id, extraEbook), supplementaryOf(t, e, id, ebookName))
				}
				wantStatus(t, "make original primary", ebookStatus(t, e, e.Token(t, "root"), id, original), http.StatusOK)
				if primaryName(t, e, id) != ebookName || supplementaryOf(t, e, id, extraEbook) != true || supplementaryOf(t, e, id, ebookName) != false {
					t.Fatalf("primary %q after switching back", primaryName(t, e, id))
				}
			}),
		scenario(r070, errInv, "error", "failure", "ebook-status-invalid-requests",
			"A non-ebook file, a podcast item and an unknown file id are rejected (400, 400, 404) and change nothing.",
			srcs(middlewareSrc, ebookSrc),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				root := e.Token(t, "root")
				wantStatus(t, "audio file", ebookStatus(t, e, root, e.IDs["saga1"], inoOf(t, e, e.IDs["saga1"], "01 - Part 1.mp3")), http.StatusBadRequest)
				wantStatus(t, "cover image", ebookStatus(t, e, root, e.IDs["saga1"], inoOf(t, e, e.IDs["saga1"], "cover.jpg")), http.StatusBadRequest)
				wantStatus(t, "podcast", ebookStatus(t, e, root, e.IDs["podcast"], inoOf(t, e, e.IDs["podcast"], "cover.jpg")), http.StatusBadRequest)
				wantStatus(t, "unknown file", ebookStatus(t, e, root, e.IDs["ebook"], "999999999"), http.StatusNotFound)
				wantSnapshot(t, e, rows)
			}),
		scenario(r070, errInv, "error", "initial", "ebook-status-authorisation",
			"PATCH ebook status answers 401 without a token, 404 for unknown item or file (also for users without update), 403 for users without update permission or access to the item, and changes nothing.",
			srcs(middlewareSrc, ebookSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				ino := inoOf(t, e, id, ebookName)
				rows := snapshot(t, e)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/items/" + id + "/ebook/" + ino + "/status"}), http.StatusUnauthorized)
				wantStatus(t, "unknown item", ebookStatus(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", ino), http.StatusNotFound)
				wantStatus(t, "unknown file before permission", ebookStatus(t, e, e.Token(t, "listener"), id, "999999999"), http.StatusNotFound)
				for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "deleter"), s.token(t, e, "podcastonly"), s.token(t, e, "tagdeny")} {
					wantStatus(t, "forbidden", ebookStatus(t, e, token, id, ino), http.StatusForbidden)
				}
				wantSnapshot(t, e, rows)
			}),
	}
}
