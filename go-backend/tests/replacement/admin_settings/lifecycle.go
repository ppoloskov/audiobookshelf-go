package admin_settings

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// lifecycleScenarios use their own server: environment overrides and restarts.
func lifecycleScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r205", "INV-ROUTE-R205-VALIDATION-001", "error", "failure", "settings-env-allow-iframe-cannot-be-disabled",
			"With ALLOW_IFRAME=1 the server starts with allowIframe true and no frame-ancestors policy, PATCH allowIframe:false answers 400 and changes nothing, and allowIframe:true is accepted.",
			[]string{"server/controllers/MiscController.js:148-151", "server/objects/settings/ServerSettings.js:224-227"},
			func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{Env: map[string]string{"ALLOW_IFRAME": "1"}})
				root := e.Token(t, "root")
				if authorizeSettings(t, e, root)["allowIframe"] != true {
					t.Fatal("allowIframe must be true at start")
				}
				if got := e.Get(t, "", "/ping").Header.Get("Content-Security-Policy"); got != "" {
					t.Fatalf("CSP = %q, want none", got)
				}
				before := storedSettings(t, e)
				wantStatus(t, "disable", e.Patch(t, root, "/api/settings", map[string]any{"allowIframe": false, "language": "fr"}), http.StatusBadRequest)
				if !sameJSON(before, storedSettings(t, e)) || authorizeSettings(t, e, root)["allowIframe"] != true {
					t.Fatal("the rejected request changed settings")
				}
				if settingsPatch(t, e, map[string]any{"allowIframe": true})["allowIframe"] != true {
					t.Fatal("allowIframe:true must be accepted")
				}
			}),
		scenario("route-r205", "INV-ROUTE-R205-PERSIST-001", "edge", "changed", "settings-persist-across-restart",
			"Settings written with PATCH /api/settings are served unchanged after the server restarts.",
			[]string{settingsHandler, "server/objects/settings/ServerSettings.js:100-140"},
			func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				patch := map[string]any{
					"language": "fr", "dateFormat": "yyyy-MM-dd", "timeFormat": "h:mma", "bookshelfView": 0, "homeBookshelfView": 0,
					"backupsToKeep": 5, "maxBackupSize": 0, "scannerCoverProvider": "audible", "sortingIgnorePrefix": true,
					"allowedOrigins": []string{"http://example.test"}, "chromecastEnabled": true, "storeCoverWithItem": true,
					"scannerParseSubtitle": true, "scannerDisableWatcher": true, "backupSchedule": "0 2 * * *",
				}
				settingsPatch(t, e, patch)
				e.Restart(t)
				wantKeys(t, "after restart", authorizeSettings(t, e, e.Token(t, "root")), patch)
				if language, _, _ := readStatus(t, e); language != "fr" {
					t.Fatalf("status language after restart = %q", language)
				}
				wantKeys(t, "row after restart", storedSettings(t, e), patch)
			}),
		scenario("route-r216", "INV-ROUTE-R216-PERSIST-001", "edge", "changed", "auth-settings-survive-restart",
			"A complete OpenID configuration with openid enabled survives a restart, including the secret fields and the advertised auth methods.",
			[]string{authPatchSource, "server/objects/settings/ServerSettings.js:136-153"},
			func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				body := openIDSettings()
				body["authActiveAuthMethods"] = []string{"local", "openid"}
				wantAuthResult(t, patchAuth(t, e, body), true)
				e.Restart(t)
				wantKeys(t, "after restart", getAuth(t, e), body)
				if _, methods, form := readStatus(t, e); !sameJSON(methods, []string{"local", "openid"}) || form["authOpenIDButtonText"] != "Sign in with IdP" {
					t.Fatalf("status after restart: %v %v", methods, form)
				}
			}),
		scenario("route-r216", "INV-ROUTE-R216-PERSIST-001", "edge", "changed", "auth-incomplete-openid-dropped-at-restart",
			"Enabling openid without a complete OpenID configuration is accepted and stored, but the server drops it from the active methods when it loads the settings again.",
			[]string{authPatchSource, "server/objects/settings/ServerSettings.js:155-168"},
			func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				wantAuthResult(t, patchAuth(t, e, map[string]any{"authActiveAuthMethods": []string{"local", "openid"}}), true)
				if !sameJSON(storedSettings(t, e)["authActiveAuthMethods"], []string{"local", "openid"}) {
					t.Fatalf("stored methods = %v", storedSettings(t, e)["authActiveAuthMethods"])
				}
				e.Restart(t)
				if _, methods, _ := readStatus(t, e); !sameJSON(methods, []string{"local"}) {
					t.Fatalf("status authMethods after restart = %v, want [local]", methods)
				}
				if got := getAuth(t, e)["authActiveAuthMethods"]; !sameJSON(got, []string{"local"}) {
					t.Fatalf("auth settings methods after restart = %v", got)
				}
			}),
	}
}
