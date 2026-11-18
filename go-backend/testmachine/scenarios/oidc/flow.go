package oidc

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// mobilePKCEScenarios cover the native client flow (flow-client-oidc-mobile-pkce):
// authorization on the server, the app link redirect, the PKCE callback and the
// tokens the app stores.
func mobilePKCEScenarios() []harness.Scenario {
	const invariant = "INV-FLOW-CLIENT-OIDC-MOBILE-PKCE-001"
	return []harness.Scenario{
		{
			Requirement: "flow-client-oidc-mobile-pkce", Invariant: invariant, Case: "normal", State: "initial",
			Assertion: "mobile-pkce-full-flow-returns-app-tokens",
			Sources:   []string{"server/Auth.js:362-380", "server/Auth.js:383-445", "server/auth/OidcAuthStrategy.js:252-281", "server/auth/OidcAuthStrategy.js:301-368", "go-backend/testmachine/contracts/clients/official-app.md:108"},
			Claim:     "The app's four calls (start with audiobookshelf://oauth and S256, provider authorize, mobile-redirect, callback with code_verifier) end in a 200 login payload whose access/refresh tokens authenticate, one linked user and one session; the provider URL carries the app's challenge and the token request carries the app's verifier.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				login := loginMobile(t, e, p, verifier, defaultUserinfo("pkce-sub", "pkce-user", "pkce@example.org"))
				requireAuthorizeURL(t, p, login.Authorize, e.URL+"/auth/openid/mobile-redirect", login.Challenge)

				var payload loginEnvelope
				login.Callback.JSON(t, &payload)
				if payload.User.AccessToken == "" || payload.User.RefreshToken == nil || payload.User.Token == "" || !payload.User.HasOpenIDLink {
					t.Fatalf("login payload = %s", login.Callback.Body)
				}
				if claims := decodeJWT(t, payload.User.AccessToken); claims["type"] != "access" || claims["userId"] != payload.User.ID {
					t.Fatalf("access token claims = %v", claims)
				}
				if claims := decodeJWT(t, *payload.User.RefreshToken); claims["type"] != "refresh" {
					t.Fatalf("refresh token claims = %v", claims)
				}
				me := e.Get(t, payload.User.AccessToken, "/api/me")
				requireStatus(t, "/api/me with app access token", me, http.StatusOK)
				if row := userRowBySub(t, e, "pkce-sub"); row == nil || row["id"] != payload.User.ID {
					t.Fatalf("linked user = %v", row)
				}
				if sessions := sessionsOf(t, e, payload.User.ID); len(sessions) != 1 {
					t.Fatalf("sessions = %v", sessions)
				}
			},
		},
		{
			Requirement: "flow-client-oidc-mobile-pkce", Invariant: invariant, Case: "error", State: "failure",
			Assertion: "mobile-pkce-wrong-verifier-denied-without-user",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:356-368", "server/Auth.js:399-432", "server/Auth.js:449-452"},
			Claim:     "A native callback whose code_verifier does not match the challenge sent at start follows the upstream web-style failure (302 /login?error=Error in callback&autoLaunch=0, because session.mobile holds the redirect URI, not true and the provider invalid_grant reaches the error branch), creates no user or session, and consumes the flow so a repeat answers 400 No session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				authorize, b, state, challenge := beginMobile(t, e, verifier)
				requireAuthorizeURL(t, p, authorize, e.URL+"/auth/openid/mobile-redirect", challenge)
				code := p.issue(t, challenge, defaultUserinfo("wrong-verifier-sub", "wrong-verifier-user", "wrong-verifier@example.org"))
				requireStatus(t, "mobile redirect", mobileRedirect(t, e, state, code), http.StatusFound)
				before := snapshotOf(t, e)

				wrong := randomVerifier()
				callback := providerRedirect(t, e, url.Values{
					"state": {state}, "code": {code}, "code_verifier": {wrong},
				}, b.header())
				requireStatus(t, "wrong verifier callback", callback, http.StatusFound)
				requireLoginRedirect(t, "wrong verifier callback", callback, "Error in callback")
				requireUnchanged(t, e, before, "wrong verifier callback")

				replay := providerRedirect(t, e, url.Values{
					"state": {state}, "code": {code}, "code_verifier": {verifier},
				}, b.header())
				requireStatus(t, "replay after wrong verifier", replay, http.StatusBadRequest)
				requireBody(t, "replay after wrong verifier", replay, "No session")
			},
		},
		{
			Requirement: "flow-client-oidc-mobile-pkce", Invariant: invariant, Case: "edge", State: "initial",
			Assertion: "mobile-pkce-defaults-method-to-s256",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:259-267", "server/auth/OidcAuthStrategy.js:365-373"},
			Claim:     "A native start that sends code_challenge without code_challenge_method is accepted, the provider URL states code_challenge_method=S256, and the flow completes with the matching verifier.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				challenge := s256(verifier)
				start := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {mobileRedirectURI},
					"code_challenge": {challenge},
				}})
				requireStatus(t, "start without method", start, http.StatusFound)
				authorize := location(t, start)
				state := authorize.Query().Get("state")
				if got := authorize.Query().Get("code_challenge_method"); got != "S256" {
					t.Fatalf("default code_challenge_method = %q, want S256", got)
				}
				b := browserFrom(start)
				code := p.issue(t, challenge, defaultUserinfo("default-method-sub", "default-method-user", "default-method@example.org"))
				requireStatus(t, "mobile redirect", mobileRedirect(t, e, state, code), http.StatusFound)
				callback := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}, "code_verifier": {verifier}}, b.header())
				requireStatus(t, "callback", callback, http.StatusOK)
				if row := userRowBySub(t, e, "default-method-sub"); row == nil {
					t.Fatal("the default-method flow did not create the user")
				}
			},
		},
		{
			Requirement: "flow-client-oidc-mobile-pkce", Invariant: invariant, Case: "edge", State: "repeat",
			Assertion: "mobile-pkce-empty-or-duplicate-verifier-denied",
			Sources:   []string{"server/Auth.js:399-404", "server/auth/OidcAuthStrategy.js:301-368"},
			Claim:     "With a valid started session, a native callback that sends an empty code_verifier or a repeated code_verifier parameter is rejected (302 /login?error=Error in callback&autoLaunch=0, since the provider refuses the token request), creates no user or session and consumes the flow.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				cases := []struct {
					name  string
					query url.Values
				}{
					{"empty verifier", url.Values{"code_verifier": {""}}},
					{"duplicate verifier", url.Values{"code_verifier": {"a", "b"}}},
				}
				for _, item := range cases {
					verifier := randomVerifier()
					_, b, state, challenge := beginMobile(t, e, verifier)
					code := p.issue(t, challenge, defaultUserinfo("verifier-"+item.name, "verifier-user", "verifier@example.org"))
					requireStatus(t, item.name+" mobile redirect", mobileRedirect(t, e, state, code), http.StatusFound)
					before := snapshotOf(t, e)
					query := url.Values{"state": {state}, "code": {code}}
					for key, values := range item.query {
						query[key] = values
					}
					callback := providerRedirect(t, e, query, b.header())
					requireStatus(t, item.name, callback, http.StatusFound)
					requireLoginRedirect(t, item.name, callback, "Error in callback")
					requireUnchanged(t, e, before, item.name)
				}
			},
		},
		{
			Requirement: "flow-client-oidc-mobile-pkce", Invariant: invariant, Case: "normal", State: "changed",
			Assertion: "mobile-pkce-repeat-login-reuses-linked-user",
			Sources:   []string{"server/models/User.js:220-226", "server/Auth.js:296-312"},
			Claim:     "A second native login for the same provider subject reuses the linked user row (same id, type and extraData) and creates a second session; both returned refresh tokens refresh.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				userinfo := defaultUserinfo("relogin-sub", "relogin-user", "relogin@example.org")
				first := loginMobile(t, e, p, randomVerifier(), userinfo)
				var firstPayload loginEnvelope
				first.Callback.JSON(t, &firstPayload)
				rowBefore := userRowBySub(t, e, "relogin-sub")
				if rowBefore == nil {
					t.Fatal("first login did not create the user")
				}
				extraBefore := rowBefore["extraData"]

				second := loginMobile(t, e, p, randomVerifier(), userinfo)
				var secondPayload loginEnvelope
				second.Callback.JSON(t, &secondPayload)
				if secondPayload.User.ID != firstPayload.User.ID || secondPayload.User.ID != rowBefore["id"] {
					t.Fatalf("second login user = %q, first %q, row %v", secondPayload.User.ID, firstPayload.User.ID, rowBefore["id"])
				}
				rowAfter := userRowBySub(t, e, "relogin-sub")
				if rowAfter["type"] != "user" || rowAfter["extraData"] != extraBefore {
					t.Fatalf("second login changed the user row: %v -> %v", rowBefore, rowAfter)
				}
				if sessions := sessionsOf(t, e, firstPayload.User.ID); len(sessions) != 2 {
					t.Fatalf("two logins created %d sessions, want 2", len(sessions))
				}
				for _, token := range []string{*firstPayload.User.RefreshToken, *secondPayload.User.RefreshToken} {
					resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": token}})
					requireStatus(t, "refresh token of repeated login", resp, http.StatusOK)
				}
			},
		},
	}
}
