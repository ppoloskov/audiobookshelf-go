package mail

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Sources of the settings routes.
const (
	emailSettingsController = "server/controllers/EmailController.js:22-42"
	emailSettingsObject     = "server/objects/settings/EmailSettings.js:13-104"
	emailSettingsRow        = "server/models/Setting.js:21-41"
)

// settingsReadScenarios are the route-r170 scenarios: the admin settings read
// and its defaults.
func settingsReadScenarios() []harness.Scenario {
	const req = "route-r170"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R170-ROUTE", "normal", "stateless", "settings-read-defaults-without-row",
			"GET /api/emails/settings as admin returns the constructed defaults (port 465, secure and rejectUnauthorized true, null host/user/pass/addresses, empty ereaderDevices) and creates no settings row.",
			[]string{emailSettingsController, emailSettingsObject, emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				_, _, exists := storedEmailSettings(t, e)
				if exists {
					t.Fatalf("precondition: the fixture has no email-settings row")
				}
				settings := emailSettings(t, e)
				wantKeys(t, "settings defaults", settings, map[string]any{
					"id": "email-settings", "host": nil, "port": 465.0, "secure": true,
					"rejectUnauthorized": true, "user": nil, "pass": nil,
					"testAddress": nil, "fromAddress": nil, "ereaderDevices": []any{},
				})
				if _, _, exists := storedEmailSettings(t, e); exists {
					t.Fatal("a read wrote an email-settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R170-ROUTE", "error", "stateless", "settings-read-admin-only",
			"GET /api/emails/settings answers 401 without a token and 404 for an authenticated non-admin, and leaves the settings row absent.",
			[]string{emailSettingsController, "server/controllers/EmailController.js:133-137", "server/Server.js:354"},
			func(t *testing.T, e *harness.Env) {
				adminOnly(t, e, http.MethodGet, "/api/emails/settings", nil)
				if _, _, exists := storedEmailSettings(t, e); exists {
					t.Fatal("a refused read wrote an email-settings row")
				}
			}),
	}
}

// settingsReflectScenarios is the route-r170 edge case: a read after an admin
// write returns the stored values with their JSON types.
func settingsReflectScenarios() []harness.Scenario {
	const req = "route-r170"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R170-ROUTE", "edge", "stateless", "settings-read-reflects-stored-values",
			"After an admin PATCH, GET /api/emails/settings returns every stored value with its JSON type (number port, boolean flags, strings, null test address, device array).",
			[]string{emailSettingsController, emailSettingsObject},
			func(t *testing.T, e *harness.Env) {
				settings := patchEmailSettings(t, e, map[string]any{
					"host": "127.0.0.1", "port": 2525, "secure": false, "rejectUnauthorized": false,
					"user": "reader", "pass": "secret", "testAddress": nil, "fromAddress": "library@example.test",
				})
				wantKeys(t, "PATCH response", settings, map[string]any{"port": 2525.0, "secure": false})
				read := emailSettings(t, e)
				wantKeys(t, "GET response", read, map[string]any{
					"id": "email-settings", "host": "127.0.0.1", "port": 2525.0, "secure": false,
					"rejectUnauthorized": false, "user": "reader", "pass": "secret",
					"testAddress": nil, "fromAddress": "library@example.test", "ereaderDevices": []any{},
				})
			}),
	}
}

// settingsRestartScenarios proves the persisted row is rebuilt into
// EmailSettings when the server starts (EmailSettings.construct).
func settingsRestartScenarios() []harness.Scenario {
	const req = "route-r171"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R171-ROUTE", "edge", "changed", "settings-survive-restart",
			"After a restart, GET /api/emails/settings returns exactly the stored settings values, so the row is reconstructed into the settings object at boot.",
			[]string{emailSettingsObject, "server/Database.js:369-375", emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				patchEmailSettings(t, e, map[string]any{
					"host": "127.0.0.1", "port": 2527, "secure": true, "rejectUnauthorized": false,
					"user": "reader", "pass": "restart-secret", "testAddress": "restart@example.test",
					"fromAddress": "restart@example.test",
				})
				deviceScenario(t, e, map[string]any{"name": "Across Restart", "email": "restart-device@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				before := emailSettings(t, e)
				e.Restart(t)
				after := emailSettings(t, e)
				if !sameJSON(before, after) {
					t.Fatalf("settings after restart = %v, want %v", after, before)
				}
				wantKeys(t, "settings after restart", after, map[string]any{
					"host": "127.0.0.1", "port": 2527.0, "secure": true, "rejectUnauthorized": false,
					"user": "reader", "pass": "restart-secret", "testAddress": "restart@example.test",
					"ereaderDevices": []any{map[string]any{"name": "Across Restart", "email": "restart-device@example.test", "availabilityOption": "adminOrUp", "users": []any{}}},
				})
			}),
		scenario(req, "INV-ROUTE-R171-VALIDATION-001", "edge", "changed", "settings-legacy-row-defaults-reject-unauthorized",
			"A stored email-settings row without rejectUnauthorized (pre-v2.10.1) is read back with rejectUnauthorized true while its other stored values are kept.",
			[]string{emailSettingsObject, "server/objects/settings/EmailSettings.js:44-47", "server/Database.js:369-375"},
			func(t *testing.T, e *harness.Env) {
				legacyRowWithoutRejectUnauthorized(t, e)
				e.Restart(t)
				after := emailSettings(t, e)
				wantKeys(t, "legacy row after restart", after, map[string]any{
					"host": "legacy.example.test", "port": 2525.0, "secure": true,
					"rejectUnauthorized": true, "fromAddress": "legacy@example.test",
				})
			}),
	}
}

// settingsWriteScenarios are the route-r171 scenarios: PATCH of the settings
// row.
func settingsWriteScenarios() []harness.Scenario {
	const req = "route-r171"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R171-ROUTE", "normal", "initial", "settings-patch-client-payload-persisted",
			"PATCH /api/emails/settings with the settings page payload returns the new settings and stores one email-settings row whose JSON matches them, including the password the form reads back.",
			[]string{emailSettingsController, emailSettingsObject, emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				_, _, existed := storedEmailSettings(t, e)
				if existed {
					t.Fatalf("precondition: this scenario creates the first settings row")
				}
				payload := map[string]any{
					"host": "smtp.example.test", "port": 587, "secure": false, "rejectUnauthorized": true,
					"user": "mailer", "pass": "mailer-secret", "testAddress": "test@example.test",
					"fromAddress": "Library <library@example.test>",
				}
				settings := patchEmailSettings(t, e, payload)
				wantKeys(t, "PATCH response", settings, payload)
				wantKeys(t, "PATCH response", settings, map[string]any{"id": "email-settings", "ereaderDevices": []any{}})
				row, updatedAt, exists := storedEmailSettings(t, e)
				if !exists {
					t.Fatal("PATCH did not create the email-settings row")
				}
				wantKeys(t, "stored row", row, payload)
				if updatedAt == "" {
					t.Fatal("stored row has no updatedAt")
				}
			}),
		scenario(req, "INV-ROUTE-R171-VALIDATION-001", "edge", "initial", "settings-patch-absent-keys-keep-values",
			"A PATCH that omits secure, rejectUnauthorized and port keeps the stored values (secure and rejectUnauthorized stay true), while a null port becomes 465.",
			[]string{emailSettingsController, emailSettingsObject},
			func(t *testing.T, e *harness.Env) {
				patchEmailSettings(t, e, map[string]any{
					"host": "127.0.0.1", "port": 465, "secure": true, "rejectUnauthorized": true,
					"fromAddress": "library@example.test",
				})
				partial := patchEmailSettings(t, e, map[string]any{"fromAddress": "other@example.test"})
				wantKeys(t, "partial PATCH", partial, map[string]any{
					"host": "127.0.0.1", "port": 465.0, "secure": true, "rejectUnauthorized": true,
					"fromAddress": "other@example.test",
				})
				row, _, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row after partial PATCH", row, map[string]any{"secure": true, "rejectUnauthorized": true})
				nullPort := patchEmailSettings(t, e, map[string]any{"port": nil})
				wantKeys(t, "null port", nullPort, map[string]any{"port": 465.0})
				row, _, _ = storedEmailSettings(t, e)
				wantKeys(t, "stored null port", row, map[string]any{"port": 465.0})
			}),
		scenario(req, "INV-ROUTE-R171-REPEAT", "edge", "repeat", "settings-patch-repeat-no-write",
			"Repeating the same admin PATCH returns the same settings and leaves the row value and updatedAt unchanged.",
			[]string{emailSettingsController, emailSettingsObject, emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				body := map[string]any{"host": "127.0.0.1", "port": 2525, "secure": false, "rejectUnauthorized": true, "fromAddress": "library@example.test"}
				patchEmailSettings(t, e, body)
				before, stamp, _ := storedEmailSettings(t, e)
				first := patchEmailSettings(t, e, body)
				second := patchEmailSettings(t, e, body)
				if !sameJSON(first, second) {
					t.Fatalf("repeat changed the response: %v vs %v", first, second)
				}
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before, after) {
					t.Fatalf("repeat changed the stored settings: %v vs %v", before, after)
				}
				if afterStamp != stamp {
					t.Fatalf("repeat rewrote the row: updatedAt %q -> %q", stamp, afterStamp)
				}
			}),
		scenario(req, "INV-ROUTE-R171-ROUTE", "edge", "changed", "settings-patch-changed-values-persisted",
			"Changing host, port, flags and addresses in a later PATCH returns and stores the new values and moves the row's updatedAt forward.",
			[]string{emailSettingsController, emailSettingsObject, emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				_, stamp, _ := storedEmailSettings(t, e)
				settings := patchEmailSettings(t, e, map[string]any{
					"host": "127.0.0.1", "port": 2526, "secure": true, "rejectUnauthorized": false,
					"testAddress": "changed@example.test", "fromAddress": "changed@example.test",
				})
				wantKeys(t, "PATCH response", settings, map[string]any{
					"port": 2526.0, "secure": true, "rejectUnauthorized": false,
					"testAddress": "changed@example.test", "fromAddress": "changed@example.test",
				})
				row, after, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row", row, map[string]any{
					"port": 2526.0, "secure": true, "rejectUnauthorized": false,
					"testAddress": "changed@example.test", "fromAddress": "changed@example.test",
				})
				if after <= stamp {
					t.Fatalf("changed PATCH must move updatedAt forward: %q -> %q", stamp, after)
				}
			}),
		scenario(req, "INV-ROUTE-R171-ROUTE", "error", "failure", "settings-patch-admin-only-no-write",
			"PATCH /api/emails/settings answers 401 without a token and 404 for a non-admin, and the row value and updatedAt stay as they were.",
			[]string{emailSettingsController, "server/controllers/EmailController.js:133-137", "server/Server.js:354"},
			func(t *testing.T, e *harness.Env) {
				before, stamp, _ := storedEmailSettings(t, e)
				adminOnly(t, e, http.MethodPatch, "/api/emails/settings", map[string]any{"host": "evil.example.test", "port": 25})
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before, after) {
					t.Fatalf("a refused PATCH changed the row: %v vs %v", before, after)
				}
				if afterStamp != stamp {
					t.Fatalf("a refused PATCH rewrote the row: updatedAt %q -> %q", stamp, afterStamp)
				}
			}),
	}
}
