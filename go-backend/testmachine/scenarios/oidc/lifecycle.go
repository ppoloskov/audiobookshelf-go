package oidc

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// lifecycleScenarios cover the settings side effects of sub-oidc-auth: enabling
// and disabling the method, boot-time initialisation, the 10-minute callback
// cookie and an end-session-less provider.
func lifecycleScenarios() []harness.Scenario {
	const (
		toggle   = "INV-OIDC-AUTH-METHOD-TOGGLE-001"
		bootInit = "INV-OIDC-BOOT-INIT-001"
		cbCookie = "INV-OIDC-WEB-CALLBACK-COOKIE-TTL-001"
		logout   = "INV-OIDC-WEB-LOGOUT-001"
	)
	return []harness.Scenario{
		{
			Requirement: "sub-oidc-auth", Invariant: toggle, Case: "edge", State: "changed",
			Assertion: "oidc-auth-method-toggle-disables-and-reenables",
			Sources:   []string{"server/controllers/MiscController.js:728-741", "server/Auth.js:169-192", "server/Server.js:388-394"},
			Claim:     "PATCH /api/auth-settings removing openid from authActiveAuthMethods makes GET /status advertise only local (the strategy is unused); re-adding openid makes /status advertise it again and a complete web login succeeds.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				root := e.Token(t, "root")
				disable := e.Patch(t, root, "/api/auth-settings", map[string]any{"authActiveAuthMethods": []string{"local"}})
				requireStatus(t, "disable openid", disable, http.StatusOK)
				if methods := statusAuthMethods(t, e); !sameJSON(methods, []any{"local"}) {
					t.Fatalf("authMethods after disable = %v", methods)
				}
				enable := e.Patch(t, root, "/api/auth-settings", map[string]any{"authActiveAuthMethods": []string{"local", "openid"}})
				requireStatus(t, "enable openid", enable, http.StatusOK)
				if methods := statusAuthMethods(t, e); !sameJSON(methods, []any{"local", "openid"}) {
					t.Fatalf("authMethods after enable = %v", methods)
				}
				login := loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("toggle-sub", "toggle-user", "toggle@example.org"))
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: bootInit, Case: "normal", State: "changed",
			Assertion: "oidc-settings-and-strategy-survive-restart",
			Sources:   []string{"server/Server.js:301-317", "server/Auth.js:111-121", "server/Database.js:440-444"},
			Claim:     "OpenID settings written through the API persist across a restart: GET /api/auth-settings still returns the issuer, GET /status still advertises openid, and a boot-initialised strategy completes a web login.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				own := harness.Start(t, harness.Options{Env: oidcEnv()})
				configureOIDC(t, own, p, nil)
				own.Restart(t)
				var settings struct {
					IssuerURL string `json:"authOpenIDIssuerURL"`
				}
				persisted := own.Get(t, own.Token(t, "root"), "/api/auth-settings")
				requireStatus(t, "auth settings after restart", persisted, http.StatusOK)
				persisted.JSON(t, &settings)
				if settings.IssuerURL != p.issuer() {
					t.Fatalf("persisted issuer = %q, want %q", settings.IssuerURL, p.issuer())
				}
				if methods := statusAuthMethods(t, own); !sameJSON(methods, []any{"local", "openid"}) {
					t.Fatalf("authMethods after restart = %v", methods)
				}
				login := loginWeb(t, own, p, own.Base+"/login", defaultUserinfo("boot-sub", "boot-user", "boot@example.org"))
				requireWebLoginAnswer(t, own, login, own.Base+"/login")
				if row := userRowBySub(t, own, "boot-sub"); row == nil {
					t.Fatal("the boot-initialised flow did not create the user")
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: cbCookie, Case: "edge", State: "failure",
			Assertion: "oidc-web-callback-without-auth-cb-cookie-redirects",
			Sources:   []string{"server/Auth.js:218-248", "server/Auth.js:259-281"},
			Claim:     "When the 10-minute auth_cb cookie is gone but the browser session is alive, a valid provider callback still creates the linked user and one session, sets the refresh cookie, and then answers 302 /login?error=No callback or already expired&autoLaunch=0 because no browser destination remains.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := userCount(t, e)
				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				code := p.issue(t, "", defaultUserinfo("no-cb-sub", "no-cb-user", "no-cb@example.org"))
				withoutCallback := browser{Session: b.Session, AuthMethod: b.AuthMethod}
				resp := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, withoutCallback.header())
				requireStatus(t, "callback without auth_cb", resp, http.StatusFound)
				requireLoginRedirect(t, "callback without auth_cb", resp, "No callback or already expired")
				if got := userCount(t, e); got != before+1 {
					t.Fatalf("the login did not create the user: %d -> %d", before, got)
				}
				row := userRowBySub(t, e, "no-cb-sub")
				if row == nil {
					t.Fatal("the login did not link the subject")
				}
				if sessions := sessionsOf(t, e, row["id"].(string)); len(sessions) != 1 {
					t.Fatalf("sessions = %v", sessions)
				}
				if _, ok := cookie(resp, "refresh_token"); !ok {
					t.Fatalf("the login did not set the refresh cookie: %v", resp.Header.Values("Set-Cookie"))
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: logout, Case: "edge", State: "initial",
			Assertion: "oidc-web-logout-without-end-session-returns-null",
			Sources:   []string{"server/Auth.js:504-523", "server/auth/OidcAuthStrategy.js:395-423"},
			Claim:     "With no authOpenIDLogoutURL configured, POST /logout for a web OIDC session answers 200 {redirect_url:null}, expires the auth cookies, deletes the session and keeps the user.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				own := harness.Start(t, harness.Options{Env: oidcEnv()})
				configureOIDC(t, own, p, map[string]any{"authOpenIDLogoutURL": nil})
				login := loginWeb(t, own, p, own.Base+"/login", defaultUserinfo("no-logout-sub", "no-logout-user", "no-logout@example.org"))
				requireWebLoginAnswer(t, own, login, own.Base+"/login")
				row := userRowBySub(t, own, "no-logout-sub")
				if row == nil {
					t.Fatal("the login did not link the user")
				}
				openIDToken, ok := cookie(login.Response, "openid_id_token")
				if !ok {
					t.Fatal("the login did not set openid_id_token")
				}
				refresh, ok := cookie(login.Response, "refresh_token")
				if !ok {
					t.Fatal("the login did not set refresh_token")
				}
				resp := own.Do(t, harness.Request{Method: http.MethodPost, Path: "/logout", Header: map[string]string{
					"Cookie": "refresh_token=" + refresh.Value + "; auth_method=openid; openid_id_token=" + openIDToken.Value,
				}})
				requireStatus(t, "logout without end-session URL", resp, http.StatusOK)
				var answer struct {
					RedirectURL *string `json:"redirect_url"`
				}
				resp.JSON(t, &answer)
				if answer.RedirectURL != nil {
					t.Fatalf("redirect_url = %q, want null", *answer.RedirectURL)
				}
				if sessions := sessionsOf(t, own, row["id"].(string)); len(sessions) != 0 {
					t.Fatalf("logout kept sessions: %v", sessions)
				}
				if userRow(t, own, row["id"].(string)) == nil {
					t.Fatal("logout deleted the user")
				}
			},
		},
	}
}

// statusAuthMethods returns the authMethods array GET /status advertises.
func statusAuthMethods(t *testing.T, e *harness.Env) []any {
	t.Helper()
	resp := e.Get(t, "", "/status")
	requireStatus(t, "status", resp, http.StatusOK)
	var status struct {
		AuthMethods []any `json:"authMethods"`
	}
	resp.JSON(t, &status)
	return status.AuthMethods
}
