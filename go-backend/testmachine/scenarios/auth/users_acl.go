package auth

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// aclUserScenarios are the user-administration and API key scenarios of
// sub-authorization-ownership-w5.
func aclUserScenarios() []harness.Scenario {
	const (
		req  = "sub-authorization-ownership-w5"
		user = "INV-W5-ACL-USER-001"
	)
	return []harness.Scenario{
		{
			Requirement: req, Invariant: user, Case: "error", State: "initial", Assertion: "admin-cannot-modify-or-delete-root",
			Sources: []string{"server/controllers/UserController.js:205-214", "server/controllers/UserController.js:205-214", "server/controllers/UserController.js:363-380"},
			Claim:   "An admin who is not root gets 403 for PATCH and DELETE of the root user and the row is unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := newAccount(t, e, map[string]any{"type": "admin"})
				token := tokenFor(t, e, admin)
				root := rootID(t, e)
				before := e.Rows(t, "SELECT username, type, pash, updatedAt FROM users WHERE id = ?", root)
				requireStatus(t, "patch root", e.Patch(t, token, "/api/users/"+root, map[string]any{"username": "hijacked"}), http.StatusForbidden)
				requireStatus(t, "delete root", e.Delete(t, token, "/api/users/"+root), http.StatusForbidden)
				after := e.Rows(t, "SELECT username, type, pash, updatedAt FROM users WHERE id = ?", root)
				if len(after) != 1 || after[0]["username"] != before[0]["username"] || after[0]["pash"] != before[0]["pash"] || after[0]["updatedAt"] != before[0]["updatedAt"] {
					t.Fatalf("root row changed: %v -> %v", before, after)
				}
			},
		},
		{
			Requirement: req, Invariant: user, Case: "error", State: "repeat", Assertion: "root-protection-holds-on-repeat",
			Sources: []string{"server/controllers/UserController.js:205-214", "server/controllers/UserController.js:363-380"},
			Claim:   "Repeating the root-user PATCH and DELETE as non-root admin keeps answering 403 and never alters the root's sessions or row.",
			Run: func(t *testing.T, e *harness.Env) {
				token := tokenFor(t, e, newAccount(t, e, map[string]any{"type": "admin"}))
				root := rootID(t, e)
				sessions := sessionCount(t, e, root)
				for range 3 {
					requireStatus(t, "patch root", e.Patch(t, token, "/api/users/"+root, map[string]any{"password": "hijack"}), http.StatusForbidden)
					requireStatus(t, "delete root", e.Delete(t, token, "/api/users/"+root), http.StatusForbidden)
				}
				if sessionCount(t, e, root) != sessions {
					t.Fatal("root sessions changed")
				}
				requireStatus(t, "root still logs in", e.Post(t, "", "/login", map[string]string{"username": "trial-root", "password": "trial-root-password"}), http.StatusOK)
			},
		},
		{
			Requirement: req, Invariant: user, Case: "normal", State: "initial", Assertion: "root-token-hidden-from-admins",
			Sources: []string{"server/controllers/UserController.js:29-35", "server/controllers/UserController.js:59-110", "server/models/User.js:603-641"},
			Claim:   "User list and user detail show the root user's legacy token as an empty string to an admin and in full to root.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := tokenFor(t, e, newAccount(t, e, map[string]any{"type": "admin"}))
				root := rootID(t, e)
				for token, wantHidden := range map[string]bool{admin: true, e.Token(t, "root"): false} {
					resp := e.Get(t, token, "/api/users/"+root)
					requireStatus(t, "get root", resp, http.StatusOK)
					var detail struct {
						Token string `json:"token"`
					}
					resp.JSON(t, &detail)
					list := e.Get(t, token, "/api/users")
					requireStatus(t, "list users", list, http.StatusOK)
					var all struct {
						Users []struct {
							ID    string `json:"id"`
							Token string `json:"token"`
						} `json:"users"`
					}
					list.JSON(t, &all)
					listed := false
					for _, u := range all.Users {
						if u.ID == root {
							listed = true
							if (u.Token == "") != wantHidden {
								t.Fatalf("listed root token empty = %v, want %v", u.Token == "", wantHidden)
							}
						}
					}
					if !listed || (detail.Token == "") != wantHidden {
						t.Fatalf("root token hidden = %v (listed %v), want %v", detail.Token == "", listed, wantHidden)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: user, Case: "error", State: "initial", Assertion: "user-cannot-delete-self",
			Sources: []string{"server/controllers/UserController.js:363-372"},
			Claim:   "DELETE /api/users/:id of the caller's own id answers 400 for admin and root and deletes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := newAccount(t, e, map[string]any{"type": "admin"})
				requireStatus(t, "admin self", e.Delete(t, tokenFor(t, e, admin), "/api/users/"+admin.ID), http.StatusBadRequest)
				requireStatus(t, "root self", e.Delete(t, e.Token(t, "root"), "/api/users/"+rootID(t, e)), http.StatusBadRequest)
				if e.Count(t, "SELECT COUNT(*) FROM users WHERE id IN (?, ?)", admin.ID, rootID(t, e)) != 2 {
					t.Fatal("no user may be deleted")
				}
			},
		},
		{
			Requirement: req, Invariant: user, Case: "error", State: "failure", Assertion: "invalid-user-requests-rejected-without-changes",
			Sources: []string{"server/controllers/UserController.js:116-130", "server/controllers/UserController.js:205-240"},
			Claim:   "Missing credentials, an invalid type, a taken username, protected keys, non-object permissions and unknown users are rejected with 400 or 404 and change no user row.",
			Run:     checkInvalidUserRequests,
		},
		{
			Requirement: req, Invariant: user, Case: "edge", State: "initial", Assertion: "malformed-permission-input-is-ignored",
			Sources: []string{"server/controllers/UserController.js:130-203"},
			Claim:   "POST /api/users ignores non-boolean permission values, unknown permission keys and non-string library lists, keeps the type defaults, and creates the user inactive when isActive is omitted.",
			Run:     checkMalformedPermissions,
		},
		{
			Requirement: req, Invariant: user, Case: "normal", State: "changed", Assertion: "admin-creates-and-deletes-user",
			Sources: []string{"server/controllers/UserController.js:197-203", "server/controllers/UserController.js:363-412"},
			Claim:   "A non-root admin creates and deletes an ordinary user: the row appears and disappears, user_added and user_removed go to admin sockets only, and the deleted user's token is denied.",
			Run:     checkAdminCreatesAndDeletes,
		},
		{
			Requirement: req, Invariant: user, Case: "error", State: "changed", Assertion: "inactive-user-is-locked-out-everywhere",
			Sources: []string{"server/auth/LocalAuthStrategy.js:55-62", "server/auth/TokenManager.js:298-303", "server/auth/TokenManager.js:320-326", "server/auth/TokenManager.js:400-406"},
			Claim:   "After deactivation a user's login, access token, API key and refresh token are all denied with 401, and all work again after reactivation.",
			Run:     checkInactiveLockout,
		},
		{
			Requirement: req, Invariant: user, Case: "error", State: "failure", Assertion: "api-key-requests-gate-and-validation",
			Sources: []string{"server/controllers/ApiKeyController.js:50-75", "server/controllers/ApiKeyController.js:112-177", "server/controllers/ApiKeyController.js:196-212"},
			Claim:   "Ordinary users get 403 on API key routes; admins get 400 for a missing name, bad expiresIn, missing or unknown user and a non-boolean isActive, and 404 for unknown keys; none of this changes the apiKeys table.",
			Run:     checkAPIKeyValidation,
		},
		{
			Requirement: req, Invariant: user, Case: "normal", State: "changed", Assertion: "api-key-root-ownership-and-lifecycle",
			Sources: []string{"server/controllers/ApiKeyController.js:66-110", "server/controllers/ApiKeyController.js:112-160", "server/controllers/ApiKeyController.js:179-190"},
			Claim:   "An admin may create keys for ordinary users (row records user and creator) but gets 403 creating a key for root or changing a root-owned key; root may do both; deleting a key removes the row and its token stops working.",
			Run:     checkAPIKeyOwnership,
		},
	}
}

func checkInvalidUserRequests(t *testing.T, e *harness.Env) {
	root := e.Token(t, "root")
	target := newAccount(t, e, nil)
	users := e.Rows(t, "SELECT id, username, type, pash, token, permissions, updatedAt FROM users ORDER BY id")
	cases := []struct {
		name         string
		method, path string
		body         any
		status       int
	}{
		{"create without password", http.MethodPost, "/api/users", map[string]any{"username": "no-password"}, http.StatusBadRequest},
		{"create without username", http.MethodPost, "/api/users", map[string]any{"password": "pw"}, http.StatusBadRequest},
		{"create with numeric password", http.MethodPost, "/api/users", map[string]any{"username": "numeric", "password": 5}, http.StatusBadRequest},
		{"create with invalid type", http.MethodPost, "/api/users", map[string]any{"username": "bad-type", "password": "pw", "type": "superuser"}, http.StatusBadRequest},
		{"create with taken username", http.MethodPost, "/api/users", map[string]any{"username": target.Name, "password": "pw"}, http.StatusBadRequest},
		{"patch pash", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"pash": "x"}, http.StatusBadRequest},
		{"patch token", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"token": "x"}, http.StatusBadRequest},
		{"patch id", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"id": "x"}, http.StatusBadRequest},
		{"patch extraData", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"extraData": map[string]any{}}, http.StatusBadRequest},
		{"patch bookmarks", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"bookmarks": []any{}}, http.StatusBadRequest},
		{"patch invalid type", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"type": "superuser"}, http.StatusBadRequest},
		{"patch permissions string", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"permissions": "all"}, http.StatusBadRequest},
		{"patch taken username", http.MethodPatch, "/api/users/" + target.ID, map[string]any{"username": listenerName}, http.StatusBadRequest},
		{"patch unknown user", http.MethodPatch, "/api/users/00000000-0000-4000-8000-000000000000", map[string]any{"isActive": false}, http.StatusNotFound},
		{"delete unknown user", http.MethodDelete, "/api/users/00000000-0000-4000-8000-000000000000", nil, http.StatusNotFound},
		{"get unknown user", http.MethodGet, "/api/users/00000000-0000-4000-8000-000000000000", nil, http.StatusNotFound},
	}
	for _, c := range cases {
		resp := e.Do(t, harness.Request{Method: c.method, Path: c.path, Token: root, JSON: c.body})
		if resp.Status != c.status {
			t.Fatalf("%s: status %d, want %d: %s", c.name, resp.Status, c.status, resp.Body)
		}
	}
	after := e.Rows(t, "SELECT id, username, type, pash, token, permissions, updatedAt FROM users ORDER BY id")
	if len(after) != len(users) {
		t.Fatalf("users changed from %d to %d rows", len(users), len(after))
	}
	for i := range users {
		for key, value := range users[i] {
			if after[i][key] != value {
				t.Fatalf("rejected requests changed user %v column %s", users[i]["id"], key)
			}
		}
	}
}

func checkMalformedPermissions(t *testing.T, e *harness.Env) {
	root := e.Token(t, "root")
	body := map[string]any{
		"username": "malformed-permissions", "password": "malformed-password",
		"permissions":         map[string]any{"download": "yes", "update": true, "bogus": true},
		"librariesAccessible": []any{1, 2},
		"itemTagsSelected":    "not-a-list",
	}
	resp := e.Post(t, root, "/api/users", body)
	requireStatus(t, "create", resp, http.StatusOK)
	var created struct {
		User struct {
			ID          string         `json:"id"`
			IsActive    bool           `json:"isActive"`
			Type        string         `json:"type"`
			Permissions map[string]any `json:"permissions"`
			Libraries   []string       `json:"librariesAccessible"`
			Tags        []string       `json:"itemTagsSelected"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	u := created.User
	if u.IsActive || u.Type != "user" || u.Permissions["download"] != true || u.Permissions["update"] != true || u.Permissions["delete"] != false {
		t.Fatalf("created user = %s", resp.Body)
	}
	if _, bogus := u.Permissions["bogus"]; bogus || len(u.Libraries) != 0 || len(u.Tags) != 0 || u.Libraries == nil || u.Tags == nil {
		t.Fatalf("created user = %s", resp.Body)
	}
	requireDenied(t, "login of an inactive new user", e.Post(t, "", "/login", map[string]string{"username": "malformed-permissions", "password": "malformed-password"}))
}

func checkAdminCreatesAndDeletes(t *testing.T, e *harness.Env) {
	admin := newAccount(t, e, map[string]any{"type": "admin"})
	adminToken := tokenFor(t, e, admin)
	adminSocket := e.Socket(t, adminToken)
	ordinarySocket := e.Socket(t, e.Token(t, "listener"))
	markAdmin, markOrdinary := adminSocket.Mark(), ordinarySocket.Mark()
	resp := e.Post(t, adminToken, "/api/users", map[string]any{"username": "created-by-admin", "password": "created-password", "isActive": true})
	requireStatus(t, "create", resp, http.StatusOK)
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	id := created.User.ID
	adminSocket.Wait(t, markAdmin, "user_added", 5*time.Second, eventFor(id))
	ordinarySocket.None(t, markOrdinary, "user_added", noEventWindow, nil)
	token, _ := e.Login(t, "created-by-admin", "created-password")
	requireStatus(t, "new user authorizes", e.Post(t, token, "/api/authorize", nil), http.StatusOK)
	requireStatus(t, "delete", e.Delete(t, adminToken, "/api/users/"+id), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM users WHERE id = ?", id) != 0 || sessionCount(t, e, id) != 0 {
		t.Fatal("the user row and its sessions must be gone")
	}
	adminSocket.Wait(t, markAdmin, "user_removed", 5*time.Second, eventFor(id))
	ordinarySocket.None(t, markOrdinary, "user_removed", noEventWindow, nil)
	if status := authorizeStatus(t, e, token); status != http.StatusUnauthorized {
		t.Fatalf("token of a deleted user: status %d, want 401", status)
	}
}

func checkInactiveLockout(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	key := newAPIKey(t, e, acc.ID, 0, true)
	patchUser(t, e, acc.ID, map[string]any{"isActive": false})
	requireDenied(t, "login", loginAs(t, e, acc, nil))
	requireDeniedWithError(t, "refresh", refreshByHeader(t, e, s.Refresh))
	for name, token := range map[string]string{"access token": s.Access, "api key": key.Token} {
		if status := authorizeStatus(t, e, token); status != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", name, status)
		}
	}
	patchUser(t, e, acc.ID, map[string]any{"isActive": true})
	requireStatus(t, "login", loginAs(t, e, acc, nil), http.StatusOK)
	requireAuthorizedAs(t, e.Post(t, s.Access, "/api/authorize", nil), acc.ID)
	requireAuthorizedAs(t, e.Post(t, key.Token, "/api/authorize", nil), acc.ID)
}

func checkAPIKeyValidation(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	admin := newAccount(t, e, map[string]any{"type": "admin"})
	adminToken := tokenFor(t, e, admin)
	key := newAPIKey(t, e, acc.ID, 0, false)
	count := e.Count(t, "SELECT COUNT(*) FROM apiKeys")
	state := e.Rows(t, "SELECT isActive, userId, updatedAt FROM apiKeys WHERE id = ?", key.ID)
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/api/api-keys"}, {http.MethodPost, "/api/api-keys"}, {http.MethodPatch, "/api/api-keys/" + key.ID}, {http.MethodDelete, "/api/api-keys/" + key.ID}} {
		resp := e.Do(t, harness.Request{Method: route.method, Path: route.path, Token: e.Token(t, "listener"), JSON: map[string]any{"name": "x", "userId": acc.ID, "isActive": true}})
		if resp.Status != http.StatusForbidden {
			t.Fatalf("ordinary %s %s: status %d, want 403", route.method, route.path, resp.Status)
		}
	}
	cases := []struct {
		name         string
		method, path string
		body         any
		status       int
	}{
		{"create without name", http.MethodPost, "/api/api-keys", map[string]any{"userId": acc.ID}, http.StatusBadRequest},
		{"create with numeric name", http.MethodPost, "/api/api-keys", map[string]any{"name": 5, "userId": acc.ID}, http.StatusBadRequest},
		{"create with negative expiry", http.MethodPost, "/api/api-keys", map[string]any{"name": "k", "userId": acc.ID, "expiresIn": -5}, http.StatusBadRequest},
		{"create with text expiry", http.MethodPost, "/api/api-keys", map[string]any{"name": "k", "userId": acc.ID, "expiresIn": "soon"}, http.StatusBadRequest},
		{"create without user", http.MethodPost, "/api/api-keys", map[string]any{"name": "k"}, http.StatusBadRequest},
		{"create for unknown user", http.MethodPost, "/api/api-keys", map[string]any{"name": "k", "userId": "00000000-0000-4000-8000-000000000000"}, http.StatusBadRequest},
		{"patch with text isActive", http.MethodPatch, "/api/api-keys/" + key.ID, map[string]any{"isActive": "yes"}, http.StatusBadRequest},
		{"patch with numeric userId", http.MethodPatch, "/api/api-keys/" + key.ID, map[string]any{"userId": 7}, http.StatusBadRequest},
		{"patch to unknown user", http.MethodPatch, "/api/api-keys/" + key.ID, map[string]any{"userId": "00000000-0000-4000-8000-000000000000"}, http.StatusBadRequest},
		{"patch unknown key", http.MethodPatch, "/api/api-keys/00000000-0000-4000-8000-000000000000", map[string]any{"isActive": true}, http.StatusNotFound},
		{"delete unknown key", http.MethodDelete, "/api/api-keys/00000000-0000-4000-8000-000000000000", nil, http.StatusNotFound},
	}
	for _, c := range cases {
		resp := e.Do(t, harness.Request{Method: c.method, Path: c.path, Token: adminToken, JSON: c.body})
		if resp.Status != c.status {
			t.Fatalf("%s: status %d, want %d: %s", c.name, resp.Status, c.status, resp.Body)
		}
	}
	after := e.Rows(t, "SELECT isActive, userId, updatedAt FROM apiKeys WHERE id = ?", key.ID)
	if e.Count(t, "SELECT COUNT(*) FROM apiKeys") != count || len(after) != 1 || after[0]["isActive"] != state[0]["isActive"] || after[0]["userId"] != state[0]["userId"] || after[0]["updatedAt"] != state[0]["updatedAt"] {
		t.Fatal("rejected API key requests changed the table")
	}
}

func checkAPIKeyOwnership(t *testing.T, e *harness.Env) {
	admin := newAccount(t, e, map[string]any{"type": "admin"})
	adminToken := tokenFor(t, e, admin)
	rootToken := e.Token(t, "root")
	root := rootID(t, e)
	ordinary := newAccount(t, e, nil)

	resp := e.Post(t, adminToken, "/api/api-keys", map[string]any{"name": "admin-made", "userId": ordinary.ID, "isActive": true})
	requireStatus(t, "admin creates for ordinary", resp, http.StatusOK)
	var created struct {
		APIKey struct {
			ID     string `json:"id"`
			APIKey string `json:"apiKey"`
		} `json:"apiKey"`
	}
	resp.JSON(t, &created)
	rows := e.Rows(t, "SELECT userId, createdByUserId, isActive, name FROM apiKeys WHERE id = ?", created.APIKey.ID)
	if len(rows) != 1 || rows[0]["userId"] != ordinary.ID || rows[0]["createdByUserId"] != admin.ID || rows[0]["isActive"] != int64(1) || rows[0]["name"] != "admin-made" {
		t.Fatalf("api key row = %v", rows)
	}
	requireAuthorizedAs(t, e.Post(t, created.APIKey.APIKey, "/api/authorize", nil), ordinary.ID)

	count := e.Count(t, "SELECT COUNT(*) FROM apiKeys")
	requireStatus(t, "admin creates for root", e.Post(t, adminToken, "/api/api-keys", map[string]any{"name": "for-root", "userId": root}), http.StatusForbidden)
	if e.Count(t, "SELECT COUNT(*) FROM apiKeys") != count {
		t.Fatal("a denied create added a key")
	}
	rootKey := newAPIKey(t, e, root, 0, false) // root may create root keys
	requireStatus(t, "admin patches root key", e.Patch(t, adminToken, "/api/api-keys/"+rootKey.ID, map[string]any{"isActive": true}), http.StatusForbidden)
	requireStatus(t, "admin reassigns to root", e.Patch(t, adminToken, "/api/api-keys/"+created.APIKey.ID, map[string]any{"userId": root}), http.StatusForbidden)
	rows = e.Rows(t, "SELECT isActive FROM apiKeys WHERE id = ?", rootKey.ID)
	if rows[0]["isActive"] != int64(0) || e.Rows(t, "SELECT userId FROM apiKeys WHERE id = ?", created.APIKey.ID)[0]["userId"] != ordinary.ID {
		t.Fatal("denied patches changed keys")
	}
	keyBefore := e.Rows(t, "SELECT updatedAt FROM apiKeys WHERE id = ?", rootKey.ID)[0]["updatedAt"].(string)
	requireStatus(t, "root patches root key", e.Patch(t, rootToken, "/api/api-keys/"+rootKey.ID, map[string]any{"isActive": true}), http.StatusOK)
	keyAfter := e.Rows(t, "SELECT isActive, updatedAt FROM apiKeys WHERE id = ?", rootKey.ID)[0]
	if keyAfter["isActive"] != int64(1) || keyAfter["updatedAt"].(string) <= keyBefore {
		t.Fatalf("an effective patch must activate the key and move updatedAt: %v (was %s)", keyAfter, keyBefore)
	}

	// The list shows the key with its user and creator; a repeated identical patch
	// changes nothing and an effective one reassigns the key.
	list := e.Get(t, adminToken, "/api/api-keys")
	requireStatus(t, "list keys", list, http.StatusOK)
	var all struct {
		APIKeys []struct {
			ID            string                              `json:"id"`
			Name          string                              `json:"name"`
			IsActive      bool                                `json:"isActive"`
			User          struct{ ID, Username, Type string } `json:"user"`
			CreatedByUser struct{ ID, Username, Type string } `json:"createdByUser"`
		} `json:"apiKeys"`
	}
	list.JSON(t, &all)
	found := false
	for _, k := range all.APIKeys {
		if k.ID == created.APIKey.ID {
			found = k.Name == "admin-made" && k.IsActive && k.User.ID == ordinary.ID && k.User.Username == ordinary.Name && k.User.Type == "user" && k.CreatedByUser.ID == admin.ID && k.CreatedByUser.Type == "admin"
		}
	}
	if !found {
		t.Fatalf("key missing or wrong in the list: %s", list.Body)
	}
	unchanged := e.Rows(t, "SELECT updatedAt FROM apiKeys WHERE id = ?", created.APIKey.ID)[0]["updatedAt"]
	requireStatus(t, "no-op patch", e.Patch(t, adminToken, "/api/api-keys/"+created.APIKey.ID, map[string]any{"isActive": true, "userId": ordinary.ID}), http.StatusOK)
	if e.Rows(t, "SELECT updatedAt FROM apiKeys WHERE id = ?", created.APIKey.ID)[0]["updatedAt"] != unchanged {
		t.Fatal("a no-op patch must not touch updatedAt")
	}
	requireStatus(t, "reassign", e.Patch(t, adminToken, "/api/api-keys/"+created.APIKey.ID, map[string]any{"userId": admin.ID}), http.StatusOK)
	reassigned := e.Rows(t, "SELECT userId, updatedAt FROM apiKeys WHERE id = ?", created.APIKey.ID)[0]
	if reassigned["userId"] != admin.ID || reassigned["updatedAt"].(string) <= unchanged.(string) {
		t.Fatalf("reassigned key row = %v", reassigned)
	}
	requireAuthorizedAs(t, e.Post(t, created.APIKey.APIKey, "/api/authorize", nil), admin.ID)

	requireStatus(t, "delete", e.Delete(t, adminToken, "/api/api-keys/"+created.APIKey.ID), http.StatusOK)
	if e.Count(t, "SELECT COUNT(*) FROM apiKeys WHERE id = ?", created.APIKey.ID) != 0 {
		t.Fatal("the key row must be deleted")
	}
	if status := authorizeStatus(t, e, created.APIKey.APIKey); status != http.StatusUnauthorized {
		t.Fatalf("deleted key: status %d, want 401", status)
	}
}
