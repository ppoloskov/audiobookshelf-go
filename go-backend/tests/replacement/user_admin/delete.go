package user_admin

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	deleteSource  = "server/controllers/UserController.js:363-406"
	removedSource = "server/controllers/UserController.js:402"
)

// ownedData is what a populated user owns, as row counts.
type ownedData struct {
	progress, authSessions, playlists, playlistItems, playbackSessions, devices int
	// feeds, shares and keysFor reference the user as owner; keysCreated as creator.
	feeds, shares, keysFor, keysCreated int
}

// countOwned counts the rows that reference a user.
func countOwned(t *testing.T, e *harness.Env, id string) ownedData {
	t.Helper()
	return ownedData{
		progress:         e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", id),
		authSessions:     e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", id),
		playlists:        e.Count(t, "SELECT COUNT(*) FROM playlists WHERE userId = ?", id),
		playlistItems:    e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId IN (SELECT id FROM playlists WHERE userId = ?)", id),
		playbackSessions: e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE userId = ?", id),
		devices:          e.Count(t, "SELECT COUNT(*) FROM devices WHERE userId = ?", id),
		feeds:            e.Count(t, "SELECT COUNT(*) FROM feeds WHERE userId = ?", id),
		shares:           e.Count(t, "SELECT COUNT(*) FROM mediaItemShares WHERE userId = ?", id),
		keysFor:          e.Count(t, "SELECT COUNT(*) FROM apiKeys WHERE userId = ?", id),
		keysCreated:      e.Count(t, "SELECT COUNT(*) FROM apiKeys WHERE createdByUserId = ?", id),
	}
}

// populate makes a user that owns progress, a bookmark, a playlist with an
// item, a closed playback session with its device and an auth session, all
// through the user's own API calls (setup owned by other packets). It returns
// the user, its access token and the playback session id.
//
// With extras it also creates the rows that name the user as owner or creator
// (admin-only routes). A media item can be shared once, so only one caller may ask for them.
func populate(t *testing.T, e *harness.Env, name string, extras bool) (userView, string, string) {
	t.Helper()
	accountType := "user"
	if extras {
		accountType = "admin" // only admins may own feeds, shares and API keys
	}
	user := createUser(t, e, map[string]any{"username": name, "type": accountType})
	_, _ = loginWithRefresh(t, e, name, userPassword)
	token := loginToken(t, e, name)
	item := e.IDs["novel"]
	wantStatus(t, e.Patch(t, token, "/api/me/progress/"+item, map[string]any{"isFinished": true}), http.StatusOK, "progress")
	wantStatus(t, e.Post(t, token, "/api/me/item/"+item+"/bookmark", map[string]any{"time": 2, "title": "mark"}), http.StatusOK, "bookmark")
	wantStatus(t, e.Post(t, token, "/api/playlists", map[string]any{"libraryId": e.IDs["library"], "name": name + " list", "items": []any{map[string]any{"libraryItemId": item}}}), http.StatusOK, "playlist")
	play := e.Post(t, token, "/api/items/"+item+"/play", map[string]any{
		"deviceInfo": map[string]any{"clientName": "admin-packet", "deviceId": "admin-packet-" + name}, "mediaPlayer": "admin-packet", "forceDirectPlay": true, "supportedMimeTypes": []string{},
	})
	wantStatus(t, play, http.StatusOK, "play")
	var started struct {
		ID string `json:"id"`
	}
	play.JSON(t, &started)
	wantStatus(t, e.Post(t, token, "/api/session/"+started.ID+"/close", map[string]any{"currentTime": 3, "timeListened": 4}), http.StatusOK, "close")
	if !extras {
		return user, token, started.ID
	}
	// An RSS feed and a media item share by the user, an API key for the user
	// made by root, and an API key for the listener made by the user.
	wantStatus(t, e.Post(t, token, "/api/feeds/item/"+item+"/open", map[string]any{"serverAddress": e.URL, "slug": name + "-feed"}), http.StatusOK, "feed")
	books := e.Rows(t, "SELECT id FROM books WHERE title = 'The Beginning'")
	if len(books) != 1 {
		t.Fatalf("the shareable book row is missing: %v", books)
	}
	wantStatus(t, e.Post(t, token, "/api/share/mediaitem", map[string]any{"slug": name + "-share", "mediaItemType": "book", "mediaItemId": books[0]["id"], "expiresAt": 0, "isDownloadable": false}), http.StatusCreated, "share")
	wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/api-keys", map[string]any{"name": name + "-own-key", "userId": user.ID, "isActive": true}), http.StatusOK, "key for the user")
	wantStatus(t, e.Post(t, token, "/api/api-keys", map[string]any{"name": name + "-made-key", "userId": e.IDs["listener"], "isActive": true}), http.StatusOK, "key by the user")
	return user, token, started.ID
}

// deleteUser sends DELETE /api/users/:id.
func deleteUser(t *testing.T, e *harness.Env, token, id string) harness.Response {
	t.Helper()
	return e.Delete(t, token, "/api/users/"+id)
}

func deleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r076", Invariant: "INV-R076-SAMPLED", Case: "normal", State: "changed", Assertion: "delete-user-cascades",
			Sources: []string{"server/routers/ApiRouter.js:138", deleteSource},
			Claim:   "DELETE /api/users/:id answers {success:true} and removes the user row with its progress, auth sessions, playlists (and their items), devices and API keys, keeps its playback sessions, RSS feeds, shares and the API keys it created with a null user id, revokes its access token and login, and leaves other users' data.",
			Run: func(t *testing.T, e *harness.Env) {
				user, token, sessionID := populate(t, e, "del-populated", true)
				owned := countOwned(t, e, user.ID)
				if owned.progress != 1 || owned.authSessions < 1 || owned.playlists != 1 || owned.playlistItems != 1 || owned.playbackSessions != 1 || owned.devices != 1 ||
					owned.feeds != 1 || owned.shares != 1 || owned.keysFor != 1 || owned.keysCreated != 1 {
					t.Fatalf("setup must give the user data: %+v", owned)
				}
				kept := map[string]string{
					"feeds":           "SELECT id FROM feeds WHERE slug = 'del-populated-feed'",
					"mediaItemShares": "SELECT id FROM mediaItemShares WHERE slug = 'del-populated-share'",
					"apiKeys":         "SELECT id FROM apiKeys WHERE name = 'del-populated-made-key'",
				}
				keptID := map[string]any{}
				for table, query := range kept {
					rows := e.Rows(t, query)
					if len(rows) != 1 {
						t.Fatalf("setup must create one %s row: %v", table, rows)
					}
					keptID[table] = rows[0]["id"]
				}
				otherProgress, items, users := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses"), e.Count(t, "SELECT COUNT(*) FROM libraryItems"), userCount(t, e)
				resp := deleteUser(t, e, e.Token(t, "root"), user.ID)
				wantStatus(t, resp, http.StatusOK, "delete")
				var body struct {
					Success bool `json:"success"`
				}
				resp.JSON(t, &body)
				if !body.Success {
					t.Fatalf("delete must answer success: %s", resp.Body)
				}
				if userRow(t, e, user.ID) != nil || userCount(t, e) != users-1 {
					t.Fatalf("the user row must be gone")
				}
				if after := countOwned(t, e, user.ID); after != (ownedData{}) {
					t.Fatalf("owned rows must be gone or detached: %+v", after)
				}
				session := e.Rows(t, "SELECT userId FROM playbackSessions WHERE id = ?", sessionID)
				if len(session) != 1 || session[0]["userId"] != nil {
					t.Fatalf("the playback session must stay with a null user id: %v", session)
				}
				// feeds.userId, mediaItemShares.userId and apiKeys.createdByUserId are SET NULL.
				for table, column := range map[string]string{"feeds": "userId", "mediaItemShares": "userId", "apiKeys": "createdByUserId"} {
					rows := e.Rows(t, "SELECT "+column+" FROM "+table+" WHERE id = ?", keptID[table])
					if len(rows) != 1 || rows[0][column] != nil {
						t.Fatalf("%s row must stay with a null %s: %v", table, column, rows)
					}
				}
				// the key made for the user (apiKeys.userId) is CASCADE: removed; the key made by the user stays for the listener.
				if e.Count(t, "SELECT COUNT(*) FROM apiKeys WHERE name = 'del-populated-own-key'") != 0 {
					t.Fatalf("the user's own API key must be deleted with it")
				}
				if madeFor := e.Rows(t, "SELECT userId FROM apiKeys WHERE id = ?", keptID["apiKeys"]); madeFor[0]["userId"] != e.IDs["listener"] {
					t.Fatalf("the key made by the user stays the listener's: %v", madeFor)
				}
				if e.Count(t, "SELECT COUNT(*) FROM mediaProgresses") != otherProgress-1 || e.Count(t, "SELECT COUNT(*) FROM libraryItems") != items {
					t.Fatalf("only the user's own rows may go")
				}
				wantStatus(t, e.Get(t, token, "/api/me"), http.StatusUnauthorized, "deleted user's token")
				if reply := e.Post(t, "", "/login", map[string]string{"username": user.Username, "password": userPassword}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("a deleted user cannot log in: %d", reply.Status)
				}
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/users/"+user.ID), http.StatusNotFound, "read deleted")
				if findUser(listUsers(t, e, e.Token(t, "root"), nil), user.ID) != nil {
					t.Fatalf("the deleted user must not be listed")
				}
			},
		},
		{
			Requirement: "route-r076", Invariant: "INV-R076-SAMPLED", Case: "normal", State: "initial", Assertion: "delete-user-without-data",
			Sources: []string{"server/routers/ApiRouter.js:138", deleteSource},
			Claim:   "DELETE /api/users/:id on a fresh user without any data answers {success:true}, removes exactly that row and leaves every other user row untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, map[string]any{"username": "del-plain"})
				listenerRow := userRow(t, e, e.IDs["listener"])
				users := userCount(t, e)
				resp := deleteUser(t, e, e.Token(t, "root"), user.ID)
				wantStatus(t, resp, http.StatusOK, "delete")
				if resp.Object(t)["success"] != true || userRow(t, e, user.ID) != nil || userCount(t, e) != users-1 {
					t.Fatalf("the user must be deleted: %s", resp.Body)
				}
				requireRowUnchanged(t, e, e.IDs["listener"], listenerRow)
			},
		},
		{
			Requirement: "route-r076", Invariant: "INV-R076-SAMPLED", Case: "edge", State: "repeat", Assertion: "delete-user-twice-and-by-admin",
			Sources: []string{deleteSource, middlewareJS},
			Claim:   "A non-root admin may delete a user, including an admin; deleting the same id again answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, map[string]any{"username": "del-by-admin", "type": "admin"})
				admin := adminToken(t, e)
				wantStatus(t, deleteUser(t, e, admin, user.ID), http.StatusOK, "first")
				if userRow(t, e, user.ID) != nil {
					t.Fatalf("the user row must be gone")
				}
				wantStatus(t, deleteUser(t, e, admin, user.ID), http.StatusNotFound, "second")
				wantStatus(t, deleteUser(t, e, e.Token(t, "root"), user.ID), http.StatusNotFound, "second as root")
			},
		},
		{
			Requirement: "route-r076", Invariant: "INV-R076-PROTECT-001", Case: "error", State: "failure", Assertion: "delete-user-rejections-change-nothing",
			Sources: []string{"server/Server.js:354", deleteSource, middlewareJS},
			Claim:   "DELETE /api/users/:id answers 401 unauthenticated, 403 to non-admins and to an admin targeting root, 400 when an admin targets itself (root included), 404 for an unknown id, and removes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				target, _, _ := populate(t, e, "del-protected", false)
				owned, before, users := countOwned(t, e, target.ID), userRow(t, e, target.ID), userCount(t, e)
				rootBefore, admin := userRow(t, e, rootID), adminToken(t, e)
				helperID := helperAdminID(t, e)
				root := e.Token(t, "root")
				for _, token := range []string{"", "not-a-token"} {
					wantStatus(t, deleteUser(t, e, token, target.ID), http.StatusUnauthorized, "token "+token)
				}
				listener := e.Token(t, "listener")
				for _, id := range []string{target.ID, e.IDs["listener"], unknownID} {
					wantStatus(t, deleteUser(t, e, listener, id), http.StatusForbidden, "listener deletes "+id)
				}
				wantStatus(t, deleteUser(t, e, admin, rootID), http.StatusForbidden, "admin deletes root")
				wantStatus(t, deleteUser(t, e, root, rootID), http.StatusBadRequest, "root deletes itself")
				wantStatus(t, deleteUser(t, e, admin, helperID), http.StatusBadRequest, "admin deletes itself")
				wantStatus(t, deleteUser(t, e, root, unknownID), http.StatusNotFound, "unknown id")
				if userCount(t, e) != users || countOwned(t, e, target.ID) != owned {
					t.Fatalf("a rejected delete must remove nothing")
				}
				requireRowUnchanged(t, e, target.ID, before)
				requireRowUnchanged(t, e, rootID, rootBefore)
				wantStatus(t, e.Get(t, admin, "/api/me"), http.StatusOK, "the admin still works")
			},
		},
		{
			Requirement: "socket-outgoing-user-removed", Invariant: "INV-SOCKET-OUTGOING-USER-REMOVED", Case: "normal", State: "initial", Assertion: "user-removed-event-to-admins",
			Sources: []string{removedSource},
			Claim:   "Deleting a user emits user_removed with the deleted user's browser view (id, username, type, token, permissions) to every admin socket and not to an ordinary user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-removed", "email": "removed@example.com"})
				audience := connectAudiences(t, e)
				marks := [3]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark()}
				wantStatus(t, deleteUser(t, e, e.Token(t, "root"), target.ID), http.StatusOK, "delete")
				for i, socket := range []*harness.Socket{audience.root, audience.admin} {
					event := socket.Wait(t, marks[i], "user_removed", waitEvent, matchUserID(target.ID))
					view := eventUser(t, event)
					if view.Username != "event-removed" || view.Type != "user" || view.Token != target.Token || view.Email == nil || *view.Email != "removed@example.com" || !view.IsActive || view.CreatedAt != target.CreatedAt {
						t.Fatalf("payload must be the deleted user's view: %+v", view)
					}
					requireViewPermissions(t, view, defaultPermissions("user"), nil, nil)
					if userRow(t, e, target.ID) != nil {
						t.Fatalf("the event must follow the deletion")
					}
				}
				audience.listener.None(t, marks[2], "user_removed", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-removed", Invariant: "INV-SOCKET-OUTGOING-USER-REMOVED", Case: "edge", State: "changed", Assertion: "user-removed-event-for-user-with-data",
			Sources: []string{removedSource, "server/models/User.js:353-390"},
			Claim:   "The user_removed payload of a user with progress and bookmarks carries them (mediaProgress and bookmarks arrays), and the deleted user's own connected socket receives no user_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				user, token, _ := populate(t, e, "del-event-data", false)
				own := e.Socket(t, token)
				audience := connectAudiences(t, e)
				marks := [2]int{audience.root.Mark(), own.Mark()}
				wantStatus(t, deleteUser(t, e, e.Token(t, "root"), user.ID), http.StatusOK, "delete")
				event := audience.root.Wait(t, marks[0], "user_removed", waitEvent, matchUserID(user.ID))
				view := eventUser(t, event)
				if len(view.MediaProgress) != 1 || view.MediaProgress[0].LibraryItemID != e.IDs["novel"] || view.MediaProgress[0].CurrentTime != 3 ||
					len(view.Bookmarks) != 1 || view.Bookmarks[0].Title != "mark" || view.Bookmarks[0].Time != 2 {
					t.Fatalf("payload must carry the progress and bookmarks: %+v", view)
				}
				own.None(t, marks[1], "user_removed", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-removed", Invariant: "INV-SOCKET-OUTGOING-USER-REMOVED", Case: "normal", State: "repeat", Assertion: "user-removed-event-once-per-deletion",
			Sources: []string{deleteSource, removedSource},
			Claim:   "Deleting a user emits user_removed once; deleting the same id again answers 404 and emits nothing more.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-removed-twice"})
				root := e.Socket(t, e.Token(t, "root"))
				mark := root.Mark()
				wantStatus(t, deleteUser(t, e, e.Token(t, "root"), target.ID), http.StatusOK, "first")
				root.Wait(t, mark, "user_removed", waitEvent, matchUserID(target.ID))
				after := root.Mark()
				wantStatus(t, deleteUser(t, e, e.Token(t, "root"), target.ID), http.StatusNotFound, "second")
				root.None(t, after, "user_removed", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-removed", Invariant: "INV-SOCKET-OUTGOING-USER-REMOVED", Case: "error", State: "failure", Assertion: "user-removed-event-not-sent-on-rejection",
			Sources: []string{deleteSource, removedSource},
			Claim:   "A rejected delete (self, root, unknown id, forbidden caller) emits no user_removed event.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := connectAudiences(t, e)
				marks := [3]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark()}
				root := e.Token(t, "root")
				wantStatus(t, deleteUser(t, e, root, rootID), http.StatusBadRequest, "self")
				wantStatus(t, deleteUser(t, e, adminToken(t, e), rootID), http.StatusForbidden, "root")
				wantStatus(t, deleteUser(t, e, root, unknownID), http.StatusNotFound, "unknown")
				wantStatus(t, deleteUser(t, e, e.Token(t, "listener"), finisherID), http.StatusForbidden, "forbidden")
				for i, socket := range []*harness.Socket{audience.root, audience.admin, audience.listener} {
					socket.None(t, marks[i], "user_removed", quiet, nil)
				}
			},
		},
	}
}
