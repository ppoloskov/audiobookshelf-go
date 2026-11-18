package mail

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const ereaderDevicesEvent = "ereader-devices-updated"

// deviceScenarios are the route-r173 scenarios: replacing the ereader device
// list, its normalization, its event and its validation.
func deviceScenarios() []harness.Scenario {
	const req = "route-r173"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R173-EVENT-001", "normal", "initial", "devices-create-normalizes-and-events",
			"POST /api/emails/ereader-devices stores the posted list, returns it with an availabilityOption and emits ereader-devices-updated with the same list to admins only.",
			[]string{"server/controllers/EmailController.js:62-82", "server/objects/settings/EmailSettings.js:75-101", "server/controllers/EmailController.js:57-60"},
			func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, e.Token(t, "root"))
				defer admin.Close()
				other := e.Socket(t, e.Token(t, "listener"))
				defer other.Close()
				adminMark, otherMark := admin.Mark(), other.Mark()

				devices := []any{map[string]any{
					"name": "Kindle", "email": "kindle@example.test",
					"availabilityOption": "userOrUp", "users": []any{},
				}}
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": devices})
				wantStatus(t, "POST ereader devices", resp, http.StatusOK)
				wantKeys(t, "POST response", resp.Object(t), map[string]any{"ereaderDevices": devices})

				row, _, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row", row, map[string]any{"ereaderDevices": devices})

				event := admin.Wait(t, adminMark, ereaderDevicesEvent, 5*time.Second, nil)
				var payload struct {
					EreaderDevices []any `json:"ereaderDevices"`
				}
				if err := json.Unmarshal(event.Args[0], &payload); err != nil {
					t.Fatalf("decode %s payload: %v", ereaderDevicesEvent, err)
				}
				if !sameJSON(payload.EreaderDevices, devices) {
					t.Fatalf("event devices = %v, want %v", payload.EreaderDevices, devices)
				}
				other.None(t, otherMark, ereaderDevicesEvent, 500*time.Millisecond, nil)
			}),
		scenario(req, "INV-ROUTE-R173-ROUTE", "edge", "initial", "devices-normalization-and-replacement",
			"The route replaces the whole list, defaults a missing availabilityOption to adminOrUp, and clears users for a non-specificUsers option.",
			[]string{"server/objects/settings/EmailSettings.js:75-101", "server/controllers/EmailController.js:62-82"},
			func(t *testing.T, e *harness.Env) {
				listenerID := e.IDs["listener"]
				devices := []any{
					map[string]any{"name": "PocketBook", "email": "pocket@example.test"},
					map[string]any{"name": "Mine", "email": "mine@example.test", "availabilityOption": "specificUsers", "users": []any{listenerID}},
					map[string]any{"name": "Bogus", "email": "bogus@example.test", "availabilityOption": "bogus", "users": []any{listenerID}},
					map[string]any{"name": "NoUsers", "email": "nousers@example.test", "availabilityOption": "specificUsers", "users": []any{}},
				}
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": devices})
				wantStatus(t, "POST ereader devices", resp, http.StatusOK)
				want := []any{
					map[string]any{"name": "PocketBook", "email": "pocket@example.test", "availabilityOption": "adminOrUp"},
					map[string]any{"name": "Mine", "email": "mine@example.test", "availabilityOption": "specificUsers", "users": []any{listenerID}},
					map[string]any{"name": "Bogus", "email": "bogus@example.test", "availabilityOption": "adminOrUp", "users": []any{}},
					map[string]any{"name": "NoUsers", "email": "nousers@example.test", "availabilityOption": "adminOrUp", "users": []any{}},
				}
				wantKeys(t, "POST response", resp.Object(t), map[string]any{"ereaderDevices": want})
				row, _, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row", row, map[string]any{"ereaderDevices": want})
			}),
		scenario(req, "INV-ROUTE-R173-ROUTE", "edge", "changed", "devices-update-existing-and-clear",
			"Posting the list again with a changed address updates that device in place, and posting an empty list clears the stored devices and returns an empty array.",
			[]string{"server/controllers/EmailController.js:62-82", "server/objects/settings/EmailSettings.js:75-101"},
			func(t *testing.T, e *harness.Env) {
				devices := []any{map[string]any{"name": "Kindle", "email": "kindle@example.test", "availabilityOption": "adminOrUp", "users": []any{}}}
				deviceScenario(t, e, devices...)
				changed := []any{
					devices[0].(map[string]any),
					map[string]any{"name": "Mine", "email": "changed@example.test", "availabilityOption": "specificUsers", "users": []any{e.IDs["listener"]}},
				}
				changed[0].(map[string]any)["email"] = "kindle-new@example.test"
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": changed})
				wantStatus(t, "update devices", resp, http.StatusOK)
				wantKeys(t, "update response", resp.Object(t), map[string]any{"ereaderDevices": changed})
				row, _, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row", row, map[string]any{"ereaderDevices": changed})

				cleared := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": []any{}})
				wantStatus(t, "clear devices", cleared, http.StatusOK)
				wantKeys(t, "clear response", cleared.Object(t), map[string]any{"ereaderDevices": []any{}})
				row, _, _ = storedEmailSettings(t, e)
				wantKeys(t, "stored row after clear", row, map[string]any{"ereaderDevices": []any{}})
			}),
		scenario(req, "INV-ROUTE-R173-REPEAT", "edge", "repeat", "devices-repeat-no-write-or-event",
			"Posting the identical device list twice returns the same array and leaves the row updatedAt unchanged without a second event.",
			[]string{"server/controllers/EmailController.js:62-82", "server/utils/areEquivalent.js:24-120"},
			func(t *testing.T, e *harness.Env) {
				devices := []any{map[string]any{"name": "Repeat", "email": "repeat@example.test", "availabilityOption": "adminOrUp", "users": []any{}}}
				deviceScenario(t, e, devices...)
				_, stamp, _ := storedEmailSettings(t, e)
				socket := e.Socket(t, e.Token(t, "root"))
				defer socket.Close()
				mark := socket.Mark()
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": devices})
				wantStatus(t, "repeat devices", resp, http.StatusOK)
				wantKeys(t, "repeat response", resp.Object(t), map[string]any{"ereaderDevices": devices})
				row, afterStamp, _ := storedEmailSettings(t, e)
				wantKeys(t, "stored row", row, map[string]any{"ereaderDevices": devices})
				if afterStamp != stamp {
					t.Fatalf("repeat rewrote the row: updatedAt %q -> %q", stamp, afterStamp)
				}
				socket.None(t, mark, ereaderDevicesEvent, 750*time.Millisecond, nil)
			}),
		scenario(req, "INV-ROUTE-R173-VALIDATION-001", "error", "failure", "devices-validation-rejects-no-write",
			"Missing or non-array ereaderDevices, an item without name or email, and every non-admin caller are rejected and leave the stored devices, updatedAt and event stream unchanged.",
			[]string{"server/controllers/EmailController.js:62-82", "server/controllers/EmailController.js:133-137", "server/Server.js:354"},
			func(t *testing.T, e *harness.Env) {
				devices := []any{map[string]any{"name": "Kept", "email": "kept@example.test", "availabilityOption": "adminOrUp", "users": []any{}}}
				deviceScenario(t, e, devices...)
				before, stamp, _ := storedEmailSettings(t, e)
				socket := e.Socket(t, e.Token(t, "root"))
				defer socket.Close()
				mark := socket.Mark()
				for _, body := range []any{
					map[string]any{},
					map[string]any{"ereaderDevices": "not-an-array"},
					map[string]any{"ereaderDevices": []any{map[string]any{"name": "NoEmail"}}},
					map[string]any{"ereaderDevices": []any{map[string]any{"email": "no-name@example.test"}}},
				} {
					wantStatus(t, "invalid payload", e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", body), http.StatusBadRequest)
				}
				adminOnly(t, e, http.MethodPost, "/api/emails/ereader-devices", map[string]any{"ereaderDevices": []any{}})
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before["ereaderDevices"], after["ereaderDevices"]) {
					t.Fatalf("a rejected request changed the devices: %v vs %v", before["ereaderDevices"], after["ereaderDevices"])
				}
				if afterStamp != stamp {
					t.Fatalf("a rejected request rewrote the row: updatedAt %q -> %q", stamp, afterStamp)
				}
				socket.None(t, mark, ereaderDevicesEvent, 750*time.Millisecond, nil)
			}),
	}
}

// deviceScenario stores exactly the given devices through the admin route.
func deviceScenario(t *testing.T, e *harness.Env, devices ...any) {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/emails/ereader-devices", map[string]any{"ereaderDevices": devices})
	wantStatus(t, "store ereader devices", resp, http.StatusOK)
}
