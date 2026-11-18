package notify

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	triggerRoute     = "route-r165"
	recordTestRoute  = "route-r169"
	triggerShape     = "INV-ROUTE-R165-ROUTE"
	triggerRepeat    = "INV-ROUTE-R165-REPEAT"
	triggerAuth      = "INV-ROUTE-R165-AUTH-001"
	recordTestShape  = "INV-ROUTE-R169-ROUTE"
	recordTestAuth   = "INV-ROUTE-R169-AUTH-001"
	triggerSrc       = "server/controllers/NotificationController.js:67-70"
	triggerManager   = "server/managers/NotificationManager.js:151-178"
	sendTestSrc      = "server/controllers/NotificationController.js:122-128"
	sendNotification = "server/managers/NotificationManager.js:222-235"
	recordTestRouter = "server/routers/ApiRouter.js:268,272"
)

// triggerScenarios covers GET /api/notifications/test, the settings page's
// "fire test event" action.
func triggerScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(triggerRoute, triggerShape, "normal", "initial", "notifications-test-delivers-to-enabled-subscription",
			"GET /api/notifications/test answers 200 and posts one Apprise payload for the enabled onTest subscription with the template rendered from the event test data (the running version), then counts one firing with no failed attempt in the settings document and its row.",
			[]string{recordTestRouter, triggerSrc, triggerManager, sendNotification, "client/components/cards/NotificationCard.vue:61-81"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				before := time.Now()
				urls := []string{"mailto://fire@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": urls,
					"titleTemplate": "Release {{version}}", "bodyTemplate": "Body {{version}}", "enabled": true, "type": nil,
				})
				resp, _ := fireTriggerResponse(t, e, e.Socket(t, e.Token(t, "root")), "0")
				if string(resp.Body) != "OK" {
					t.Fatalf("body = %q, want OK", resp.Body)
				}
				version := "v" + serverVersion(t, e)
				call := stub.requireExactly(t, 1)[0]
				call.requireDelivery(t, urls, "Release "+version, "Body "+version)
				fired := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if !fired.Enabled || fired.NumTimesFired != 1 || fired.NumConsecutiveFailedAttempts != 0 || fired.LastAttemptFailed {
					t.Fatalf("record after the delivery = %+v", fired)
				}
				requireTimestamp(t, "lastFiredAt", fired.LastFiredAt, before)
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(getNotifications(t, e).Settings))
			}),
		scenario(triggerRoute, triggerRepeat, "normal", "repeat", "notifications-test-repeats-increment-count",
			"Firing GET /api/notifications/test twice delivers the same rendered payload twice and increments numTimesFired to 2 while the consecutive failure count stays 0.",
			[]string{triggerSrc, triggerManager},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				urls := []string{"mailto://repeat@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": urls,
					"titleTemplate": "Repeat", "bodyTemplate": "Repeat body", "enabled": true, "type": nil,
				})
				fireTriggerSynced(t, e, "0")
				fireTriggerSynced(t, e, "0")
				calls := stub.requireExactly(t, 2)
				for i, call := range calls {
					call.requireDelivery(t, urls, "Repeat", "Repeat body")
					if i > 0 && !call.At.After(calls[i-1].At) {
						t.Fatalf("delivery %d at %s is not after %s", i, call.At, calls[i-1].At)
					}
				}
				fired := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if fired.NumTimesFired != 2 || fired.NumConsecutiveFailedAttempts != 0 {
					t.Fatalf("counters after two deliveries = %+v", fired)
				}
			}),
		scenario(triggerRoute, triggerShape, "normal", "changed", "notifications-test-uses-the-updated-template",
			"After PATCH /api/notifications/:id changes a subscription's title and urls, GET /api/notifications/test delivers the new rendered title to the new urls.",
			[]string{triggerSrc, triggerManager, updateRecSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://old@example.test"},
					"titleTemplate": "Old {{version}}", "bodyTemplate": "Old body", "enabled": true, "type": nil,
				})
				fireTriggerSynced(t, e, "0")
				stub.requireExactly(t, 1)
				updated := []string{"mailto://new@example.test"}
				if resp, _ := patchRecord(t, e, record.ID, map[string]any{
					"id": record.ID, "urls": updated, "titleTemplate": "New {{version}}", "bodyTemplate": "New body",
				}); resp.Status != http.StatusOK {
					t.Fatalf("PATCH status %d: %.300s", resp.Status, resp.Body)
				}
				fireTriggerSynced(t, e, "0")
				calls := stub.requireExactly(t, 2)
				calls[1].requireDelivery(t, updated, "New v"+serverVersion(t, e), "New body")
			}),
		scenario(triggerRoute, triggerShape, "edge", "initial", "notifications-test-without-subscribers-sends-nothing",
			"With an Apprise URL configured but no subscriptions, GET /api/notifications/test answers 200, delivers nothing and leaves the settings document unchanged.",
			[]string{triggerSrc, triggerManager, "server/objects/settings/NotificationSettings.js:49-59"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				stamp := takeSettingsSnapshot(t, e)
				fireTriggerSynced(t, e, "0")
				stub.requireNoDelivery(t, "a trigger without subscribers")
				requireUnchangedValue(t, e, stamp.document, "a trigger without subscribers")
			}),
		scenario(triggerRoute, triggerShape, "edge", "force", "notifications-test-fail-override-counts-a-failure",
			"GET /api/notifications/test?fail=1 skips the Apprise request, records one failed attempt with lastFiredAt set and keeps the subscription enabled while the limit is not reached.",
			[]string{triggerSrc, triggerManager, "client/components/cards/NotificationCard.vue:63-81"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 3, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				awaitTriggerSlot()
				fireTriggerSynced(t, e, "1")
				stub.requireNoDelivery(t, "a deliberately failing trigger")
				settings := getNotifications(t, e).Settings
				failed := requireRecord(t, settings, record.ID)
				if !failed.Enabled || failed.NumConsecutiveFailedAttempts != 1 || !failed.LastAttemptFailed || failed.LastFiredAt == nil || failed.NumTimesFired != 1 {
					t.Fatalf("record after the forced failure = %+v", failed)
				}
				persisted := requireRecord(t, storedDocument(t, e), record.ID)
				if persisted.NumConsecutiveFailedAttempts != 1 || !persisted.LastAttemptFailed || persisted.LastFiredAt == nil {
					t.Fatalf("persisted record = %+v", persisted)
				}
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(settings))
			}),
		scenario(triggerRoute, triggerAuth, "error", "failure", "notifications-test-rejections-change-nothing",
			"GET /api/notifications/test answers 401 without a token and 403 for an authenticated non-admin, delivering nothing and leaving the settings document unchanged.",
			[]string{recordTestRouter, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				requireAdminGate(t, e, http.MethodGet, "/api/notifications/test", nil)
				stub.requireNoDelivery(t, "a rejected trigger")
				stamp.requireUnchanged(t, e, "rejected trigger requests")
			}),
	}
}

// recordTestScenarios covers GET /api/notifications/:id/test, the per-record
// "test" button of the settings page.
func recordTestScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(recordTestRoute, recordTestShape, "normal", "stateless", "notification-test-delivers-event-test-data",
			"GET /api/notifications/:id/test answers 200 and posts one Apprise payload rendered from the event's test data, without changing the record counters or writing the settings row.",
			[]string{recordTestRouter, sendTestSrc, sendNotification, "server/utils/notifications.js:40-58"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				urls := []string{"mailto://record@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onBackupCompleted", "urls": urls,
					"titleTemplate": "Backup {{backupPath}}", "bodyTemplate": "{{backupSize}} at {{completionTime}}", "enabled": true, "type": nil,
				})
				stamp := takeSettingsSnapshot(t, e)
				resp := e.Get(t, e.Token(t, "root"), "/api/notifications/"+record.ID+"/test")
				wantStatus(t, "GET /api/notifications/:id/test", resp, http.StatusOK)
				if string(resp.Body) != "OK" {
					t.Fatalf("body = %q, want OK", resp.Body)
				}
				call := stub.requireExactly(t, 1)[0]
				call.requireDelivery(t, urls, "Backup path/to/backup", "1.23 MB at 12:00 AM")
				untouched := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if untouched.NumTimesFired != 0 || untouched.LastFiredAt != nil || untouched.LastAttemptFailed {
					t.Fatalf("the record test changed the counters: %+v", untouched)
				}
				stamp.requireUnchanged(t, e, "a per-record test delivery")
			}),
		scenario(recordTestRoute, recordTestAuth, "error", "stateless", "notification-test-errors",
			"GET /api/notifications/:id/test answers 401 without a token, 403 for an authenticated non-admin, 404 for an unknown id and 400 with the text Apprise is not configured when no Apprise URL is set.",
			[]string{recordTestRouter, sendTestSrc, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onBackupFailed", true))
				requireAdminGate(t, e, http.MethodGet, "/api/notifications/"+record.ID+"/test", nil)
				wantStatus(t, "unknown id", e.Get(t, e.Token(t, "root"), "/api/notifications/2f1c3d4e-0000-4000-8000-000000000000/test"), http.StatusNotFound)
				useApprise(t, e, "", 5, 20)
				unconfigured := e.Get(t, e.Token(t, "root"), "/api/notifications/"+record.ID+"/test")
				wantStatus(t, "unconfigured Apprise URL", unconfigured, http.StatusBadRequest)
				if string(unconfigured.Body) != "Apprise is not configured" {
					t.Fatalf("body = %q, want the client-visible Apprise message", unconfigured.Body)
				}
				stub.requireNoDelivery(t, "rejected record tests")
			}),
		scenario(recordTestRoute, recordTestShape, "edge", "stateless", "notification-test-unreachable-apprise-is-500",
			"GET /api/notifications/:id/test answers 500 when the Apprise endpoint rejects the payload with a 400, when the record names an event the catalogue does not know and when the endpoint does not answer within the six second timeout.",
			[]string{sendTestSrc, sendNotification},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onBackupFailed", true))
				stub.respond(http.StatusBadRequest, 0)
				wantStatus(t, "Apprise rejection", e.Get(t, e.Token(t, "root"), "/api/notifications/"+record.ID+"/test"), http.StatusInternalServerError)
				stub.requireExactly(t, 1)
				_, unknown := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onUnknownEvent", "urls": []string{"mailto://unknown@example.test"},
					"titleTemplate": "", "bodyTemplate": "", "enabled": true, "type": nil,
				})
				stub.respond(http.StatusOK, 0)
				wantStatus(t, "unknown event name", e.Get(t, e.Token(t, "root"), "/api/notifications/"+unknown.ID+"/test"), http.StatusInternalServerError)
				if got := stub.Count(); got != 1 {
					t.Fatalf("a record with an unknown event name posted %d payloads, want none", got-1)
				}
				stub.respond(http.StatusOK, 7*time.Second)
				started := time.Now()
				wantStatus(t, "Apprise timeout", e.Get(t, e.Token(t, "root"), "/api/notifications/"+record.ID+"/test"), http.StatusInternalServerError)
				if elapsed := time.Since(started); elapsed < 5*time.Second {
					t.Fatalf("the timeout answered after %s, want about the six second client timeout", elapsed)
				}
				stub.requireExactly(t, 2)
			}),
	}
}
