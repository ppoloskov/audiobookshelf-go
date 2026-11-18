package library_manage

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func updateItemScenarios() []harness.Scenario {
	return []harness.Scenario{
		removeFoldersScenario("update-removing-all-folders-removes-their-items", "library",
			"PATCH /api/libraries/:id with folders [] on the fixture book library removes every item of the removed folder with its progress, playlist entries, feeds and metadata, deletes authors and series left empty, keeps the files, emits item_removed per item before library_updated, and answers the library without folders."),
		removeFoldersScenario("update-removing-podcast-folder-removes-episode-data", "podcastLibrary",
			"PATCH /api/libraries/:id with folders [] on the fixture podcast library removes its podcasts with their episode progress, playlist entries and feeds, keeps the files, and emits item_removed per item before library_updated."),
	}
}

// removeFoldersScenario empties the folders of a fixture library on its own server.
func removeFoldersScenario(assertion, key, claim string) harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r020", Invariant: "INV-R020-FOLDERS-001", Case: "normal", State: "changed", Assertion: assertion,
		Sources: []string{updateRoute, "server/controllers/LibraryController.js:408-467", "server/routers/ApiRouter.js:373-407", "server/routers/ApiRouter.js:414-520"},
		Claim:   claim,
		Run: func(t *testing.T, _ *harness.Env) {
			var resp harness.Response
			r := startRemoval(t, key, func(e *harness.Env, lib libraryJSON) {
				resp = e.Patch(t, e.Token(t, "root"), "/api/libraries/"+lib.ID, map[string]any{"folders": []any{}})
			})
			requireStatus(t, resp, http.StatusOK, "patch folders")
			got := decodeLibrary(t, resp)
			if len(got.Folders) != 0 || got.ID != r.lib.ID || got.Name != r.lib.Name {
				t.Fatalf("unexpected answer: %+v", got)
			}
			r.checkItemsRemoved(t)
			if r.after.Folders != 0 || r.after.Libraries != 1 {
				t.Fatalf("folder rows go and the library stays: %+v", r.after)
			}
			receivers := []string{"root", "listener"}
			if key == "library" {
				if r.after.Authors != 0 || r.after.Series != 0 || r.before.Authors == 0 || r.before.Series == 0 {
					t.Fatalf("authors and series left empty must be removed: before %+v after %+v", r.before, r.after)
				}
			} else {
				receivers = append(receivers, "limited")
			}
			updated := r.a.expect(t, "library_updated", r.lib.ID, receivers, []string{"anonymous"})
			if len(updated.Folders) != 0 {
				t.Fatalf("library_updated must carry the library without folders: %+v", updated)
			}
			lastRemoved, updatedAt := -1, -1
			for i, ev := range r.a.sockets["root"].Events()[r.a.marks["root"]:] {
				switch ev.Name {
				case "item_removed":
					lastRemoved = i
				case "library_updated":
					updatedAt = i
				}
			}
			if lastRemoved < 0 || updatedAt < lastRemoved {
				t.Fatalf("library_updated must follow every item_removed (%d, %d)", lastRemoved, updatedAt)
			}
			checkRow(t, r.e, got)
		},
	}
}
