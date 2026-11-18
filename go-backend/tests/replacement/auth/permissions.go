package auth

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const noEventWindow = 600 * time.Millisecond

// permissionScenarios are the cross-route workflow scenarios of
// flow-client-permissions-ownership.
func permissionScenarios() []harness.Scenario {
	const (
		req   = "flow-client-permissions-ownership"
		admin = "INV-FLOW-CLIENT-PERMISSIONS-OWNERSHIP-001"
		owned = "INV-FLOW-CLIENT-PERMISSIONS-OWNERSHIP-002"
		event = "INV-FLOW-CLIENT-PERMISSIONS-OWNERSHIP-003"
	)
	return []harness.Scenario{
		{
			Requirement: req, Invariant: admin, Case: "normal", State: "initial", Assertion: "admin-creates-user-with-selected-permissions",
			Sources: []string{"server/controllers/UserController.js:116-203", "server/models/User.js:603-641"},
			Claim:   "POST /api/users keeps the requested permission flags, library allowlist and tag selection, persists them inside the permissions column and answers them split into permissions, librariesAccessible and itemTagsSelected.",
			Run:     checkCreateUserPermissions,
		},
		{
			Requirement: req, Invariant: event, Case: "normal", State: "initial", Assertion: "user-added-event-goes-to-admins-only",
			Sources: []string{"server/controllers/UserController.js:197-203", "server/SocketAuthority.js:81-87"},
			Claim:   "Creating a user emits user_added with the new user to admin sockets and not to an ordinary user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, e.Token(t, "root"))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				markAdmin, markOrdinary := admin.Mark(), ordinary.Mark()
				acc := newAccount(t, e, nil)
				admin.Wait(t, markAdmin, "user_added", 5*time.Second, eventFor(acc.ID))
				ordinary.None(t, markOrdinary, "user_added", noEventWindow, nil)
			},
		},
		{
			Requirement: req, Invariant: event, Case: "normal", State: "changed", Assertion: "user-updated-event-goes-to-the-acting-admin-only",
			Sources: []string{"server/controllers/UserController.js:350-355", "server/SocketAuthority.js:68-78"},
			Claim:   "A permission update through PATCH /api/users/:id persists, answers the user and emits user_updated to the acting admin's sockets only, not to the updated user or other users.",
			Run:     checkUserUpdatedEvent,
		},
		{
			Requirement: req, Invariant: admin, Case: "error", State: "initial", Assertion: "ordinary-user-denied-admin-routes",
			Sources: []string{"server/controllers/UserController.js:484-518", "server/controllers/ApiKeyController.js:196-212", "server/controllers/UserController.js:484-492"},
			Claim:   "An ordinary user is denied with 403 on the user administration and API key routes and nothing is created, changed or deleted.",
			Run:     func(t *testing.T, e *harness.Env) { checkAdminRoutesDenied(t, e, "listener") },
		},
		{
			Requirement: req, Invariant: admin, Case: "error", State: "repeat", Assertion: "admin-route-denial-repeats-for-other-users",
			Sources: []string{"server/controllers/UserController.js:484-518", "server/controllers/ApiKeyController.js:196-212"},
			Claim:   "The same denials hold for another ordinary user and when the requests are repeated.",
			Run: func(t *testing.T, e *harness.Env) {
				checkAdminRoutesDenied(t, e, "finisher")
				checkAdminRoutesDenied(t, e, "finisher")
			},
		},
		{
			Requirement: req, Invariant: owned, Case: "error", State: "failure", Assertion: "playlist-of-another-owner-is-forbidden",
			Sources: []string{"server/controllers/PlaylistController.js:575-594"},
			Claim:   "A playlist can be read, updated and deleted only by its owner: another user, even an admin, gets 403 and the playlist is unchanged; an unknown id is 404.",
			Run:     checkPlaylistOwnership,
		},
		{
			Requirement: req, Invariant: owned, Case: "normal", State: "changed", Assertion: "playlist-owner-update-and-delete-emit-owner-event",
			Sources: []string{"server/controllers/PlaylistController.js:166-272", "server/controllers/PlaylistController.js:270"},
			Claim:   "The owner renames and deletes their playlist, the row follows, and playlist_removed goes to the owner's socket only.",
			Run:     checkPlaylistOwnerWrites,
		},
		{
			Requirement: req, Invariant: owned, Case: "error", State: "initial", Assertion: "collection-writes-need-update-permission",
			Sources: []string{"server/controllers/CollectionController.js:446-454"},
			Claim:   "A user without update and delete permission can read a collection but POST, PATCH and DELETE collection routes answer 403 and change nothing.",
			Run:     checkCollectionDenied,
		},
		{
			Requirement: req, Invariant: owned, Case: "edge", State: "changed", Assertion: "collection-write-follows-update-permission",
			Sources: []string{"server/controllers/CollectionController.js:450-453", "server/controllers/UserController.js:254-320"},
			Claim:   "A user with update permission can create a collection; after an admin revokes update the same token is denied with 403 and no collection is added.",
			Run:     checkCollectionRevocation,
		},
	}
}

func checkCreateUserPermissions(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, map[string]any{
		"permissions":         map[string]any{"download": false, "update": true, "upload": true},
		"librariesAccessible": []string{e.IDs["library"]},
		"itemTagsSelected":    []string{"p-tag"},
	})
	resp := e.Get(t, e.Token(t, "root"), "/api/users/"+acc.ID)
	requireStatus(t, "get user", resp, http.StatusOK)
	var user struct {
		Permissions map[string]any `json:"permissions"`
		Libraries   []string       `json:"librariesAccessible"`
		Tags        []string       `json:"itemTagsSelected"`
		IsActive    bool           `json:"isActive"`
	}
	resp.JSON(t, &user)
	if user.Permissions["download"] != false || user.Permissions["update"] != true || user.Permissions["upload"] != true || user.Permissions["delete"] != false {
		t.Fatalf("permissions = %v", user.Permissions)
	}
	if _, nested := user.Permissions["librariesAccessible"]; nested || len(user.Libraries) != 1 || user.Libraries[0] != e.IDs["library"] || len(user.Tags) != 1 || user.Tags[0] != "p-tag" || !user.IsActive {
		t.Fatalf("answer = %s", resp.Body)
	}
	rows := e.Rows(t, "SELECT permissions FROM users WHERE id = ?", acc.ID)
	var stored struct {
		Update    bool     `json:"update"`
		Download  bool     `json:"download"`
		Libraries []string `json:"librariesAccessible"`
		Tags      []string `json:"itemTagsSelected"`
	}
	if len(rows) != 1 {
		t.Fatal("user row missing")
	}
	if err := jsonDecode(rows[0]["permissions"], &stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Update || stored.Download || len(stored.Libraries) != 1 || len(stored.Tags) != 1 {
		t.Fatalf("stored permissions = %v", rows[0]["permissions"])
	}
}

func checkUserUpdatedEvent(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	target := e.Socket(t, tokenFor(t, e, acc))
	actor := e.Socket(t, e.Token(t, "root"))
	bystander := e.Socket(t, e.Token(t, "listener"))
	markTarget, markActor, markBystander := target.Mark(), actor.Mark(), bystander.Mark()
	updatedBefore := e.Rows(t, "SELECT updatedAt FROM users WHERE id = ?", acc.ID)[0]["updatedAt"].(string)
	patchUser(t, e, acc.ID, map[string]any{"permissions": map[string]any{"delete": true}})
	actor.Wait(t, markActor, "user_updated", 5*time.Second, eventFor(acc.ID))
	target.None(t, markTarget, "user_updated", noEventWindow, nil)
	bystander.None(t, markBystander, "user_updated", noEventWindow, nil)
	rows := e.Rows(t, "SELECT permissions, updatedAt FROM users WHERE id = ?", acc.ID)
	if updated, _ := rows[0]["updatedAt"].(string); updated <= updatedBefore {
		t.Fatalf("updatedAt %q must move past %q", updated, updatedBefore)
	}
	var stored struct {
		Delete bool `json:"delete"`
	}
	if err := jsonDecode(rows[0]["permissions"], &stored); err != nil || !stored.Delete {
		t.Fatalf("stored permissions = %v (%v)", rows[0]["permissions"], err)
	}
}

// adminRoutes lists the admin-only user and API key requests an ordinary user must not complete.
func adminRoutes(selfID, otherID string) []struct {
	Method, Path string
	Body         any
} {
	return []struct {
		Method, Path string
		Body         any
	}{
		{http.MethodGet, "/api/users", nil},
		{http.MethodPost, "/api/users", map[string]string{"username": "denied-user", "password": "denied-password"}},
		{http.MethodGet, "/api/users/" + selfID, nil},
		{http.MethodPatch, "/api/users/" + selfID, map[string]any{"type": "admin"}},
		{http.MethodPatch, "/api/users/" + otherID, map[string]any{"isActive": false}},
		{http.MethodDelete, "/api/users/" + otherID, nil},
		{http.MethodGet, "/api/users/online", nil},
		{http.MethodGet, "/api/users/00000000-0000-4000-8000-000000000000", nil}, // 403 is decided before the 404 lookup
		{http.MethodGet, "/api/api-keys", nil},
		{http.MethodPost, "/api/api-keys", map[string]any{"name": "denied", "userId": selfID}},
		{http.MethodPatch, "/api/api-keys/anything", map[string]any{"isActive": true}},
		{http.MethodDelete, "/api/api-keys/anything", nil},
	}
}

func checkAdminRoutesDenied(t *testing.T, e *harness.Env, who string) {
	selfID, otherID := e.IDs["listener"], e.IDs["listener"]
	if who == "finisher" {
		selfID = e.Rows(t, "SELECT id FROM users WHERE username = 'trial-finisher'")[0]["id"].(string)
	}
	users := e.Rows(t, "SELECT id, type, isActive, updatedAt FROM users ORDER BY id")
	keys := e.Count(t, "SELECT COUNT(*) FROM apiKeys")
	token := e.Token(t, who)
	for _, route := range adminRoutes(selfID, otherID) {
		resp := e.Do(t, harness.Request{Method: route.Method, Path: route.Path, Token: token, JSON: route.Body})
		if resp.Status != http.StatusForbidden {
			t.Fatalf("%s %s: status %d, want 403", route.Method, route.Path, resp.Status)
		}
	}
	after := e.Rows(t, "SELECT id, type, isActive, updatedAt FROM users ORDER BY id")
	if len(after) != len(users) || e.Count(t, "SELECT COUNT(*) FROM apiKeys") != keys {
		t.Fatal("denied requests changed users or API keys")
	}
	for i := range users {
		for key, value := range users[i] {
			if after[i][key] != value {
				t.Fatalf("denied requests changed user %v column %s", users[i]["id"], key)
			}
		}
	}
}

// newPlaylist creates a playlist as the given token and returns its id.
func newPlaylist(t *testing.T, e *harness.Env, token, name string) string {
	t.Helper()
	resp := e.Post(t, token, "/api/playlists", map[string]any{"libraryId": e.IDs["library"], "name": name, "items": []any{}})
	requireStatus(t, "create playlist", resp, http.StatusOK)
	var created struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &created)
	if created.ID == "" {
		t.Fatalf("playlist answer: %s", resp.Body)
	}
	return created.ID
}

func checkPlaylistOwnership(t *testing.T, e *harness.Env) {
	owner := e.Token(t, "listener")
	id := newPlaylist(t, e, owner, "owned by listener")
	before := e.Rows(t, "SELECT name, userId, updatedAt FROM playlists WHERE id = ?", id)
	for _, who := range []string{"finisher", "root"} {
		token := e.Token(t, who)
		for _, call := range []struct{ method, path string }{
			{http.MethodGet, "/api/playlists/" + id},
			{http.MethodPatch, "/api/playlists/" + id},
			{http.MethodDelete, "/api/playlists/" + id},
			{http.MethodPost, "/api/playlists/" + id + "/batch/add"},
		} {
			resp := e.Do(t, harness.Request{Method: call.method, Path: call.path, Token: token, JSON: map[string]any{"name": "taken over", "items": []any{}}})
			if resp.Status != http.StatusForbidden {
				t.Fatalf("%s %s %s: status %d, want 403", who, call.method, call.path, resp.Status)
			}
		}
	}
	if status := e.Get(t, e.Token(t, "finisher"), "/api/playlists/00000000-0000-4000-8000-000000000000").Status; status != http.StatusNotFound {
		t.Fatalf("unknown playlist: status %d, want 404", status)
	}
	after := e.Rows(t, "SELECT name, userId, updatedAt FROM playlists WHERE id = ?", id)
	if len(after) != 1 || after[0]["name"] != before[0]["name"] || after[0]["userId"] != before[0]["userId"] || after[0]["updatedAt"] != before[0]["updatedAt"] {
		t.Fatalf("denied requests changed the playlist: %v -> %v", before, after)
	}
	// The owner check comes before the library check: a stranger without access
	// to the playlist's library still gets 403, not 404.
	stranger := newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
	requireStatus(t, "stranger without library access", e.Get(t, tokenFor(t, e, stranger), "/api/playlists/"+id), http.StatusForbidden)
	requireStatus(t, "owner read", e.Get(t, owner, "/api/playlists/"+id), http.StatusOK)
	requireStatus(t, "cleanup", e.Delete(t, owner, "/api/playlists/"+id), http.StatusOK)
}

func checkPlaylistOwnerWrites(t *testing.T, e *harness.Env) {
	owner := e.Token(t, "listener")
	id := newPlaylist(t, e, owner, "before rename")
	ownerSocket := e.Socket(t, owner)
	otherSocket := e.Socket(t, e.Token(t, "finisher"))
	markOwner, markOther := ownerSocket.Mark(), otherSocket.Mark()
	updatedBefore := e.Rows(t, "SELECT updatedAt FROM playlists WHERE id = ?", id)[0]["updatedAt"].(string)
	requireStatus(t, "rename", e.Patch(t, owner, "/api/playlists/"+id, map[string]string{"name": "after rename"}), http.StatusOK)
	rows := e.Rows(t, "SELECT name, updatedAt FROM playlists WHERE id = ?", id)
	if len(rows) != 1 || rows[0]["name"] != "after rename" || rows[0]["updatedAt"].(string) <= updatedBefore {
		t.Fatalf("playlist row after rename = %v", rows)
	}
	requireStatus(t, "delete", e.Delete(t, owner, "/api/playlists/"+id), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM playlists WHERE id = ?", id) != 0 {
		t.Fatal("playlist row must be deleted")
	}
	ownerSocket.Wait(t, markOwner, "playlist_removed", 5*time.Second, eventFor(id))
	otherSocket.None(t, markOther, "playlist_removed", noEventWindow, nil)
}

func checkCollectionDenied(t *testing.T, e *harness.Env) {
	token := e.Token(t, "listener") // fixture listener: update=false, delete=false
	id := e.IDs["collection"]
	before := e.Rows(t, "SELECT name, updatedAt FROM collections WHERE id = ?", id)
	count := e.Count(t, "SELECT COUNT(*) FROM collections")
	requireStatus(t, "read", e.Get(t, token, "/api/collections/"+id), http.StatusOK)
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": "denied", "books": []string{e.IDs["novel"]}}},
		{http.MethodPatch, "/api/collections/" + id, map[string]string{"name": "denied"}},
		{http.MethodPost, "/api/collections/" + id + "/book", map[string]string{"id": e.IDs["novel"]}},
		{http.MethodPost, "/api/collections/" + id + "/batch/add", map[string]any{"books": []string{e.IDs["novel"]}}},
		{http.MethodPost, "/api/collections/" + id + "/batch/remove", map[string]any{"books": []string{e.IDs["novel"]}}},
		{http.MethodDelete, "/api/collections/" + id, nil},
	} {
		resp := e.Do(t, harness.Request{Method: call.method, Path: call.path, Token: token, JSON: call.body})
		if resp.Status != http.StatusForbidden {
			t.Fatalf("%s %s: status %d, want 403", call.method, call.path, resp.Status)
		}
	}
	// The library check comes before the permission check: without access to the
	// collection's library the answer is 404 even for a forbidden method.
	restricted := newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
	requireStatus(t, "inaccessible library", e.Patch(t, tokenFor(t, e, restricted), "/api/collections/"+id, map[string]string{"name": "denied"}), http.StatusNotFound)
	after := e.Rows(t, "SELECT name, updatedAt FROM collections WHERE id = ?", id)
	if len(after) != 1 || after[0]["name"] != before[0]["name"] || after[0]["updatedAt"] != before[0]["updatedAt"] || e.Count(t, "SELECT COUNT(*) FROM collections") != count {
		t.Fatal("denied requests changed collections")
	}
}

func checkCollectionRevocation(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, map[string]any{"permissions": map[string]any{"update": true}})
	token := tokenFor(t, e, acc)
	body := map[string]any{"libraryId": e.IDs["library"], "name": "revocation scenario", "books": []string{e.IDs["novel"]}}
	resp := e.Post(t, token, "/api/collections", body)
	requireStatus(t, "create as updater", resp, http.StatusOK)
	var created struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &created)
	if e.Count(t, "SELECT COUNT(*) FROM collections WHERE id = ?", created.ID) != 1 {
		t.Fatal("collection row missing")
	}
	patchUser(t, e, acc.ID, map[string]any{"permissions": map[string]any{"update": false}})
	count := e.Count(t, "SELECT COUNT(*) FROM collections")
	requireStatus(t, "create after revocation", e.Post(t, token, "/api/collections", body), http.StatusForbidden)
	if e.Count(t, "SELECT COUNT(*) FROM collections") != count {
		t.Fatal("a denied create added a collection")
	}
	requireStatus(t, "cleanup", e.Delete(t, e.Token(t, "root"), "/api/collections/"+created.ID), http.StatusOK)
}
