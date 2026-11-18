package oidc

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// flowScenarios cover the remaining sub-oidc-auth obligations that are visible
// in a complete flow: state binding, cookies and claims, token exchange, the
// invalid-verifier redirect quirk, logout and unlink/re-link.
func flowScenarios() []harness.Scenario {
	const (
		mobileState = "INV-OIDC-MOBILE-STATE-001"
		webCookies  = "INV-OIDC-WEB-COOKIE-JWT-001"
		exchange    = "INV-OIDC-MOBILE-TOKEN-EXCHANGE-001"
		badVerifier = "INV-OIDC-MOBILE-INVALID-VERIFIER-001"
		logout      = "INV-OIDC-WEB-LOGOUT-001"
		webTTL      = "INV-OIDC-WEB-SESSION-TTL-001"
		mobileTTL   = "INV-OIDC-MOBILE-REDIRECT-TTL-001"
		unlink      = "INV-OIDC-UNLINK-001"
	)
	return []harness.Scenario{
		{
			Requirement: "sub-oidc-auth", Invariant: mobileState, Case: "normal", State: "initial",
			Assertion: "oidc-mobile-state-bound-to-flow-and-used-once",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:256-281", "server/auth/OidcAuthStrategy.js:301-368", "server/auth/OidcAuthStrategy.js:479-505"},
			Claim:     "A native start rejects an unlisted redirect_uri and a missing challenge with 400, and a valid start binds the challenge and a state that the provider URL and app link both carry; the mobile-redirect state is consumed once so a second use answers 400.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				unlisted := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {"https://evil.example/cb"},
					"code_challenge": {s256(randomVerifier())}, "code_challenge_method": {"S256"},
				}})
				requireStatus(t, "unlisted redirect_uri", unlisted, http.StatusBadRequest)
				noChallenge := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {mobileRedirectURI},
				}})
				requireStatus(t, "missing challenge", noChallenge, http.StatusBadRequest)
				requireUnchanged(t, e, before, "denied native starts")

				verifier := randomVerifier()
				challenge := s256(verifier)
				authorize, b, state, startedChallenge := beginMobile(t, e, verifier)
				requireAuthorizeURL(t, p, authorize, e.URL+"/auth/openid/mobile-redirect", challenge)
				if startedChallenge != challenge {
					t.Fatalf("challenge = %q, want %q", startedChallenge, challenge)
				}
				code := p.issue(t, challenge, defaultUserinfo("bound-sub", "bound-user", "bound@example.org"))
				appLink := mobileRedirect(t, e, state, code)
				requireStatus(t, "mobile redirect", appLink, http.StatusFound)
				target := location(t, appLink)
				if target.Query().Get("state") != state || target.Query().Get("code") != code {
					t.Fatalf("app link = %s", target)
				}
				replay := mobileRedirect(t, e, state, code)
				requireStatus(t, "consumed mobile state", replay, http.StatusBadRequest)
				requireNoLocation(t, "consumed mobile state", replay)

				callback := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}, "code_verifier": {verifier}}, b.header())
				requireStatus(t, "bound callback", callback, http.StatusOK)
				if row := userRowBySub(t, e, "bound-sub"); row == nil {
					t.Fatal("the bound flow did not create the user")
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: webCookies, Case: "normal", State: "initial",
			Assertion: "oidc-web-start-and-callback-cookie-and-jwt-shape",
			Sources:   []string{"server/Auth.js:218-248", "server/Auth.js:296-305", "server/Auth.js:430-445", "server/auth/TokenManager.js:108-171", "server/Server.js:301-317"},
			Claim:     "A web start sets HttpOnly auth_cb (10 minute) and auth_method=openid (10 year) cookies and a session; the callback sets an HttpOnly Path=/ refresh_token and a Secure SameSite=Strict openid_id_token, and the access token's userId/username claims identify that new user with type access.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				callback := e.Base + "/login"
				start := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {callback}}})
				requireStatus(t, "web start", start, http.StatusFound)
				requireCookie(t, start, "auth_cb", callback, 600)
				requireCookie(t, start, "auth_method", "openid", 315360000)
				if cookieValue(start, "connect.sid") == "" {
					t.Fatal("web start must create a session")
				}
				b := browserFrom(start)
				authorize := location(t, start)
				state := authorize.Query().Get("state")
				code := p.issue(t, "", defaultUserinfo("cookie-sub", "cookie-user", "cookie@example.org"))
				resp := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
				requireStatus(t, "web callback", resp, http.StatusFound)
				accessToken := location(t, resp).Query().Get("accessToken")
				if accessToken == "" {
					t.Fatalf("web callback redirect = %s", location(t, resp))
				}
				refresh, ok := cookie(resp, "refresh_token")
				if !ok || !refresh.HttpOnly || refresh.Path != "/" || refresh.Value == "" {
					t.Fatalf("refresh_token cookie = %+v", refresh)
				}
				idToken, ok := cookie(resp, "openid_id_token")
				if !ok || !idToken.Secure || idToken.SameSite != http.SameSiteStrictMode || idToken.Value == "" {
					t.Fatalf("openid_id_token cookie = %+v", idToken)
				}
				row := userRowBySub(t, e, "cookie-sub")
				if row == nil {
					t.Fatal("backend did not link the subject")
				}
				claims := decodeJWT(t, accessToken)
				if claims["userId"] != row["id"] || claims["username"] != "cookie-user" || claims["type"] != "access" {
					t.Fatalf("access claims = %v", claims)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: exchange, Case: "normal", State: "initial",
			Assertion: "oidc-mobile-token-exchange-persists-one-user-and-session",
			Sources:   []string{"server/Auth.js:383-445", "server/auth/OidcAuthStrategy.js:112-164"},
			Claim:     "The native exchange with the app's verifier returns the login payload with accessToken and refreshToken, persists exactly one linked user and one session whose refreshToken is the returned value, and replaying the callback answers 400 No session without creating a second user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				before := userCount(t, e)
				login := loginMobile(t, e, p, verifier, defaultUserinfo("exchange-sub", "exchange-user", "exchange@example.org"))
				var payload loginEnvelope
				login.Callback.JSON(t, &payload)
				if payload.User.AccessToken == "" || payload.User.RefreshToken == nil {
					t.Fatalf("mobile payload = %s", login.Callback.Body)
				}
				if got := userCount(t, e); got != before+1 {
					t.Fatalf("mobile exchange user count = %d -> %d", before, got)
				}
				row := userRowBySub(t, e, "exchange-sub")
				if row == nil || row["id"] != payload.User.ID {
					t.Fatalf("linked user = %v", row)
				}
				sessions := sessionsOf(t, e, payload.User.ID)
				if len(sessions) != 1 || sessions[0]["refreshToken"] != *payload.User.RefreshToken {
					t.Fatalf("sessions = %v", sessions)
				}
				replay := providerRedirect(t, e, url.Values{"state": {login.State}, "code": {login.Code}, "code_verifier": {verifier}}, login.Start.header())
				requireStatus(t, "exchange replay", replay, http.StatusBadRequest)
				requireBody(t, "exchange replay", replay, "No session")
				if got := userCount(t, e); got != before+1 {
					t.Fatalf("replay changed the user count: %d", got)
				}
				if got := len(sessionsOf(t, e, payload.User.ID)); got != 1 {
					t.Fatalf("replay created sessions: %d", got)
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: badVerifier, Case: "error", State: "failure",
			Assertion: "oidc-mobile-wrong-verifier-follows-web-redirect",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:356-368", "server/Auth.js:399-432", "server/Auth.js:449-452"},
			Claim:     "A mobile callback with a wrong PKCE verifier follows the web failure branch (302 /login?error=Error in callback&autoLaunch=0) because session.mobile holds the redirect URI rather than true and the provider invalid_grant is an error, not a plain failure; it creates no linked user or session and consumes the flow so a replay returns 400 No session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				_, b, state, challenge := beginMobile(t, e, verifier)
				code := p.issue(t, challenge, defaultUserinfo("wrong-pkce-sub", "wrong-pkce-user", "wrong-pkce@example.org"))
				requireStatus(t, "mobile redirect", mobileRedirect(t, e, state, code), http.StatusFound)
				before := snapshotOf(t, e)
				callback := providerRedirect(t, e, url.Values{
					"state": {state}, "code": {code}, "code_verifier": {randomVerifier()},
				}, b.header())
				requireStatus(t, "wrong verifier callback", callback, http.StatusFound)
				requireLoginRedirect(t, "wrong verifier callback", callback, "Error in callback")
				requireUnchanged(t, e, before, "wrong verifier callback")
				replay := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}, "code_verifier": {verifier}}, b.header())
				requireStatus(t, "replay after wrong verifier", replay, http.StatusBadRequest)
				requireBody(t, "replay after wrong verifier", replay, "No session")
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: logout, Case: "normal", State: "initial",
			Assertion: "oidc-web-logout-returns-end-session-url-and-revokes-session",
			Sources:   []string{"server/Auth.js:486-523", "server/auth/OidcAuthStrategy.js:395-423"},
			Claim:     "After a web OIDC login, POST /logout with the refresh cookie, auth_method=openid and the openid_id_token cookie answers 200 with the provider end-session URL carrying that id_token as id_token_hint and post_logout_redirect_uri=<origin>/audiobookshelf/login, expires refresh_token/auth_method/openid_id_token, deletes the session row and keeps the user.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				login := loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("logout-sub", "logout-user", "logout@example.org"))
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
				row := userRowBySub(t, e, "logout-sub")
				if row == nil {
					t.Fatal("login did not link the user")
				}
				idToken, ok := cookie(login.Response, "openid_id_token")
				if !ok || idToken.Value == "" {
					t.Fatal("login did not set openid_id_token")
				}
				refresh, ok := cookie(login.Response, "refresh_token")
				if !ok {
					t.Fatal("login did not set refresh_token")
				}
				resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/logout", Header: map[string]string{
					"Cookie": "refresh_token=" + refresh.Value + "; auth_method=openid; openid_id_token=" + idToken.Value,
				}})
				requireStatus(t, "oidc logout", resp, http.StatusOK)
				var answer struct {
					RedirectURL *string `json:"redirect_url"`
				}
				resp.JSON(t, &answer)
				if answer.RedirectURL == nil {
					t.Fatalf("logout redirect_url is null: %s", resp.Body)
				}
				target, err := url.Parse(*answer.RedirectURL)
				if err != nil {
					t.Fatalf("logout redirect_url: %v", err)
				}
				if got, want := target.Scheme+"://"+target.Host+target.Path, p.endpoint("logout"); got != want {
					t.Fatalf("end-session endpoint = %q, want %q", got, want)
				}
				query := target.Query()
				if query.Get("id_token_hint") != idToken.Value {
					t.Fatalf("id_token_hint = %q, want the cookie value", query.Get("id_token_hint"))
				}
				if query.Get("post_logout_redirect_uri") != e.Base+"/login" {
					t.Fatalf("post_logout_redirect_uri = %q, want %q", query.Get("post_logout_redirect_uri"), e.Base+"/login")
				}
				for _, name := range []string{"refresh_token", "auth_method", "openid_id_token"} {
					item, ok := cookie(resp, name)
					if !ok || item.Value != "" {
						t.Fatalf("%s cookie was not expired: %v", name, resp.Header.Values("Set-Cookie"))
					}
				}
				if sessions := sessionsOf(t, e, row["id"].(string)); len(sessions) != 0 {
					t.Fatalf("logout kept sessions: %v", sessions)
				}
				if userRow(t, e, row["id"].(string)) == nil {
					t.Fatal("logout deleted the user")
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: webTTL, Case: "edge", State: "changed",
			Assertion: "oidc-web-flow-completes-after-other-requests",
			Sources:   []string{"server/Server.js:301-313", "server/libs/memorystore/index.js:15-28"},
			Claim:     "A started web flow completes after the same browser makes two unrelated authenticated and unauthenticated requests; the session is not single-request (the store TTL is 24 hours, not exercised exactly).",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				code := p.issue(t, "", defaultUserinfo("late-sub", "late-user", "late@example.org"))
				for _, path := range []string{"/status", "/ping"} {
					resp := e.Do(t, harness.Request{Path: path, Header: map[string]string{"Cookie": b.header()}})
					requireStatus(t, "intervening "+path, resp, http.StatusOK)
				}
				callback := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
				requireStatus(t, "late callback", callback, http.StatusFound)
				if location(t, callback).Query().Get("accessToken") == "" {
					t.Fatalf("late callback did not sign in: %v", callback.Header.Values("Location"))
				}
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: mobileTTL, Case: "edge", State: "repeat",
			Assertion: "oidc-mobile-redirect-consumed-or-unknown-state",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:18-22", "server/auth/OidcAuthStrategy.js:483-505"},
			Claim:     "The mobile redirect state lives for ten minutes and is one-time: using it twice or presenting a state the server does not hold answers 400 State parameter mismatch with no Location and no credentials.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				verifier := randomVerifier()
				_, _, state, challenge := beginMobile(t, e, verifier)
				code := p.issue(t, challenge, defaultUserinfo("ttl-mobile-sub", "ttl-mobile-user", "ttl-mobile@example.org"))
				requireStatus(t, "mobile redirect", mobileRedirect(t, e, state, code), http.StatusFound)
				for _, item := range []struct {
					name  string
					state string
				}{
					{"consumed state", state},
					{"never issued state", "unknown-state-value"},
				} {
					resp := mobileRedirect(t, e, item.state, code)
					requireStatus(t, item.name, resp, http.StatusBadRequest)
					requireNoLocation(t, item.name, resp)
					requireNoTokenMaterial(t, item.name, resp)
				}
				requireUnchanged(t, e, before, "mobile redirect states")
			},
		},
		{
			Requirement: "sub-oidc-auth", Invariant: unlink, Case: "normal", State: "changed",
			Assertion: "oidc-email-link-unlink-relink",
			Sources:   []string{"server/models/User.js:286-306", "server/controllers/UserController.js:409-428"},
			Claim:     "After an email-matched link is removed through PATCH /api/users/:id/openid-unlink (the same user loses hasOpenIDLink and keeps its sessions), the next OIDC login with that email links the same user again and adds one session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDMatchExistingBy": "email"})
				defer configureOIDC(t, e, p, nil)
				root := e.Token(t, "root")
				id := createUser(t, e, map[string]any{
					"username": "relink-user", "password": "relink-password",
					"email": "relink@example.org", "isActive": true,
				})
				userinfo := defaultUserinfo("relink-sub", "relink-provider", "relink@example.org")
				first := loginWeb(t, e, p, e.Base+"/login", userinfo)
				accessToken, _ := requireWebLoginAnswer(t, e, first, e.Base+"/login")
				if row := userRowBySub(t, e, "relink-sub"); row == nil || row["id"] != id {
					t.Fatalf("first link = %v", row)
				}

				unlinkResp := e.Patch(t, root, "/api/users/"+id+"/openid-unlink", nil)
				requireStatus(t, "openid unlink", unlinkResp, http.StatusOK)
				row := userRow(t, e, id)
				if row == nil || columnJSON(t, row, "extraData")["authOpenIDSub"] != nil {
					t.Fatalf("unlink left the subject: %v", row)
				}
				me := e.Get(t, accessToken, "/api/me")
				requireStatus(t, "/api/me after unlink", me, http.StatusOK)
				var profile struct {
					HasOpenIDLink bool `json:"hasOpenIDLink"`
				}
				me.JSON(t, &profile)
				if profile.HasOpenIDLink {
					t.Fatalf("/api/me still reports an OpenID link: %s", me.Body)
				}
				if sessions := sessionsOf(t, e, id); len(sessions) != 1 {
					t.Fatalf("unlink changed sessions: %v", sessions)
				}

				second := loginWeb(t, e, p, e.Base+"/login", userinfo)
				requireWebLoginAnswer(t, e, second, e.Base+"/login")
				row = userRowBySub(t, e, "relink-sub")
				if row == nil || row["id"] != id {
					t.Fatalf("relink did not reuse the user: %v", row)
				}
				if sessions := sessionsOf(t, e, id); len(sessions) != 2 {
					t.Fatalf("relink sessions = %v", sessions)
				}
			},
		},
	}
}
