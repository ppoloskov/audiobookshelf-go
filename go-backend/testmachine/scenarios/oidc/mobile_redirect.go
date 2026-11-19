package oidc

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// mobileRedirectScenarios cover GET /auth/openid/mobile-redirect (route-r003):
// the provider's answer is forwarded to the app link registered at start.
func mobileRedirectScenarios() []harness.Scenario {
	const (
		state    = "INV-R003-MOBILE-STATE-001"
		replay   = "INV-R003-MOBILE-REPLAY-001"
		validate = "INV-R003-MOBILE-VALIDATION-001"
		ttl      = "INV-R003-MOBILE-STATE-TTL-001"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r003", Invariant: state, Case: "normal", State: "initial",
			Assertion: "mobile-redirect-forwards-code-and-state-to-app-link",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:315-330", "server/auth/OidcAuthStrategy.js:483-505"},
			Claim:     "For an allowlisted audiobookshelf://oauth start, the provider's callback to /auth/openid/mobile-redirect answers 302 to audiobookshelf://oauth with the provider code and the same state as query parameters, and that state is then no longer resolvable.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				authorize, _, started, challenge := beginMobile(t, e, verifier)
				code := p.issue(t, challenge, defaultUserinfo("redirect-sub", "redirect-user", "redirect@example.org"))
				resp := mobileRedirect(t, e, started, code)
				requireStatus(t, "mobile redirect", resp, http.StatusFound)
				appLink := location(t, resp)
				if appLink.Scheme+":"+"//"+appLink.Host != mobileRedirectURI || appLink.Path != "" {
					t.Fatalf("app link = %s, want %s", appLink, mobileRedirectURI)
				}
				if got := appLink.Query().Get("code"); got != code {
					t.Fatalf("app link code = %q, want %q", got, code)
				}
				if got := appLink.Query().Get("state"); got != started {
					t.Fatalf("app link state = %q, want %q", got, started)
				}
				if got := authorize.Query().Get("state"); got != started {
					t.Fatalf("provider state = %q, want the saved state %q", got, started)
				}
				second := mobileRedirect(t, e, started, code)
				requireStatus(t, "consumed state", second, http.StatusBadRequest)
				requireNoLocation(t, "consumed state", second)
				requireNoTokenMaterial(t, "consumed state", second)
			},
		},
		{
			Requirement: "route-r003", Invariant: replay, Case: "error", State: "repeat",
			Assertion: "mobile-redirect-replay-400-changes-nothing",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:489-501"},
			Claim:     "Repeating /auth/openid/mobile-redirect with an already used state answers status 400 with no Location and creates no user or session, while the app callback that received the first answer still completes.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				verifier := randomVerifier()
				_, b, started, challenge := beginMobile(t, e, verifier)
				code := p.issue(t, challenge, defaultUserinfo("replay-sub", "replay-user", "replay@example.org"))
				first := mobileRedirect(t, e, started, code)
				requireStatus(t, "first redirect", first, http.StatusFound)
				requireUnchanged(t, e, before, "mobile redirect")
				second := mobileRedirect(t, e, started, code)
				requireStatus(t, "replayed redirect", second, http.StatusBadRequest)
				requireNoLocation(t, "replayed redirect", second)
				requireUnchanged(t, e, before, "replayed mobile redirect")

				callback := providerRedirect(t, e, url.Values{
					"state": {started}, "code": {code}, "code_verifier": {verifier},
				}, b.header())
				requireStatus(t, "app callback after replay", callback, http.StatusOK)
				row := userRowBySub(t, e, "replay-sub")
				if row == nil || len(sessionsOf(t, e, row["id"].(string))) != 1 {
					t.Fatalf("the first answer must still complete: user %v", row)
				}
			},
		},
		{
			Requirement: "route-r003", Invariant: validate, Case: "error", State: "failure",
			Assertion: "mobile-redirect-requires-held-state",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:315-330", "server/auth/OidcAuthStrategy.js:486-492"},
			Claim:     "A mobile-redirect request without a state, with an empty state, with a state that was never issued, or with the state of a web flow answers status 400 without a Location or credentials and changes no user or session.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				webAuthorize, _ := beginWeb(t, e, e.Base+"/login")
				cases := []struct {
					name  string
					query url.Values
				}{
					{"no state", url.Values{"code": {"any-code"}}},
					{"empty state", url.Values{"state": {""}, "code": {"any-code"}}},
					{"never issued state", url.Values{"state": {"never-issued-state"}, "code": {"any-code"}}},
					{"web flow state", url.Values{"state": {webAuthorize.Query().Get("state")}, "code": {"any-code"}}},
				}
				for _, item := range cases {
					resp := e.Do(t, harness.Request{Path: "/auth/openid/mobile-redirect", Query: item.query})
					requireStatus(t, item.name, resp, http.StatusBadRequest)
					requireNoLocation(t, item.name, resp)
					requireNoTokenMaterial(t, item.name, resp)
				}
				requireUnchanged(t, e, before, "denied mobile redirects")
			},
		},
		{
			Requirement: "route-r003", Invariant: ttl, Case: "edge", State: "failure",
			Assertion: "mobile-redirect-state-not-held-is-400",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:18-22", "server/auth/OidcAuthStrategy.js:489-501"},
			Claim:     "The mobile redirect map keeps a state for ten minutes and drops it afterwards; a state the server does not hold (never issued or expired) answers status 400 with no Location and no credentials, the same answer a consumed state gives.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				before := snapshotOf(t, e)
				unknown := mobileRedirect(t, e, "expired-or-unknown-state", "code")
				requireStatus(t, "unknown state", unknown, http.StatusBadRequest)
				requireNoLocation(t, "unknown state", unknown)
				requireNoTokenMaterial(t, "unknown state", unknown)
				requireUnchanged(t, e, before, "unknown mobile state")
			},
		},
		{
			Requirement: "route-r003", Invariant: state, Case: "edge", State: "initial",
			Assertion: "mobile-redirect-second-allowlisted-uri",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:280-283", "server/auth/OidcAuthStrategy.js:315-330"},
			Claim:     "With two allowlisted redirect URIs, a start for audiobookshelf://login forwards the provider result to that URI, not to the default audiobookshelf://oauth.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, map[string]any{"authOpenIDMobileRedirectURIs": []string{mobileRedirectURI, "audiobookshelf://login"}})
				defer configureOIDC(t, e, p, nil)
				verifier := randomVerifier()
				resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
					"response_type": {"code"}, "redirect_uri": {"audiobookshelf://login"},
					"code_challenge": {s256(verifier)}, "code_challenge_method": {"S256"},
				}})
				requireStatus(t, "start for second URI", resp, http.StatusFound)
				b := browserFrom(resp)
				started := location(t, resp).Query().Get("state")
				code := p.issue(t, s256(verifier), defaultUserinfo("second-uri-sub", "second-uri-user", "second@example.org"))
				redirect := mobileRedirect(t, e, started, code)
				requireStatus(t, "second URI redirect", redirect, http.StatusFound)
				appLink := location(t, redirect)
				if appLink.Scheme != "audiobookshelf" || appLink.Host != "login" {
					t.Fatalf("app link = %q, want audiobookshelf://login", appLink)
				}
				if appLink.Query().Get("state") != started || appLink.Query().Get("code") != code {
					t.Fatalf("app link = %q, want the provider state and code", appLink)
				}
				callback := providerRedirect(t, e, url.Values{
					"state": {started}, "code": {code}, "code_verifier": {verifier},
				}, b.header())
				requireStatus(t, "second URI callback", callback, http.StatusOK)
			},
		},
		{
			Requirement: "route-r003", Invariant: state, Case: "normal", State: "changed",
			Assertion: "mobile-redirect-new-flow-after-consumed",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:328", "server/auth/OidcAuthStrategy.js:493-505"},
			Claim:     "After one mobile state is consumed, a second start and redirect complete normally with its own code and state.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				configureOIDC(t, e, p, nil)
				firstVerifier := randomVerifier()
				_, _, firstState, firstChallenge := beginMobile(t, e, firstVerifier)
				firstCode := p.issue(t, firstChallenge, defaultUserinfo("changed-a", "changed-a", "changed-a@example.org"))
				requireStatus(t, "first redirect", mobileRedirect(t, e, firstState, firstCode), http.StatusFound)

				secondVerifier := randomVerifier()
				_, _, secondState, secondChallenge := beginMobile(t, e, secondVerifier)
				if secondState == firstState {
					t.Fatal("a new start reused the consumed state")
				}
				secondCode := p.issue(t, secondChallenge, defaultUserinfo("changed-b", "changed-b", "changed-b@example.org"))
				second := mobileRedirect(t, e, secondState, secondCode)
				requireStatus(t, "second redirect", second, http.StatusFound)
				appLink := location(t, second)
				if appLink.Query().Get("state") != secondState || appLink.Query().Get("code") != secondCode {
					t.Fatalf("second app link = %s", appLink)
				}
			},
		},
	}
}
