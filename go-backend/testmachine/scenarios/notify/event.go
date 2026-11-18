package notify

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	notifyEvent     = "cap-event-notifications-updated"
	eventDocument   = "INV-CAP-EVENT-NOTIFICATIONS-UPDATED"
	eventAdmission  = "INV-CAP-EVENT-NOTIFICATIONS-UPDATED-ADMISSION-001"
	eventEmitSrc    = "server/managers/NotificationManager.js:174-177"
	eventEmitSocket = "server/SocketAuthority.js:57-65"
	eventClientSrc  = "client/pages/config/notifications.vue:167-172"
)

// watchers holds one socket per audience the notification settings page cares
// about: an admin, an authenticated non-admin and an unauthenticated client.
type watchers struct {
	admin     *harness.Socket
	listener  *harness.Socket
	anonymous *harness.Socket
}

// watch connects the three sockets.
func watch(t *testing.T, e *harness.Env) *watchers {
	t.Helper()
	return &watchers{
		admin:     e.Socket(t, e.Token(t, "root")),
		listener:  e.Socket(t, e.Token(t, "listener")),
		anonymous: e.Socket(t, ""),
	}
}

// settingsEvent fires GET /api/notifications/test and returns the
// notifications_updated payload, proving the listener receives the same payload
// and the unauthenticated socket none.
func (w *watchers) settingsEvent(t *testing.T, e *harness.Env, fail string) map[string]any {
	t.Helper()
	adminMark, listenerMark, anonymousMark := w.admin.Mark(), w.listener.Mark(), w.anonymous.Mark()
	wantStatus(t, "GET /api/notifications/test", triggerTest(t, e, fail), http.StatusOK)
	adminEvent := w.admin.Wait(t, adminMark, "notifications_updated", eventWait, nil)
	listenerEvent := w.listener.Wait(t, listenerMark, "notifications_updated", eventWait, nil)
	w.anonymous.None(t, anonymousMark, "notifications_updated", quietWindow, nil)
	payload := socketPayload(t, adminEvent)
	requireJSONEqual(t, "listener notifications_updated payload", socketPayload(t, listenerEvent), payload)
	return payload
}

// requireNone fails when any socket receives a notifications_updated event.
func (w *watchers) requireNone(t *testing.T, what string) {
	t.Helper()
	marks := []int{w.admin.Mark(), w.listener.Mark(), w.anonymous.Mark()}
	time.Sleep(quietWindow) // the operation already answered; a late event must appear here
	if events := eventsSince(w.admin, marks[0], "notifications_updated"); len(events) != 0 {
		t.Fatalf("%s: the admin socket received %d notifications_updated events", what, len(events))
	}
	if events := eventsSince(w.listener, marks[1], "notifications_updated"); len(events) != 0 {
		t.Fatalf("%s: the listener socket received %d notifications_updated events", what, len(events))
	}
	if events := eventsSince(w.anonymous, marks[2], "notifications_updated"); len(events) != 0 {
		t.Fatalf("%s: the unauthenticated socket received %d notifications_updated events", what, len(events))
	}
}

// eventsSince lists the events with the name from an index on.
func eventsSince(socket *harness.Socket, from int, name string) []harness.Event {
	var found []harness.Event
	for _, event := range socket.Events()[from:] {
		if event.Name == name {
			found = append(found, event)
		}
	}
	return found
}

// markerTimestamp is a non-nil lastFiredAt that only marks "expect a live
// timestamp"; requireSettingsPayload validates the value the server sent.
func markerTimestamp() *int64 {
	value := int64(1)
	return &value
}

// firedRecord returns record with the delivery state a successful firing leaves.
func firedRecord(record notificationRecord, times int) notificationRecord {
	record.NumTimesFired = times
	record.LastAttemptFailed = false
	record.NumConsecutiveFailedAttempts = 0
	record.LastFiredAt = markerTimestamp()
	return record
}

// eventScenarios covers the notifications_updated event the delivery flow emits
// after every admitted trigger: its whole-settings payload and when it is absent.
func eventScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(notifyEvent, eventDocument, "normal", "initial", "notifications-updated-event-carries-the-whole-document",
			"A test trigger emits notifications_updated with the entire settings object: id, appriseType, appriseApiUrl, the full notification records (including counters and createdAt), maxFailedAttempts, maxNotificationQueue and notificationDelay.",
			[]string{eventEmitSrc, eventEmitSocket, eventClientSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				urls := []string{"mailto://event@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": urls,
					"titleTemplate": "Event {{version}}", "bodyTemplate": "Event body", "enabled": true, "type": nil,
				})
				w := watch(t, e)
				before := time.Now()
				payload := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, payload, stub.URL("/notify"), 5, 20, []notificationRecord{firedRecord(record, 1)}, before)
			}),
		scenario(notifyEvent, eventDocument, "normal", "repeat", "notifications-updated-event-repeats-per-trigger",
			"Two consecutive test triggers emit two notifications_updated payloads whose single record counts numTimesFired 1 and then 2.",
			[]string{eventEmitSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				w := watch(t, e)
				before := time.Now()
				first := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, first, stub.URL("/notify"), 5, 20, []notificationRecord{firedRecord(record, 1)}, before)
				second := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, second, stub.URL("/notify"), 5, 20, []notificationRecord{firedRecord(record, 2)}, before)
			}),
		scenario(notifyEvent, eventDocument, "normal", "changed", "notifications-updated-event-reflects-changed-settings",
			"After PATCH /api/notifications changes the queue limit and PATCH /api/notifications/:id changes the templates, the next notifications_updated payload carries both new values.",
			[]string{eventEmitSrc, settingsModel, recordPatchSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://changed@example.test"},
					"titleTemplate": "Before", "bodyTemplate": "Body", "enabled": true, "type": nil,
				})
				useApprise(t, e, stub.URL("/notify"), 5, 11)
				if resp, _ := patchRecord(t, e, record.ID, map[string]any{"id": record.ID, "titleTemplate": "After {{version}}"}); resp.Status != http.StatusOK {
					t.Fatalf("record PATCH status %d", resp.Status)
				}
				updated := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				w := watch(t, e)
				before := time.Now()
				payload := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, payload, stub.URL("/notify"), 5, 11, []notificationRecord{firedRecord(updated, 1)}, before)
			}),
		scenario(notifyEvent, eventDocument, "edge", "initial", "notifications-updated-event-with-no-subscribers",
			"A trigger with no subscriptions still emits notifications_updated with the settings object and an empty notifications array, so there is no no-change suppression.",
			[]string{eventEmitSrc, "server/objects/settings/NotificationSettings.js:57-59"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				w := watch(t, e)
				payload := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, payload, stub.URL("/notify"), 5, 20, nil, time.Now())
			}),
		scenario(notifyEvent, eventDocument, "edge", "force", "notifications-updated-event-reflects-a-fail-override",
			"The failed-attempt override ?fail=1 emits notifications_updated whose record has enabled false at the reached limit, numConsecutiveFailedAttempts 1, lastAttemptFailed true and a live lastFiredAt.",
			[]string{eventEmitSrc, triggerSrc, flowRecordSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 1, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				w := watch(t, e)
				before := time.Now()
				awaitTriggerSlot()
				payload := w.settingsEvent(t, e, "1")
				failed := record
				failed.Enabled = false
				failed.NumTimesFired = 1
				failed.NumConsecutiveFailedAttempts = 1
				failed.LastAttemptFailed = true
				failed.LastFiredAt = markerTimestamp()
				requireSettingsPayload(t, payload, stub.URL("/notify"), 1, 20, []notificationRecord{failed}, before)
			}),
		scenario(notifyEvent, eventAdmission, "error", "failure", "notifications-updated-event-absent-when-not-admitted",
			"No notifications_updated event is emitted when the notification system has no Apprise URL or when a non-admin calls the trigger route, and the settings document stays unchanged.",
			[]string{eventEmitSrc, flowSettingsSrc, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, "", 5, 20)
				clearRecords(t, e)
				createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				w := watch(t, e)
				wantStatus(t, "trigger without an Apprise URL", triggerTest(t, e, "0"), http.StatusOK)
				w.requireNone(t, "a trigger on unusable settings")
				stamp.requireUnchanged(t, e, "a trigger on unusable settings")
				listener := e.Do(t, harness.Request{Path: "/api/notifications/test", Token: e.Token(t, "listener")})
				wantStatus(t, "trigger as a non-admin", listener, http.StatusForbidden)
				w.requireNone(t, "a rejected trigger")
				stub.requireNoDelivery(t, "an unadmitted trigger")
			}),
	}
}
