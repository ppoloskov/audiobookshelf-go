package auth

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// ownershipScenarios cover the remaining obligations of
// flow-client-permissions-ownership: separate collection permissions, every
// playlist route, the active-user gate and per-user progress and sessions.
func ownershipScenarios() []harness.Scenario {
	const (
		req   = "flow-client-permissions-ownership"
		owned = "INV-FLOW-CLIENT-PERMISSIONS-OWNERSHIP-002"
		peruu = "INV-FLOW-CLIENT-PERMISSIONS-OWNERSHIP-004"
	)
	return []harness.Scenario{
		{
			Requirement: req, Invariant: owned, Case: "error", State: "initial", Assertion: "collection-delete-and-update-need-separate-permissions",
			Sources: []string{"server/controllers/CollectionController.js:446-453"},
			Claim:   "Collection PATCH and POST need update, DELETE of a collection needs delete: an update-only user is denied DELETE (403, row kept) but may patch and remove a book (bookId removal is exempt from delete), a delete-only user is denied PATCH and POST (403) but may delete.",
			Run:     checkCollectionPermissionSplit,
		},
		{
			Requirement: req, Invariant: owned, Case: "error", State: "failure", Assertion: "playlist-item-routes-are-owner-only",
			Sources: []string{"server/routers/ApiRouter.js:325-335", "server/controllers/PlaylistController.js:575-594"},
			Claim:   "The playlist item add/remove and batch add/remove routes answer 403 to another user (admin included) and leave the playlist's items unchanged, while the owner may use them.",
			Run:     checkPlaylistItemRoutes,
		},
		{
			Requirement: req, Invariant: owned, Case: "normal", State: "changed", Assertion: "playlist-list-and-from-collection-are-per-user",
			Sources: []string{"server/controllers/PlaylistController.js:138-160", "server/controllers/PlaylistController.js:511-570"},
			Claim:   "GET /api/playlists lists only the caller's playlists, and POST /api/playlists/collection/:id creates a playlist owned by the caller that no other user lists.",
			Run:     checkPlaylistListAndFromCollection,
		},
		{
			Requirement: req, Invariant: peruu, Case: "error", State: "changed", Assertion: "inactive-user-denied-on-per-user-routes",
			Sources: []string{"server/auth/TokenManager.js:320-326", "server/routers/ApiRouter.js:166-186"},
			Claim:   "A deactivated user's token is denied with 401 on playlist, progress and listening-session routes, creates nothing, and works again after reactivation.",
			Run:     checkInactiveOnOwnedRoutes,
		},
		{
			Requirement: req, Invariant: peruu, Case: "normal", State: "initial", Assertion: "progress-and-sessions-are-per-user",
			Sources: []string{"server/controllers/MeController.js:157-175", "server/controllers/MeController.js:60-80", "server/controllers/UserController.js:434-470"},
			Claim:   "Progress and listening sessions are per user: each user's lists contain exactly their own rows, a progress write leaves other users' rows untouched, and another user's sessions are visible to admins only.",
			Run:     checkProgressIsolation,
		},
	}
}

// ids decodes the "id" of every element of the array at key in a JSON object.
func idsAt(t *testing.T, resp harness.Response, key string) []string {
	t.Helper()
	requireStatus(t, "list", resp, http.StatusOK)
	var body map[string][]struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &body)
	var out []string
	for _, item := range body[key] {
		out = append(out, item.ID)
	}
	return out
}

func contains(list []string, id string) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func checkCollectionPermissionSplit(t *testing.T, e *harness.Env) {
	root := e.Token(t, "root")
	create := func(name string) string {
		resp := e.Post(t, root, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": name, "books": []string{e.IDs["novel"], e.IDs["saga1"]}})
		requireStatus(t, "create collection", resp, http.StatusOK)
		var created struct {
			ID string `json:"id"`
		}
		resp.JSON(t, &created)
		return created.ID
	}
	updater := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"update": true, "delete": false}}))
	deleter := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"update": false, "delete": true}}))
	id := create("permission split")

	// update only
	before := e.Rows(t, "SELECT updatedAt FROM collections WHERE id = ?", id)[0]["updatedAt"].(string)
	requireStatus(t, "updater patches", e.Patch(t, updater, "/api/collections/"+id, map[string]string{"name": "renamed by updater"}), http.StatusOK)
	row := e.Rows(t, "SELECT name, updatedAt FROM collections WHERE id = ?", id)[0]
	if row["name"] != "renamed by updater" || row["updatedAt"].(string) <= before {
		t.Fatalf("collection row after patch = %v", row)
	}
	requireStatus(t, "updater deletes collection", e.Delete(t, updater, "/api/collections/"+id), http.StatusForbidden)
	if e.Count(t, "SELECT COUNT(*) FROM collections WHERE id = ?", id) != 1 {
		t.Fatal("a denied delete removed the collection")
	}
	requireStatus(t, "updater removes a book", e.Delete(t, updater, "/api/collections/"+id+"/book/"+e.IDs["saga1"]), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE collectionId = ?", id) != 1 {
		t.Fatal("the book must be removed from the collection")
	}

	// delete only
	requireStatus(t, "deleter patches", e.Patch(t, deleter, "/api/collections/"+id, map[string]string{"name": "by deleter"}), http.StatusForbidden)
	requireStatus(t, "deleter adds a book", e.Post(t, deleter, "/api/collections/"+id+"/book", map[string]string{"id": e.IDs["saga1"]}), http.StatusForbidden)
	if e.Rows(t, "SELECT name FROM collections WHERE id = ?", id)[0]["name"] != "renamed by updater" || e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE collectionId = ?", id) != 1 {
		t.Fatal("denied writes changed the collection")
	}
	requireStatus(t, "deleter deletes", e.Delete(t, deleter, "/api/collections/"+id), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM collections WHERE id = ?", id) != 0 {
		t.Fatal("the collection must be deleted")
	}
}

// playlistItemCount counts the media items of a playlist.
func playlistItemCount(t *testing.T, e *harness.Env, id string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId = ?", id)
}

func checkPlaylistItemRoutes(t *testing.T, e *harness.Env) {
	owner := e.Token(t, "listener")
	id := newPlaylist(t, e, owner, "item routes")
	defer e.Delete(t, owner, "/api/playlists/"+id)
	requireStatus(t, "owner adds", e.Post(t, owner, "/api/playlists/"+id+"/item", map[string]string{"libraryItemId": e.IDs["novel"]}), http.StatusOK)
	if playlistItemCount(t, e, id) != 1 {
		t.Fatal("the owner's add must add the item")
	}
	items := map[string]any{"items": []map[string]string{{"libraryItemId": e.IDs["saga1"]}}}
	for _, who := range []string{"finisher", "root"} {
		token := e.Token(t, who)
		for _, call := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/api/playlists/" + id + "/item", map[string]string{"libraryItemId": e.IDs["saga1"]}},
			{http.MethodDelete, "/api/playlists/" + id + "/item/" + e.IDs["novel"], nil},
			{http.MethodPost, "/api/playlists/" + id + "/batch/add", items},
			{http.MethodPost, "/api/playlists/" + id + "/batch/remove", map[string]any{"items": []map[string]string{{"libraryItemId": e.IDs["novel"]}}}},
		} {
			resp := e.Do(t, harness.Request{Method: call.method, Path: call.path, Token: token, JSON: call.body})
			if resp.Status != http.StatusForbidden {
				t.Fatalf("%s %s %s: status %d, want 403", who, call.method, call.path, resp.Status)
			}
		}
	}
	if playlistItemCount(t, e, id) != 1 {
		t.Fatal("denied requests changed the playlist items")
	}
	requireStatus(t, "owner batch add", e.Post(t, owner, "/api/playlists/"+id+"/batch/add", items), http.StatusOK)
	requireStatus(t, "owner batch remove", e.Post(t, owner, "/api/playlists/"+id+"/batch/remove", items), http.StatusOK)
	requireStatus(t, "owner removes", e.Delete(t, owner, "/api/playlists/"+id+"/item/"+e.IDs["novel"]), http.StatusOK)
}

func checkPlaylistListAndFromCollection(t *testing.T, e *harness.Env) {
	owner, other := e.Token(t, "listener"), e.Token(t, "finisher")
	own := newPlaylist(t, e, owner, "listener only")
	defer e.Delete(t, owner, "/api/playlists/"+own)
	if list := idsAt(t, e.Get(t, other, "/api/playlists"), "playlists"); contains(list, own) || contains(list, e.IDs["playlist"]) {
		t.Fatalf("another user must not list the owner's playlists: %v", list)
	}
	if list := idsAt(t, e.Get(t, owner, "/api/playlists"), "playlists"); !contains(list, own) || !contains(list, e.IDs["playlist"]) {
		t.Fatalf("the owner must list their playlists: %v", list)
	}
	resp := e.Post(t, other, "/api/playlists/collection/"+e.IDs["collection"], nil)
	requireStatus(t, "from collection", resp, http.StatusOK)
	var created struct {
		ID     string `json:"id"`
		UserID string `json:"userId"`
	}
	resp.JSON(t, &created)
	rows := e.Rows(t, "SELECT userId FROM playlists WHERE id = ?", created.ID)
	if len(rows) != 1 || rows[0]["userId"] != created.UserID || created.UserID == e.IDs["listener"] {
		t.Fatalf("playlist from collection: %s / %v", resp.Body, rows)
	}
	if contains(idsAt(t, e.Get(t, owner, "/api/playlists"), "playlists"), created.ID) {
		t.Fatal("the new playlist must be private to its creator")
	}
	requireStatus(t, "other user's access", e.Get(t, owner, "/api/playlists/"+created.ID), http.StatusForbidden)
	requireStatus(t, "cleanup", e.Delete(t, other, "/api/playlists/"+created.ID), http.StatusOK)
}

func checkInactiveOnOwnedRoutes(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	token := tokenFor(t, e, acc)
	id := newPlaylist(t, e, token, "inactive owner")
	defer e.Delete(t, e.Token(t, "root"), "/api/playlists/"+id) // owner check blocks root; harmless if refused
	patchUser(t, e, acc.ID, map[string]any{"isActive": false})
	playlists := e.Count(t, "SELECT COUNT(*) FROM playlists")
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/playlists", nil},
		{http.MethodPost, "/api/playlists", map[string]any{"libraryId": e.IDs["library"], "name": "denied", "items": []any{}}},
		{http.MethodDelete, "/api/playlists/" + id, nil},
		{http.MethodGet, "/api/me/progress", nil},
		{http.MethodPatch, "/api/me/progress/" + e.IDs["novel"], map[string]any{"currentTime": 5}},
		{http.MethodGet, "/api/me/listening-sessions", nil},
	} {
		if status := e.Do(t, harness.Request{Method: call.method, Path: call.path, Token: token, JSON: call.body}).Status; status != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d, want 401", call.method, call.path, status)
		}
	}
	if e.Count(t, "SELECT COUNT(*) FROM playlists") != playlists || e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", acc.ID) != 0 {
		t.Fatal("denied requests changed data")
	}
	patchUser(t, e, acc.ID, map[string]any{"isActive": true})
	requireStatus(t, "reactivated", e.Get(t, token, "/api/playlists/"+id), http.StatusOK)
	requireStatus(t, "cleanup", e.Delete(t, token, "/api/playlists/"+id), http.StatusOK)
}

func checkProgressIsolation(t *testing.T, e *harness.Env) {
	listener, finisher, root := e.Token(t, "listener"), e.Token(t, "finisher"), e.Token(t, "root")
	progressUsers := func(token string) (ids []string, owners map[string]bool) {
		resp := e.Get(t, token, "/api/me/progress")
		requireStatus(t, "progress", resp, http.StatusOK)
		var list struct {
			MediaProgress []struct {
				ID     string `json:"id"`
				UserID string `json:"userId"`
			} `json:"mediaProgress"`
		}
		resp.JSON(t, &list)
		owners = map[string]bool{}
		for _, p := range list.MediaProgress {
			ids = append(ids, p.ID)
			owners[p.UserID] = true
		}
		return ids, owners
	}
	ownIDs := func(userID string) []string {
		var ids []string
		for _, row := range e.Rows(t, "SELECT id FROM mediaProgresses WHERE userId = ?", userID) {
			ids = append(ids, row["id"].(string))
		}
		return ids
	}
	listenerRows := e.Rows(t, "SELECT id, currentTime, updatedAt FROM mediaProgresses WHERE userId = ? ORDER BY id", e.IDs["listener"])
	got, owners := progressUsers(listener)
	if len(got) != len(listenerRows) || len(got) < 2 || len(owners) != 1 || !owners[e.IDs["listener"]] {
		t.Fatalf("listener progress = %v from %v", got, owners)
	}
	finisherID := e.Rows(t, "SELECT id FROM users WHERE username = 'trial-finisher'")[0]["id"].(string)
	got, owners = progressUsers(finisher)
	if len(got) != len(ownIDs(finisherID)) || len(owners) > 1 || (len(owners) == 1 && !owners[finisherID]) {
		t.Fatalf("finisher progress = %v from %v", got, owners)
	}
	// a write by the finisher leaves the listener's rows untouched
	requireStatus(t, "finisher writes progress", e.Patch(t, finisher, "/api/me/progress/"+e.IDs["novel"], map[string]any{"currentTime": 7, "duration": 45, "isFinished": false}), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ? AND mediaItemId = (SELECT mediaId FROM libraryItems WHERE id = ?)", finisherID, e.IDs["novel"]) != 1 {
		t.Fatal("the finisher's progress row is missing")
	}
	after := e.Rows(t, "SELECT id, currentTime, updatedAt FROM mediaProgresses WHERE userId = ? ORDER BY id", e.IDs["listener"])
	if len(after) != len(listenerRows) {
		t.Fatal("the listener's progress rows changed in number")
	}
	for i := range after {
		for key, value := range listenerRows[i] {
			if after[i][key] != value {
				t.Fatalf("another user's write changed listener progress %v column %s", listenerRows[i]["id"], key)
			}
		}
	}
	// sessions
	type sessionList struct {
		Total    int `json:"total"`
		Sessions []struct {
			UserID string `json:"userId"`
		} `json:"sessions"`
	}
	var mine, theirs sessionList
	e.Get(t, listener, "/api/me/listening-sessions?itemsPerPage=50").JSON(t, &mine)
	e.Get(t, finisher, "/api/me/listening-sessions?itemsPerPage=50").JSON(t, &theirs)
	want := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE userId = ?", e.IDs["listener"])
	if want == 0 || mine.Total != want || len(mine.Sessions) != want || theirs.Total != e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE userId = ?", finisherID) {
		t.Fatalf("listening sessions: listener %d/%d (db %d), finisher %d", mine.Total, len(mine.Sessions), want, theirs.Total)
	}
	for _, s := range mine.Sessions {
		if s.UserID != e.IDs["listener"] {
			t.Fatalf("listener's session list holds %q's session", s.UserID)
		}
	}
	var asAdmin sessionList
	e.Get(t, root, "/api/users/"+e.IDs["listener"]+"/listening-sessions?itemsPerPage=50").JSON(t, &asAdmin)
	if asAdmin.Total != want {
		t.Fatalf("admin view of the listener's sessions: %d, want %d", asAdmin.Total, want)
	}
	requireStatus(t, "other user's sessions", e.Get(t, finisher, "/api/users/"+e.IDs["listener"]+"/listening-sessions"), http.StatusForbidden)
	requireStatus(t, "cleanup", e.Delete(t, finisher, "/api/me/progress/"+e.Rows(t, "SELECT id FROM mediaProgresses WHERE userId = ? AND currentTime = 7", finisherID)[0]["id"].(string)), http.StatusOK)
}
