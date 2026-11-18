package oidc

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// groupScenarios cover the group-claim and advanced-permission settings of
// sub-oidc-auth: the callback maps the provider's claims onto the user's type
// and permissions.
func groupScenarios() []harness.Scenario {
	const (
		groups = "INV-OIDC-GROUP-CLAIM-001"
		perms  = "INV-OIDC-ADVANCED-PERMS-001"
	)
	return []harness.Scenario{
		{
			Requirement: "sub-oidc-auth", Invariant: groups, Case: "edge", State: "changed",
			Assertion: "oidc-group-claim-maps-admin-then-downgrades-user",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:80-89", "server/auth/OidcAuthStrategy.js:174-223"},
			Claim:     "With authOpenIDGroupClaim=groups the provider scope gains that claim, a groups=[admin] login makes the new user an admin, and a later groups=[user] login for the same subject changes the same user to user while both logins create a session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDGroupClaim": "groups"})
				defer configureOIDC(t, e, p, nil)
				userinfo := defaultUserinfo("group-sub", "group-user", "group@example.org")
				adminInfo := cloneObject(userinfo)
				adminInfo["groups"] = []any{"admin"}
				login := loginWeb(t, e, p, e.Base+"/login", adminInfo)
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
				if got := login.Authorize.Query().Get("scope"); got != "openid profile email groups" {
					t.Fatalf("authorization scope = %q, want openid profile email groups", got)
				}
				row := userRowBySub(t, e, "group-sub")
				if row == nil || row["type"] != "admin" {
					t.Fatalf("groups=[admin] user = %v", row)
				}

				userInfo := cloneObject(userinfo)
				userInfo["groups"] = []any{"user"}
				second := loginWeb(t, e, p, e.Base+"/login", userInfo)
				requireWebLoginAnswer(t, e, second, e.Base+"/login")
				row = userRowBySub(t, e, "group-sub")
				if row == nil || row["type"] != "user" {
					t.Fatalf("groups=[user] user = %v", row)
				}
				if sessions := sessionsOf(t, e, row["id"].(string)); len(sessions) != 2 {
					t.Fatalf("sessions = %v", sessions)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: groups, Case: "error", State: "failure",
			Assertion: "oidc-group-claim-missing-denied",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:174-185"},
			Claim:     "With a group claim configured, a userinfo without that claim is denied with the login redirect and creates neither a user nor a session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDGroupClaim": "groups"})
				defer configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				denied := loginWebDenied(t, e, p, defaultUserinfo("no-group-sub", "no-group-user", "no-group@example.org"))
				requireLoginRedirect(t, "missing group claim", denied, "Unauthorized")
				noKnownGroup := defaultUserinfo("other-group-sub", "other-group-user", "other-group@example.org")
				noKnownGroup["groups"] = []any{"other"}
				denied = loginWebDenied(t, e, p, noKnownGroup)
				requireLoginRedirect(t, "no known group", denied, "Unauthorized")
				requireUnchanged(t, e, before, "missing group claim")
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: groups, Case: "error", State: "failure",
			Assertion: "oidc-root-user-cannot-be-downgraded",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:200-219", "server/models/User.js:286-306"},
			Claim:     "A root user matched by email whose group claim says user is denied with the Unauthorized login redirect and gets no session; the root account and its type remain, and clearing the link with the unlink route leaves a usable root again; the same root logging in with groups=[admin] succeeds without a type change.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDGroupClaim": "groups", "authOpenIDMatchExistingBy": "email"})
				defer configureOIDC(t, e, p, nil)
				root := e.Token(t, "root")
				rows := e.Rows(t, "SELECT id FROM users WHERE type = 'root'")
				if len(rows) != 1 {
					t.Fatalf("want one root user, got %d", len(rows))
				}
				rootID, _ := rows[0]["id"].(string)
				update := e.Patch(t, root, "/api/users/"+rootID, map[string]any{"email": "root-oidc@example.org"})
				requireStatus(t, "set root email", update, http.StatusOK)
				sessionsBefore := len(sessionsOf(t, e, rootID))
				userinfo := defaultUserinfo("root-sub", "root-provider", "root-oidc@example.org")
				userinfo["groups"] = []any{"user"}
				denied := loginWebDenied(t, e, p, userinfo)
				requireLoginRedirect(t, "root downgrade", denied, "Unauthorized")
				rootRow := userRow(t, e, rootID)
				if rootRow == nil || rootRow["type"] != "root" || rootRow["isActive"] != int64(1) {
					t.Fatalf("root user = %v", rootRow)
				}
				if got := len(sessionsOf(t, e, rootID)); got != sessionsBefore {
					t.Fatalf("denied root login created a session: %d -> %d", sessionsBefore, got)
				}
				adminInfo := cloneObject(userinfo)
				adminInfo["groups"] = []any{"admin"}
				adminLogin := loginWeb(t, e, p, e.Base+"/login", adminInfo)
				requireWebLoginAnswer(t, e, adminLogin, e.Base+"/login")
				rootRow = userRow(t, e, rootID)
				if rootRow == nil || rootRow["type"] != "root" {
					t.Fatalf("root+admin login changed the root: %v", rootRow)
				}
				if got := len(sessionsOf(t, e, rootID)); got != sessionsBefore+1 {
					t.Fatalf("root+admin login sessions = %d, want %d", got, sessionsBefore+1)
				}
				// Upstream links the subject before refusing the downgrade; clear it
				// so later scenarios see an unlinked root.
				unlinkResp := e.Patch(t, root, "/api/users/"+rootID+"/openid-unlink", nil)
				requireStatus(t, "cleanup root link", unlinkResp, http.StatusOK)
				if extra := columnJSON(t, userRow(t, e, rootID), "extraData"); extra["authOpenIDSub"] != nil {
					t.Fatalf("root link was not cleared: %v", extra)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: perms, Case: "normal", State: "changed",
			Assertion: "oidc-advanced-perms-claim-applies-to-user",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:230-244", "server/models/User.js:953-1000"},
			Claim:     "With authOpenIDAdvancedPermsClaim=abs_perms, a non-admin login whose claim disables canDownload and canAccessExplicitContent persists those two permission changes on the linked user (other defaults unchanged) and the login payload reports them.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDAdvancedPermsClaim": "abs_perms"})
				defer configureOIDC(t, e, p, nil)
				userinfo := defaultUserinfo("perms-sub", "perms-user", "perms@example.org")
				userinfo["abs_perms"] = map[string]any{"canDownload": false, "canAccessExplicitContent": false}
				login := loginWeb(t, e, p, e.Base+"/login", userinfo)
				accessToken, _ := requireWebLoginAnswer(t, e, login, e.Base+"/login")
				row := userRowBySub(t, e, "perms-sub")
				if row == nil {
					t.Fatal("the perms login did not create the user")
				}
				permissions := columnJSON(t, row, "permissions")
				if permissions["download"] != false || permissions["accessExplicitContent"] != false {
					t.Fatalf("permissions = %v", permissions)
				}
				if permissions["accessAllLibraries"] != true || permissions["accessAllTags"] != true || permissions["update"] != false {
					t.Fatalf("untouched permissions changed: %v", permissions)
				}
				me := e.Get(t, accessToken, "/api/me")
				requireStatus(t, "/api/me after permission claim", me, http.StatusOK)
				var answer struct {
					Permissions map[string]any `json:"permissions"`
				}
				me.JSON(t, &answer)
				if answer.Permissions["download"] != false || answer.Permissions["accessExplicitContent"] != false {
					t.Fatalf("/api/me permissions = %v", answer.Permissions)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: perms, Case: "error", State: "failure",
			Assertion: "oidc-advanced-perms-missing-denied-and-user-removed",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:230-244", "server/auth/OidcAuthStrategy.js:159-166"},
			Claim:     "With an advanced-permissions claim configured, a userinfo without it is denied with the login redirect, and the user row the callback had just auto-registered is removed again: no user with that subject and no session remains.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDAdvancedPermsClaim": "abs_perms"})
				defer configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				denied := loginWebDenied(t, e, p, defaultUserinfo("no-perms-sub", "no-perms-user", "no-perms@example.org"))
				requireLoginRedirect(t, "missing permission claim", denied, "Unauthorized")
				requireUnchanged(t, e, before, "missing permission claim")
				if row := userRowBySub(t, e, "no-perms-sub"); row != nil {
					t.Fatalf("the rejected auto-registration left a user: %v", row)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: perms, Case: "edge", State: "changed",
			Assertion: "oidc-advanced-perms-claim-ignored-for-admin",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:230-236"},
			Claim:     "An admin user that matches by email keeps its permissions when the advanced-permissions claim would change them; the login succeeds and the claim is ignored for the admin.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDAdvancedPermsClaim": "abs_perms", "authOpenIDMatchExistingBy": "email"})
				defer configureOIDC(t, e, p, nil)
				id := createUser(t, e, map[string]any{
					"username": "perms-admin", "password": "perms-admin-password", "type": "admin",
					"email": "perms-admin@example.org", "isActive": true,
				})
				before := userRow(t, e, id)
				userinfo := defaultUserinfo("perms-admin-sub", "provider-admin", "perms-admin@example.org")
				userinfo["abs_perms"] = map[string]any{"canDownload": false, "canAccessAllLibraries": false}
				login := loginWeb(t, e, p, e.Base+"/login", userinfo)
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
				after := userRow(t, e, id)
				if after == nil || after["type"] != "admin" {
					t.Fatalf("admin user = %v", after)
				}
				if columnJSON(t, after, "permissions")["download"] != true || columnJSON(t, before, "permissions")["download"] != true {
					t.Fatalf("admin permissions changed: %v", after)
				}
				if sessions := sessionsOf(t, e, id); len(sessions) != 1 {
					t.Fatalf("sessions = %v", sessions)
				}
			},
		},
	}
}
