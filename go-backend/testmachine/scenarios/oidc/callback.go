package oidc

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// callbackScenarios cover GET /auth/openid/callback (route-r004): the provider's
// authorization result is turned into a server session for a web browser or
// into the JSON login answer of the native apps.
func callbackScenarios() []harness.Scenario {
	const (
		web          = "INV-R004-OIDC-CALLBACK-COOKIE-JWT-001"
		webReplay    = "INV-R004-OIDC-CALLBACK-REPLAY-001"
		mobile       = "INV-R004-OIDC-MOBILE-CODE-EXCHANGE-001"
		mobileReplay = "INV-R004-OIDC-MOBILE-EXCHANGE-REPLAY-001"
		verifier     = "INV-R004-WEB-EMPTY-VERIFIER-001"
		webTTL       = "INV-R004-WEB-SESSION-TTL-001"
		webError     = "INV-R004-OIDC-CALLBACK-ERROR-001"
		inactive     = "INV-R004-WEB-DEACTIVATED-USER-001"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r004", Invariant: web, Case: "normal", State: "initial",
			Assertion: "web-callback-signs-in-links-subject-and-sets-cookies",
			Sources:   []string{"server/Auth.js:259-312", "server/Auth.js:383-455", "server/auth/OidcAuthStrategy.js:117-166", "server/models/User.js:311-341"},
			Claim:     "A web callback with a valid provider answer answers 302 to the auth_cb URL with setToken/accessToken (no state), sets an HttpOnly refresh_token cookie and a Secure SameSite=Strict openid_id_token cookie, links the provider sub on exactly one new user, creates exactly one session whose refreshToken is the cookie, and the access token authenticates /api/me as that user.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				callback := e.Base + "/login"
				userinfo := defaultUserinfo("web-success-sub", "web-success-user", "web-success@example.org")
				login := loginWeb(t, e, p, callback, userinfo)
				accessToken, refreshToken := requireWebLoginAnswer(t, e, login, callback)

				row := userRowBySub(t, e, "web-success-sub")
				if row == nil {
					t.Fatal("the web callback did not link the provider subject")
				}
				if row["username"] != "web-success-user" || row["email"] != "web-success@example.org" || row["type"] != "user" || row["isActive"] != int64(1) {
					t.Fatalf("registered user = %+v", row)
				}
				if login.Location.Query().Get("setToken") != row["token"] {
					t.Fatalf("setToken does not carry the user's legacy token: %s", login.Location)
				}
				if login.Location.Query().Get("state") != "" {
					t.Fatalf("web success redirect must not echo state: %s", login.Location)
				}
				extra := columnJSON(t, row, "extraData")
				if extra["authOpenIDSub"] != "web-success-sub" {
					t.Fatalf("extraData = %v", extra)
				}
				sessions := sessionsOf(t, e, row["id"].(string))
				if len(sessions) != 1 || sessions[0]["refreshToken"] != refreshToken {
					t.Fatalf("sessions = %v, refresh cookie %q", sessions, refreshToken)
				}
				claims := decodeJWT(t, accessToken)
				if claims["userId"] != row["id"] || claims["username"] != "web-success-user" || claims["type"] != "access" {
					t.Fatalf("access token claims = %v", claims)
				}
				if expiry, ok := claims["exp"].(float64); !ok || int64(expiry) <= time.Now().Unix() {
					t.Fatalf("access token exp = %v", claims["exp"])
				}

				me := e.Get(t, accessToken, "/api/me")
				requireStatus(t, "authenticated /api/me", me, http.StatusOK)
				var answer struct {
					ID            string `json:"id"`
					Username      string `json:"username"`
					HasOpenIDLink bool   `json:"hasOpenIDLink"`
				}
				me.JSON(t, &answer)
				if answer.ID != row["id"] || answer.Username != "web-success-user" || !answer.HasOpenIDLink {
					t.Fatalf("/api/me = %s", me.Body)
				}
			},
		},
		{
			Requirement: "route-r004", Invariant: webError, Case: "error", State: "failure",
			Assertion: "web-callback-bad-id-token-denied",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:117-166", "server/Auth.js:422-447"},
			Claim:     "A web callback whose ID token has a broken RS256 signature, a foreign issuer, a foreign audience or an expired exp answers 302 to /login?error=Unauthorized&autoLaunch=0, creates no user or session, and leaves the flow consumed so a replay answers the no-sign-in-session redirect.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				modes := []struct {
					name string
					mode idTokenMode
				}{
					{"broken signature", idTokenBadSignature},
					{"wrong issuer", idTokenWrongIssuer},
					{"wrong audience", idTokenWrongAudience},
					{"expired token", idTokenExpired},
				}
				for _, item := range modes {
					p.reset()
					p.setMode(item.mode)
					authorize, b := beginWeb(t, e, e.Base+"/login")
					state := authorize.Query().Get("state")
					code := p.issue(t, "", defaultUserinfo("bad-token-"+state, "bad-token-user", "bad-token@example.org"))
					resp := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
					requireStatus(t, item.name, resp, http.StatusFound)
					requireLoginRedirect(t, item.name, resp, "Unauthorized")
					requireUnchanged(t, e, before, item.name)

					replay := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
					requireStatus(t, item.name+" replay", replay, http.StatusFound)
					requireLoginRedirect(t, item.name+" replay", replay, "No sign-in session")
				}
				p.reset()
			},
		},
		{
			Requirement: "route-r004", Invariant: webError, Case: "error", State: "failure",
			Assertion: "web-callback-provider-error-and-state-mismatch-denied",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:117-166", "server/Auth.js:406-432", "server/Auth.js:449-452"},
			Claim:     "A web callback carrying the provider error access_denied, a callback whose state does not match the started flow, and a callback whose token endpoint answers 400 all answer 302 to /login?error=Unauthorized&autoLaunch=0 and create no user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)

				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				denied := providerRedirect(t, e, url.Values{"error": {"access_denied"}, "state": {state}}, b.header())
				requireStatus(t, "provider error", denied, http.StatusFound)
				requireLoginRedirect(t, "provider error", denied, "Unauthorized")

				authorize, b = beginWeb(t, e, e.Base+"/login")
				state = authorize.Query().Get("state")
				code := p.issue(t, "", defaultUserinfo("mismatch-sub", "mismatch-user", "mismatch@example.org"))
				mismatch := providerRedirect(t, e, url.Values{"state": {"not-" + state}, "code": {code}}, b.header())
				requireStatus(t, "state mismatch", mismatch, http.StatusFound)
				requireLoginRedirect(t, "state mismatch", mismatch, "Unauthorized")

				authorize, b = beginWeb(t, e, e.Base+"/login")
				state = authorize.Query().Get("state")
				code = p.issue(t, "", defaultUserinfo("failed-token-sub", "failed-token-user", "failed-token@example.org"))
				p.failToken(http.StatusBadRequest)
				failed := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
				p.reset()
				requireStatus(t, "token endpoint failure", failed, http.StatusFound)
				requireLoginRedirect(t, "token endpoint failure", failed, "Error in callback")

				requireUnchanged(t, e, before, "denied web callbacks")
			},
		},
		{
			Requirement: "route-r004", Invariant: webReplay, Case: "error", State: "repeat",
			Assertion: "web-callback-replay-no-sign-in-session",
			Sources:   []string{"server/Auth.js:386-397", "server/auth/OidcAuthStrategy.js:117-166"},
			Claim:     "Replaying a consumed web callback with the same cookies answers 302 to /login?error=No sign-in session&autoLaunch=0 and creates no additional user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				login := loginWeb(t, e, p, e.Base+"/login", defaultUserinfo("web-replay-sub", "web-replay-user", "web-replay@example.org"))
				requireWebLoginAnswer(t, e, login, e.Base+"/login")
				before := snapshotOf(t, e)
				replay := providerRedirect(t, e, url.Values{"state": {login.State}, "code": {login.Code}}, login.Start.header())
				requireStatus(t, "web replay", replay, http.StatusFound)
				requireLoginRedirect(t, "web replay", replay, "No sign-in session")
				requireUnchanged(t, e, before, "web replay")
			},
		},
		{
			Requirement: "route-r004", Invariant: verifier, Case: "edge", State: "failure",
			Assertion: "web-callback-no-session-verifier-matrix",
			Sources:   []string{"server/Auth.js:386-398"},
			Claim:     "Without a browser session, a callback with no code_verifier or an empty one is treated as web and answers 302 to /login?error=No sign-in session&autoLaunch=0, while any truthy code_verifier (a non-empty value, a repeated parameter or the openid-mobile cookie) answers 400 No session; neither creates a user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				webCases := []struct {
					name  string
					query url.Values
				}{
					{"absent verifier", url.Values{"state": {"s"}, "code": {"c"}}},
					{"empty verifier", url.Values{"state": {"s"}, "code": {"c"}, "code_verifier": {""}}},
				}
				for _, item := range webCases {
					resp := providerRedirect(t, e, item.query, "auth_method=openid")
					requireStatus(t, item.name, resp, http.StatusFound)
					requireLoginRedirect(t, item.name, resp, "No sign-in session")
				}
				mobileCases := []struct {
					name   string
					query  url.Values
					cookie string
				}{
					{"non-empty verifier", url.Values{"state": {"s"}, "code": {"c"}, "code_verifier": {"v"}}, ""},
					{"repeated verifier", url.Values{"state": {"s"}, "code": {"c"}, "code_verifier": {"a", "b"}}, ""},
					{"mobile cookie", url.Values{"state": {"s"}, "code": {"c"}}, "auth_method=openid-mobile"},
				}
				for _, item := range mobileCases {
					resp := providerRedirect(t, e, item.query, item.cookie)
					requireStatus(t, item.name, resp, http.StatusBadRequest)
					requireBody(t, item.name, resp, "No session")
					requireNoTokenMaterial(t, item.name, resp)
				}
				requireUnchanged(t, e, before, "no-session callbacks")
			},
		},
		{
			Requirement: "route-r004", Invariant: webTTL, Case: "edge", State: "changed",
			Assertion: "web-callback-survives-intervening-requests",
			Sources:   []string{"server/Server.js:301-313", "server/libs/memorystore/index.js:15-28", "server/auth/OidcAuthStrategy.js:355-363"},
			Claim:     "A started web flow still completes after the same browser makes other requests; the session cookie is unchanged by those requests, so the flow is not tied to the first request (the store's inactivity TTL is 24 hours; the exact boundary is not exercised).",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				code := p.issue(t, "", defaultUserinfo("ttl-sub", "ttl-user", "ttl@example.org"))
				status := e.Do(t, harness.Request{Path: "/status", Header: map[string]string{"Cookie": b.header()}})
				requireStatus(t, "intervening status request", status, http.StatusOK)
				callback := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
				requireStatus(t, "callback after intervening request", callback, http.StatusFound)
				if got := location(t, callback).Query().Get("accessToken"); got == "" {
					t.Fatalf("callback after intervening request did not sign in: %s", callback.Header.Values("Location"))
				}
			},
		},
		{
			Requirement: "route-r004", Invariant: inactive, Case: "edge", State: "changed",
			Assertion: "web-callback-deactivated-user-denied",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:131-150", "server/models/User.js:290-295"},
			Claim:     "When a user linked to a subject is deactivated after a flow starts, the callback answers 302 to /login?error=Unauthorized&autoLaunch=0, creates no session for that user and leaves the user row otherwise unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				userinfo := defaultUserinfo("inactive-sub", "inactive-user", "inactive@example.org")
				loginWeb(t, e, p, e.Base+"/login", userinfo)
				row := userRowBySub(t, e, "inactive-sub")
				if row == nil {
					t.Fatal("first login did not create the user")
				}
				id := row["id"].(string)
				resp := e.Patch(t, e.Token(t, "root"), "/api/users/"+id, map[string]any{"isActive": false})
				requireStatus(t, "deactivate user", resp, http.StatusOK)
				before := snapshotOf(t, e)
				sessionsBefore := len(sessionsOf(t, e, id))

				authorize, b := beginWeb(t, e, e.Base+"/login")
				state := authorize.Query().Get("state")
				code := p.issue(t, "", userinfo)
				callback := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
				requireStatus(t, "callback for inactive user", callback, http.StatusFound)
				requireLoginRedirect(t, "callback for inactive user", callback, "Unauthorized")
				if got := len(sessionsOf(t, e, id)); got != sessionsBefore {
					t.Fatalf("denied callback created a session: %d -> %d", sessionsBefore, got)
				}
				requireUnchanged(t, e, before, "inactive user callback")
			},
		},
		{
			Requirement: "route-r004", Invariant: mobile, Case: "normal", State: "initial",
			Assertion: "mobile-callback-returns-login-payload-with-pkce-verifier",
			Sources:   []string{"server/Auth.js:383-445", "server/auth/OidcAuthStrategy.js:301-368", "server/auth/TokenManager.js:184-200"},
			Claim:     "The native callback answers 200 with the login payload: a linked active user carrying accessToken, refreshToken, legacy token, hasOpenIDLink and default permissions, Source docker, a first-displayOrder userDefaultLibraryId and the server settings; the token endpoint received the app's code_verifier, and exactly one session row holds the returned refresh token.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				userinfo := defaultUserinfo("mobile-success-sub", "mobile-success-user", "mobile-success@example.org")
				login := loginMobile(t, e, p, verifier, userinfo)

				var payload loginEnvelope
				login.Callback.JSON(t, &payload)
				if payload.User.ID == "" || payload.User.Username != "mobile-success-user" || payload.User.Type != "user" || !payload.User.IsActive || !payload.User.HasOpenIDLink {
					t.Fatalf("mobile login user = %+v", payload.User)
				}
				if payload.User.AccessToken == "" || payload.User.RefreshToken == nil || *payload.User.RefreshToken == "" || payload.User.Token == "" {
					t.Fatalf("mobile login tokens missing: %s", login.Callback.Body)
				}
				if payload.Source != "docker" || payload.EreaderDevices == nil {
					t.Fatalf("mobile login envelope = %s", login.Callback.Body)
				}
				if payload.UserDefaultLibraryID == nil {
					t.Fatal("mobile login has no userDefaultLibraryId")
				}
				first := e.Rows(t, "SELECT id FROM libraries ORDER BY displayOrder ASC LIMIT 1")
				if len(first) != 1 || *payload.UserDefaultLibraryID != first[0]["id"] {
					t.Fatalf("userDefaultLibraryId = %v, want %v", *payload.UserDefaultLibraryID, first)
				}
				if payload.User.Permissions["download"] != true || payload.User.Permissions["accessAllLibraries"] != true {
					t.Fatalf("default permissions = %v", payload.User.Permissions)
				}
				if payload.ServerSettings.Version == "" || payload.ServerSettings.Language == "" {
					t.Fatalf("server settings = %+v", payload.ServerSettings)
				}

				form := p.tokenForm()
				if form == nil {
					t.Fatal("the provider token endpoint was not called")
				}
				for key, want := range map[string]string{
					"grant_type":    "authorization_code",
					"code":          login.Code,
					"code_verifier": verifier,
					"redirect_uri":  e.URL + "/auth/openid/mobile-redirect",
				} {
					if got := form.Get(key); got != want {
						t.Fatalf("token request %s = %q, want %q", key, got, want)
					}
				}

				row := userRowBySub(t, e, "mobile-success-sub")
				if row == nil || row["id"] != payload.User.ID {
					t.Fatalf("linked user = %v, payload id %q", row, payload.User.ID)
				}
				sessions := sessionsOf(t, e, payload.User.ID)
				if len(sessions) != 1 || sessions[0]["refreshToken"] != *payload.User.RefreshToken {
					t.Fatalf("sessions = %v", sessions)
				}
				refresh := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": *payload.User.RefreshToken}})
				requireStatus(t, "refresh with native login token", refresh, http.StatusOK)
			},
		},
		{
			Requirement: "route-r004", Invariant: mobileReplay, Case: "error", State: "repeat",
			Assertion: "mobile-callback-replay-400-no-session",
			Sources:   []string{"server/Auth.js:386-397"},
			Claim:     "Repeating the native callback with the consumed session answers 400 with the body No session and creates no second user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				login := loginMobile(t, e, p, verifier, defaultUserinfo("mobile-replay-sub", "mobile-replay-user", "mobile-replay@example.org"))
				row := userRowBySub(t, e, "mobile-replay-sub")
				if row == nil {
					t.Fatal("first mobile login did not create the user")
				}
				usersBefore := userCount(t, e)
				// The consumed code would be rejected by the provider too, but the
				// route must stop at the missing session before any token request.
				replay := providerRedirect(t, e, url.Values{
					"state": {login.State}, "code": {login.Code}, "code_verifier": {verifier},
				}, login.Start.header())
				requireStatus(t, "mobile replay", replay, http.StatusBadRequest)
				requireBody(t, "mobile replay", replay, "No session")
				requireNoTokenMaterial(t, "mobile replay", replay)
				if got := len(sessionsOf(t, e, row["id"].(string))); got != 1 {
					t.Fatalf("replay created sessions: %d, want 1", got)
				}
				if got := userCount(t, e); got != usersBefore {
					t.Fatalf("replay changed the user count: %d -> %d", usersBefore, got)
				}
			},
		},
	}
}

// requireLoginRedirect checks the upstream login error redirect.
func requireLoginRedirect(t *testing.T, what string, resp harness.Response, message string) {
	t.Helper()
	target := location(t, resp)
	if target.Path != "/login" {
		t.Fatalf("%s redirect path = %q, want /login: %s", what, target.Path, resp.Header.Values("Location"))
	}
	if got := target.Query().Get("error"); got != message {
		t.Fatalf("%s error = %q, want %q", what, got, message)
	}
	if got := target.Query().Get("autoLaunch"); got != "0" {
		t.Fatalf("%s autoLaunch = %q, want 0", what, got)
	}
}

// requireBody checks a plain-text denial body.
func requireBody(t *testing.T, what string, resp harness.Response, want string) {
	t.Helper()
	if got := strings.TrimSpace(string(resp.Body)); got != want {
		t.Fatalf("%s body = %q, want %q", what, got, want)
	}
}
