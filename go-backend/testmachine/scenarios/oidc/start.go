package oidc

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// startScenarios cover GET /auth/openid (route-r002): starting a web or native
// authorization at the configured provider.
func startScenarios() []harness.Scenario {
	const (
		cookie = "INV-R002-OIDC-START-COOKIE-001"
		reject = "INV-R002-OIDC-START-REJECT-001"
		origin = "INV-R002-OIDC-START-REDIRECT-001"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r002", Invariant: reject, Case: "error", State: "failure",
			Assertion: "start-without-oidc-settings-denies",
			Sources:   []string{"server/Auth.js:362-376", "server/auth/OidcAuthStrategy.js:49-74", "server/auth/OidcAuthStrategy.js:291-386"},
			Claim:     "On a server whose OpenID settings are unset, GET /auth/openid with a same-origin callback answers an error status with no Location, no auth_cb/auth_method cookie and no token material, and creates no user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotOf(t, e)
				resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {e.Base + "/login"}}})
				if resp.Status < 400 {
					t.Fatalf("unconfigured start: status %d, want an error: %s", resp.Status, resp.Body)
				}
				requireNoLocation(t, "unconfigured start", resp)
				if value := cookieValue(resp, "auth_cb"); value != "" {
					t.Fatalf("unconfigured start set auth_cb: %v", resp.Header.Values("Set-Cookie"))
				}
				if value := cookieValue(resp, "auth_method"); value != "" {
					t.Fatalf("unconfigured start set auth_method: %v", resp.Header.Values("Set-Cookie"))
				}
				requireNoTokenMaterial(t, "unconfigured start", resp)
				requireUnchanged(t, e, before, "unconfigured start")
			},
		},
		{
			Requirement: "route-r002", Invariant: cookie, Case: "normal", State: "initial",
			Assertion: "start-web-redirects-to-provider-with-auth-cookies",
			Sources:   []string{"server/Auth.js:218-249", "server/Auth.js:362-376", "server/auth/OidcAuthStrategy.js:291-386"},
			Claim:     "A web start answers 302 to the configured authorization endpoint carrying client_id, response_type=code, scope openid profile email, a random state, the origin-only /auth/openid/callback redirect_uri and an S256 code challenge, and sets HttpOnly auth_cb (10 min) and auth_method=openid (10 years) cookies plus a session; it returns no token.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				callback := e.Base + "/login"
				resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {callback}}})
				requireStatus(t, "web start", resp, http.StatusFound)
				authorize := location(t, resp)
				requireAuthorizeURL(t, p, authorize, e.URL+webCallbackPath, "")
				state := authorize.Query().Get("state")
				if len(state) < 20 {
					t.Fatalf("web start state = %q, want a random value", state)
				}
				requireCookie(t, resp, "auth_cb", callback, 600)
				requireCookie(t, resp, "auth_method", "openid", 315360000)
				if cookieValue(resp, "connect.sid") == "" {
					t.Fatalf("web start must create an express session: %v", resp.Header.Values("Set-Cookie"))
				}
				requireNoTokenMaterial(t, "web start", resp)
			},
		},
		{
			Requirement: "route-r002", Invariant: cookie, Case: "normal", State: "initial",
			Assertion: "start-mobile-redirects-with-pkce-and-app-redirect-uri",
			Sources:   []string{"server/Auth.js:362-376", "server/auth/OidcAuthStrategy.js:252-281", "server/auth/OidcAuthStrategy.js:315-330"},
			Claim:     "A native start with an allowlisted audiobookshelf://oauth redirect_uri and an S256 challenge answers 302 to the provider with response_type=code, that challenge and method, a random state and the server's own /auth/openid/mobile-redirect redirect_uri, sets auth_method=openid-mobile and no auth_cb, and returns no token.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				challenge := s256(verifier)
				resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type":         {"code"},
					"redirect_uri":          {mobileRedirectURI},
					"client_id":             {"Audiobookshelf-App"},
					"code_challenge":        {challenge},
					"code_challenge_method": {"S256"},
				}})
				requireStatus(t, "mobile start", resp, http.StatusFound)
				authorize := location(t, resp)
				requireAuthorizeURL(t, p, authorize, e.URL+"/auth/openid/mobile-redirect", challenge)
				requireCookie(t, resp, "auth_method", "openid-mobile", 315360000)
				if value := cookieValue(resp, "auth_cb"); value != "" {
					t.Fatalf("mobile start must not set auth_cb: %v", resp.Header.Values("Set-Cookie"))
				}
				requireNoTokenMaterial(t, "mobile start", resp)
			},
		},
		{
			Requirement: "route-r002", Invariant: origin, Case: "edge", State: "changed",
			Assertion: "start-subfolder-setting-changes-redirect-uris",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:330-332", "server/auth/OidcAuthStrategy.js:343-347", "server/objects/settings/ServerSettings.js:342"},
			Claim:     "With authOpenIDSubfolderForRedirectURLs=/audiobookshelf the web start's provider redirect_uri becomes <origin>/audiobookshelf/auth/openid/callback and the native start's becomes <origin>/audiobookshelf/auth/openid/mobile-redirect; restoring the empty setting returns the origin-only callback URL; a malformed setting value makes the native start answer an error status without a Location or credentials.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDSubfolderForRedirectURLs": "/audiobookshelf"})
				defer configureOIDC(t, e, p, nil)

				web := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {e.Base + "/login"}}})
				requireStatus(t, "web start with subfolder", web, http.StatusFound)
				if got, want := location(t, web).Query().Get("redirect_uri"), e.Base+webCallbackPath; got != want {
					t.Fatalf("web redirect_uri = %q, want %q", got, want)
				}
				verifier := randomVerifier()
				mobile := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {mobileRedirectURI},
					"code_challenge": {s256(verifier)}, "code_challenge_method": {"S256"},
				}})
				requireStatus(t, "mobile start with subfolder", mobile, http.StatusFound)
				if got, want := location(t, mobile).Query().Get("redirect_uri"), e.Base+"/auth/openid/mobile-redirect"; got != want {
					t.Fatalf("mobile redirect_uri = %q, want %q", got, want)
				}

				before := snapshotOf(t, e)
				configureOIDC(t, e, p, map[string]any{"authOpenIDSubfolderForRedirectURLs": "https://["})
				malformed := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {mobileRedirectURI},
					"code_challenge": {s256(verifier)}, "code_challenge_method": {"S256"},
				}})
				if malformed.Status < 400 {
					t.Fatalf("malformed subfolder start: status %d, want an error: %s", malformed.Status, malformed.Body)
				}
				requireNoLocation(t, "malformed subfolder start", malformed)
				if value := cookieValue(malformed, "auth_method"); value != "" {
					t.Fatalf("malformed subfolder start set auth_method: %v", malformed.Header.Values("Set-Cookie"))
				}
				requireNoTokenMaterial(t, "malformed subfolder start", malformed)
				requireUnchanged(t, e, before, "malformed subfolder start")

				configureOIDC(t, e, p, nil)
				plain := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {e.Base + "/login"}}})
				requireStatus(t, "web start after restore", plain, http.StatusFound)
				if got, want := location(t, plain).Query().Get("redirect_uri"), e.URL+webCallbackPath; got != want {
					t.Fatalf("restored web redirect_uri = %q, want %q", got, want)
				}
			},
		},
		{
			Requirement: "route-r002", Invariant: cookie, Case: "edge", State: "repeat",
			Assertion: "start-twice-yields-independent-flows",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:310-311", "server/auth/OidcAuthStrategy.js:355-373", "server/Auth.js:430-445"},
			Claim:     "Two web starts each with their own client session yield different states and both complete: each callback signs in its own browser and creates its own session row.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				userinfo := defaultUserinfo("repeat-sub", "repeat-user", "repeat@example.org")
				first := loginWeb(t, e, p, e.Base+"/login", userinfo)
				second := loginWeb(t, e, p, e.Base+"/login", userinfo)
				if first.State == second.State {
					t.Fatalf("two starts reused state %q", first.State)
				}
				requireWebLoginAnswer(t, e, first, e.Base+"/login")
				requireWebLoginAnswer(t, e, second, e.Base+"/login")
				row := userRowBySub(t, e, "repeat-sub")
				if row == nil {
					t.Fatal("the repeated flow must link the subject")
				}
				if got := len(sessionsOf(t, e, row["id"].(string))); got != 2 {
					t.Fatalf("two web logins created %d sessions, want 2", got)
				}
			},
		},
		{
			Requirement: "route-r002", Invariant: reject, Case: "error", State: "failure",
			Assertion: "start-invalid-mobile-inputs-denied",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:252-281", "server/auth/OidcAuthStrategy.js:301-341"},
			Claim:     "A start is denied with 400 and no Location, credential cookie or user/session when the mobile redirect_uri is not allowlisted, code_challenge is absent, the challenge method is not S256, response_type is not code, or a state is sent on the web flow.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				verifier := randomVerifier()
				cases := []struct {
					name  string
					query url.Values
				}{
					{"unlisted redirect_uri", url.Values{"response_type": {"code"}, "redirect_uri": {"https://evil.example/callback"}, "code_challenge": {s256(verifier)}, "code_challenge_method": {"S256"}}},
					{"missing code_challenge", url.Values{"response_type": {"code"}, "redirect_uri": {mobileRedirectURI}, "code_challenge_method": {"S256"}}},
					{"plain challenge method", url.Values{"response_type": {"code"}, "redirect_uri": {mobileRedirectURI}, "code_challenge": {verifier}, "code_challenge_method": {"plain"}}},
					{"wrong response_type", url.Values{"response_type": {"token"}, "redirect_uri": {mobileRedirectURI}, "code_challenge": {s256(verifier)}}},
					{"state on web flow", url.Values{"callback": {e.Base + "/login"}, "state": {"client-state"}}},
				}
				for _, item := range cases {
					resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: item.query})
					requireStatus(t, item.name, resp, http.StatusBadRequest)
					requireNoLocation(t, item.name, resp)
					if value := cookieValue(resp, "auth_method"); value != "" {
						t.Fatalf("%s set auth_method: %v", item.name, resp.Header.Values("Set-Cookie"))
					}
					requireNoTokenMaterial(t, item.name, resp)
				}
				requireUnchanged(t, e, before, "denied starts")
			},
		},
		{
			Requirement: "route-r002", Invariant: reject, Case: "error", State: "failure",
			Assertion: "start-invalid-web-callback-denied",
			Sources:   []string{"server/Auth.js:231-244", "server/auth/OidcAuthStrategy.js:518-553"},
			Claim:     "A web start is denied with 400, no Location, no auth_cb cookie and no user/session when the callback is absent, cross-origin, protocol-relative, backslash-prefixed or outside the router base path.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				cases := []struct {
					name  string
					query url.Values
				}{
					{"missing callback", url.Values{}},
					{"cross-origin callback", url.Values{"callback": {"https://evil.example/audiobookshelf/login"}}},
					{"protocol-relative callback", url.Values{"callback": {"//evil.example/audiobookshelf/login"}}},
					{"backslash callback", url.Values{"callback": {`/\evil.example/audiobookshelf/login`}}},
					{"decoded protocol-relative path", url.Values{"callback": {e.URL + "/%2F%2Fevil.example"}}},
					{"unparsable escape", url.Values{"callback": {e.URL + "/audiobookshelf/%"}}},
					{"outside base path", url.Values{"callback": {e.URL + "/login"}}},
				}
				for _, item := range cases {
					resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: item.query})
					requireStatus(t, item.name, resp, http.StatusBadRequest)
					requireNoLocation(t, item.name, resp)
					if value := cookieValue(resp, "auth_cb"); value != "" {
						t.Fatalf("%s set auth_cb: %v", item.name, resp.Header.Values("Set-Cookie"))
					}
					requireNoTokenMaterial(t, item.name, resp)
				}
				requireUnchanged(t, e, before, "denied callbacks")
			},
		},
	}
}

// requireAuthorizeURL checks the provider URL a start redirects to.
func requireAuthorizeURL(t *testing.T, p *mockProvider, authorize *url.URL, redirectURI, challenge string) {
	t.Helper()
	if got, want := authorize.Scheme+"://"+authorize.Host+authorize.Path, p.endpoint("authorize"); got != want {
		t.Fatalf("authorization endpoint = %q, want %q", got, want)
	}
	query := authorize.Query()
	want := map[string]string{
		"client_id":             p.clientID,
		"response_type":         "code",
		"scope":                 "openid profile email",
		"redirect_uri":          redirectURI,
		"code_challenge_method": "S256",
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Fatalf("authorization %s = %q, want %q (%s)", key, got, value, authorize)
		}
	}
	if query.Get("state") == "" {
		t.Fatalf("authorization URL has no state: %s", authorize)
	}
	if challenge != "" {
		if got := query.Get("code_challenge"); got != challenge {
			t.Fatalf("authorization code_challenge = %q, want %q", got, challenge)
		}
	} else if len(query.Get("code_challenge")) != 43 {
		t.Fatalf("web authorization code_challenge = %q, want an S256 challenge", query.Get("code_challenge"))
	}
	if nonce := query.Get("nonce"); nonce != "" {
		t.Fatalf("authorization URL unexpectedly carries nonce %q", nonce)
	}
}

// requireCookie checks one Set-Cookie of a start answer. Express
// percent-encodes the auth_cb URL, and cookie-parser decodes it again for the
// handler, so the value is compared after decoding.
func requireCookie(t *testing.T, resp harness.Response, name, value string, maxAge int) {
	t.Helper()
	item, ok := cookie(resp, name)
	if !ok {
		t.Fatalf("answer has no %s cookie: %v", name, resp.Header.Values("Set-Cookie"))
	}
	decoded, err := url.QueryUnescape(item.Value)
	if err != nil {
		t.Fatalf("%s cookie value %q: %v", name, item.Value, err)
	}
	if decoded != value || !item.HttpOnly || item.MaxAge != maxAge {
		t.Fatalf("%s cookie = %+v, want value %q, HttpOnly, MaxAge %d", name, item, value, maxAge)
	}
}
