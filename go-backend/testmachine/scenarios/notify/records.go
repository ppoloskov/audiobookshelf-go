package notify

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	createRoute    = "route-r166"
	deleteRoute    = "route-r167"
	updateRoute    = "route-r168"
	createShape    = "INV-ROUTE-R166-ROUTE"
	createRepeat   = "INV-ROUTE-R166-REPEAT"
	createAuth     = "INV-ROUTE-R166-AUTH-001"
	deleteShape    = "INV-ROUTE-R167-ROUTE"
	deleteRepeat   = "INV-ROUTE-R167-REPEAT"
	deleteAuth     = "INV-ROUTE-R167-AUTH-001"
	updateShape    = "INV-ROUTE-R168-ROUTE"
	updateRepeat   = "INV-ROUTE-R168-REPEAT"
	updateAuth     = "INV-ROUTE-R168-AUTH-001"
	createSrc      = "server/controllers/NotificationController.js:78-90"
	deleteSrc      = "server/controllers/NotificationController.js:93-103"
	updateRecSrc   = "server/controllers/NotificationController.js:106-112"
	recordModelSrc = "server/objects/Notification.js:11-79"
	recordPatchSrc = "server/objects/settings/NotificationSettings.js:107-116"
	routerRecords  = "server/routers/ApiRouter.js:269-271"
)

// createScenarios covers POST /api/notifications.
func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(createRoute, createShape, "normal", "initial", "notifications-create-stores-the-record",
			"POST /api/notifications with the edit modal payload answers 200 with the whole settings object holding one new record whose id is a v4 UUID and whose libraryId, type and lastFiredAt are null, enabled true, both counters 0, a millisecond createdAt and the posted urls and templates.",
			[]string{routerRecords, createSrc, recordModelSrc, "client/components/modals/notification/NotificationEditModal.vue:146-162"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				before := time.Now()
				resp, record := createRecord(t, e, map[string]any{
					"libraryId":     nil,
					"eventName":     "onBackupCompleted",
					"urls":          []string{"mailto://one@example.test"},
					"titleTemplate": "Backup {{backupPath}}",
					"bodyTemplate":  "Size {{backupSize}}",
					"enabled":       true,
					"type":          nil,
				})
				requireCreatedRecord(t, record, before, "onBackupCompleted", []string{"mailto://one@example.test"}, true)
				if record.TitleTemplate != "Backup {{backupPath}}" || record.BodyTemplate != "Size {{backupSize}}" {
					t.Fatalf("stored templates = %q / %q", record.TitleTemplate, record.BodyTemplate)
				}
				if record.LibraryID != nil {
					t.Fatalf("libraryId = %q, want null", *record.LibraryID)
				}
				if record.Type != nil {
					t.Fatalf("type = %q, want null", *record.Type)
				}
				if record.LastFiredAt != nil || record.LastAttemptFailed || record.NumConsecutiveFailedAttempts != 0 || record.NumTimesFired != 0 {
					t.Fatalf("new record delivery state = %+v", record)
				}
				expected := defaultSettingsDocument()
				expected["appriseApiUrl"] = stub.URL("/notify")
				expected["notifications"] = []any{recordDocument(record)}
				requireJSONEqual(t, "POST response settings", resp.Object(t), expected)
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), expected)
				requireJSONEqual(t, "GET after the create", settingsObject(t, e), expected)
			}),
		scenario(createRoute, createRepeat, "normal", "repeat", "notifications-create-twice-yields-two-records",
			"Posting the same payload twice stores two records with different v4 UUIDs in creation order instead of replacing or ignoring the duplicate.",
			[]string{createSrc, "server/objects/settings/NotificationSettings.js:97-105"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				body := recordBody("onBackupFailed", true)
				_, first := createRecord(t, e, body)
				_, second := createRecord(t, e, body)
				if first.ID == second.ID {
					t.Fatalf("both creations share id %s", first.ID)
				}
				stored := getNotifications(t, e).Settings.Notifications
				if len(stored) != 2 || stored[0].ID != first.ID || stored[1].ID != second.ID {
					t.Fatalf("stored records = %+v, want the two creations in order", stored)
				}
				for _, record := range stored {
					if !uuidPattern.MatchString(record.ID) {
						t.Fatalf("id %q is not a v4 UUID", record.ID)
					}
					if record.EventName != "onBackupFailed" || !record.Enabled {
						t.Fatalf("stored record = %+v", record)
					}
				}
			}),
		scenario(createRoute, createShape, "normal", "changed", "notifications-create-survives-a-settings-write",
			"A created record keeps its id, createdAt, urls and templates when a later PATCH /api/notifications rewrites the settings document.",
			[]string{createSrc, settingsModel, settingsDBWrite},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://keep@example.test"},
					"titleTemplate": "Keep {{version}}", "bodyTemplate": "Body", "enabled": false, "type": "warning",
				})
				useApprise(t, e, stub.URL("/notify"), 2, 30)
				kept := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				requireJSONEqual(t, "record after the settings write", recordDocument(kept), recordDocument(record))
				settings := getNotifications(t, e).Settings
				if settings.MaxFailedAttempts != 2 || settings.MaxNotificationQueue != 30 {
					t.Fatalf("settings write did not apply: %+v", settings)
				}
			}),
		scenario(createRoute, createShape, "normal", "changed", "notifications-create-survives-a-restart",
			"A created record with an explicit type, urls and templates is served identically after the server restarts and is still listed in the persisted settings row.",
			[]string{createSrc, recordModelSrc, "server/objects/Notification.js:21-39", settingsDBWrite},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, map[string]any{
					"libraryId":     nil,
					"eventName":     "onRSSFeedDisabled",
					"urls":          []string{"mailto://reload@example.test"},
					"titleTemplate": "Disabled {{title}}",
					"bodyTemplate":  "{{numFailed}} attempts",
					"enabled":       true,
					"type":          "info",
				})
				e.Restart(t)
				reopened := getNotifications(t, e).Settings
				requireJSONEqual(t, "record after the restart", recordDocument(requireRecord(t, reopened, record.ID)), recordDocument(record))
				requireJSONEqual(t, "persisted settings after the restart", mustStoredValue(t, e), settingsDocumentOf(reopened))
			}),
		scenario(createRoute, createShape, "edge", "initial", "notifications-create-edge-values",
			"POST /api/notifications stores a disabled record with two urls, the explicit type info and a library id, keeping enabled false and the typed values exactly.",
			[]string{createSrc, recordModelSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				before := time.Now()
				urls := []string{"mailto://first@example.test", "json://127.0.0.1:1/notify"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId":     e.IDs["podcastLibrary"],
					"eventName":     "onRSSFeedFailed",
					"urls":          urls,
					"titleTemplate": "",
					"bodyTemplate":  "",
					"enabled":       false,
					"type":          "info",
				})
				requireCreatedRecord(t, record, before, "onRSSFeedFailed", urls, false)
				if record.LibraryID == nil || *record.LibraryID != e.IDs["podcastLibrary"] {
					t.Fatalf("libraryId = %v, want %q", record.LibraryID, e.IDs["podcastLibrary"])
				}
				if record.Type == nil || *record.Type != "info" {
					t.Fatalf("type = %v, want info", record.Type)
				}
				if record.TitleTemplate != "" || record.BodyTemplate != "" {
					t.Fatalf("empty templates stored as %q / %q", record.TitleTemplate, record.BodyTemplate)
				}
			}),
		scenario(createRoute, createAuth, "error", "failure", "notifications-create-rejections-change-nothing",
			"POST /api/notifications answers 401 without a token and 403 for an authenticated non-admin; a body without eventName and a body with an empty urls list answer 200 without storing anything, and none of these requests changes the settings document.",
			[]string{routerRecords, createSrc, "server/objects/settings/NotificationSettings.js:97-105"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				stamp := takeSettingsSnapshot(t, e)
				requireAdminGate(t, e, http.MethodPost, "/api/notifications", recordBody("onTest", true))
				wantStatus(t, "POST without an event name", e.Post(t, e.Token(t, "root"), "/api/notifications", map[string]any{}), http.StatusOK)
				wantStatus(t, "POST with no urls", e.Post(t, e.Token(t, "root"), "/api/notifications", map[string]any{"eventName": "onTest", "urls": []string{}}), http.StatusOK)
				stamp.requireUnchanged(t, e, "rejected notification creations")
			}),
	}
}

// deleteScenarios covers DELETE /api/notifications/:id.
func deleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(deleteRoute, deleteShape, "normal", "initial", "notifications-delete-removes-the-record",
			"DELETE /api/notifications/:id answers 200 with the whole settings object without that record and the notification-settings row no longer lists it.",
			[]string{routerRecords, deleteSrc, "server/objects/settings/NotificationSettings.js:65-71"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, kept := createRecord(t, e, recordBody("onTest", true))
				_, removed := createRecord(t, e, recordBody("onBackupFailed", true))
				resp := e.Delete(t, e.Token(t, "root"), "/api/notifications/"+removed.ID)
				wantStatus(t, "DELETE /api/notifications/:id", resp, http.StatusOK)
				response := decode[settingsDocument](t, resp.Body)
				if recordIndex(response, removed.ID) >= 0 {
					t.Fatalf("response still lists the deleted record")
				}
				requireJSONEqual(t, "surviving record", recordDocument(requireRecord(t, response, kept.ID)), recordDocument(kept))
				if recordIndex(getNotifications(t, e).Settings, removed.ID) >= 0 {
					t.Fatalf("GET still lists the deleted record")
				}
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(response))
			}),
		scenario(deleteRoute, deleteRepeat, "normal", "repeat", "notifications-delete-unknown-is-404",
			"Deleting an already deleted id and an id that never existed answers 404 and leaves the settings document and its row unchanged.",
			[]string{routerRecords, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				wantStatus(t, "first delete", e.Delete(t, e.Token(t, "root"), "/api/notifications/"+record.ID), http.StatusOK)
				stamp := takeSettingsSnapshot(t, e)
				wantStatus(t, "second delete", e.Delete(t, e.Token(t, "root"), "/api/notifications/"+record.ID), http.StatusNotFound)
				wantStatus(t, "unknown id", e.Delete(t, e.Token(t, "root"), "/api/notifications/2f1c3d4e-0000-4000-8000-000000000000"), http.StatusNotFound)
				stamp.requireUnchanged(t, e, "deletes of absent notifications")
			}),
		scenario(deleteRoute, deleteShape, "normal", "changed", "notifications-delete-stops-delivery",
			"A deleted onTest subscription receives no delivery when GET /api/notifications/test fires afterwards, and the settings counters stay untouched.",
			[]string{deleteSrc, "server/managers/NotificationManager.js:151-178"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://gone@example.test"},
					"titleTemplate": "Fired {{version}}", "bodyTemplate": "Body", "enabled": true, "type": nil,
				})
				wantStatus(t, "delete the subscription", e.Delete(t, e.Token(t, "root"), "/api/notifications/"+record.ID), http.StatusOK)
				stamp := takeSettingsSnapshot(t, e)
				fireTriggerSynced(t, e, "0")
				stub.requireNoDelivery(t, "a deleted subscription")
				requireUnchangedValue(t, e, stamp.document, "a trigger after the subscription was deleted")
			}),
		scenario(deleteRoute, deleteShape, "edge", "initial", "notifications-delete-last-record-empties-the-list",
			"Deleting the last remaining record leaves notifications as an empty array in the response, the settings document and the persisted row.",
			[]string{deleteSrc, "server/objects/settings/NotificationSettings.js:65-71"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onBackupCompleted", true))
				resp := e.Delete(t, e.Token(t, "root"), "/api/notifications/"+record.ID)
				wantStatus(t, "delete the last record", resp, http.StatusOK)
				body := resp.Object(t)
				notifications, ok := body["notifications"].([]any)
				if !ok || len(notifications) != 0 {
					t.Fatalf("notifications = %v, want an empty array", body["notifications"])
				}
				requireJSONEqual(t, "GET after the last delete", settingsObject(t, e)["notifications"], []any{})
				requireJSONEqual(t, "persisted notifications", mustStoredValue(t, e)["notifications"], []any{})
			}),
		scenario(deleteRoute, deleteAuth, "error", "failure", "notifications-delete-rejections-change-nothing",
			"DELETE /api/notifications/:id answers 401 without a token, 403 for an authenticated non-admin (also for an unknown id, because the admin check runs before the id lookup) and 404 for an unknown id to an admin, and keeps the existing record.",
			[]string{routerRecords, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				requireAdminGate(t, e, http.MethodDelete, "/api/notifications/"+record.ID, nil)
				unknown := "/api/notifications/2f1c3d4e-0000-4000-8000-000000000000"
				wantStatus(t, "non-admin with an unknown id", e.Delete(t, e.Token(t, "listener"), unknown), http.StatusForbidden)
				wantStatus(t, "unknown id as admin", e.Delete(t, e.Token(t, "root"), unknown), http.StatusNotFound)
				stamp.requireUnchanged(t, e, "rejected deletes")
				requireRecord(t, getNotifications(t, e).Settings, record.ID)
			}),
	}
}

// updateRecordScenarios covers PATCH /api/notifications/:id.
func updateRecordScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(updateRoute, updateShape, "normal", "initial", "notifications-patch-updates-fields",
			"PATCH /api/notifications/:id with the record id and new urls and templates answers 200 with the whole settings object carrying those values while id, eventName, enabled and createdAt stay unchanged.",
			[]string{routerRecords, updateRecSrc, recordModelSrc, recordPatchSrc, "client/components/modals/notification/NotificationEditModal.vue:127-145"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				urls := []string{"mailto://updated@example.test", "json://127.0.0.1:1/updated"}
				resp, settings := patchRecord(t, e, record.ID, map[string]any{
					"id": record.ID, "urls": urls, "titleTemplate": "Updated {{version}}", "bodyTemplate": "Updated body",
				})
				wantStatus(t, "PATCH /api/notifications/:id", resp, http.StatusOK)
				updated := requireRecord(t, settings, record.ID)
				if len(updated.URLs) != len(urls) || updated.URLs[0] != urls[0] || updated.URLs[1] != urls[1] {
					t.Fatalf("urls = %v, want %v", updated.URLs, urls)
				}
				if updated.TitleTemplate != "Updated {{version}}" || updated.BodyTemplate != "Updated body" {
					t.Fatalf("templates = %q / %q", updated.TitleTemplate, updated.BodyTemplate)
				}
				if updated.CreatedAt != record.CreatedAt || updated.EventName != record.EventName || updated.Enabled != record.Enabled {
					t.Fatalf("immutable fields changed: %+v", updated)
				}
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(settings))
			}),
		scenario(updateRoute, updateRepeat, "normal", "repeat", "notifications-patch-no-change-writes-nothing",
			"Repeating the identical record PATCH answers 200 with the same record and does not rewrite the notification-settings row.",
			[]string{updateRecSrc, recordModelSrc, recordPatchSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onBackupFailed", true))
				body := map[string]any{"id": record.ID, "titleTemplate": "Steady {{errorMsg}}"}
				if resp, _ := patchRecord(t, e, record.ID, body); resp.Status != http.StatusOK {
					t.Fatalf("first PATCH status %d", resp.Status)
				}
				_, beforeUpdatedAt, exists := storedSettings(t, e)
				if !exists {
					t.Fatal("precondition: the settings row must exist")
				}
				resp, settings := patchRecord(t, e, record.ID, body)
				wantStatus(t, "repeated PATCH", resp, http.StatusOK)
				if got := requireRecord(t, settings, record.ID).TitleTemplate; got != "Steady {{errorMsg}}" {
					t.Fatalf("titleTemplate after the repeat = %q", got)
				}
				_, afterUpdatedAt, _ := storedSettings(t, e)
				if afterUpdatedAt != beforeUpdatedAt {
					t.Fatalf("repeated record PATCH rewrote the settings row (updatedAt %q, want %q)", afterUpdatedAt, beforeUpdatedAt)
				}
			}),
		scenario(updateRoute, updateShape, "normal", "changed", "notifications-patch-enable-resets-failure-state",
			"Enabling a notification that upstream disabled after its failed attempts resets lastFiredAt to null, lastAttemptFailed to false and the consecutive failure count to 0 while numTimesFired keeps its value.",
			[]string{updateRecSrc, "server/objects/Notification.js:71-95", "server/managers/NotificationManager.js:157-177"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 1, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				awaitTriggerSlot()
				fireTriggerSynced(t, e, "1")
				failed := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if failed.Enabled || failed.NumConsecutiveFailedAttempts != 1 || !failed.LastAttemptFailed || failed.LastFiredAt == nil || failed.NumTimesFired != 1 {
					t.Fatalf("record after the failed attempt = %+v", failed)
				}
				resp, settings := patchRecord(t, e, record.ID, map[string]any{"id": record.ID, "enabled": true})
				wantStatus(t, "PATCH enabling the record", resp, http.StatusOK)
				enabled := requireRecord(t, settings, record.ID)
				if !enabled.Enabled || enabled.LastFiredAt != nil || enabled.LastAttemptFailed || enabled.NumConsecutiveFailedAttempts != 0 {
					t.Fatalf("record after enabling = %+v", enabled)
				}
				if enabled.NumTimesFired != 1 {
					t.Fatalf("numTimesFired = %d, want the recorded 1", enabled.NumTimesFired)
				}
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(settings))
			}),
		scenario(updateRoute, updateShape, "edge", "initial", "notifications-patch-empty-body-keeps-record",
			"A PATCH /api/notifications/:id without an id in the body and one naming an unknown body id both answer 200 with the unchanged settings document.",
			[]string{routerRecords, updateRecSrc, recordPatchSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				resp, settings := patchRecord(t, e, record.ID, map[string]any{})
				wantStatus(t, "PATCH without an id", resp, http.StatusOK)
				requireJSONEqual(t, "settings after a bodiless PATCH", settingsDocumentOf(settings), stamp.document)
				resp, settings = patchRecord(t, e, record.ID, map[string]any{"id": "2f1c3d4e-0000-4000-8000-000000000000", "titleTemplate": "x"})
				wantStatus(t, "PATCH naming an unknown body id", resp, http.StatusOK)
				requireJSONEqual(t, "settings after an unknown body id", settingsDocumentOf(settings), stamp.document)
				stamp.requireUnchanged(t, e, "record patches without a target")
			}),
		scenario(updateRoute, updateAuth, "error", "failure", "notifications-patch-rejections-change-nothing",
			"PATCH /api/notifications/:id answers 401 without a token, 403 for an authenticated non-admin and 404 for an unknown path id to an admin, and the existing record keeps its values.",
			[]string{routerRecords, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				body := map[string]any{"id": record.ID, "titleTemplate": "Rejected {{version}}"}
				requireAdminGate(t, e, http.MethodPatch, "/api/notifications/"+record.ID, body)
				wantStatus(t, "unknown path id", e.Patch(t, e.Token(t, "root"), "/api/notifications/2f1c3d4e-0000-4000-8000-000000000000", body), http.StatusNotFound)
				stamp.requireUnchanged(t, e, "rejected record patches")
			}),
	}
}

// requireCreatedRecord asserts the fields setData gives a new notification.
func requireCreatedRecord(t *testing.T, record notificationRecord, before time.Time, eventName string, urls []string, enabled bool) {
	t.Helper()
	if !uuidPattern.MatchString(record.ID) {
		t.Fatalf("id %q is not a v4 UUID", record.ID)
	}
	if record.EventName != eventName || record.Enabled != enabled {
		t.Fatalf("record = %+v, want event %q enabled %v", record, eventName, enabled)
	}
	if len(record.URLs) != len(urls) {
		t.Fatalf("urls = %v, want %v", record.URLs, urls)
	}
	for i := range urls {
		if record.URLs[i] != urls[i] {
			t.Fatalf("urls = %v, want %v", record.URLs, urls)
		}
	}
	created := time.UnixMilli(record.CreatedAt)
	if record.CreatedAt <= 0 || created.Before(before.Add(-time.Second)) || created.After(time.Now().Add(arrivalLag)) {
		t.Fatalf("createdAt = %s, want within the request", created)
	}
}
