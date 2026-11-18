package library_manage

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

var (
	everyoneButAnonymous = []string{"root", "listener"}
	ignoredByEvents      = []string{"limited", "anonymous"}
)

func eventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-library-updated", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-UPDATED", Case: "normal", State: "initial", Assertion: "event-updated-audience-and-payload",
			Sources: []string{"server/controllers/LibraryController.js:482-490", "server/SocketAuthority.js:57-65"},
			Claim:   "A PATCH that changes a library emits library_updated once with the answered library document to every authenticated socket whose user can access the library, and not to a limited user without access or an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-update")
				a, _ := everyone(t, e)
				defer a.close()
				got := patchLibrary(t, e, lib.ID, map[string]any{"name": "Event Renamed", "settings": map[string]any{"audiobooksOnly": true}})
				payload := a.expect(t, "library_updated", lib.ID, everyoneButAnonymous, ignoredByEvents)
				if !reflect.DeepEqual(payload, got) && !sameLibrary(payload, got) {
					t.Fatalf("event payload differs from the answer:\n%+v\n%+v", payload, got)
				}
				if payload.Name != "Event Renamed" || payload.Settings["audiobooksOnly"] != true {
					t.Fatalf("payload lacks the change: %+v", payload)
				}
			},
		},
		{
			Requirement: "socket-outgoing-library-updated", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-UPDATED", Case: "edge", State: "changed", Assertion: "event-updated-for-folder-change-and-granted-user",
			Sources: []string{"server/controllers/LibraryController.js:470-490", "server/models/User.js:646-650"},
			Claim:   "A folder-only PATCH emits library_updated with the new folder list, and a limited user receives it only once the library is in its librariesAccessible.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-folder")
				granted := createUser(t, e, "lm-granted", false, []string{lib.ID})
				a, _ := everyone(t, e)
				a.sockets["granted"] = e.Socket(t, granted.Token)
				a.rearm()
				defer a.close()
				added := dir(e, "ev-added")
				got := patchLibrary(t, e, lib.ID, map[string]any{"folders": []any{libraryFolderEntry(lib.Folders[0]), map[string]any{"fullPath": added}}})
				payload := a.expect(t, "library_updated", lib.ID, []string{"root", "listener", "granted"}, ignoredByEvents)
				if len(payload.Folders) != 2 || !reflect.DeepEqual(folderPaths(payload), folderPaths(got)) {
					t.Fatalf("payload folders %+v", payload.Folders)
				}
			},
		},
		{
			Requirement: "socket-outgoing-library-updated", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-UPDATED", Case: "edge", State: "repeat", Assertion: "event-updated-only-when-something-changed",
			Sources: []string{"server/controllers/LibraryController.js:482-490", "server/controllers/LibraryController.js:913-949"},
			Claim:   "Repeating the same PATCH, an empty PATCH and a reorder emit no further library_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-repeat")
				a, _ := everyone(t, e)
				defer a.close()
				patchLibrary(t, e, lib.ID, map[string]any{"icon": "star"})
				a.expect(t, "library_updated", lib.ID, []string{"root"}, nil)
				a.rearm()
				patchLibrary(t, e, lib.ID, map[string]any{"icon": "star"})
				patchLibrary(t, e, lib.ID, map[string]any{})
				requireStatus(t, postOrder(t, e, e.Token(t, "root"), []orderEntry{{"id": lib.ID, "newOrder": lib.DisplayOrder + 50}}), http.StatusOK, "reorder")
				a.silent(t, "library_updated")
			},
		},
		{
			Requirement: "socket-outgoing-library-updated", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-UPDATED", Case: "error", State: "failure", Assertion: "event-updated-not-sent-for-rejected-patch",
			Sources: []string{"server/controllers/LibraryController.js:239-366"},
			Claim:   "A PATCH rejected with 400, 403 or 404 emits no library_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-rejected")
				a, limited := everyone(t, e)
				defer a.close()
				path := "/api/libraries/" + lib.ID
				requireStatus(t, e.Patch(t, e.Token(t, "root"), path, map[string]any{"name": "x", "settings": map[string]any{"disableWatcher": "yes"}}), http.StatusBadRequest, "invalid")
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), path, map[string]any{"name": "x"}), http.StatusForbidden, "non-admin")
				requireStatus(t, e.Patch(t, limited.Token, path, map[string]any{"name": "x"}), http.StatusForbidden, "limited")
				requireStatus(t, e.Patch(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000", map[string]any{"name": "x"}), http.StatusNotFound, "unknown")
				a.silent(t, "library_updated")
			},
		},
		{
			Requirement: "socket-outgoing-library-added", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-ADDED", Case: "normal", State: "initial", Assertion: "event-added-audience-and-payload",
			Sources: []string{"server/controllers/LibraryController.js:162-171", "server/SocketAuthority.js:57-65"},
			Claim:   "Creating a library emits library_added once with the created document to the sockets of the admin and of all-access users, and not to a limited user or an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				a, _ := everyone(t, e)
				defer a.close()
				lib := newBookLibrary(t, e, "ev-add")
				payload := a.expect(t, "library_added", lib.ID, everyoneButAnonymous, ignoredByEvents)
				if !sameLibrary(payload, lib) {
					t.Fatalf("payload differs from the create answer:\n%+v\n%+v", payload, lib)
				}
			},
		},
		{
			Requirement: "socket-outgoing-library-added", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-ADDED", Case: "edge", State: "repeat", Assertion: "event-added-once-per-create",
			Sources: []string{"server/controllers/LibraryController.js:162-171"},
			Claim:   "Each of two identical create requests emits its own library_added carrying its own id.",
			Run: func(t *testing.T, e *harness.Env) {
				a, _ := everyone(t, e)
				defer a.close()
				body := map[string]any{"name": "Event Twin", "folders": []any{map[string]any{"fullPath": dir(e, "ev-twin")}}}
				first, second := createLibrary(t, e, body), createLibrary(t, e, body)
				a.expect(t, "library_added", first.ID, []string{"root", "listener"}, nil)
				a.expect(t, "library_added", second.ID, []string{"root", "listener"}, nil)
			},
		},
		{
			Requirement: "socket-outgoing-library-added", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-ADDED", Case: "edge", State: "changed", Assertion: "event-added-follows-user-access",
			Sources: []string{"server/controllers/LibraryController.js:166-171", "server/models/User.js:646-650"},
			Claim:   "A limited user's socket gets no library_added for a new library, and a socket of the same user opened after an admin grants accessAllLibraries gets it.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "lm-later", false, []string{e.IDs["podcastLibrary"]})
				before := connect(t, e, map[string]string{"user": user.Token})
				defer before.close()
				first := newBookLibrary(t, e, "ev-later-a")
				before.expect(t, "library_added", first.ID, nil, []string{"user"})
				resp := e.Patch(t, e.Token(t, "root"), "/api/users/"+user.ID, map[string]any{"permissions": map[string]any{"accessAllLibraries": true}})
				requireStatus(t, resp, http.StatusOK, "grant all libraries")
				after := connect(t, e, map[string]string{"user": user.Token})
				defer after.close()
				second := newBookLibrary(t, e, "ev-later-b")
				after.expect(t, "library_added", second.ID, []string{"user"}, nil)
			},
		},
		{
			Requirement: "socket-outgoing-library-added", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-ADDED", Case: "error", State: "failure", Assertion: "event-added-not-sent-for-rejected-create",
			Sources: []string{"server/controllers/LibraryController.js:51-131"},
			Claim:   "A create rejected with 400 or 403 emits no library_added.",
			Run: func(t *testing.T, e *harness.Env) {
				a, _ := everyone(t, e)
				defer a.close()
				folder := dir(e, "ev-rejected-create")
				requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/libraries", map[string]any{"name": 5, "folders": []any{map[string]any{"fullPath": folder}}}), http.StatusBadRequest, "invalid")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/libraries", map[string]any{"name": "x", "folders": []any{map[string]any{"fullPath": folder}}}), http.StatusForbidden, "non-admin")
				a.silent(t, "library_added")
			},
		},
		{
			Requirement: "socket-outgoing-library-removed", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-REMOVED", Case: "normal", State: "initial", Assertion: "event-removed-to-every-authenticated-socket",
			Sources: []string{"server/controllers/LibraryController.js:553-560", "server/SocketAuthority.js:57-65"},
			Claim:   "Deleting a library emits library_removed once with the deleted document to every authenticated socket, including a limited user without access to it, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-remove")
				a, _ := everyone(t, e)
				defer a.close()
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/"+lib.ID), http.StatusOK, "delete")
				payload := a.expect(t, "library_removed", lib.ID, []string{"root", "listener", "limited"}, []string{"anonymous"})
				if !reflect.DeepEqual(payload, lib) {
					t.Fatalf("payload differs from the library document:\n%+v\n%+v", payload, lib)
				}
			},
		},
		{
			Requirement: "socket-outgoing-library-removed", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-REMOVED", Case: "edge", State: "changed", Assertion: "event-removed-carries-latest-document",
			Sources: []string{"server/controllers/LibraryController.js:543-556"},
			Claim:   "library_removed carries the document as it was after earlier updates, with the files of its folders left on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-latest")
				patched := patchLibrary(t, e, lib.ID, map[string]any{"name": "Latest Name", "displayOrder": lib.DisplayOrder + 20})
				a, _ := everyone(t, e)
				defer a.close()
				marker := filepath.Join(lib.Folders[0].FullPath, "keep.txt")
				if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/"+lib.ID), http.StatusOK, "delete")
				payload := a.expect(t, "library_removed", lib.ID, []string{"root"}, nil)
				if payload.Name != "Latest Name" || payload.DisplayOrder != patched.DisplayOrder || payload.LastUpdate != patched.LastUpdate {
					t.Fatalf("payload is not the latest document: %+v", payload)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("library folder content must stay: %v", err)
				}
			},
		},
		{
			Requirement: "socket-outgoing-library-removed", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-REMOVED", Case: "edge", State: "repeat", Assertion: "event-removed-once-then-not-again",
			Sources: []string{"server/controllers/LibraryController.js:497-566", "server/controllers/LibraryController.js:1481-1484"},
			Claim:   "A second DELETE of the same library emits no second library_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-twice")
				a, _ := everyone(t, e)
				defer a.close()
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/"+lib.ID), http.StatusOK, "first delete")
				a.expect(t, "library_removed", lib.ID, []string{"root", "listener"}, nil)
				a.rearm()
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/"+lib.ID), http.StatusNotFound, "second delete")
				a.silent(t, "library_removed")
			},
		},
		{
			Requirement: "socket-outgoing-library-removed", Invariant: "INV-SOCKET-OUTGOING-LIBRARY-REMOVED", Case: "error", State: "failure", Assertion: "event-removed-not-sent-for-rejected-delete",
			Sources: []string{"server/controllers/LibraryController.js:497-505"},
			Claim:   "A DELETE rejected with 403 or 404 emits no library_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "ev-kept")
				a, limited := everyone(t, e)
				defer a.close()
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/libraries/"+lib.ID), http.StatusForbidden, "non-admin")
				requireStatus(t, e.Delete(t, limited.Token, "/api/libraries/"+lib.ID), http.StatusForbidden, "limited")
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown")
				a.silent(t, "library_removed")
			},
		},
	}
}
