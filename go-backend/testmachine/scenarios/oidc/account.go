package oidc

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// accountScenarios cover the user side of the OIDC callback in sub-oidc-auth:
// auto-registration, the user_added event, email matching and running the same
// login twice.
func accountScenarios() []harness.Scenario {
	const (
		webCallback = "INV-OIDC-WEB-CALLBACK-001"
		emailLink   = "INV-OIDC-EMAIL-LINK-001"
		userAdded   = "INV-OIDC-USER-ADDED-001"
		idToken     = "INV-OIDC-IDTOKEN-VALIDATION-001"
	)
	return []harness.Scenario{
		{
			Requirement: "sub-oidc-auth", Invariant: webCallback, Case: "normal", State: "initial",
			Assertion: "oidc-web-auto-register-creates-linked-user",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:137-158", "server/models/User.js:311-341", "server/Auth.js:296-312"},
			Claim:     "With authOpenIDAutoRegister on, an unknown subject creates exactly one active 'user' whose username is preferred_username, whose email is the verified email, whose extraData carries authOpenIDSub, and whose session refreshes.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := userCount(t, e)
				login := loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("auto-sub", "auto-user", "auto@example.org"))
				_, refresh := requireWebLoginAnswer(t, e, login, e.Base+"/login")
				if got := userCount(t, e); got != before+1 {
					t.Fatalf("auto-registration created %d users, want %d", got-before, 1)
				}
				row := userRowBySub(t, e, "auto-sub")
				if row == nil {
					t.Fatal("the auto-registered user is not linked")
				}
				if row["username"] != "auto-user" || row["email"] != "auto@example.org" || row["type"] != "user" || row["isActive"] != int64(1) {
					t.Fatalf("auto-registered user = %+v", row)
				}
				if extra := columnJSON(t, row, "extraData"); extra["authOpenIDSub"] != "auto-sub" {
					t.Fatalf("extraData = %v", extra)
				}
				if sessions := sessionsOf(t, e, row["id"].(string)); len(sessions) != 1 || sessions[0]["refreshToken"] != refresh {
					t.Fatalf("sessions = %v", sessions)
				}
				refreshed := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": refresh}})
				requireStatus(t, "refresh auto-registered session", refreshed, http.StatusOK)
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: webCallback, Case: "error", State: "failure",
			Assertion: "oidc-auto-register-disabled-or-provider-failed-creates-nothing",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:137-146", "server/auth/OidcAuthStrategy.js:406-432"},
			Claim:     "With authOpenIDAutoRegister off an unknown subject is denied, and a provider error denies the flow; both answer 302 to /login?error=Unauthorized&autoLaunch=0 and create no user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDAutoRegister": false})
				defer configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				disabled := loginWebDenied(t, e, p, defaultUserinfo("disabled-sub", "disabled-user", "disabled@example.org"))
				requireLoginRedirect(t, "auto-register disabled", disabled, "Unauthorized")
				requireUnchanged(t, e, before, "auto-register disabled")

				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				providerError := providerRedirect(t, e, map[string][]string{"error": {"access_denied"}, "state": {state}}, b.header())
				requireStatus(t, "provider error", providerError, http.StatusFound)
				requireLoginRedirect(t, "provider error", providerError, "Unauthorized")
				requireUnchanged(t, e, before, "provider error")
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: idToken, Case: "error", State: "failure",
			Assertion: "oidc-invalid-id-tokens-create-no-user",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:117-166", "server/auth/OidcAuthStrategy.js:406-432"},
			Claim:     "A callback whose ID token has a broken signature, a foreign issuer, a foreign audience or an expired exp is denied with the login redirect and creates no user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				for _, mode := range []idTokenMode{idTokenBadSignature, idTokenWrongIssuer, idTokenWrongAudience, idTokenExpired} {
					p.reset()
					p.setMode(mode)
					denied := loginWebDenied(t, e, p, defaultUserinfo("invalid-token-sub", "invalid-token-user", "invalid-token@example.org"))
					requireLoginRedirect(t, "invalid ID token", denied, "Unauthorized")
					requireUnchanged(t, e, before, "invalid ID token")
				}
				p.reset()
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: userAdded, Case: "normal", State: "initial",
			Assertion: "oidc-auto-register-emits-user-added-to-admins-only",
			Sources:   []string{"server/models/User.js:311-341", "server/SocketAuthority.js"},
			Claim:     "An auto-registration emits one user_added event with the legacy browser user shape (id, username, type, isActive, hasOpenIDLink, permissions, token) to the root socket and no user_added to an ordinary user socket.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				admin := e.Socket(t, e.Token(t, "root"))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				adminMark, ordinaryMark := admin.Mark(), ordinary.Mark()
				loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("event-sub", "event-user", "event@example.org"))
				row := userRowBySub(t, e, "event-sub")
				if row == nil {
					t.Fatal("the auto-registration did not create the user")
				}
				event := admin.Wait(t, adminMark, "user_added", 5*time.Second, func(args []json.RawMessage) bool {
					return len(args) == 1 && hasID(args[0], row["id"].(string))
				})
				var payload struct {
					ID            string         `json:"id"`
					Username      string         `json:"username"`
					Email         *string        `json:"email"`
					Type          string         `json:"type"`
					Token         string         `json:"token"`
					IsActive      bool           `json:"isActive"`
					HasOpenIDLink bool           `json:"hasOpenIDLink"`
					Permissions   map[string]any `json:"permissions"`
					Libraries     []string       `json:"librariesAccessible"`
					MediaProgress []any          `json:"mediaProgress"`
					Bookmarks     []any          `json:"bookmarks"`
				}
				if err := json.Unmarshal(event.Args[0], &payload); err != nil {
					t.Fatalf("user_added payload: %v", err)
				}
				if payload.ID != row["id"] || payload.Username != "event-user" || payload.Type != "user" || !payload.IsActive || !payload.HasOpenIDLink || payload.Token == "" {
					t.Fatalf("user_added payload = %+v", payload)
				}
				if payload.Email == nil || *payload.Email != "event@example.org" {
					t.Fatalf("user_added email = %v", payload.Email)
				}
				if payload.Permissions["download"] != true || payload.Libraries == nil || payload.MediaProgress == nil || payload.Bookmarks == nil {
					t.Fatalf("user_added shape = %+v", payload)
				}
				ordinary.None(t, ordinaryMark, "user_added", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: emailLink, Case: "normal", State: "changed",
			Assertion: "oidc-email-match-links-existing-user-and-keeps-fields",
			Sources:   []string{"server/models/User.js:220-308", "server/auth/OidcAuthStrategy.js:131-153"},
			Claim:     "With authOpenIDMatchExistingBy=email, a verified email matches an existing unlinked user: the callback keeps its id, username, email and extraData values, adds only authOpenIDSub, and creates one session for that same user.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDMatchExistingBy": "email"})
				defer configureOIDC(t, e, p, nil)
				id := createUser(t, e, map[string]any{
					"username": "email-link-user", "password": "email-link-password",
					"email": "email-link@example.org", "isActive": true,
				})
				before := userRow(t, e, id)
				created := userCount(t, e)
				login := loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("email-link-sub", "provider-name", "email-link@example.org"))
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
				after := userRow(t, e, id)
				if after == nil || after["id"] != id {
					t.Fatalf("email match did not keep the user: %v", after)
				}
				if after["username"] != "email-link-user" || after["email"] != "email-link@example.org" || after["type"] != "user" {
					t.Fatalf("linked user fields = %+v", after)
				}
				beforeExtra := columnJSON(t, before, "extraData")
				afterExtra := columnJSON(t, after, "extraData")
				if afterExtra["authOpenIDSub"] != "email-link-sub" {
					t.Fatalf("extraData = %v", afterExtra)
				}
				for key, value := range beforeExtra {
					if key == "authOpenIDSub" {
						continue
					}
					if !sameJSON(afterExtra[key], value) {
						t.Fatalf("extraData %s changed: %v -> %v", key, value, afterExtra[key])
					}
				}
				if got := userCount(t, e); got != created {
					t.Fatalf("email match changed the user count: %d -> %d", created, got)
				}
				if sessions := sessionsOf(t, e, id); len(sessions) != 1 {
					t.Fatalf("sessions = %v", sessions)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: emailLink, Case: "error", State: "failure",
			Assertion: "oidc-email-match-unverified-email-denied",
			Sources:   []string{"server/models/User.js:230-256"},
			Claim:     "With email matching configured, a userinfo email that is explicitly unverified is denied with the login redirect, links nothing to the matching user and creates no session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDMatchExistingBy": "email"})
				defer configureOIDC(t, e, p, nil)
				id := createUser(t, e, map[string]any{
					"username": "unverified-user", "password": "unverified-password",
					"email": "unverified@example.org", "isActive": true,
				})
				before := snapshotOf(t, e)
				userinfo := defaultUserinfo("unverified-sub", "unverified-provider", "unverified@example.org")
				userinfo["email_verified"] = false
				denied := loginWebDenied(t, e, p, userinfo)
				requireLoginRedirect(t, "unverified email", denied, "Unauthorized")
				requireUnchanged(t, e, before, "unverified email")
				row := userRow(t, e, id)
				if extra := columnJSON(t, row, "extraData"); extra["authOpenIDSub"] != nil {
					t.Fatalf("the denied login linked the user: %v", extra)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: webCallback, Case: "normal", State: "repeat",
			Assertion: "oidc-repeat-web-login-reuses-user-adds-session",
			Sources:   []string{"server/models/User.js:220-227", "server/auth/TokenManager.js:184-200"},
			Claim:     "Two web logins of the same subject leave one linked user with the same id and create two refreshable sessions without a second user_added event.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				admin := e.Socket(t, e.Token(t, "root"))
				mark := admin.Mark()
				userinfo := defaultUserinfo("web-repeat-sub", "web-repeat-user", "web-repeat@example.org")
				first := loginWeb(t, e, p, e.Base+"/login", userinfo)
				requireWebLoginAnswer(t, e, first, e.Base+"/login")
				row := userRowBySub(t, e, "web-repeat-sub")
				if row == nil {
					t.Fatal("first login did not create the user")
				}
				admin.Wait(t, mark, "user_added", 5*time.Second, func(args []json.RawMessage) bool {
					return len(args) == 1 && hasID(args[0], row["id"].(string))
				})
				afterFirst := userCount(t, e)
				mark = admin.Mark()

				second := loginWeb(t, e, p, e.Base+"/login", userinfo)
				_, refresh := requireWebLoginAnswer(t, e, second, e.Base+"/login")
				if got := userCount(t, e); got != afterFirst {
					t.Fatalf("second login created a user: %d -> %d", afterFirst, got)
				}
				if got := userRowBySub(t, e, "web-repeat-sub"); got == nil || got["id"] != row["id"] {
					t.Fatalf("second login changed the user: %v -> %v", row, got)
				}
				if sessions := sessionsOf(t, e, row["id"].(string)); len(sessions) != 2 {
					t.Fatalf("sessions = %v", sessions)
				}
				admin.None(t, mark, "user_added", 500*time.Millisecond, nil)
				refreshed := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": refresh}})
				requireStatus(t, "refresh second session", refreshed, http.StatusOK)
			},
		},
	}
}

// loginWebDenied runs a web flow and returns its callback answer, which the
// caller requires to be a denial.
func loginWebDenied(t *testing.T, e *harness.Env, p *mockProvider, userinfo map[string]any) harness.Response {
	t.Helper()
	authorize, b := beginWeb(t, e, e.Base+"/login")
	state := authorize.Query().Get("state")
	code := p.issue(t, "", userinfo)
	resp := providerRedirect(t, e, map[string][]string{"state": {state}, "code": {code}}, b.header())
	requireStatus(t, "denied callback", resp, http.StatusFound)
	return resp
}

// hasID reports whether the object has the given id field.
func hasID(raw json.RawMessage, id string) bool {
	var object struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(raw, &object) == nil && object.ID == id
}
