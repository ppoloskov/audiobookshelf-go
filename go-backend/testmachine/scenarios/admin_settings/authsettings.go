package admin_settings

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	authGetSource   = "server/controllers/MiscController.js:635-640"
	authPatchSource = "server/controllers/MiscController.js:650-735"
)

// openIDSettings is a complete OpenID configuration as the authentication page sends it.
func openIDSettings() map[string]any {
	return map[string]any{
		"authOpenIDIssuerURL": "https://idp.example/", "authOpenIDAuthorizationURL": "https://idp.example/auth",
		"authOpenIDTokenURL": "https://idp.example/token", "authOpenIDUserInfoURL": "https://idp.example/userinfo",
		"authOpenIDJwksURL": "https://idp.example/jwks", "authOpenIDLogoutURL": "https://idp.example/logout",
		"authOpenIDClientID": "abs-client", "authOpenIDClientSecret": "s3cret", "authOpenIDTokenSigningAlgorithm": "ES256",
		"authOpenIDButtonText": "Sign in with IdP", "authOpenIDAutoLaunch": true, "authOpenIDAutoRegister": true,
		"authOpenIDMatchExistingBy": "email", "authOpenIDMobileRedirectURIs": []string{"audiobookshelf://oauth", "https://app.example/cb"},
		"authOpenIDGroupClaim": "groups", "authOpenIDAdvancedPermsClaim": "abs_perms",
		"authOpenIDSubfolderForRedirectURLs": "/audiobookshelf", "authLoginCustomMessage": "<b>Welcome</b>",
	}
}

func patchAuth(t *testing.T, e *harness.Env, body any) harness.Response {
	t.Helper()
	return e.Patch(t, e.Token(t, "root"), "/api/auth-settings", body)
}

// wantAuthResult checks {updated} and returns serverSettings (secret-free).
func wantAuthResult(t *testing.T, resp harness.Response, updated bool) map[string]any {
	t.Helper()
	got := responseSettings(t, resp)
	if resp.Object(t)["updated"] != updated {
		t.Fatalf("updated = %v, want %v: %.300s", resp.Object(t)["updated"], updated, resp.Body)
	}
	wantNoSecrets(t, "auth-settings response serverSettings", got)
	return got
}

func getAuth(t *testing.T, e *harness.Env) map[string]any {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/auth-settings")
	wantStatus(t, "GET auth-settings", resp, http.StatusOK)
	return resp.Object(t)
}

func authReadScenarios() []harness.Scenario {
	const req = "route-r215"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R215-ROUTE", "normal", "stateless", "auth-settings-initial-document",
			"GET /api/auth-settings returns the authentication settings of the fixture (local only, OpenID defaults, null secrets, sample permissions text) without authOpenIDSubfolderForRedirectURLs.",
			[]string{authGetSource, "server/objects/settings/ServerSettings.js:312-340"},
			func(t *testing.T, e *harness.Env) {
				got := getAuth(t, e)
				wantKeys(t, "auth settings", got, map[string]any{
					"authLoginCustomMessage": nil, "authActiveAuthMethods": []string{"local"},
					"authOpenIDIssuerURL": nil, "authOpenIDAuthorizationURL": nil, "authOpenIDTokenURL": nil,
					"authOpenIDUserInfoURL": nil, "authOpenIDJwksURL": nil, "authOpenIDLogoutURL": nil,
					"authOpenIDClientID": nil, "authOpenIDClientSecret": nil, "authOpenIDTokenSigningAlgorithm": "RS256",
					"authOpenIDButtonText": "Login with OpenId", "authOpenIDAutoLaunch": false, "authOpenIDAutoRegister": false,
					"authOpenIDMatchExistingBy": nil, "authOpenIDMobileRedirectURIs": []string{"audiobookshelf://oauth"},
					"authOpenIDGroupClaim": "", "authOpenIDAdvancedPermsClaim": "",
				})
				if _, present := got["authOpenIDSubfolderForRedirectURLs"]; present {
					t.Fatalf("authOpenIDSubfolderForRedirectURLs must be absent while unset: %v", got["authOpenIDSubfolderForRedirectURLs"])
				}
				var sample map[string]any
				text, _ := got["authOpenIDSamplePermissions"].(string)
				if err := json.Unmarshal([]byte(text), &sample); err != nil || sample["canDownload"] == nil {
					t.Fatalf("authOpenIDSamplePermissions must be JSON text of sample permissions: %q", text)
				}
			}),
		scenario(req, "INV-ROUTE-R215-ROUTE", "error", "stateless", "auth-settings-admin-only",
			"GET /api/auth-settings answers 401 without a token and 403 for a non-admin user.",
			[]string{authGetSource},
			func(t *testing.T, e *harness.Env) { adminOnly(t, e, http.MethodGet, "/api/auth-settings", nil) }),
	}
}

func authPatchScenarios() []harness.Scenario {
	const req = "route-r216"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R216-ROUTE", "error", "initial", "auth-patch-admin-only",
			"PATCH /api/auth-settings answers 401 without a token and 403 for a non-admin user and changes nothing.",
			[]string{authPatchSource},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				adminOnly(t, e, http.MethodPatch, "/api/auth-settings", map[string]any{"authOpenIDButtonText": "x"})
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("a refused request changed the settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R216-VALIDATION-001", "error", "failure", "auth-patch-invalid-values-ignored",
			"PATCH /api/auth-settings ignores empty or unknown auth methods, bad redirect URIs and wrong value types with updated:false, rejects malformed JSON with 400, and stores nothing.",
			[]string{authPatchSource},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				bodies := []map[string]any{
					{"authActiveAuthMethods": []string{}}, {"authActiveAuthMethods": []string{"x"}}, {"authActiveAuthMethods": "local"},
					{"authOpenIDMobileRedirectURIs": []string{"*", "audiobookshelf://oauth"}}, {"authOpenIDMobileRedirectURIs": []string{"not a uri"}},
					{"authOpenIDMobileRedirectURIs": []string{"https://host:8080/cb"}}, {"authOpenIDMobileRedirectURIs": "audiobookshelf://oauth2"},
					{"authOpenIDAutoLaunch": "true"}, {"authOpenIDAutoRegister": nil}, {"authOpenIDAutoLaunch": 1},
					{"authOpenIDIssuerURL": 5}, {"authLoginCustomMessage": 5}, {"authOpenIDButtonText": []string{"x"}},
				}
				for _, body := range bodies {
					wantAuthResult(t, patchAuth(t, e, body), false)
				}
				wantStatus(t, "malformed", patchAuth(t, e, `{bad`), http.StatusBadRequest)
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("ignored values changed the settings row")
				}
				wantSame(t, "settings row", stamp, settingsUpdatedAt(t, e))
			}),
		scenario(req, "INV-ROUTE-R216-ROUTE", "normal", "initial", "auth-patch-openid-settings-persist",
			"PATCH /api/auth-settings stores a complete OpenID configuration and answers {updated:true, serverSettings} without the secret fields.",
			[]string{authPatchSource, "server/objects/settings/ServerSettings.js:299-308", "client/pages/config/authentication.vue:325-362"},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				got := wantAuthResult(t, patchAuth(t, e, openIDSettings()), true)
				wantMovedForward(t, "settings row", stamp, settingsUpdatedAt(t, e))
				visible := map[string]any{
					"authOpenIDIssuerURL": "https://idp.example/", "authOpenIDAuthorizationURL": "https://idp.example/auth",
					"authOpenIDTokenURL": "https://idp.example/token", "authOpenIDUserInfoURL": "https://idp.example/userinfo",
					"authOpenIDJwksURL": "https://idp.example/jwks", "authOpenIDLogoutURL": "https://idp.example/logout",
					"authOpenIDTokenSigningAlgorithm": "ES256", "authOpenIDButtonText": "Sign in with IdP",
					"authOpenIDAutoLaunch": true, "authOpenIDAutoRegister": true, "authOpenIDMatchExistingBy": "email",
					"authOpenIDSubfolderForRedirectURLs": "/audiobookshelf", "authLoginCustomMessage": "<b>Welcome</b>",
					"authActiveAuthMethods": []string{"local"},
				}
				wantKeys(t, "response", got, visible)
				stored := map[string]any{"authOpenIDClientID": "abs-client", "authOpenIDClientSecret": "s3cret",
					"authOpenIDMobileRedirectURIs": []string{"audiobookshelf://oauth", "https://app.example/cb"},
					"authOpenIDGroupClaim":         "groups", "authOpenIDAdvancedPermsClaim": "abs_perms"}
				for key, value := range visible {
					stored[key] = value
				}
				onlyChanged(t, "settings row", before, storedSettings(t, e), stored)
				if _, methods, form := readStatus(t, e); !sameJSON(methods, []string{"local"}) || form["authOpenIDButtonText"] != nil {
					t.Fatalf("status must not advertise inactive OpenID: %v %v", methods, form)
				}
			}),
		scenario("route-r215", "INV-ROUTE-R215-ROUTE", "edge", "stateless", "auth-settings-get-returns-secrets-to-admin",
			"GET /api/auth-settings returns the OpenID client id, client secret, mobile redirect URIs and claims verbatim to an admin.",
			[]string{authGetSource},
			func(t *testing.T, e *harness.Env) {
				wantKeys(t, "auth settings", getAuth(t, e), map[string]any{
					"authOpenIDClientID": "abs-client", "authOpenIDClientSecret": "s3cret", "authOpenIDGroupClaim": "groups",
					"authOpenIDAdvancedPermsClaim": "abs_perms", "authOpenIDSubfolderForRedirectURLs": "/audiobookshelf",
					"authOpenIDMobileRedirectURIs": []string{"audiobookshelf://oauth", "https://app.example/cb"},
				})
			}),
		secretsScenario(),
		scenario(req, "INV-ROUTE-R216-REPEAT", "edge", "repeat", "auth-patch-repeat-reports-no-update",
			"Sending the same authentication settings again answers updated:false and leaves the row unchanged.",
			[]string{authPatchSource},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				wantAuthResult(t, patchAuth(t, e, openIDSettings()), false)
				wantSame(t, "settings row", stamp, settingsUpdatedAt(t, e))
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("repeat changed the settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R216-ROUTE", "normal", "changed", "auth-patch-enable-openid-visible-in-status",
			"Enabling openid next to local (unknown methods dropped, list sorted) is stored and GET /status then shows authMethods, the sanitized message and the OpenID button settings.",
			[]string{authPatchSource, "server/Server.js:384-399", "server/objects/settings/ServerSettings.js:348-357"},
			func(t *testing.T, e *harness.Env) {
				got := wantAuthResult(t, patchAuth(t, e, map[string]any{"authActiveAuthMethods": []string{"openid", "local", "x"}}), true)
				if !sameJSON(got["authActiveAuthMethods"], []string{"local", "openid"}) || !sameJSON(storedSettings(t, e)["authActiveAuthMethods"], []string{"local", "openid"}) {
					t.Fatalf("methods = %v", got["authActiveAuthMethods"])
				}
				_, methods, form := readStatus(t, e)
				if !sameJSON(methods, []string{"local", "openid"}) {
					t.Fatalf("status authMethods = %v", methods)
				}
				wantKeys(t, "authFormData", form, map[string]any{"authLoginCustomMessage": "<b>Welcome</b>", "authOpenIDButtonText": "Sign in with IdP", "authOpenIDAutoLaunch": true})
			}),
		scenario(req, "INV-ROUTE-R216-VALIDATION-001", "edge", "changed", "auth-patch-message-sanitized-and-empty-strings-cleared",
			"authLoginCustomMessage is HTML-sanitized, an empty string stores null (authOpenIDSubfolderForRedirectURLs keeps the empty string), and null clears a value.",
			[]string{authPatchSource, "server/utils/htmlSanitizer.js:9-25"},
			func(t *testing.T, e *harness.Env) {
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authLoginCustomMessage": `<b>hi</b><script>alert(1)</script><i onclick="x()">t</i>`}), true)
				if got := getAuth(t, e)["authLoginCustomMessage"]; got != "<b>hi</b><i>t</i>" {
					t.Fatalf("sanitized message = %v", got)
				}
				wantAuthResult(t, patchAuth(t, e, map[string]any{
					"authLoginCustomMessage": "", "authOpenIDMatchExistingBy": "", "authOpenIDSubfolderForRedirectURLs": "", "authOpenIDLogoutURL": nil,
				}), true)
				got := getAuth(t, e)
				wantKeys(t, "cleared", got, map[string]any{
					"authLoginCustomMessage": nil, "authOpenIDMatchExistingBy": nil, "authOpenIDSubfolderForRedirectURLs": "", "authOpenIDLogoutURL": nil,
				})
				if _, _, form := readStatus(t, e); form["authLoginCustomMessage"] != "" {
					t.Fatalf("status message = %v, want empty string", form["authLoginCustomMessage"])
				}
			}),
		scenario(req, "INV-ROUTE-R216-VALIDATION-001", "edge", "changed", "auth-patch-mobile-redirect-uri-rules",
			"authOpenIDMobileRedirectURIs accepts a lone * and well-formed scheme://host/path URIs and ignores * mixed with others.",
			[]string{authPatchSource},
			func(t *testing.T, e *harness.Env) {
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authOpenIDMobileRedirectURIs": []string{"*"}}), true)
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authOpenIDMobileRedirectURIs": []string{"*", "https://app.example/cb"}}), false)
				if got := getAuth(t, e)["authOpenIDMobileRedirectURIs"]; !sameJSON(got, []string{"*"}) {
					t.Fatalf("redirect URIs = %v, want [*]", got)
				}
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authOpenIDMobileRedirectURIs": []string{"audiobookshelf://oauth", "https://app.example/cb"}}), true)
			}),
		scenario(req, "INV-ROUTE-R216-SIDEEFFECT-001", "edge", "changed", "auth-patch-disabling-local-stops-password-login",
			"Removing local from the active methods makes POST /login fail without a token (existing tokens keep working); restoring local makes it succeed again.",
			[]string{authPatchSource, "server/Auth.js:169-177"},
			func(t *testing.T, e *harness.Env) {
				login := func() harness.Response {
					return e.Post(t, "", "/login", map[string]string{"username": "trial-listener", "password": "trial-listener-password"})
				}
				root := e.Token(t, "root")
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authActiveAuthMethods": []string{"openid"}}), true)
				if _, methods, _ := readStatus(t, e); !sameJSON(methods, []string{"openid"}) {
					t.Fatalf("status authMethods = %v", methods)
				}
				if resp := login(); resp.Status < 400 || json.Valid(resp.Body) && harness.Dig(resp.Object(t), "user", "accessToken") != nil {
					t.Fatalf("password login must fail while local is disabled: %d %.200s", resp.Status, resp.Body)
				}
				wantStatus(t, "existing token", e.Get(t, root, "/api/me"), http.StatusOK)
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authActiveAuthMethods": []string{"local"}}), true)
				wantStatus(t, "login after restoring local", login(), http.StatusOK)
			}),
	}
}
