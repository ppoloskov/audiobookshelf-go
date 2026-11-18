package user_admin

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	updateSource  = "server/controllers/UserController.js:205-354"
	updatedSource = "server/controllers/UserController.js:347"
)

// updateResult is the PATCH /api/users/:id response.
type updateResult struct {
	Success bool     `json:"success"`
	User    userView `json:"user"`
}

// patchUser sends PATCH /api/users/:id.
func patchUser(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return e.Patch(t, token, "/api/users/"+id, body)
}

// patchOK patches and requires 200 {success:true}; it returns the user view.
func patchOK(t *testing.T, e *harness.Env, token, id string, body any) userView {
	t.Helper()
	resp := patchUser(t, e, token, id, body)
	wantStatus(t, resp, http.StatusOK, "PATCH /api/users/"+id)
	var result updateResult
	resp.JSON(t, &result)
	if !result.Success || result.User.ID != id {
		t.Fatalf("PATCH must answer success and the user: %s", resp.Body)
	}
	return result.User
}

// loginWithRefresh logs in asking for the refresh token in the body.
func loginWithRefresh(t *testing.T, e *harness.Env, username, password string) (access, refresh string) {
	t.Helper()
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": username, "password": password}, Header: map[string]string{"X-Return-Tokens": "true"}})
	wantStatus(t, resp, http.StatusOK, "login "+username)
	var body struct {
		User struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	resp.JSON(t, &body)
	if body.User.AccessToken == "" || body.User.RefreshToken == "" {
		t.Fatalf("login must return both tokens: %s", resp.Body)
	}
	return body.User.AccessToken, body.User.RefreshToken
}

// refreshStatus posts /auth/refresh with the refresh token.
func refreshStatus(t *testing.T, e *harness.Env, refresh string) int {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"X-Refresh-Token": refresh}}).Status
}

func updateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r075", Invariant: "INV-R075-SAMPLED", Case: "normal", State: "initial", Assertion: "update-user-fields-and-row",
			Sources: []string{"server/routers/ApiRouter.js:137", updateSource},
			Claim:   "PATCH /api/users/:id changes email, type, isActive and the permission flags and selection lists, answers {success, user} and stores them, leaving username, password hash, token and creation time as they were.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "upd-fields", "type": "user", "email": "old@example.com"})
				before := userRow(t, e, target.ID)
				flags := defaultPermissions("admin")
				flags["download"], flags["accessAllLibraries"] = false, false
				view := patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{
					"email": "new@example.com", "type": "admin", "isActive": false,
					"permissions": map[string]any{"download": false, "upload": true, "accessAllLibraries": false, "librariesAccessible": []string{e.IDs["library"]}, "itemTagsSelected": []string{"T1"}},
				})
				if view.Username != "upd-fields" || view.Type != "admin" || view.IsActive || view.Email == nil || *view.Email != "new@example.com" || view.Token != target.Token || view.CreatedAt != target.CreatedAt {
					t.Fatalf("response view wrong: %+v", view)
				}
				flags = map[string]bool{"download": false, "update": false, "delete": false, "upload": true, "createEreader": false, "accessAllLibraries": false, "accessAllTags": true, "accessExplicitContent": false, "selectedTagsNotAccessible": false}
				requireViewPermissions(t, view, flags, []string{e.IDs["library"]}, []string{"T1"})
				row := userRow(t, e, target.ID)
				if row["type"] != "admin" || row["email"] != "new@example.com" || row["isActive"] != int64(0) || row["username"] != "upd-fields" || row["pash"] != before["pash"] || row["token"] != before["token"] || row["createdAt"] != before["createdAt"] || row["updatedAt"] == before["updatedAt"] {
					t.Fatalf("row wrong: %v", row)
				}
				requireRowPermissions(t, e, target.ID, flags, []string{e.IDs["library"]}, []string{"T1"})
				if again := getUser(t, e, e.Token(t, "root"), target.ID); again.Type != "admin" || again.IsActive {
					t.Fatalf("a read must reflect the update: %+v", again)
				}
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-PERMISSIONS-001", Case: "edge", State: "initial", Assertion: "update-user-lists-only-inside-permissions",
			Sources: []string{updateSource},
			Claim:   "PATCH librariesAccessible or itemTagsSelected at the top level changes nothing unless a permissions object is sent too; with one they are applied, and empty arrays (truthy) clear them.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "upd-lists"})
				before := userRow(t, e, target.ID)
				patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{"librariesAccessible": []string{e.IDs["library"]}, "itemTagsSelected": []string{"T"}})
				requireRowUnchanged(t, e, target.ID, before)
				view := patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{"permissions": map[string]any{}, "librariesAccessible": []string{e.IDs["library"]}, "itemTagsSelected": []string{"T"}})
				requireViewPermissions(t, view, defaultPermissions("user"), []string{e.IDs["library"]}, []string{"T"})
				requireRowPermissions(t, e, target.ID, defaultPermissions("user"), []string{e.IDs["library"]}, []string{"T"})
				patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{"permissions": map[string]any{"librariesAccessible": []string{}, "itemTagsSelected": []string{}}})
				requireRowPermissions(t, e, target.ID, defaultPermissions("user"), nil, nil) // empty arrays are truthy: they clear the lists
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-PERMISSIONS-001", Case: "edge", State: "initial", Assertion: "update-user-ignores-invalid-values",
			Sources: []string{updateSource},
			Claim:   "PATCH skips unknown permission keys, non-boolean permission values, malformed lists, an empty password, an empty email and an unchanged type while applying the valid parts of the same request.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "upd-invalid", "email": "keep@example.com"})
				before := userRow(t, e, target.ID)
				flags := defaultPermissions("user")
				flags["delete"] = true
				view := patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{
					"email": "", "password": "", "type": "user",
					"permissions": map[string]any{"bogus": true, "download": "no", "delete": true, "librariesAccessible": "all", "itemTagsSelected": []any{1}},
				})
				requireViewPermissions(t, view, flags, nil, nil)
				requireRowPermissions(t, e, target.ID, flags, nil, nil)
				row := userRow(t, e, target.ID)
				if row["email"] != "keep@example.com" || row["pash"] != before["pash"] || row["type"] != "user" {
					t.Fatalf("ignored fields must stay: %v", row)
				}
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-NOOP-001", Case: "normal", State: "repeat", Assertion: "update-user-repeat-changes-nothing",
			Sources: []string{updateSource, updatedSource},
			Claim:   "Sending the same PATCH again answers success with the same user and writes nothing (the row, including its update time, is unchanged) and emits no user_updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				target := createUser(t, e, map[string]any{"username": "upd-repeat"})
				body := map[string]any{"email": "repeat@example.com", "isActive": true, "permissions": map[string]any{"upload": true}}
				first := patchOK(t, e, root, target.ID, body)
				row := userRow(t, e, target.ID)
				socket := e.Socket(t, root)
				mark := socket.Mark()
				second := patchOK(t, e, root, target.ID, body)
				if jsonString(first) != jsonString(second) {
					t.Fatalf("a repeated PATCH must return the same user:\n%s\n%s", jsonString(first), jsonString(second))
				}
				requireRowUnchanged(t, e, target.ID, row)
				socket.None(t, mark, "user_updated", quiet, nil)
				patchOK(t, e, root, target.ID, map[string]any{})
				socket.None(t, mark, "user_updated", quiet, nil)
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-CREDENTIALS-001", Case: "normal", State: "changed", Assertion: "update-user-password-replaces-login",
			Sources: []string{updateSource, "server/auth/TokenManager.js:467-504"},
			Claim:   "PATCH password stores a new bcrypt hash, makes the old password fail and the new one log in, deletes the user's auth sessions so the old refresh token stops working, and leaves the stored api token as it was.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "upd-password"})
				_, refresh := loginWithRefresh(t, e, target.Username, userPassword)
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", target.ID) != 1 {
					t.Fatalf("login must create one auth session")
				}
				before := userRow(t, e, target.ID)
				patchOK(t, e, e.Token(t, "root"), target.ID, map[string]any{"password": "changed-password"})
				row := userRow(t, e, target.ID)
				pash, _ := row["pash"].(string)
				if !bcryptHash.MatchString(pash) || row["pash"] == before["pash"] || row["token"] != before["token"] || row["username"] != before["username"] {
					t.Fatalf("row after password change wrong: %v", row)
				}
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", target.ID) != 0 {
					t.Fatalf("the user's auth sessions must be deleted")
				}
				if status := refreshStatus(t, e, refresh); status != http.StatusUnauthorized {
					t.Fatalf("the old refresh token must stop working: %d", status)
				}
				if reply := e.Post(t, "", "/login", map[string]string{"username": target.Username, "password": userPassword}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("the old password must stop working: %d", reply.Status)
				}
				loginWithRefresh(t, e, target.Username, "changed-password")
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-CREDENTIALS-001", Case: "edge", State: "changed", Assertion: "update-own-password-keeps-current-session",
			Sources: []string{updateSource, "server/auth/TokenManager.js:467-498"},
			Claim:   "An admin changing its own password while presenting its refresh token keeps that one auth session (rotated, still refreshable) and loses its other sessions, while the old password stops working.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := createUser(t, e, map[string]any{"username": "upd-self-admin", "type": "admin"})
				access, refresh := loginWithRefresh(t, e, admin.Username, userPassword)
				loginWithRefresh(t, e, admin.Username, userPassword)
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", admin.ID) != 2 {
					t.Fatalf("two logins must make two sessions")
				}
				resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/users/" + admin.ID, Token: access, JSON: map[string]any{"password": "self-changed"}, Header: map[string]string{"X-Refresh-Token": refresh}})
				wantStatus(t, resp, http.StatusOK, "own password change")
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", admin.ID) != 1 {
					t.Fatalf("only the presenting session must survive")
				}
				if reply := e.Post(t, "", "/login", map[string]string{"username": admin.Username, "password": userPassword}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("the old password must stop working: %d", reply.Status)
				}
				loginWithRefresh(t, e, admin.Username, "self-changed")
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-CREDENTIALS-001", Case: "normal", State: "changed", Assertion: "update-user-username-rotates-token",
			Sources: []string{updateSource, "server/auth/TokenManager.js:467-504"},
			Claim:   "PATCH username renames the user, issues a new stored api token, deletes its auth sessions, and the user logs in under the new name only; a taken name is rejected with 400 and nothing changes; sending the current name changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				target := createUser(t, e, map[string]any{"username": "upd-name-old"})
				loginWithRefresh(t, e, target.Username, userPassword)
				before := userRow(t, e, target.ID)
				wantStatus(t, patchUser(t, e, root, target.ID, map[string]any{"username": "trial-listener"}), http.StatusBadRequest, "taken name")
				requireRowUnchanged(t, e, target.ID, before)
				patchOK(t, e, root, target.ID, map[string]any{"username": "upd-name-old"})
				requireRowUnchanged(t, e, target.ID, before)
				view := patchOK(t, e, root, target.ID, map[string]any{"username": "upd-name-new"})
				row := userRow(t, e, target.ID)
				if view.Username != "upd-name-new" || row["username"] != "upd-name-new" || row["token"] == before["token"] || view.Token != row["token"] || row["pash"] != before["pash"] {
					t.Fatalf("rename wrong: view %+v row %v", view, row)
				}
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", target.ID) != 0 {
					t.Fatalf("the user's auth sessions must be deleted")
				}
				if reply := e.Post(t, "", "/login", map[string]string{"username": "upd-name-old", "password": userPassword}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("the old name must stop working: %d", reply.Status)
				}
				loginWithRefresh(t, e, "upd-name-new", userPassword)
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-ACTIVE-001", Case: "normal", State: "changed", Assertion: "update-user-deactivate-blocks-access",
			Sources: []string{updateSource, "server/auth/LocalAuthStrategy.js:55", "server/auth/TokenManager.js:299-322", "server/SocketAuthority.js:231-234"},
			Claim:   "PATCH isActive false makes login, API calls with an existing access token and socket authentication fail; PATCH isActive true restores them.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				target := createUser(t, e, map[string]any{"username": "upd-active"})
				token := loginToken(t, e, target.Username)
				wantStatus(t, e.Get(t, token, "/api/me"), http.StatusOK, "active")
				patchOK(t, e, root, target.ID, map[string]any{"isActive": false})
				if userRow(t, e, target.ID)["isActive"] != int64(0) {
					t.Fatalf("isActive must be stored as 0")
				}
				if reply := e.Post(t, "", "/login", map[string]string{"username": target.Username, "password": userPassword}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("an inactive user cannot log in: %d", reply.Status)
				}
				wantStatus(t, e.Get(t, token, "/api/me"), http.StatusUnauthorized, "inactive user's token")
				socket := e.Socket(t, "")
				socket.Emit(t, "auth", token)
				socket.Wait(t, 0, "auth_failed", waitEvent, nil)
				patchOK(t, e, root, target.ID, map[string]any{"isActive": true})
				wantStatus(t, e.Get(t, token, "/api/me"), http.StatusOK, "reactivated token")
				loginToken(t, e, target.Username)
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-ROOT-001", Case: "edge", State: "initial", Assertion: "update-root-rules",
			Sources: []string{updateSource},
			Claim:   "Root may edit itself but its type cannot change (the type key is ignored); a non-root admin editing the root user gets 403, while an admin may edit other users and promote a user to admin.",
			Run: func(t *testing.T, e *harness.Env) {
				root, admin := e.Token(t, "root"), adminToken(t, e)
				rootBefore := userRow(t, e, rootID)
				wantStatus(t, patchUser(t, e, admin, rootID, map[string]any{"email": "x@example.com"}), http.StatusForbidden, "admin edits root")
				requireRowUnchanged(t, e, rootID, rootBefore)
				view := patchOK(t, e, root, rootID, map[string]any{"type": "user", "email": "root@example.com"})
				if view.Type != "root" || view.Email == nil || *view.Email != "root@example.com" || userRow(t, e, rootID)["type"] != "root" {
					t.Fatalf("root keeps its type: %+v", view)
				}
				patchOK(t, e, root, rootID, map[string]any{"email": ""})
				target := createUser(t, e, map[string]any{"username": "upd-promote"})
				if promoted := patchOK(t, e, admin, target.ID, map[string]any{"type": "admin"}); promoted.Type != "admin" || userRow(t, e, target.ID)["type"] != "admin" {
					t.Fatalf("an admin may promote a user: %+v", promoted)
				}
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-PERMISSIONS-001", Case: "normal", State: "changed", Assertion: "update-user-flag-matrix",
			Sources: []string{updateSource, "client/components/modals/AccountModal.vue:299-304", "server/models/User.js:134-148"},
			Claim:   "PATCH with the account modal's body (full permissions object plus top-level lists) flips each of the nine flags away from the type default and then back, for user, admin and guest, in the response and in users.permissions.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				for _, accountType := range []string{"user", "admin", "guest"} {
					target := createUser(t, e, map[string]any{"username": "upd-matrix-" + accountType, "type": accountType})
					defaults := defaultPermissions(accountType)
					away := flipped(defaults)
					libraries, tags := []string{e.IDs["library"]}, []string{"T1", "T2"}
					view := patchOK(t, e, root, target.ID, modalBody(away, libraries, tags))
					requireViewPermissions(t, view, away, libraries, tags)
					requireRowPermissions(t, e, target.ID, away, libraries, tags)
					view = patchOK(t, e, root, target.ID, modalBody(defaults, []string{}, []string{}))
					requireViewPermissions(t, view, defaults, nil, nil)
					requireRowPermissions(t, e, target.ID, defaults, nil, nil)
				}
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-CREDENTIALS-001", Case: "edge", State: "changed", Assertion: "update-username-case-variant-and-with-password",
			Sources: []string{updateSource, "server/models/User.js:484-491"},
			Claim:   "PATCH may rename a user to a case variant of its own or another name (the check is exact), and username with password in one request makes one new token, deletes the auth sessions, emits one user_updated, and logs in under the new name and password only.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				target := createUser(t, e, map[string]any{"username": "Upd-Case"})
				before := userRow(t, e, target.ID)
				view := patchOK(t, e, root, target.ID, map[string]any{"username": "upd-case"})
				row := userRow(t, e, target.ID)
				if view.Username != "upd-case" || row["username"] != "upd-case" || row["token"] == before["token"] {
					t.Fatalf("a case-only rename must apply and rotate the token: %+v %v", view, row)
				}
				other := createUser(t, e, map[string]any{"username": "upd-case-other", "password": "other-password"})
				if renamed := patchOK(t, e, root, other.ID, map[string]any{"username": "UPD-CASE"}); renamed.Username != "UPD-CASE" {
					t.Fatalf("a rename to a case variant of another user's name must apply: %+v", renamed)
				}
				loginWithRefresh(t, e, "upd-case", userPassword)
				loginWithRefresh(t, e, "upd-case", userPassword)
				socket := e.Socket(t, root)
				mark := socket.Mark()
				before = userRow(t, e, target.ID)
				view = patchOK(t, e, root, target.ID, map[string]any{"username": "upd-both", "password": "both-password"})
				row = userRow(t, e, target.ID)
				if view.Username != "upd-both" || row["username"] != "upd-both" || row["token"] == before["token"] || view.Token != row["token"] || row["pash"] == before["pash"] {
					t.Fatalf("username and password must both change with one new token: %+v %v", view, row)
				}
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", target.ID) != 0 {
					t.Fatalf("the auth sessions must be deleted")
				}
				socket.Wait(t, mark, "user_updated", waitEvent, matchUserID(target.ID))
				count := 0
				for _, event := range socket.Events()[mark:] {
					if event.Name == "user_updated" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("one PATCH must emit one user_updated, got %d", count)
				}
				for _, pair := range [][2]string{{"upd-case", userPassword}, {"upd-both", userPassword}, {"upd-case", "both-password"}} {
					if reply := e.Post(t, "", "/login", map[string]string{"username": pair[0], "password": pair[1]}); reply.Status != http.StatusUnauthorized {
						t.Fatalf("login %v must fail: %d", pair, reply.Status)
					}
				}
				loginWithRefresh(t, e, "upd-both", "both-password")
			},
		},
		{
			Requirement: "route-r075", Invariant: "INV-R075-VALIDATE-001", Case: "error", State: "failure", Assertion: "update-user-rejections-change-nothing",
			Sources: []string{"server/Server.js:354", updateSource, middlewareJS},
			Claim:   "PATCH /api/users/:id answers 401 unauthenticated, 403 to non-admins (also for their own id), 404 for an unknown id, and 400 for id, pash, token, extraData or bookmarks keys, a non-string email or username, an unknown or root type and non-object permissions, changing no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				target := createUser(t, e, map[string]any{"username": "upd-rejected", "email": "r@example.com"})
				before := userRow(t, e, target.ID)
				listenerBefore := userRow(t, e, e.IDs["listener"])
				for _, token := range []string{"", "not-a-token"} {
					wantStatus(t, patchUser(t, e, token, target.ID, map[string]any{"email": "z@example.com"}), http.StatusUnauthorized, "token "+token)
				}
				listener := e.Token(t, "listener")
				for _, id := range []string{target.ID, e.IDs["listener"], unknownID} {
					wantStatus(t, patchUser(t, e, listener, id, map[string]any{"email": "z@example.com"}), http.StatusForbidden, "listener patches "+id)
				}
				wantStatus(t, patchUser(t, e, root, unknownID, map[string]any{"email": "z@example.com"}), http.StatusNotFound, "unknown id")
				for name, body := range map[string]map[string]any{
					"id": {"id": "x"}, "pash": {"pash": "x"}, "token": {"token": "x"}, "extraData": {"extraData": map[string]any{}}, "bookmarks": {"bookmarks": []any{}},
					"email number": {"email": 5}, "username number": {"username": 5}, "type unknown": {"type": "superuser"}, "type root": {"type": "root"}, "permissions string": {"permissions": "all"},
				} {
					wantStatus(t, patchUser(t, e, root, target.ID, withCopy(body, map[string]any{"email2": "ignored"})), http.StatusBadRequest, name)
				}
				requireRowUnchanged(t, e, target.ID, before)
				requireRowUnchanged(t, e, e.IDs["listener"], listenerBefore)
			},
		},
		{
			Requirement: "socket-outgoing-user-updated", Invariant: "INV-SOCKET-OUTGOING-USER-UPDATED", Case: "normal", State: "initial", Assertion: "user-updated-event-to-acting-admin-only",
			Sources: []string{updatedSource},
			Claim:   "A PATCH that changes a user emits user_updated with the updated browser view (equal to the response) to the sockets of the admin who made the request only: not to the edited user, another admin or an ordinary user.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-updated"})
				targetSocket := e.Socket(t, loginToken(t, e, target.Username))
				audience := connectAudiences(t, e)
				marks := [4]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark(), targetSocket.Mark()}
				resp := patchUser(t, e, e.Token(t, "root"), target.ID, map[string]any{"email": "event@example.com"})
				wantStatus(t, resp, http.StatusOK, "patch")
				event := audience.root.Wait(t, marks[0], "user_updated", waitEvent, matchUserID(target.ID))
				requireSameUserJSON(t, event, resp)
				if view := eventUser(t, event); view.Email == nil || *view.Email != "event@example.com" {
					t.Fatalf("payload must carry the new email: %+v", view)
				}
				audience.admin.None(t, marks[1], "user_updated", quiet, nil)
				audience.listener.None(t, marks[2], "user_updated", quiet, nil)
				targetSocket.None(t, marks[3], "user_updated", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-updated", Invariant: "INV-SOCKET-OUTGOING-USER-UPDATED", Case: "edge", State: "changed", Assertion: "user-updated-event-follows-the-acting-admin",
			Sources: []string{updatedSource},
			Claim:   "When a non-root admin edits a user, that admin's socket receives user_updated and root's does not; a PATCH of a user's own record by an admin also reaches the admin's sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-follow"})
				audience := connectAudiences(t, e)
				admin := adminToken(t, e)
				marks := [2]int{audience.root.Mark(), audience.admin.Mark()}
				patchOK(t, e, admin, target.ID, map[string]any{"type": "admin"})
				event := audience.admin.Wait(t, marks[1], "user_updated", waitEvent, matchUserID(target.ID))
				if eventUser(t, event).Type != "admin" {
					t.Fatalf("payload must carry the new type: %+v", eventUser(t, event))
				}
				audience.root.None(t, marks[0], "user_updated", quiet, nil)
				helper := getUser(t, e, e.Token(t, "root"), userRow(t, e, helperAdminID(t, e))["id"].(string))
				mark := audience.admin.Mark()
				patchOK(t, e, admin, helper.ID, map[string]any{"email": "helper@example.com"})
				audience.admin.Wait(t, mark, "user_updated", waitEvent, matchUserID(helper.ID))
			},
		},
		{
			Requirement: "socket-outgoing-user-updated", Invariant: "INV-SOCKET-OUTGOING-USER-UPDATED", Case: "normal", State: "repeat", Assertion: "user-updated-event-per-change",
			Sources: []string{updateSource, updatedSource},
			Claim:   "Each PATCH that changes something emits its own user_updated carrying the then-current value; repeating the last PATCH emits nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-twice"})
				root := e.Token(t, "root")
				socket := e.Socket(t, root)
				for _, email := range []string{"one@example.com", "two@example.com"} {
					mark := socket.Mark()
					patchOK(t, e, root, target.ID, map[string]any{"email": email})
					event := socket.Wait(t, mark, "user_updated", waitEvent, matchUserID(target.ID))
					if view := eventUser(t, event); view.Email == nil || *view.Email != email {
						t.Fatalf("payload must carry %s: %+v", email, view)
					}
				}
				mark := socket.Mark()
				patchOK(t, e, root, target.ID, map[string]any{"email": "two@example.com"})
				socket.None(t, mark, "user_updated", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-updated", Invariant: "INV-SOCKET-OUTGOING-USER-UPDATED", Case: "error", State: "failure", Assertion: "user-updated-event-not-sent-without-change",
			Sources: []string{updateSource, updatedSource},
			Claim:   "A rejected PATCH and a PATCH that changes nothing emit no user_updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "event-nochange"})
				audience := connectAudiences(t, e)
				mark := audience.root.Mark()
				root := e.Token(t, "root")
				wantStatus(t, patchUser(t, e, root, target.ID, map[string]any{"type": "superuser"}), http.StatusBadRequest, "invalid")
				wantStatus(t, patchUser(t, e, e.Token(t, "listener"), target.ID, map[string]any{"email": "x@example.com"}), http.StatusForbidden, "forbidden")
				patchOK(t, e, root, target.ID, map[string]any{"type": "user", "isActive": true})
				audience.root.None(t, mark, "user_updated", quiet, nil)
			},
		},
	}
}

// helperAdminID returns the id of the helper admin.
func helperAdminID(t *testing.T, e *harness.Env) string {
	t.Helper()
	adminToken(t, e)
	rows := e.Rows(t, "SELECT id FROM users WHERE username = ?", helperAdmin)
	id, _ := rows[0]["id"].(string)
	return id
}
