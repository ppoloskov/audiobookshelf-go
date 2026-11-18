package notify

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	notifySocket   = "socket-outgoing-notifications-updated"
	socketDocument = "INV-SOCKET-OUTGOING-NOTIFICATIONS-UPDATED"
	socketAudience = "INV-SOCKET-OUTGOING-NOTIFICATIONS-UPDATED-AUDIENCE-001"
	socketEmitSrc  = "server/managers/NotificationManager.js:174-177"
	socketSendSrc  = "server/SocketAuthority.js:57-65"
)

// socketScenarios covers the client-visible side of notifications_updated: the
// payload field types, the audience and the ordering of repeated emissions.
func socketScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(notifySocket, socketAudience, "normal", "initial", "notifications-socket-reaches-every-authenticated-client",
			"notifications_updated reaches the admin socket and the authenticated non-admin listener socket with the same object payload and never the unauthenticated socket.",
			[]string{socketEmitSrc, socketSendSrc, eventClientSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				w := watch(t, e)
				before := time.Now()
				payload := w.settingsEvent(t, e, "0") // asserts listener equality and anonymous silence
				requireSettingsPayload(t, payload, stub.URL("/notify"), 5, 20, []notificationRecord{firedRecord(record, 1)}, before)
			}),
		scenario(notifySocket, socketDocument, "normal", "repeat", "notifications-socket-two-triggers-in-order",
			"Two triggers produce exactly two notifications_updated events per authenticated socket, in trigger order, with numTimesFired 1 and then 2.",
			[]string{socketEmitSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				fireTrigger(t, e, socket, "0")
				fireTrigger(t, e, socket, "0")
				socket.Wait(t, mark, "notifications_updated", eventWait, func(args []json.RawMessage) bool {
					return recordFiredTimes(t, args, 2)
				})
				events := eventsSince(socket, mark, "notifications_updated")
				if len(events) != 2 {
					t.Fatalf("%d notifications_updated events, want 2", len(events))
				}
				if !recordFiredTimes(t, events[0].Args, 1) || !recordFiredTimes(t, events[1].Args, 2) {
					t.Fatalf("event counters are not 1 then 2: %s / %s", events[0].Args, events[1].Args)
				}
				if !events[1].At.After(events[0].At) {
					t.Fatalf("second event at %s is not after the first at %s", events[1].At, events[0].At)
				}
				if got := requireRecord(t, getNotifications(t, e).Settings, record.ID).NumTimesFired; got != 2 {
					t.Fatalf("numTimesFired after two triggers = %d", got)
				}
			}),
		scenario(notifySocket, socketDocument, "normal", "changed", "notifications-socket-after-a-settings-change",
			"After the Apprise URL and the queue limit change, the next notifications_updated payload carries the new values as a string and a number while the record keeps its created id.",
			[]string{socketEmitSrc, settingsModel},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				other := newAppriseStub(t)
				useApprise(t, e, other.URL("/notify"), 5, 12)
				w := watch(t, e)
				before := time.Now()
				payload := w.settingsEvent(t, e, "0")
				requireSettingsPayload(t, payload, other.URL("/notify"), 5, 12, []notificationRecord{firedRecord(record, 1)}, before)
				if payload["appriseType"] != "api" || payload["id"] != "notification-settings" {
					t.Fatalf("payload identity fields = %v / %v", payload["id"], payload["appriseType"])
				}
				if delay, ok := payload["notificationDelay"].(float64); !ok || delay != 1000 {
					t.Fatalf("notificationDelay = %v, want the number 1000", payload["notificationDelay"])
				}
			}),
		scenario(notifySocket, socketAudience, "error", "failure", "notifications-socket-absent-for-a-rejected-trigger",
			"A non-admin trigger is rejected with 403 and no notifications_updated event reaches the admin, the listener or the unauthenticated socket.",
			[]string{socketEmitSrc, socketSendSrc, "server/controllers/NotificationController.js:137-150"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				createRecord(t, e, recordBody("onTest", true))
				w := watch(t, e)
				listener := e.Do(t, harness.Request{Path: "/api/notifications/test", Token: e.Token(t, "listener")})
				wantStatus(t, "trigger as a non-admin", listener, http.StatusForbidden)
				w.requireNone(t, "a rejected trigger")
				stub.requireNoDelivery(t, "a rejected trigger")
			}),
		scenario(notifySocket, socketDocument, "edge", "failure", "notifications-socket-absent-when-unusable",
			"With no Apprise URL the trigger answers 200 and emits no notifications_updated event to any socket.",
			[]string{socketEmitSrc, flowSettingsSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, "", 5, 20)
				clearRecords(t, e)
				createRecord(t, e, recordBody("onTest", true))
				w := watch(t, e)
				wantStatus(t, "trigger without an Apprise URL", triggerTest(t, e, "0"), http.StatusOK)
				w.requireNone(t, "a trigger on unusable settings")
				stub.requireNoDelivery(t, "a trigger on unusable settings")
			}),
		scenario(notifySocket, socketDocument, "edge", "force", "notifications-socket-after-a-fail-override",
			"The failed-attempt override emits one notifications_updated payload whose record types are a boolean enabled false, a number count and a string event name.",
			[]string{socketEmitSrc, triggerSrc},
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
				records, _ := payload["notifications"].([]any)
				first, _ := records[0].(map[string]any)
				if enabled, ok := first["enabled"].(bool); !ok || enabled {
					t.Fatalf("payload enabled = %v, want the boolean false", first["enabled"])
				}
				if name, ok := first["eventName"].(string); !ok || name != "onTest" {
					t.Fatalf("payload eventName = %v, want the string onTest", first["eventName"])
				}
			}),
	}
}

// recordFiredTimes reports whether the payload's single record counts times
// firings.
func recordFiredTimes(t *testing.T, args []json.RawMessage, times int) bool {
	t.Helper()
	if len(args) != 1 {
		return false
	}
	var payload struct {
		Notifications []struct {
			NumTimesFired int `json:"numTimesFired"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(args[0], &payload); err != nil || len(payload.Notifications) != 1 {
		return false
	}
	return payload.Notifications[0].NumTimesFired == times
}
