package auth

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// logoutScenarios are the scenarios of POST /logout (route-r006).
func logoutScenarios() []harness.Scenario {
	const (
		local = "INV-R006-LOCAL-LOGOUT-001"
		many  = "INV-R006-ALL-DEVICES-001"
		oidc  = "INV-LOGOUT-OIDC-SESSION-001"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r006", Invariant: local, Case: "normal", State: "initial", Assertion: "logout-cookie-revokes-session",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout with the refresh cookie answers 200 {redirect_url:null}, expires the refresh_token and auth_method cookies, deletes that sessions row and keeps the user, so the token no longer refreshes.",
			Run:     checkLogoutByCookie,
		},
		{
			Requirement: "route-r006", Invariant: local, Case: "normal", State: "initial", Assertion: "logout-header-token-revokes-session",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout with the x-refresh-token header deletes that sessions row and answers redirect_url null.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				requireLoggedOut(t, logoutWith(t, e, s.Refresh, ""))
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("the session row must be deleted")
				}
				requireDeniedWithError(t, "refresh after logout", refreshByHeader(t, e, s.Refresh))
			},
		},
		{
			Requirement: "route-r006", Invariant: local, Case: "edge", State: "initial", Assertion: "logout-without-token-succeeds",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout needs no authentication and without any refresh token answers 200 {redirect_url:null} and changes no sessions row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := sessionSnapshot(t, e)
				requireLoggedOut(t, e.Post(t, "", "/logout", nil))
				if after := sessionSnapshot(t, e); len(after) != len(before) {
					t.Fatalf("sessions changed from %d to %d rows", len(before), len(after))
				}
			},
		},
		{
			Requirement: "route-r006", Invariant: local, Case: "edge", State: "repeat", Assertion: "logout-repeat-succeeds",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "Repeating POST /logout with an already revoked refresh token answers 200 {redirect_url:null} again and changes nothing else.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				other := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				loginNative(t, e, other)
				requireLoggedOut(t, logoutWith(t, e, s.Refresh, ""))
				requireLoggedOut(t, logoutWith(t, e, s.Refresh, ""))
				if sessionCount(t, e, acc.ID) != 0 || sessionCount(t, e, other.ID) != 1 {
					t.Fatal("a repeated logout must not touch other sessions")
				}
			},
		},
		{
			Requirement: "route-r006", Invariant: local, Case: "error", State: "failure", Assertion: "logout-with-unknown-token-changes-nothing",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout with an unknown or malformed refresh token answers 200 {redirect_url:null} and deletes no sessions row, also with allDevices=1.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				loginNative(t, e, acc)
				before := sessionSnapshot(t, e)
				for _, query := range []string{"", "allDevices=1"} {
					requireLoggedOut(t, logoutWith(t, e, "garbage", query))
					requireLoggedOut(t, logoutWith(t, e, foreignToken(t, acc.ID, "refresh"), query))
				}
				if after := sessionSnapshot(t, e); len(after) != len(before) {
					t.Fatalf("sessions changed from %d to %d rows", len(before), len(after))
				}
			},
		},
		{
			Requirement: "route-r006", Invariant: local, Case: "edge", State: "initial", Assertion: "logout-keeps-other-sessions",
			Sources: []string{"server/Auth.js:486-523", "server/auth/TokenManager.js:511-543"},
			Claim:   "Logging out one session deletes only that sessions row; another session of the same user still refreshes.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				first := loginNative(t, e, acc)
				second := loginNative(t, e, acc)
				requireLoggedOut(t, logoutWith(t, e, first.Refresh, ""))
				if sessionCount(t, e, acc.ID) != 1 {
					t.Fatal("exactly the other session must remain")
				}
				refreshed(t, "other session", refreshByHeader(t, e, second.Refresh))
			},
		},
		{
			Requirement: "route-r006", Invariant: oidc, Case: "normal", State: "initial", Assertion: "logout-with-openid-cookies-returns-end-session-url",
			Sources: []string{"server/Auth.js:504-523", "server/auth/OidcAuthStrategy.js:395-423"},
			Claim:   "With OpenID configured with an end-session URL, POST /logout carrying auth_method=openid and an openid_id_token cookie answers that URL with id_token_hint and the same-origin post_logout_redirect_uri (none for openid-mobile), expires the auth_method and openid_id_token cookies and deletes the session.",
			Run:     checkOpenIDLogout,
		},
		{
			Requirement: "route-r006", Invariant: many, Case: "normal", State: "initial", Assertion: "all-devices-logout-revokes-user-sessions",
			Sources: []string{"server/Auth.js:486-499", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout?allDevices=1 deletes every sessions row of the token's user and no row of other users.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				other := newAccount(t, e, nil)
				first := loginNative(t, e, acc)
				second := loginNative(t, e, acc)
				bystander := loginNative(t, e, other)
				requireLoggedOut(t, logoutWith(t, e, first.Refresh, "allDevices=1"))
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("all sessions of the user must be deleted")
				}
				requireDeniedWithError(t, "second device", refreshByHeader(t, e, second.Refresh))
				refreshed(t, "other user", refreshByHeader(t, e, bystander.Refresh))
			},
		},
		{
			Requirement: "route-r006", Invariant: many, Case: "edge", State: "changed", Assertion: "all-devices-logout-with-rotated-token",
			Sources: []string{"server/Auth.js:486-499", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout?allDevices=1 with a just-rotated (grace) refresh token still deletes all sessions of its user.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				first := loginNative(t, e, acc)
				loginNative(t, e, acc)
				refreshed(t, "rotation", refreshByHeader(t, e, first.Refresh))
				requireLoggedOut(t, logoutWith(t, e, first.Refresh, "allDevices=1"))
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("all sessions of the user must be deleted")
				}
			},
		},
		{
			Requirement: "route-r006", Invariant: many, Case: "edge", State: "initial", Assertion: "all-devices-needs-exactly-1",
			Sources: []string{"server/Auth.js:486-499", "server/auth/TokenManager.js:511-543"},
			Claim:   "POST /logout with allDevices=true (not 1) deletes only the presented session.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				first := loginNative(t, e, acc)
				loginNative(t, e, acc)
				requireLoggedOut(t, logoutWith(t, e, first.Refresh, "allDevices=true"))
				if sessionCount(t, e, acc.ID) != 1 {
					t.Fatal("only the presented session may be deleted")
				}
			},
		},
	}
}

// requireLoggedOut requires the logout answer: 200 and a redirect_url that is JSON null.
func requireLoggedOut(t *testing.T, resp harness.Response) {
	t.Helper()
	requireStatus(t, "logout", resp, http.StatusOK)
	object := resp.Object(t)
	value, present := object["redirect_url"]
	if !present || value != nil {
		t.Fatalf("logout answer must be {redirect_url:null}: %s", resp.Body)
	}
}

func checkLogoutByCookie(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginBrowser(t, e, acc)
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/logout", Header: map[string]string{"Cookie": "refresh_token=" + s.Refresh + "; auth_method=local"}})
	requireLoggedOut(t, resp)
	for _, name := range []string{"refresh_token", "auth_method"} {
		cleared := false
		for _, cookie := range resp.Cookies {
			if cookie.Name == name && cookie.Value == "" && cookie.Path == "/" && cookie.Expires.Before(time.Now()) {
				cleared = true
			}
		}
		if !cleared {
			t.Fatalf("logout must expire the %s cookie: %v", name, resp.Header.Values("Set-Cookie"))
		}
	}
	if sessionCount(t, e, acc.ID) != 0 {
		t.Fatal("the session row must be deleted")
	}
	if e.Count(t, "SELECT COUNT(*) FROM users WHERE id = ?", acc.ID) != 1 {
		t.Fatal("logout must keep the user")
	}
	requireDeniedWithError(t, "refresh after logout", refreshByCookie(t, e, s.Refresh))
}

func checkOpenIDLogout(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	root := e.Token(t, "root")
	provider := "http://127.0.0.1:9/realm"
	settings := map[string]any{
		"authActiveAuthMethods":           []string{"local", "openid"},
		"authOpenIDIssuerURL":             provider,
		"authOpenIDAuthorizationURL":      provider + "/auth",
		"authOpenIDTokenURL":              provider + "/token",
		"authOpenIDUserInfoURL":           provider + "/userinfo",
		"authOpenIDJwksURL":               provider + "/jwks",
		"authOpenIDLogoutURL":             provider + "/logout",
		"authOpenIDClientID":              "abs-client",
		"authOpenIDClientSecret":          "abs-secret",
		"authOpenIDTokenSigningAlgorithm": "RS256",
	}
	requireStatus(t, "configure openid", e.Patch(t, root, "/api/auth-settings", settings), http.StatusOK)
	for _, method := range []string{"openid", "openid-mobile"} {
		acc := newAccount(t, e, nil)
		s := loginNative(t, e, acc)
		resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/logout", Header: map[string]string{
			"x-refresh-token": s.Refresh,
			"Cookie":          "auth_method=" + method + "; openid_id_token=id-token-value",
		}})
		requireStatus(t, "logout", resp, http.StatusOK)
		var answer struct {
			RedirectURL *string `json:"redirect_url"`
		}
		resp.JSON(t, &answer)
		if answer.RedirectURL == nil {
			t.Fatalf("%s: redirect_url is null: %s", method, resp.Body)
		}
		target, err := url.Parse(*answer.RedirectURL)
		if err != nil || target.Host != "127.0.0.1:9" || target.Path != "/realm/logout" || target.Query().Get("id_token_hint") != "id-token-value" {
			t.Fatalf("%s: redirect_url = %q (%v)", method, *answer.RedirectURL, err)
		}
		wantRedirect := e.URL + "/audiobookshelf/login"
		if method == "openid-mobile" {
			wantRedirect = ""
		}
		if got := target.Query().Get("post_logout_redirect_uri"); got != wantRedirect {
			t.Fatalf("%s: post_logout_redirect_uri = %q, want %q", method, got, wantRedirect)
		}
		for _, name := range []string{"auth_method", "openid_id_token", "refresh_token"} {
			cleared := false
			for _, cookie := range resp.Cookies {
				if cookie.Name == name && cookie.Value == "" && cookie.Expires.Before(time.Now()) {
					cleared = true
				}
			}
			if !cleared {
				t.Fatalf("%s: cookie %s not expired: %v", method, name, resp.Header.Values("Set-Cookie"))
			}
		}
		if sessionCount(t, e, acc.ID) != 0 {
			t.Fatalf("%s: session must be deleted", method)
		}
	}
}
