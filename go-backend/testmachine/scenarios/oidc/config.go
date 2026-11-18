package oidc

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// configScenarios cover GET /auth/openid/config (route-r005): an admin reads the
// provider discovery document for an issuer.
func configScenarios() []harness.Scenario {
	const invariant = "INV-R005-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: "route-r005", Invariant: invariant, Case: "normal", State: "stateless",
			Assertion: "config-admin-gets-seven-discovery-fields",
			Sources:   []string{"server/Auth.js:464", "server/auth/OidcAuthStrategy.js:439-476"},
			Claim:     "GET /auth/openid/config?issuer=<provider> as root answers 200 with exactly the issuer, authorization, token, userinfo, end-session and jwks endpoints and the signing alg list of the provider document, and nothing else.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				resp := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: e.Token(t, "root"), Query: map[string][]string{"issuer": {p.issuer()}}})
				requireStatus(t, "admin discovery", resp, http.StatusOK)
				var answer map[string]any
				resp.JSON(t, &answer)
				want := map[string]any{
					"issuer":                                p.issuer(),
					"authorization_endpoint":                p.endpoint("authorize"),
					"token_endpoint":                        p.endpoint("token"),
					"userinfo_endpoint":                     p.endpoint("userinfo"),
					"end_session_endpoint":                  p.endpoint("logout"),
					"jwks_uri":                              p.endpoint("jwks"),
					"id_token_signing_alg_values_supported": []any{"RS256"},
				}
				if len(answer) != len(want) {
					t.Fatalf("discovery answer has %d fields, want %d: %s", len(answer), len(want), resp.Body)
				}
				for key, value := range want {
					if got, ok := answer[key]; !ok || !sameJSON(got, value) {
						t.Fatalf("discovery %s = %#v, want %#v: %s", key, got, value, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r005", Invariant: invariant, Case: "error", State: "stateless",
			Assertion: "config-denies-anonymous-nonadmin-and-bad-issuer",
			Sources:   []string{"server/Auth.js:464-480", "server/auth/OidcAuthStrategy.js:439-476"},
			Claim:     "GET /auth/openid/config answers 401 with no provider data to an anonymous caller and 403 to an ordinary user; an admin request without issuer, with an unparseable issuer, or with an unreachable issuer answers 400 and never echoes the configured client secret, issuer URL or an internal error/stack.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				anonymous := e.Get(t, "", "/auth/openid/config")
				requireStatus(t, "anonymous discovery", anonymous, http.StatusUnauthorized)
				requireNoProviderDetail(t, p, "anonymous discovery", anonymous)

				listener := e.Get(t, e.Token(t, "listener"), "/auth/openid/config")
				requireStatus(t, "non-admin discovery", listener, http.StatusForbidden)
				requireNoProviderDetail(t, p, "non-admin discovery", listener)

				root := e.Token(t, "root")
				missing := e.Get(t, root, "/auth/openid/config")
				requireStatus(t, "missing issuer", missing, http.StatusBadRequest)
				requireNoProviderDetail(t, p, "missing issuer", missing)

				invalid := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: root, Query: map[string][]string{"issuer": {"not a url"}}})
				requireStatus(t, "invalid issuer", invalid, http.StatusBadRequest)
				requireNoProviderDetail(t, p, "invalid issuer", invalid)

				unreachable := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: root, Query: map[string][]string{"issuer": {"http://127.0.0.1:1/realm"}}})
				requireStatus(t, "unreachable issuer", unreachable, http.StatusBadRequest)
				requireNoProviderDetail(t, p, "unreachable issuer", unreachable)
			},
		},
		{
			Requirement: "route-r005", Invariant: invariant, Case: "edge", State: "stateless",
			Assertion: "config-trailing-slash-and-missing-document",
			Sources:   []string{"server/auth/OidcAuthStrategy.js:439-476"},
			Claim:     "An issuer with a trailing slash is accepted and answers the same seven fields, while an issuer whose discovery document answers 404 is reported as a 400 failure without provider content.",
			Run: func(t *testing.T, e *harness.Env) {
				p := mockProviderFor(t)
				root := e.Token(t, "root")
				trailing := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: root, Query: map[string][]string{"issuer": {p.issuer() + "/"}}})
				requireStatus(t, "trailing slash issuer", trailing, http.StatusOK)
				var answer map[string]any
				trailing.JSON(t, &answer)
				if answer["issuer"] != p.issuer() || answer["jwks_uri"] != p.endpoint("jwks") {
					t.Fatalf("trailing-slash discovery = %s", trailing.Body)
				}

				missing := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: root, Query: map[string][]string{"issuer": {p.issuer() + "/missing"}}})
				requireStatus(t, "missing discovery document", missing, http.StatusBadRequest)
				requireNoProviderDetail(t, p, "missing discovery document", missing)

				queryIssuer := e.Do(t, harness.Request{Path: "/auth/openid/config", Token: root, Query: map[string][]string{"issuer": {p.issuer() + "?x=1"}}})
				requireStatus(t, "issuer with query", queryIssuer, http.StatusBadRequest)
				requireNoProviderDetail(t, p, "issuer with query", queryIssuer)
			},
		},
	}
}

// requireNoProviderDetail fails when an error answer leaks provider settings or
// an internal error trace to the caller.
func requireNoProviderDetail(t *testing.T, p *mockProvider, what string, resp harness.Response) {
	t.Helper()
	text := string(resp.Body)
	for _, marker := range []string{p.clientSecret, p.clientID, p.issuer(), "stack", "TypeError", "AxiosError"} {
		if strings.Contains(text, marker) {
			t.Fatalf("%s leaks %q: %s", what, marker, text)
		}
	}
	requireNoTokenMaterial(t, what, resp)
}

// sameJSON compares decoded JSON values by value, which is enough for strings
// and string arrays.
func sameJSON(got, want any) bool { return reflect.DeepEqual(got, want) }
