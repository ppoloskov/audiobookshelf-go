package mail

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	sendEbookController = "server/controllers/EmailController.js:96-125"
	sendEbookManager    = "server/managers/EmailManager.js:39-63"
	deviceAccessRule    = "server/objects/settings/EmailSettings.js:136-160"
)

// sendScenarios are the route-r174 scenarios: sending a library item's ebook
// to a configured device, its access rules and its failure answers.
func sendScenarios() []harness.Scenario {
	const req = "route-r174"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R174-ROUTE", "normal", "initial", "send-ebook-admin-delivers-to-device",
			"POST /api/emails/send-ebook-to-device as admin delivers one 'Here is your Ebook!' message to the device address and answers 200 without changing the stored settings.",
			[]string{sendEbookController, sendEbookManager, "server/routers/ApiRouter.js:281"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "AdminTarget", "email": "admin-target@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				_, stamp, _ := storedEmailSettings(t, e)
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "AdminTarget"})
				wantStatus(t, "send ebook", resp, http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				if message.From != "library@example.test" || len(message.To) != 1 || message.To[0] != "admin-target@example.test" {
					t.Fatalf("envelope from=%q to=%v", message.From, message.To)
				}
				part := parseMessage(t, message.Message)
				if got := part.header(t, "Subject"); got != "Here is your Ebook!" {
					t.Fatalf("Subject = %q", got)
				}
				_, afterStamp, _ := storedEmailSettings(t, e)
				if afterStamp != stamp {
					t.Fatalf("sending the ebook rewrote the settings row: %q -> %q", stamp, afterStamp)
				}
			}),
		scenario(req, "INV-ROUTE-R174-ACCESS-001", "error", "failure", "send-ebook-preconditions-refuse-before-smtp",
			"The controller answers 404 for an unknown device, 404 for an item without an ebook, 403 for a device the user may not use (before the item lookup) and 401 without a token, with no SMTP connection and no settings change.",
			[]string{sendEbookController, deviceAccessRule, "server/routers/ApiRouter.js:281"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e,
					map[string]any{"name": "AdminsOnly", "email": "admins@example.test", "availabilityOption": "adminOrUp", "users": []any{}},
				)
				before, stamp, _ := storedEmailSettings(t, e)
				root, listener := e.Token(t, "root"), e.Token(t, "listener")
				cases := []struct {
					what   string
					token  string
					body   map[string]any
					status int
				}{
					{"unknown device wins over the user's rights", listener, map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "Missing"}, http.StatusNotFound},
					{"device access refused for a non-admin", listener, map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "AdminsOnly"}, http.StatusForbidden},
					{"access check precedes the item lookup", listener, map[string]any{"libraryItemId": "00000000-0000-0000-0000-000000000000", "deviceName": "AdminsOnly"}, http.StatusForbidden},
					{"unknown item", root, map[string]any{"libraryItemId": "00000000-0000-0000-0000-000000000000", "deviceName": "AdminsOnly"}, http.StatusNotFound},
					{"item without an ebook", root, map[string]any{"libraryItemId": e.IDs["novel"], "deviceName": "AdminsOnly"}, http.StatusNotFound},
				}
				for _, tc := range cases {
					resp := e.Post(t, tc.token, "/api/emails/send-ebook-to-device", tc.body)
					wantStatus(t, tc.what, resp, tc.status)
				}
				anonymous := e.Post(t, "", "/api/emails/send-ebook-to-device", map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "AdminsOnly"})
				wantStatus(t, "send ebook without token", anonymous, http.StatusUnauthorized)
				wantNoSMTPConnection(t, server, "refused send-ebook requests")
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before, after) || afterStamp != stamp {
					t.Fatalf("a refused send changed the settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R174-ACCESS-001", "error", "failure", "send-ebook-item-access-refused-for-user",
			"A user who may use the device but whose library or tag permissions exclude the book gets 403 from the item access check before any SMTP connection.",
			[]string{"server/controllers/EmailController.js:112-117", "server/models/User.js:644-683"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "EveryUser", "email": "every@example.test", "availabilityOption": "userOrUp", "users": []any{}})
				cases := []struct {
					what        string
					username    string
					permissions map[string]any
				}{
					{"without access to the book's library", "no-audiobooks", map[string]any{
						"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]},
					}},
					{"without access to any item tag", "no-tags", map[string]any{
						"accessAllTags": false, "itemTagsSelected": []string{}, "selectedTagsNotAccessible": false,
					}},
				}
				for _, tc := range cases {
					token := createRestrictedUser(t, e, tc.username, tc.permissions)
					resp := e.Post(t, token, "/api/emails/send-ebook-to-device",
						map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "EveryUser"})
					wantStatus(t, "send by a user "+tc.what, resp, http.StatusForbidden)
				}
				wantNoSMTPConnection(t, server, "refused item access")
			}),
		scenario(req, "INV-ROUTE-R174-ACCESS-001", "edge", "changed", "send-ebook-device-availability-for-users",
			"A non-admin may send to a userOrUp device and to a specificUsers device that lists them, and is refused 403 for a device restricted to someone else.",
			[]string{sendEbookController, deviceAccessRule, "client/components/cards/LazyBookCard.vue:811"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				listenerID := e.IDs["listener"]
				deviceScenario(t, e,
					map[string]any{"name": "EveryUser", "email": "every@example.test", "availabilityOption": "userOrUp", "users": []any{}},
					map[string]any{"name": "JustMe", "email": "justme@example.test", "availabilityOption": "specificUsers", "users": []any{listenerID}},
					map[string]any{"name": "SomeoneElse", "email": "else@example.test", "availabilityOption": "specificUsers", "users": []any{"00000000-0000-0000-0000-000000000000"}},
				)
				listener := e.Token(t, "listener")
				for _, device := range []string{"EveryUser", "JustMe"} {
					resp := e.Post(t, listener, "/api/emails/send-ebook-to-device", map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": device})
					wantStatus(t, "send to "+device, resp, http.StatusOK)
				}
				refused := e.Post(t, listener, "/api/emails/send-ebook-to-device", map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "SomeoneElse"})
				wantStatus(t, "send to another user's device", refused, http.StatusForbidden)
				messages := waitForMessages(t, server, 2)
				if len(messages) != 2 {
					t.Fatalf("delivered %d messages, want 2", len(messages))
				}
				for i, want := range []string{"every@example.test", "justme@example.test"} {
					if len(messages[i].To) != 1 || messages[i].To[0] != want {
						t.Fatalf("message %d to = %v, want %s", i, messages[i].To, want)
					}
				}
			}),
		scenario(req, "INV-ROUTE-R174-REPEAT", "edge", "repeat", "send-ebook-repeat-delivers-again",
			"Repeating the same send delivers a second identical-envelope message and leaves the stored settings unchanged.",
			[]string{sendEbookManager, emailSettingsRow},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "RepeatTarget", "email": "repeat@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				_, stamp, _ := storedEmailSettings(t, e)
				for i := 0; i < 2; i++ {
					resp := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
						map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "RepeatTarget"})
					wantStatus(t, "send ebook", resp, http.StatusOK)
				}
				messages := waitForMessages(t, server, 2)
				if messages[0].From != messages[1].From || !sameJSON(messages[0].To, messages[1].To) {
					t.Fatalf("repeat envelopes differ: %v vs %v", messages[0].To, messages[1].To)
				}
				_, afterStamp, _ := storedEmailSettings(t, e)
				if afterStamp != stamp {
					t.Fatalf("repeated sends rewrote the settings row: %q -> %q", stamp, afterStamp)
				}
			}),
		scenario(req, "INV-ROUTE-R174-ROUTE", "edge", "changed", "send-ebook-follows-changed-sender-and-device",
			"After the admin changes fromAddress and a device's email, the next send uses the new envelope sender and recipient.",
			[]string{sendEbookManager, emailSettingsObject, sendEbookController},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "old-sender@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "Changed", "email": "old-recipient@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				patchEmailSettings(t, e, map[string]any{"fromAddress": "new-sender@example.test"})
				deviceScenario(t, e, map[string]any{"name": "Changed", "email": "new-recipient@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "Changed"})
				wantStatus(t, "send ebook", resp, http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				if message.From != "new-sender@example.test" || len(message.To) != 1 || message.To[0] != "new-recipient@example.test" {
					t.Fatalf("changed envelope from=%q to=%v", message.From, message.To)
				}
			}),
		scenario(req, "INV-ROUTE-R174-FAILURE-001", "error", "failure", "send-ebook-smtp-failure-safe-for-user",
			"When SMTP verification fails, a non-admin's send answers 400 with the fixed verification message and the settings row is unchanged.",
			[]string{sendEbookManager, sendEbookController, "server/controllers/EmailController.js:133-137"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				server.Fail("greeting", "421 4.3.2 try later")
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "Failing", "email": "fail@example.test", "availabilityOption": "userOrUp", "users": []any{}})
				before, stamp, _ := storedEmailSettings(t, e)
				resp := e.Post(t, e.Token(t, "listener"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "Failing"})
				wantStatus(t, "send with SMTP down", resp, http.StatusBadRequest)
				if got := strings.TrimSpace(string(resp.Body)); got != "Failed to verify SMTP connection configuration" {
					t.Fatalf("user-facing error = %q, want the fixed message without SMTP detail", got)
				}
				if strings.Contains(string(resp.Body), "421") || strings.Contains(string(resp.Body), "try later") {
					t.Fatalf("error leaks the SMTP server reply: %q", resp.Body)
				}
				waitForConnections(t, server, 1)
				if messages := server.Messages(); len(messages) != 0 {
					t.Fatalf("a refused send transmitted %d messages", len(messages))
				}
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before, after) || afterStamp != stamp {
					t.Fatalf("a failed send changed the settings row")
				}
			}),
	}
}
