package notify

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	settingsRoute   = "route-r162"
	settingsData    = "route-r164"
	settingsPatch   = "route-r163"
	settingsShape   = "INV-ROUTE-R162-ROUTE"
	settingsAuth    = "INV-ROUTE-R162-AUTH-001"
	dataShape       = "INV-ROUTE-R164-ROUTE"
	dataAuth        = "INV-ROUTE-R164-AUTH-001"
	patchShape      = "INV-ROUTE-R163-ROUTE"
	patchRepeat     = "INV-ROUTE-R163-REPEAT"
	patchAuth       = "INV-ROUTE-R163-AUTH-001"
	settingsSource  = "server/controllers/NotificationController.js:25-42"
	settingsRouter  = "server/routers/ApiRouter.js:265-267"
	settingsModel   = "server/objects/settings/NotificationSettings.js:21-31"
	settingsDocSrc  = "server/utils/notifications.js:3-108"
	settingsDBWrite = "server/Database.js:440-449"
)

// settingsReadScenarios covers GET /api/notifications: the default document of
// an untouched install, the admin gate and a read that reflects stored state.
func settingsReadScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(settingsRoute, settingsShape, "normal", "stateless", "notifications-get-defaults",
			"GET /api/notifications answers 200 with the untouched settings document (id notification-settings, appriseType api, null appriseApiUrl, no notifications, 5 failed attempts, queue 20, delay 1000) and the six-event catalogue whose onTest test data carries the running server version.",
			[]string{settingsRouter, settingsSource, settingsModel, settingsDocSrc},
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/notifications")
				wantStatus(t, "GET /api/notifications", resp, http.StatusOK)
				body := resp.Object(t)
				requireJSONEqual(t, "default settings", body["settings"], defaultSettingsDocument())
				requireJSONEqual(t, "event catalogue", body["data"], notificationDataDocument(serverVersion(t, e)))
			}),
		scenario(settingsRoute, settingsAuth, "error", "stateless", "notifications-get-rejects-non-admin",
			"GET /api/notifications answers 401 without a token and 403 for an authenticated non-admin, and neither request creates the settings row.",
			[]string{settingsRouter, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				requireAdminGate(t, e, http.MethodGet, "/api/notifications", nil)
				if rows := e.Count(t, "SELECT count(*) FROM settings WHERE key = 'notification-settings'"); rows != 0 {
					t.Fatalf("rejected reads wrote %d notification-settings rows", rows)
				}
			}),
		scenario(settingsRoute, settingsShape, "edge", "stateless", "notifications-get-reflects-stored-settings",
			"After a settings write and a created notification, GET /api/notifications returns exactly the stored document with that record, and an unknown query parameter does not change the answer.",
			[]string{settingsRouter, settingsSource, settingsModel},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				_, record := createRecord(t, e, recordBody("onTest", true))
				expected := defaultSettingsDocument()
				expected["appriseApiUrl"] = stub.URL("/notify")
				expected["notifications"] = []any{recordDocument(record)}
				resp := e.Do(t, harness.Request{Path: "/api/notifications", Query: map[string][]string{"include": {"settings"}}, Token: e.Token(t, "root")})
				wantStatus(t, "GET /api/notifications with a query", resp, http.StatusOK)
				body := resp.Object(t)
				requireJSONEqual(t, "settings after a write", body["settings"], expected)
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), expected)
			}),
	}
}

// dataScenarios covers the deprecated GET /api/notificationdata alias.
func dataScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(settingsData, dataShape, "normal", "stateless", "notificationdata-returns-event-catalogue",
			"GET /api/notificationdata answers 200 with the bare event catalogue object that GET /api/notifications nests under data.",
			[]string{"server/routers/ApiRouter.js:267", "server/controllers/NotificationController.js:44-56", settingsDocSrc},
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/notificationdata")
				wantStatus(t, "GET /api/notificationdata", resp, http.StatusOK)
				requireJSONEqual(t, "notificationdata body", resp.Object(t), notificationDataDocument(serverVersion(t, e)))
				notifications := e.Get(t, e.Token(t, "root"), "/api/notifications")
				wantStatus(t, "GET /api/notifications", notifications, http.StatusOK)
				requireJSONEqual(t, "notificationdata matches notifications.data", resp.Object(t), notifications.Object(t)["data"])
			}),
		scenario(settingsData, dataAuth, "error", "stateless", "notificationdata-rejects-non-admin",
			"GET /api/notificationdata answers 401 without a token and 403 for an authenticated non-admin.",
			[]string{"server/routers/ApiRouter.js:267", "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				requireAdminGate(t, e, http.MethodGet, "/api/notificationdata", nil)
			}),
		scenario(settingsData, dataShape, "edge", "stateless", "notificationdata-ignores-notification-settings",
			"GET /api/notificationdata returns only the event catalogue and never the settings object, also after the notification settings were written.",
			[]string{"server/controllers/NotificationController.js:44-56"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 4, 9)
				resp := e.Get(t, e.Token(t, "root"), "/api/notificationdata")
				wantStatus(t, "GET /api/notificationdata", resp, http.StatusOK)
				body := resp.Object(t)
				if _, present := body["settings"]; present {
					t.Fatalf("notificationdata carries a settings object: %.300s", resp.Body)
				}
				requireJSONEqual(t, "notificationdata body", body, notificationDataDocument(serverVersion(t, e)))
			}),
	}
}

// settingsWriteScenarios covers PATCH /api/notifications: the stored document,
// the no-op repeat, persistence across a restart, clearing the Apprise URL and
// the rejected request.
func settingsWriteScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(settingsPatch, patchShape, "normal", "initial", "notifications-settings-patch-persists",
			"PATCH /api/notifications with the settings page payload answers 200 and the text OK, stores appriseApiUrl, maxNotificationQueue and maxFailedAttempts in the notification-settings row and leaves notificationDelay at 1000.",
			[]string{settingsRouter, "server/controllers/NotificationController.js:38-44", settingsModel, settingsDBWrite},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				resp := e.Patch(t, e.Token(t, "root"), "/api/notifications", map[string]any{
					"appriseApiUrl": stub.URL("/notify"), "maxNotificationQueue": 7, "maxFailedAttempts": 3,
				})
				wantStatus(t, "PATCH /api/notifications", resp, http.StatusOK)
				if string(resp.Body) != "OK" {
					t.Fatalf("PATCH /api/notifications body = %q, want OK", resp.Body)
				}
				settings := getNotifications(t, e).Settings
				if settings.AppriseAPIURL == nil || *settings.AppriseAPIURL != stub.URL("/notify") {
					t.Fatalf("appriseApiUrl = %v, want %q", settings.AppriseAPIURL, stub.URL("/notify"))
				}
				if settings.MaxNotificationQueue != 7 || settings.MaxFailedAttempts != 3 || settings.NotificationDelay != 1000 {
					t.Fatalf("stored limits = queue %d attempts %d delay %d", settings.MaxNotificationQueue, settings.MaxFailedAttempts, settings.NotificationDelay)
				}
				value, updatedAt, exists := storedSettings(t, e)
				if !exists || updatedAt == "" {
					t.Fatalf("notification-settings row exists=%v updatedAt=%q", exists, updatedAt)
				}
				requireJSONEqual(t, "persisted settings", value, settingsObject(t, e))
			}),
		scenario(settingsPatch, patchRepeat, "normal", "repeat", "notifications-settings-patch-repeat-writes-nothing",
			"Repeating the identical PATCH /api/notifications payload answers 200 again and leaves the notification-settings row value and updatedAt unchanged, because NotificationSettings.update reports no change.",
			[]string{"server/controllers/NotificationController.js:38-44", settingsModel},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 3, 8)
				before, beforeUpdatedAt, exists := storedSettings(t, e)
				if !exists || beforeUpdatedAt == "" {
					t.Fatalf("precondition: the settings row exists=%v", exists)
				}
				resp := e.Patch(t, e.Token(t, "root"), "/api/notifications", map[string]any{
					"appriseApiUrl": stub.URL("/notify"), "maxNotificationQueue": 8, "maxFailedAttempts": 3,
				})
				wantStatus(t, "repeated PATCH /api/notifications", resp, http.StatusOK)
				after, afterUpdatedAt, _ := storedSettings(t, e)
				requireJSONEqual(t, "persisted settings after the repeat", after, before)
				if afterUpdatedAt != beforeUpdatedAt {
					t.Fatalf("repeated PATCH rewrote the settings row (updatedAt %q, want %q)", afterUpdatedAt, beforeUpdatedAt)
				}
			}),
		scenario(settingsPatch, patchShape, "normal", "changed", "notifications-settings-patch-survives-a-restart",
			"After PATCH /api/notifications changes the queue size and the failed-attempt limit, a restarted server serves and persists the new values together with the earlier Apprise URL.",
			[]string{"server/controllers/NotificationController.js:38-44", settingsModel, settingsDBWrite},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 4, 9)
				// Records are cleared so the comparison covers the settings values:
				// Notification.construct rewrites a stored null type to "info" when
				// the server reloads it, which the untouched row does not show.
				clearRecords(t, e)
				settings := getNotifications(t, e).Settings
				if settings.MaxNotificationQueue != 9 || settings.MaxFailedAttempts != 4 {
					t.Fatalf("precondition: queue %d attempts %d", settings.MaxNotificationQueue, settings.MaxFailedAttempts)
				}
				e.Restart(t)
				reopened := getNotifications(t, e).Settings
				if reopened.AppriseAPIURL == nil || *reopened.AppriseAPIURL != stub.URL("/notify") {
					t.Fatalf("after restart appriseApiUrl = %v", reopened.AppriseAPIURL)
				}
				if reopened.MaxNotificationQueue != 9 || reopened.MaxFailedAttempts != 4 || reopened.NotificationDelay != 1000 {
					t.Fatalf("after restart limits = queue %d attempts %d delay %d", reopened.MaxNotificationQueue, reopened.MaxFailedAttempts, reopened.NotificationDelay)
				}
				value, _, exists := storedSettings(t, e)
				if !exists {
					t.Fatal("no notification-settings row after the restart")
				}
				requireJSONEqual(t, "persisted settings after the restart", value, settingsDocumentOf(reopened))
			}),
		scenario(settingsPatch, patchShape, "edge", "initial", "notifications-settings-patch-clears-the-apprise-url",
			"PATCH /api/notifications with appriseApiUrl null clears the configured URL, after which GET /api/notifications/test answers 200 without delivering anything and without writing the settings row.",
			[]string{"server/controllers/NotificationController.js:38-44", "server/managers/NotificationManager.js:151-153", "client/pages/config/notifications.vue:122-131"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 4, 9)
				resp := e.Patch(t, e.Token(t, "root"), "/api/notifications", map[string]any{
					"appriseApiUrl": nil, "maxNotificationQueue": 9, "maxFailedAttempts": 4,
				})
				wantStatus(t, "PATCH /api/notifications with a null URL", resp, http.StatusOK)
				settings := getNotifications(t, e).Settings
				if settings.AppriseAPIURL != nil {
					t.Fatalf("appriseApiUrl = %q, want null", *settings.AppriseAPIURL)
				}
				stamp := takeSettingsSnapshot(t, e)
				wantStatus(t, "GET /api/notifications/test without an Apprise URL", triggerTest(t, e, "0"), http.StatusOK)
				stub.requireNoDelivery(t, "an unusable notification system")
				stamp.requireUnchanged(t, e, "a trigger without an Apprise URL")
			}),
		scenario(settingsPatch, patchAuth, "error", "failure", "notifications-settings-patch-rejections-change-nothing",
			"PATCH /api/notifications answers 401 without a token, 403 for an authenticated non-admin and 400 for a body that is not JSON, and none of the rejected requests changes the settings document or its row.",
			[]string{settingsRouter, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 2, 6)
				stamp := takeSettingsSnapshot(t, e)
				requireAdminGate(t, e, http.MethodPatch, "/api/notifications", map[string]any{"maxFailedAttempts": 1})
				malformed := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/notifications", Token: e.Token(t, "root"), JSON: "{"})
				wantStatus(t, "PATCH /api/notifications with a malformed body", malformed, http.StatusBadRequest)
				stamp.requireUnchanged(t, e, "rejected settings writes")
			}),
	}
}

// mustStoredValue returns the persisted settings value or fails.
func mustStoredValue(t *testing.T, e *harness.Env) map[string]any {
	t.Helper()
	value, _, exists := storedSettings(t, e)
	if !exists {
		t.Fatal("no notification-settings row was written")
	}
	return value
}
