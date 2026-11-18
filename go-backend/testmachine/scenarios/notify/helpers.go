package notify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventWait   = 20 * time.Second       // a socket event sent while a request is handled
	triggerWait = 75 * time.Second       // the podcast cron check, download and delivery
	quietWindow = 800 * time.Millisecond // window in which an event must not arrive
	arrivalLag  = 5 * time.Second        // timestamp sanity bound for a delivery
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// scenario builds a harness.Scenario with one claim and its upstream sources.
func scenario(requirement, invariant, caseName, state, assertion, claim string, sources []string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: caseName, State: state,
		Assertion: assertion, Sources: sources, Claim: claim, Run: run,
	}
}

// wantStatus fails unless the response has exactly the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: status %d, want %d: %.300s", what, resp.Status, status, resp.Body)
	}
}

// decode unmarshals a JSON body into a new T.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %T: %v: %.400s", value, err, body)
	}
	return value
}

// normalize round-trips a value through JSON so numbers compare by value.
func normalize(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// requireJSONEqual fails unless both values are the same JSON document,
// including every key, so an extra field is a failure too.
func requireJSONEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		gotRaw, _ := json.MarshalIndent(normalize(got), "", " ")
		wantRaw, _ := json.MarshalIndent(normalize(want), "", " ")
		t.Fatalf("%s:\n got %s\nwant %s", what, gotRaw, wantRaw)
	}
}

// settingsDocument is the JSON of Database.notificationSettings
// (server/objects/settings/NotificationSettings.js toJSON).
type settingsDocument struct {
	ID                   string               `json:"id"`
	AppriseType          string               `json:"appriseType"`
	AppriseAPIURL        *string              `json:"appriseApiUrl"`
	Notifications        []notificationRecord `json:"notifications"`
	MaxFailedAttempts    int                  `json:"maxFailedAttempts"`
	MaxNotificationQueue int                  `json:"maxNotificationQueue"`
	NotificationDelay    int                  `json:"notificationDelay"`
}

// notificationRecord is one settings.notifications entry
// (server/objects/Notification.js toJSON).
type notificationRecord struct {
	ID                           string   `json:"id"`
	LibraryID                    *string  `json:"libraryId"`
	EventName                    string   `json:"eventName"`
	URLs                         []string `json:"urls"`
	TitleTemplate                string   `json:"titleTemplate"`
	BodyTemplate                 string   `json:"bodyTemplate"`
	Enabled                      bool     `json:"enabled"`
	Type                         *string  `json:"type"`
	LastFiredAt                  *int64   `json:"lastFiredAt"`
	LastAttemptFailed            bool     `json:"lastAttemptFailed"`
	NumConsecutiveFailedAttempts int      `json:"numConsecutiveFailedAttempts"`
	NumTimesFired                int      `json:"numTimesFired"`
	CreatedAt                    int64    `json:"createdAt"`
}

// notificationEvent is one entry of the notificationData document
// (server/utils/notifications.js).
type notificationEvent struct {
	Name             string         `json:"name"`
	RequiresLibrary  bool           `json:"requiresLibrary"`
	LibraryMediaType string         `json:"libraryMediaType"`
	Description      string         `json:"description"`
	DescriptionKey   string         `json:"descriptionKey"`
	Variables        []string       `json:"variables"`
	Defaults         map[string]any `json:"defaults"`
	TestData         map[string]any `json:"testData"`
}

// notificationsResponse is the GET /api/notifications body.
type notificationsResponse struct {
	Data     notificationData `json:"data"`
	Settings settingsDocument `json:"settings"`
}

type notificationData struct {
	Events []notificationEvent `json:"events"`
}

// getNotifications reads GET /api/notifications as root.
func getNotifications(t *testing.T, e *harness.Env) notificationsResponse {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/notifications")
	wantStatus(t, "GET /api/notifications", resp, http.StatusOK)
	return decode[notificationsResponse](t, resp.Body)
}

// settingsObject returns the settings object of GET /api/notifications as a
// decoded map, for assertions that pin the whole document.
func settingsObject(t *testing.T, e *harness.Env) map[string]any {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/notifications")
	wantStatus(t, "GET /api/notifications", resp, http.StatusOK)
	settings, ok := resp.Object(t)["settings"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/notifications has no settings object: %.300s", resp.Body)
	}
	return settings
}

// useApprise points the notification settings at an Apprise stub the way
// client/pages/config/notifications.vue submitForm does: it always sends the
// three keys, with a null URL when the field is empty.
func useApprise(t *testing.T, e *harness.Env, apiURL string, maxAttempts, maxQueue int) {
	t.Helper()
	body := map[string]any{"appriseApiUrl": nil, "maxNotificationQueue": maxQueue, "maxFailedAttempts": maxAttempts}
	if apiURL != "" {
		body["appriseApiUrl"] = apiURL
	}
	resp := e.Patch(t, e.Token(t, "root"), "/api/notifications", body)
	wantStatus(t, "PATCH /api/notifications", resp, http.StatusOK)
}

// createRecord posts one notification with the record edit modal's payload shape
// and returns the response and the stored record.
func createRecord(t *testing.T, e *harness.Env, body map[string]any) (harness.Response, notificationRecord) {
	t.Helper()
	before := getNotifications(t, e).Settings.Notifications
	resp := e.Post(t, e.Token(t, "root"), "/api/notifications", body)
	wantStatus(t, "POST /api/notifications", resp, http.StatusOK)
	after := decode[settingsDocument](t, resp.Body).Notifications
	if len(after) != len(before)+1 {
		t.Fatalf("POST /api/notifications stored %d records, want %d: %.400s", len(after), len(before)+1, resp.Body)
	}
	return resp, after[len(after)-1]
}

// recordBody is the payload the notification edit modal creates for an event.
func recordBody(eventName string, enabled bool) map[string]any {
	return map[string]any{
		"libraryId":     nil,
		"eventName":     eventName,
		"urls":          []string{"mailto://notify@example.test"},
		"titleTemplate": "",
		"bodyTemplate":  "",
		"enabled":       enabled,
		"type":          nil,
	}
}

// patchRecord updates one record through PATCH /api/notifications/:id and
// returns the settings document of the response.
func patchRecord(t *testing.T, e *harness.Env, pathID string, body map[string]any) (harness.Response, settingsDocument) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/notifications/"+pathID, body)
	return resp, decode[settingsDocument](t, resp.Body)
}

// recordIndex returns the index of the record or -1.
func recordIndex(doc settingsDocument, id string) int {
	for i, record := range doc.Notifications {
		if record.ID == id {
			return i
		}
	}
	return -1
}

// requireRecord finds a record and fails when it is absent.
func requireRecord(t *testing.T, doc settingsDocument, id string) notificationRecord {
	t.Helper()
	index := recordIndex(doc, id)
	if index < 0 {
		t.Fatalf("settings document has no notification %s: %+v", id, doc.Notifications)
	}
	return doc.Notifications[index]
}

// clearRecords deletes every notification through the API, so a scenario can
// pin the whole settings document without records of earlier scenarios.
func clearRecords(t *testing.T, e *harness.Env) {
	t.Helper()
	for _, record := range getNotifications(t, e).Settings.Notifications {
		resp := e.Delete(t, e.Token(t, "root"), "/api/notifications/"+record.ID)
		wantStatus(t, "DELETE /api/notifications/"+record.ID, resp, http.StatusOK)
	}
	if remaining := getNotifications(t, e).Settings.Notifications; len(remaining) != 0 {
		t.Fatalf("%d notifications survived the clear", len(remaining))
	}
}

// storedSettings reads the persisted notification-settings row.
func storedSettings(t *testing.T, e *harness.Env) (value map[string]any, updatedAt string, exists bool) {
	t.Helper()
	rows := e.Rows(t, "SELECT value, updatedAt FROM settings WHERE key = 'notification-settings'")
	switch len(rows) {
	case 0:
		return nil, "", false
	case 1:
	default:
		t.Fatalf("settings holds %d notification-settings rows, want at most one", len(rows))
	}
	text, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatalf("notification-settings value is %T, want JSON text", rows[0]["value"])
	}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("notification-settings value is not a JSON object: %v", err)
	}
	updatedAt, _ = rows[0]["updatedAt"].(string)
	return value, updatedAt, true
}

// settingsSnapshot records the settings document and the persisted row for a
// "nothing changed" assertion.
type settingsSnapshot struct {
	document  map[string]any
	updatedAt string
	exists    bool
}

// takeSettingsSnapshot reads the document and its persisted row.
func takeSettingsSnapshot(t *testing.T, e *harness.Env) settingsSnapshot {
	t.Helper()
	_, updatedAt, exists := storedSettings(t, e)
	return settingsSnapshot{document: settingsObject(t, e), updatedAt: updatedAt, exists: exists}
}

// requireUnchanged fails unless the settings document and its row are unchanged.
func (s settingsSnapshot) requireUnchanged(t *testing.T, e *harness.Env, what string) {
	t.Helper()
	if current := settingsObject(t, e); !reflect.DeepEqual(normalize(current), normalize(s.document)) {
		t.Fatalf("%s changed the settings document: %+v, want %+v", what, current, s.document)
	}
	value, updatedAt, exists := storedSettings(t, e)
	if exists != s.exists {
		t.Fatalf("%s created or removed the settings row (exists %v, want %v)", what, exists, s.exists)
	}
	if !exists {
		return
	}
	if !reflect.DeepEqual(normalize(value), normalize(s.document)) {
		t.Fatalf("%s changed the persisted settings: %+v, want %+v", what, value, s.document)
	}
	if updatedAt != s.updatedAt {
		t.Fatalf("%s rewrote the settings row (updatedAt %q, want %q)", what, updatedAt, s.updatedAt)
	}
}

// requireAdminGate asserts 401 without a token and 403 for a fixture user that
// is not an admin; the request must not reach the handler.
func requireAdminGate(t *testing.T, e *harness.Env, method, path string, body any) {
	t.Helper()
	anonymous := e.Do(t, harness.Request{Method: method, Path: path, JSON: body})
	wantStatus(t, method+" "+path+" without a token", anonymous, http.StatusUnauthorized)
	listener := e.Do(t, harness.Request{Method: method, Path: path, JSON: body, Token: e.Token(t, "listener")})
	wantStatus(t, method+" "+path+" as a non-admin", listener, http.StatusForbidden)
}

// triggerTest fires GET /api/notifications/test with the fail query of the client
// (client/components/cards/NotificationCard.vue fireTestEvent).
func triggerTest(t *testing.T, e *harness.Env, fail string) harness.Response {
	t.Helper()
	query := url.Values{}
	query.Set("fail", fail)
	return e.Do(t, harness.Request{Path: "/api/notifications/test", Query: query, Token: e.Token(t, "root")})
}

// fireTrigger fires the test trigger and waits for the notifications_updated
// event of that very trigger. NotificationManager queues a trigger that arrives
// while another one is sending (server/managers/NotificationManager.js:186-198)
// and processes it after notificationDelay, so an answered request alone does not
// mean the counters are final; the event is emitted once the trigger finished.
func fireTrigger(t *testing.T, e *harness.Env, socket *harness.Socket, fail string) harness.Event {
	t.Helper()
	_, event := fireTriggerResponse(t, e, socket, fail)
	return event
}

// fireTriggerResponse is fireTrigger that also returns the answered request.
func fireTriggerResponse(t *testing.T, e *harness.Env, socket *harness.Socket, fail string) (harness.Response, harness.Event) {
	t.Helper()
	mark := socket.Mark()
	resp := triggerTest(t, e, fail)
	wantStatus(t, "GET /api/notifications/test", resp, http.StatusOK)
	return resp, socket.Wait(t, mark, "notifications_updated", eventWait, nil)
}

// fireTriggerSynced fires the test trigger and waits for its event on a fresh
// admin socket.
func fireTriggerSynced(t *testing.T, e *harness.Env, fail string) harness.Event {
	t.Helper()
	return fireTrigger(t, e, e.Socket(t, e.Token(t, "root")), fail)
}

// awaitTriggerSlot waits out the server's inter-notification delay before a
// trigger whose semantics depend on immediate admission. NotificationManager
// queues a trigger that arrives while another one is sending and stores only
// eventName and eventData in the queue, so a queued trigger silently loses the
// ?fail override (server/managers/NotificationManager.js:186-210). The delay is
// an internal timer of the manager, not an observable effect.
func awaitTriggerSlot() {
	time.Sleep(1200 * time.Millisecond)
}

// requireUnchangedValue fails unless the settings document and its persisted
// row still hold want; it does not compare the row timestamp, because an
// admitted trigger rewrites the row with the same value.
func requireUnchangedValue(t *testing.T, e *harness.Env, want map[string]any, what string) {
	t.Helper()
	requireJSONEqual(t, what+" settings document", settingsObject(t, e), want)
	requireJSONEqual(t, what+" persisted settings", mustStoredValue(t, e), want)
}

// serverVersion reads the running server version from /status, outside the API.
func serverVersion(t *testing.T, e *harness.Env) string {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/status", Raw: true})
	wantStatus(t, "GET /status", resp, http.StatusOK)
	version, _ := resp.Object(t)["serverVersion"].(string)
	if version == "" {
		t.Fatalf("GET /status has no serverVersion: %.300s", resp.Body)
	}
	return version
}

// requireSettingsPayload pins the whole notifications_updated payload: every
// top-level field and every record field. A record with a non-nil LastFiredAt
// must carry a live timestamp there; its value is validated, not fixed, because
// it is the delivery clock.
func requireSettingsPayload(t *testing.T, payload map[string]any, apiURL string, maxAttempts, maxQueue int, records []notificationRecord, notBefore time.Time) {
	t.Helper()
	notifications, ok := payload["notifications"].([]any)
	if !ok || len(notifications) != len(records) {
		t.Fatalf("payload notifications = %v, want %d records", payload["notifications"], len(records))
	}
	for i, want := range records {
		got, ok := notifications[i].(map[string]any)
		if !ok {
			t.Fatalf("payload record %d is %T, want an object", i, notifications[i])
		}
		document := recordDocument(want)
		if want.LastFiredAt != nil {
			number, ok := got["lastFiredAt"].(float64)
			if !ok {
				t.Fatalf("payload record %d lastFiredAt = %v, want a timestamp", i, got["lastFiredAt"])
			}
			stamp := int64(number)
			requireTimestamp(t, fmt.Sprintf("record %d lastFiredAt", i), &stamp, notBefore)
			document["lastFiredAt"] = number
		}
		requireJSONEqual(t, fmt.Sprintf("payload record %d", i), got, document)
	}
	expected := defaultSettingsDocument()
	if apiURL != "" {
		expected["appriseApiUrl"] = apiURL
	}
	expected["maxFailedAttempts"] = maxAttempts
	expected["maxNotificationQueue"] = maxQueue
	expected["notifications"] = notifications
	requireJSONEqual(t, "notifications_updated payload", payload, expected)
}

// socketPayload decodes the single object argument of an event.
func socketPayload(t *testing.T, event harness.Event) map[string]any {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s event has %d arguments, want 1: %s", event.Name, len(event.Args), event.Args)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Args[0], &payload); err != nil {
		t.Fatalf("%s payload is not a JSON object: %v: %.300s", event.Name, err, event.Args[0])
	}
	return payload
}

// requireTimestamp fails unless the optional millisecond timestamp is around now.
func requireTimestamp(t *testing.T, what string, value *int64, notBefore time.Time) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s is null, want a millisecond timestamp", what)
	}
	at := time.UnixMilli(*value)
	if *value <= 0 || at.Before(notBefore.Add(-time.Second)) || at.After(time.Now().Add(arrivalLag)) {
		t.Fatalf("%s = %s, want within %s of the request", what, at, arrivalLag)
	}
}

// storedDocument reads the persisted settings row as a typed document.
func storedDocument(t *testing.T, e *harness.Env) settingsDocument {
	t.Helper()
	raw, err := json.Marshal(mustStoredValue(t, e))
	if err != nil {
		t.Fatalf("encode stored settings: %v", err)
	}
	return decode[settingsDocument](t, raw)
}
