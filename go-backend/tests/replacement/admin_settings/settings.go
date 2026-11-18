package admin_settings

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	settingsHandler = "server/controllers/MiscController.js:139-175"
	settingsKeys    = "server/objects/settings/ServerSettings.js:8-29"
)

// clientSettings has one new value for every key the settings pages write
// (client/pages/config/index.vue, backups.vue, log.vue, BackupScheduleModal.vue).
func clientSettings() map[string]any {
	return map[string]any{
		"scannerParseSubtitle": true, "scannerFindCovers": true, "scannerCoverProvider": "audible",
		"scannerPreferMatchedMetadata": true, "scannerDisableWatcher": true,
		"storeCoverWithItem": true, "storeMetadataWithItem": true, "allowIframe": true,
		"allowedOrigins": []string{"http://example.test"}, "backupSchedule": "0 2 * * *",
		"backupsToKeep": 5, "maxBackupSize": 3, "logLevel": 3,
		"homeBookshelfView": 0, "bookshelfView": 0, "dateFormat": "yyyy-MM-dd", "timeFormat": "h:mma",
		"language": "de", "chromecastEnabled": true, "sortingIgnorePrefix": true,
	}
}

func settingsScenarios() []harness.Scenario {
	const req = "route-r205"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R205-AUTH-001", "error", "initial", "settings-patch-admin-only",
			"PATCH /api/settings answers 401 without a token and 403 for a non-admin and changes nothing.",
			[]string{settingsHandler},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				adminOnly(t, e, http.MethodPatch, "/api/settings", map[string]any{"language": "xx"})
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("a refused request changed the settings row")
				}
				wantSame(t, "settings row", stamp, settingsUpdatedAt(t, e))
			}),
		scenario(req, "INV-ROUTE-R205-VALIDATION-001", "error", "failure", "settings-patch-invalid-body-rejected",
			"PATCH /api/settings with a non-object body or non-array allowedOrigins answers 400 and applies none of the keys.",
			[]string{settingsHandler, "server/Server.js:349-352"},
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				before := storedSettings(t, e)
				for _, raw := range []string{`"x"`, `5`, `null`, `{bad`} {
					wantStatus(t, "body "+raw, e.Patch(t, root, "/api/settings", raw), http.StatusBadRequest)
				}
				wantStatus(t, "allowedOrigins string", e.Patch(t, root, "/api/settings",
					map[string]any{"allowedOrigins": "http://x", "language": "xx"}), http.StatusBadRequest)
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("a rejected request changed the settings row")
				}
				if language, _, _ := readStatus(t, e); language != before["language"] {
					t.Fatalf("language changed to %q", language)
				}
			}),
		scenario(req, "INV-ROUTE-R205-ROUTE", "normal", "initial", "settings-patch-every-client-key",
			"PATCH /api/settings stores every key the settings pages write, returns them in serverSettings with timeZone and no secrets, and leaves other keys alone.",
			[]string{settingsHandler, settingsKeys, "server/objects/settings/ServerSettings.js:299-308", "server/models/Setting.js:36-41"},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				patch := clientSettings()
				got := settingsPatch(t, e, patch)
				wantMovedForward(t, "settings row", stamp, settingsUpdatedAt(t, e))
				wantKeys(t, "response", got, patch)
				wantKeys(t, "response", got, map[string]any{
					"timeZone": "UTC", "version": "2.37.2", "metadataFileFormat": "json",
					"rateLimitLoginRequests": 10, "rateLimitLoginWindow": 600000,
					"podcastEpisodeSchedule": "0 * * * *", "sortingPrefixes": []string{"the", "a"},
					"authActiveAuthMethods": []string{"local"}, "loggerDailyLogsToKeep": 7,
				})
				wantNoSecrets(t, "serverSettings", got)
				after := storedSettings(t, e)
				onlyChanged(t, "settings row", before, after, patch)
				if after["tokenSecret"] == nil || after["tokenSecret"] == "" {
					t.Fatal("tokenSecret must stay in the settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R205-REPEAT", "edge", "repeat", "settings-patch-repeat-no-change",
			"Repeating the same PATCH /api/settings returns the same serverSettings and leaves the row as it was.",
			[]string{settingsHandler, "server/objects/settings/ServerSettings.js:365-378"},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				// allowedOrigins is left out: upstream compares arrays by reference, so
				// every patch that carries it rewrites the row (not a client-visible contract).
				body := clientSettings()
				delete(body, "allowedOrigins")
				first := settingsPatch(t, e, body)
				second := settingsPatch(t, e, body)
				if !sameJSON(first, second) {
					t.Fatalf("repeat changed the response: %v vs %v", first, second)
				}
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("repeat changed the settings row")
				}
				wantSame(t, "settings row", stamp, settingsUpdatedAt(t, e))
			}),
		scenario(req, "INV-ROUTE-R205-ROUTE", "edge", "changed", "settings-patch-booleans-round-trip",
			"Every boolean setting the pages toggle can be switched off and on again: response and row follow each write.",
			[]string{settingsHandler, settingsKeys},
			func(t *testing.T, e *harness.Env) {
				keys := []string{"scannerParseSubtitle", "scannerFindCovers", "scannerPreferMatchedMetadata", "scannerDisableWatcher",
					"storeCoverWithItem", "storeMetadataWithItem", "allowIframe", "chromecastEnabled", "sortingIgnorePrefix"}
				for _, value := range []bool{false, true, false, true} {
					patch := map[string]any{}
					for _, key := range keys {
						patch[key] = value
					}
					wantKeys(t, "response", settingsPatch(t, e, patch), patch)
					wantKeys(t, "row", storedSettings(t, e), patch)
				}
			}),
		scenario(req, "INV-ROUTE-R205-VALIDATION-001", "edge", "initial", "settings-patch-ignores-unpatchable-keys",
			"Keys outside the patchable list (tokenSecret, version, rate limits, backupPath, sortingPrefixes, auth settings, unknown keys) are ignored while a patchable key in the same body is applied.",
			[]string{settingsHandler, settingsKeys},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				stamp := settingsUpdatedAt(t, e)
				got := settingsPatch(t, e, map[string]any{
					"tokenSecret": "x", "version": "0.0.1", "buildNumber": 999, "rateLimitLoginRequests": 1,
					"rateLimitLoginWindow": 1, "backupPath": "/x", "metadataFileFormat": "abs",
					"sortingPrefixes": []string{"zzz"}, "authActiveAuthMethods": []string{"openid"},
					"authLoginCustomMessage": "hello", "podcastEpisodeSchedule": "1 1 1 1 1", "id": "other",
					"loggerDailyLogsToKeep": 99, "authOpenIDClientSecret": "s", "unknownKey": 1,
					"chromecastEnabled": false,
				})
				if got["chromecastEnabled"] != false {
					t.Fatalf("chromecastEnabled not applied: %v", got["chromecastEnabled"])
				}
				onlyChanged(t, "settings row", before, storedSettings(t, e), map[string]any{"chromecastEnabled": false})
				wantMovedForward(t, "settings row", stamp, settingsUpdatedAt(t, e))
				wantKeys(t, "response", got, map[string]any{
					"version": "2.37.2", "rateLimitLoginRequests": 10, "sortingPrefixes": []string{"the", "a"},
					"authActiveAuthMethods": []string{"local"}, "metadataFileFormat": "json", "id": "server-settings",
				})
			}),
		scenario(req, "INV-ROUTE-R205-ROUTE", "normal", "changed", "settings-change-visible-to-other-users",
			"After PATCH /api/settings, another user's POST /api/authorize and the public GET /status show the new values.",
			[]string{settingsHandler, "server/Auth.js:95-104", "server/Server.js:384-399"},
			func(t *testing.T, e *harness.Env) {
				settingsPatch(t, e, map[string]any{"language": "fr", "dateFormat": "dd.MM.yyyy"})
				seen := authorizeSettings(t, e, e.Token(t, "listener"))
				wantKeys(t, "listener authorize", seen, map[string]any{"language": "fr", "dateFormat": "dd.MM.yyyy"})
				wantNoSecrets(t, "listener authorize serverSettings", seen)
				if language, _, _ := readStatus(t, e); language != "fr" {
					t.Fatalf("status language = %q, want fr", language)
				}
			}),
		scenario(req, "INV-ROUTE-R205-SIDEEFFECT-001", "edge", "changed", "settings-allowed-origins-drive-cors",
			"allowedOrigins controls Access-Control-Allow-Origin: a listed origin is echoed with an OPTIONS 200, another origin and an emptied list get no CORS headers.",
			[]string{settingsHandler, "server/Server.js:283-294"},
			func(t *testing.T, e *harness.Env) {
				preflight := func(origin string) harness.Response {
					return e.Do(t, harness.Request{Method: http.MethodOptions, Path: "/api/me", Header: map[string]string{"Origin": origin}})
				}
				settingsPatch(t, e, map[string]any{"allowedOrigins": []string{"http://example.test"}})
				listed := preflight("http://example.test")
				if listed.Status != http.StatusOK || listed.Header.Get("Access-Control-Allow-Origin") != "http://example.test" || listed.Header.Get("Access-Control-Allow-Credentials") != "true" {
					t.Fatalf("listed origin must be allowed: %d %v", listed.Status, listed.Header)
				}
				if other := preflight("http://other.test"); other.Header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("unlisted origin got CORS headers: %v", other.Header)
				}
				settingsPatch(t, e, map[string]any{"allowedOrigins": []string{}})
				if cleared := preflight("http://example.test"); cleared.Header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("emptied list still allows the origin: %v", cleared.Header)
				}
			}),
		scenario(req, "INV-ROUTE-R205-SIDEEFFECT-001", "edge", "changed", "settings-allow-iframe-drives-frame-ancestors",
			"allowIframe false makes every response carry Content-Security-Policy frame-ancestors 'self'; true removes it.",
			[]string{settingsHandler, "server/Server.js:262-266"},
			func(t *testing.T, e *harness.Env) {
				settingsPatch(t, e, map[string]any{"allowIframe": false})
				if got := e.Get(t, "", "/ping").Header.Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
					t.Fatalf("CSP with allowIframe=false = %q", got)
				}
				settingsPatch(t, e, map[string]any{"allowIframe": true})
				if got := e.Get(t, "", "/ping").Header.Get("Content-Security-Policy"); got != "" {
					t.Fatalf("CSP with allowIframe=true = %q, want none", got)
				}
			}),
		scenario(req, "INV-ROUTE-R205-SIDEEFFECT-001", "edge", "changed", "settings-sorting-ignore-prefix-orders-library",
			"sortingIgnorePrefix switches the library title sort between title and titleIgnorePrefix.",
			[]string{settingsHandler, "server/utils/queries/libraryItemsBookFilters.js:254-262"},
			func(t *testing.T, e *harness.Env) {
				ids := e.IDs
				settingsPatch(t, e, map[string]any{"sortingIgnorePrefix": true})
				wantOrder(t, e, []string{ids["saga1"], ids["ebook"], ids["disposable"], ids["saga2"], ids["novel"]})
				settingsPatch(t, e, map[string]any{"sortingIgnorePrefix": false})
				wantOrder(t, e, []string{ids["ebook"], ids["disposable"], ids["novel"], ids["saga1"], ids["saga2"]})
			}),
		scenario(req, "INV-ROUTE-R205-SIDEEFFECT-001", "edge", "changed", "settings-backup-schedule-starts-and-stops-backups",
			"Setting backupSchedule starts the scheduler (backups appear and are recreated after deletion) and setting it to false stops it.",
			[]string{settingsHandler, "server/managers/BackupManager.js:73-95"},
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				backups := func() []string {
					var ids []string
					for _, item := range e.Get(t, root, "/api/backups").Object(t)["backups"].([]any) {
						ids = append(ids, harness.Dig(item, "id").(string))
					}
					return ids
				}
				clear := func() {
					for _, id := range backups() {
						wantStatus(t, "delete backup", e.Delete(t, root, "/api/backups/"+id), http.StatusOK)
					}
				}
				clear()
				got := settingsPatch(t, e, map[string]any{"backupSchedule": "* * * * * *"})
				if got["backupSchedule"] != "* * * * * *" || storedSettings(t, e)["backupSchedule"] != "* * * * * *" {
					t.Fatal("backupSchedule not stored")
				}
				harness.Eventually(t, 15*time.Second, "scheduled backup", func() bool { return len(backups()) > 0 })
				clear()
				harness.Eventually(t, 15*time.Second, "scheduled backup recreated", func() bool { return len(backups()) > 0 })
				if got := settingsPatch(t, e, map[string]any{"backupSchedule": false}); got["backupSchedule"] != false {
					t.Fatalf("backupSchedule = %v, want false", got["backupSchedule"])
				}
				if storedSettings(t, e)["backupSchedule"] != false {
					t.Fatal("backupSchedule false not stored")
				}
				clear()
				never(t, 3*time.Second, "backup created after the schedule was disabled", func() bool { return len(backups()) > 0 })
			}),
	}
}

// libraryOrder returns the book library item ids sorted by title.
func libraryOrder(t *testing.T, e *harness.Env) []string {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/libraries/" + e.IDs["library"] + "/items", Token: e.Token(t, "root"),
		Query: map[string][]string{"sort": {"media.metadata.title"}}})
	wantStatus(t, "library items", resp, http.StatusOK)
	var ids []string
	for _, item := range resp.Object(t)["results"].([]any) {
		ids = append(ids, harness.Dig(item, "id").(string))
	}
	return ids
}

func wantOrder(t *testing.T, e *harness.Env, want []string) {
	t.Helper()
	if got := libraryOrder(t, e); !sameJSON(got, want) {
		t.Fatalf("library title order = %v, want %v", got, want)
	}
}

func secretsScenario() harness.Scenario {
	return scenario("route-r205", "INV-ROUTE-R205-SECRETS-001", "edge", "changed", "settings-response-never-includes-secrets",
		"Once OpenID secrets and the token secret are stored, no PATCH /api/settings, authorize, login, status or socket init payload contains them.",
		[]string{settingsHandler, "server/objects/settings/ServerSettings.js:299-308", "server/Auth.js:95-104"},
		func(t *testing.T, e *harness.Env) {
			row := storedSettings(t, e)
			for _, key := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret"} {
				if row[key] == nil || row[key] == "" {
					t.Fatalf("precondition: row must hold %s", key)
				}
			}
			wantNoSecrets(t, "PATCH /api/settings response", settingsPatch(t, e, map[string]any{}))
			wantNoSecrets(t, "authorize serverSettings", authorizeSettings(t, e, e.Token(t, "root")))
			for user, password := range map[string]string{"trial-listener": "trial-listener-password"} {
				resp := e.Post(t, "", "/login", map[string]string{"username": user, "password": password})
				wantStatus(t, "login "+user, resp, http.StatusOK)
				login, ok := resp.Object(t)["serverSettings"].(map[string]any)
				if !ok {
					t.Fatalf("login %s has no serverSettings: %.200s", user, resp.Body)
				}
				wantNoSecrets(t, "login serverSettings of "+user, login)
			}
			socket := e.Socket(t, e.Token(t, "root"))
			defer socket.Close()
			literals := []string{row["tokenSecret"].(string), row["authOpenIDClientSecret"].(string), row["authOpenIDClientID"].(string)}
			bodies := [][]byte{e.Get(t, "", "/status").Body}
			for _, event := range socket.Events() {
				for _, arg := range event.Args {
					bodies = append(bodies, arg)
				}
			}
			for _, body := range bodies {
				for _, literal := range literals {
					if strings.Contains(string(body), literal) {
						t.Fatalf("a public or socket payload contains a stored secret: %.200s", body)
					}
				}
			}
		})
}
