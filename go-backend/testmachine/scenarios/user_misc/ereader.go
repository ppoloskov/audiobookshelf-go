package user_misc

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	ereaderSource  = "server/controllers/MeController.js:589-639"
	emailSource    = "server/objects/settings/EmailSettings.js:140-166"
	ereaderClient  = "client/components/modals/emails/UserEReaderDeviceModal.vue:113-160"
	ereaderPage    = "client/pages/account.vue:275-300"
	ereaderEventOf = "server/controllers/MeController.js:632-634"
)

// device builds one ereader device of the current user.
func device(name, email, userID string) ereaderDevice {
	return ereaderDevice{Name: name, Email: email, AvailabilityOption: "specificUsers", Users: []string{userID}}
}

// postDevices sends the ereader device payload and returns the response.
func postDevices(t *testing.T, e *harness.Env, token string, devices []ereaderDevice) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/me/ereader-devices", map[string]any{"ereaderDevices": devices})
}

// requireDevicesStatus requires a status and decodes the device response.
func requireDevicesStatus(t *testing.T, resp harness.Response, status int, what string) devicesResponse {
	t.Helper()
	wantStatus(t, resp, status, what)
	var body devicesResponse
	if status == http.StatusOK {
		resp.JSON(t, &body)
	}
	return body
}

// loginDevices returns the ereader devices served in the login payload.
func loginDevices(t *testing.T, e *harness.Env, username, password string) []ereaderDevice {
	t.Helper()
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": username, "password": password}})
	wantStatus(t, resp, http.StatusOK, "login")
	var body struct {
		EreaderDevices []ereaderDevice `json:"ereaderDevices"`
	}
	resp.JSON(t, &body)
	return body.EreaderDevices
}

// eventDevices decodes the payload of one ereader-devices-updated event.
func eventDevices(t *testing.T, event harness.Event) devicesResponse {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("ereader-devices-updated must carry one argument: %v", event.Args)
	}
	var body devicesResponse
	if err := json.Unmarshal(event.Args[0], &body); err != nil {
		t.Fatalf("ereader-devices-updated payload: %v", err)
	}
	return body
}

func ereaderScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r121", Invariant: "INV-R121-CONTRACT-001", Case: "normal", State: "initial", Assertion: "ereader-create-devices",
			Sources: []string{"server/routers/ApiRouter.js:195", ereaderSource, emailSource, ereaderClient, ereaderPage},
			Claim:   "POST /api/me/ereader-devices with two valid specificUsers devices of the caller answers 200 with exactly those devices and persists them (plus the settings defaults) in the email-settings row.",
			Run: func(t *testing.T, e *harness.Env) {
				if rows := e.Rows(t, "SELECT key FROM settings WHERE key = 'email-settings'"); len(rows) != 0 {
					t.Fatalf("fixture must not have email settings yet: %v", rows)
				}
				listener := e.Token(t, "listener")
				want := []ereaderDevice{
					device("Kobo", "listener@example.com", e.IDs["listener"]),
					device("Kindle", "kindle@example.com", e.IDs["listener"]),
				}
				body := requireDevicesStatus(t, postDevices(t, e, listener, want), http.StatusOK, "create devices")
				equalJSON(t, "response devices", body.EreaderDevices, want)
				settings := emailSettings(t, e)
				if settings.ID != "email-settings" {
					t.Fatalf("persisted settings id %q", settings.ID)
				}
				equalJSON(t, "persisted devices", settings.EreaderDevices, want)
				equalJSON(t, "login devices", loginDevices(t, e, "trial-listener", "trial-listener-password"), want)
			},
		},
		{
			Requirement: "socket-outgoing-ereader-devices-updated", Invariant: "INV-SOCKET-OUTGOING-EREADER-DEVICES-UPDATED", Case: "normal", State: "initial", Assertion: "ereader-event-create",
			Sources: []string{ereaderEventOf, ereaderSource, "client/pages/account.vue:289"},
			Claim:   "A successful ereader device write emits ereader-devices-updated with {ereaderDevices} naming only the caller's devices to the caller's sockets, and to no other user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				ls := e.Socket(t, listener)
				rs := e.Socket(t, e.Token(t, "root"))
				fs := e.Socket(t, e.Token(t, "finisher"))
				mark, rootMark, finisherMark := ls.Mark(), rs.Mark(), fs.Mark()
				want := []ereaderDevice{
					device("Kobo", "listener@example.com", e.IDs["listener"]),
					device("Kindle", "kindle@example.com", e.IDs["listener"]),
					device("Pocket", "pocket@example.com", e.IDs["listener"]),
				}
				body := requireDevicesStatus(t, postDevices(t, e, listener, want), http.StatusOK, "create three devices")
				equalJSON(t, "response devices", body.EreaderDevices, want)
				event := ls.Wait(t, mark, "ereader-devices-updated", 5*time.Second, nil)
				equalJSON(t, "event devices", eventDevices(t, event).EreaderDevices, want)
				rs.None(t, rootMark, "ereader-devices-updated", 500*time.Millisecond, nil)
				fs.None(t, finisherMark, "ereader-devices-updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "socket-outgoing-ereader-devices-updated", Invariant: "INV-SOCKET-OUTGOING-EREADER-DEVICES-UPDATED", Case: "normal", State: "repeat", Assertion: "ereader-event-repeat",
			Sources: []string{ereaderEventOf, ereaderSource},
			Claim:   "Re-sending the stored device list unchanged answers 200 but emits no ereader-devices-updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, e.Token(t, "listener"))
				mark := socket.Mark()
				want := emailSettings(t, e).EreaderDevices
				requireDevicesStatus(t, postDevices(t, e, e.Token(t, "listener"), want), http.StatusOK, "repeat write")
				socket.None(t, mark, "ereader-devices-updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r121", Invariant: "INV-R121-PERSIST-001", Case: "normal", State: "repeat", Assertion: "ereader-repeat-unchanged",
			Sources: []string{ereaderSource, emailSource},
			Claim:   "Re-sending the same device list leaves the persisted email-settings row and the response byte-identical (the settings update reports no change).",
			Run: func(t *testing.T, e *harness.Env) {
				before := emailSettings(t, e).EreaderDevices
				first := anyJSON(t, postDevices(t, e, e.Token(t, "listener"), before))
				second := anyJSON(t, postDevices(t, e, e.Token(t, "listener"), before))
				equalJSON(t, "response of the repeat write", second, first)
				equalJSON(t, "persisted devices", emailSettings(t, e).EreaderDevices, before)
			},
		},
		{
			Requirement: "route-r121", Invariant: "INV-R121-PERSIST-001", Case: "edge", State: "changed", Assertion: "ereader-other-user-preserved",
			Sources: []string{ereaderSource, emailSource},
			Claim:   "Root writing its own device keeps the listener's devices in the settings row, answers with root's device only and leaves the listener's readable list of three devices intact.",
			Run: func(t *testing.T, e *harness.Env) {
				listenerDevices := emailSettings(t, e).EreaderDevices
				rootDevice := device("RootPad", "root@example.com", rootID)
				body := requireDevicesStatus(t, postDevices(t, e, e.Token(t, "root"), []ereaderDevice{rootDevice}), http.StatusOK, "root device")
				equalJSON(t, "root response", body.EreaderDevices, []ereaderDevice{rootDevice})
				all := append(append([]ereaderDevice{}, listenerDevices...), rootDevice)
				equalJSON(t, "persisted devices", emailSettings(t, e).EreaderDevices, all)
				equalJSON(t, "listener login devices", loginDevices(t, e, "trial-listener", "trial-listener-password"), listenerDevices)

				// Root may read its device back, but the listener never sees it.
				rootLogin := loginDevices(t, e, "trial-root", "trial-root-password")
				equalJSON(t, "root login devices", rootLogin, []ereaderDevice{rootDevice})
			},
		},
		{
			Requirement: "socket-outgoing-ereader-devices-updated", Invariant: "INV-SOCKET-OUTGOING-EREADER-DEVICES-UPDATED", Case: "edge", State: "changed", Assertion: "ereader-event-changed-list",
			Sources: []string{ereaderEventOf, ereaderSource},
			Claim:   "Removing a device by posting the remaining list emits ereader-devices-updated with the new shorter list and keeps the other user's device in the settings row.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				socket := e.Socket(t, listener)
				mark := socket.Mark()
				want := []ereaderDevice{
					device("Kobo", "listener@example.com", e.IDs["listener"]),
					device("Kindle", "kindle@example.com", e.IDs["listener"]),
				}
				requireDevicesStatus(t, postDevices(t, e, listener, want), http.StatusOK, "shrink the list")
				event := socket.Wait(t, mark, "ereader-devices-updated", 5*time.Second, nil)
				equalJSON(t, "event devices", eventDevices(t, event).EreaderDevices, want)
				want = append([]ereaderDevice{device("RootPad", "root@example.com", rootID)}, want...)
				equalJSON(t, "persisted devices", emailSettings(t, e).EreaderDevices, want)
			},
		},
		{
			Requirement: "socket-outgoing-ereader-devices-updated", Invariant: "INV-SOCKET-OUTGOING-EREADER-DEVICES-UPDATED", Case: "error", State: "failure", Assertion: "ereader-event-rejected-write",
			Sources: []string{"server/controllers/MeController.js:601-627", ereaderEventOf},
			Claim:   "A rejected ereader device write (duplicate name of another user's device) answers 400, persists nothing and emits no ereader-devices-updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				socket := e.Socket(t, listener)
				before := emailSettings(t, e).EreaderDevices
				mark := socket.Mark()
				resp := postDevices(t, e, listener, []ereaderDevice{device("RootPad", "dup@example.com", e.IDs["listener"])})
				requireDevicesStatus(t, resp, http.StatusBadRequest, "duplicate device name")
				equalJSON(t, "persisted devices after rejection", emailSettings(t, e).EreaderDevices, before)
				socket.None(t, mark, "ereader-devices-updated", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "route-r121", Invariant: "INV-R121-VALIDATION-001", Case: "error", State: "failure", Assertion: "ereader-validation-errors",
			Sources: []string{"server/controllers/MeController.js:595-607", ereaderSource},
			Claim:   "POST /api/me/ereader-devices answers 400 for a missing or non-array ereaderDevices, an item without name or email, a non-specificUsers availability option, and a users list that is not exactly the caller, and 401 without a token; none of these changes the settings row.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				before := emailSettings(t, e)
				userID := e.IDs["listener"]
				bodies := []any{
					map[string]any{},
					map[string]any{"ereaderDevices": "Kobo"},
					map[string]any{"ereaderDevices": []any{map[string]any{"email": "a@example.com", "availabilityOption": "specificUsers", "users": []string{userID}}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "Kobo", "availabilityOption": "specificUsers", "users": []string{userID}}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "Kobo", "email": "a@example.com", "availabilityOption": "userOrUp"}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "Kobo", "email": "a@example.com", "availabilityOption": "specificUsers", "users": []string{finisherID}}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "Kobo", "email": "a@example.com", "availabilityOption": "specificUsers", "users": []string{userID, finisherID}}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "Kobo", "email": "a@example.com", "availabilityOption": "specificUsers", "users": []string{}}}},
				}
				for _, body := range bodies {
					requireDevicesStatus(t, e.Post(t, listener, "/api/me/ereader-devices", body), http.StatusBadRequest, "invalid device payload")
					equalJSON(t, "persisted devices after rejection", emailSettings(t, e), before)
				}
				requireUnauthenticated(t, e, http.MethodPost, "/api/me/ereader-devices")
				equalJSON(t, "persisted devices after 401", emailSettings(t, e), before)
			},
		},
		{
			Requirement: "route-r121", Invariant: "INV-R121-VALIDATION-001", Case: "error", State: "failure", Assertion: "ereader-duplicate-name",
			Sources: []string{"server/controllers/MeController.js:615-627"},
			Claim:   "A device name already used by any stored device (here another user's RootPad) answers 400 with the settings row unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				before := emailSettings(t, e)
				resp := postDevices(t, e, e.Token(t, "listener"), []ereaderDevice{device("RootPad", "dup@example.com", e.IDs["listener"])})
				requireDevicesStatus(t, resp, http.StatusBadRequest, "duplicate name")
				equalJSON(t, "persisted devices", emailSettings(t, e), before)
			},
		},
		{
			Requirement: "route-r121", Invariant: "INV-R121-CONTRACT-001", Case: "edge", State: "changed", Assertion: "ereader-empty-list",
			Sources: []string{ereaderSource, emailSource, "client/pages/account.vue:289"},
			Claim:   "Posting an empty ereaderDevices array is valid: it removes every device of the caller, keeps the other user's device and answers 200 with an empty list.",
			Run: func(t *testing.T, e *harness.Env) {
				body := requireDevicesStatus(t, postDevices(t, e, e.Token(t, "listener"), []ereaderDevice{}), http.StatusOK, "clear devices")
				if body.EreaderDevices == nil || len(body.EreaderDevices) != 0 {
					t.Fatalf("clearing must return an empty list: %s", body.EreaderDevices)
				}
				settings := emailSettings(t, e).EreaderDevices
				if len(settings) != 1 || settings[0].Name != "RootPad" {
					t.Fatalf("only root's device may remain: %v", settings)
				}
			},
		},
		{
			Requirement: "socket-outgoing-ereader-devices-updated", Invariant: "INV-SOCKET-OUTGOING-EREADER-DEVICES-UPDATED", Case: "normal", State: "changed", Assertion: "ereader-event-admin-settings",
			Sources: []string{"server/routers/ApiRouter.js:280", "server/controllers/EmailController.js:64-85", "server/SocketAuthority.js:80-87"},
			Claim:   "The admin email-settings writer for the same event emits ereader-devices-updated with the complete, unfiltered stored device list to every admin socket (root and non-root admin) and to no ordinary user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				admin := e.Socket(t, adminToken(t, e))
				listener := e.Socket(t, e.Token(t, "listener"))
				rootMark, adminMark, listenerMark := root.Mark(), admin.Mark(), listener.Mark()
				shared := ereaderDevice{Name: "Shared", Email: "shared@example.com", AvailabilityOption: "userOrUp"}
				specific := device("Finisher", "finisher@example.com", finisherID)
				want := []ereaderDevice{shared, specific}
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": want})
				body := requireDevicesStatus(t, resp, http.StatusOK, "admin device settings")
				equalJSON(t, "response devices", body.EreaderDevices, want)
				equalJSON(t, "persisted devices", emailSettings(t, e).EreaderDevices, want)
				equalJSON(t, "root event", eventDevices(t, root.Wait(t, rootMark, "ereader-devices-updated", 5*time.Second, nil)).EreaderDevices, want)
				equalJSON(t, "admin event", eventDevices(t, admin.Wait(t, adminMark, "ereader-devices-updated", 5*time.Second, nil)).EreaderDevices, want)
				listener.None(t, listenerMark, "ereader-devices-updated", 500*time.Millisecond, nil)

				adminMark = admin.Mark()
				for _, body := range []any{
					map[string]any{},
					map[string]any{"ereaderDevices": "nope"},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "NoEmail"}}},
				} {
					wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", body), http.StatusBadRequest, "invalid admin device payload")
				}
				wantStatus(t, e.Post(t, e.Token(t, "listener"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": want}), http.StatusNotFound, "non-admin device settings")
				admin.None(t, adminMark, "ereader-devices-updated", 500*time.Millisecond, nil)
				equalJSON(t, "persisted devices after rejected writes", emailSettings(t, e).EreaderDevices, want)
			},
		},
	}
}
