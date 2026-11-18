package mail

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	testMailManager   = "server/managers/EmailManager.js:12-37"
	testMailRoute     = "server/routers/ApiRouter.js:279"
	transportSettings = "server/objects/settings/EmailSettings.js:115-134"
)

// testMailScenarios are the route-r172 scenarios: the SMTP test-email route
// and the message it sends.
func testMailScenarios() []harness.Scenario {
	const req = "route-r172"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R172-ROUTE", "normal", "initial", "test-email-delivers-text-message",
			"POST /api/emails/test as admin verifies the connection, sends one text/plain 'Success!' message to the test address and answers 200.",
			[]string{testMailRoute, testMailManager, transportSettings},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "Library <library@example.test>", "test-inbox@example.test")
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "POST /api/emails/test", resp, http.StatusOK)
				if string(resp.Body) != "OK" {
					t.Fatalf("test email body = %q, want OK", resp.Body)
				}
				messages := waitForMessages(t, server, 1)
				sessions := server.Sessions()
				if len(sessions) != 2 || server.Connections() != 2 {
					t.Fatalf("want one verify and one delivery connection, got %d: %v", server.Connections(), sessions)
				}
				if sessions[0].Message != "" || sessions[0].From != "" {
					t.Fatalf("first connection must only verify the config: %+v", sessions[0])
				}
				message := messages[0]
				if message.From != "library@example.test" || len(message.To) != 1 || message.To[0] != "test-inbox@example.test" {
					t.Fatalf("envelope from=%q to=%v", message.From, message.To)
				}
				part := parseMessage(t, message.Message)
				if got := part.header(t, "From"); got != "Library <library@example.test>" {
					t.Fatalf("From header = %q", got)
				}
				if got := part.header(t, "To"); got != "test-inbox@example.test" {
					t.Fatalf("To header = %q", got)
				}
				if got := part.header(t, "Subject"); got != "Test email from Audiobookshelf" {
					t.Fatalf("Subject = %q", got)
				}
				if got := part.header(t, "MIME-Version"); got != "1.0" {
					t.Fatalf("MIME-Version = %q", got)
				}
				if got := part.mediaType(t); got != "text/plain" {
					t.Fatalf("media type = %q, want text/plain", got)
				}
				if got := strings.TrimRight(string(part.Body), "\r\n"); got != "Success!" {
					t.Fatalf("body = %q, want Success!", got)
				}
				walkParts(part, func(child *mailPart) {
					if child.Header.Get("Content-Disposition") != "" {
						t.Fatalf("test email must have no attachment: %v", child.Header)
					}
				})
			}),
		scenario(req, "INV-ROUTE-R172-ROUTE", "edge", "initial", "test-email-test-address-falls-back-to-sender",
			"With no testAddress, POST /api/emails/test sends the message to the configured fromAddress.",
			[]string{testMailManager, transportSettings},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "")
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "POST /api/emails/test", resp, http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				if message.From != "library@example.test" || len(message.To) != 1 || message.To[0] != "library@example.test" {
					t.Fatalf("fallback envelope from=%q to=%v", message.From, message.To)
				}
				part := parseMessage(t, message.Message)
				if got := part.header(t, "To"); got != "library@example.test" {
					t.Fatalf("To header = %q", got)
				}
			}),
		scenario(req, "INV-ROUTE-R172-ROUTE", "error", "failure", "test-email-admin-only-no-smtp",
			"POST /api/emails/test answers 401 without a token and 404 for a non-admin, opens no SMTP connection and leaves the settings row unchanged.",
			[]string{testMailRoute, "server/controllers/EmailController.js:133-137", "server/controllers/EmailController.js:43-48", "server/Server.js:354"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "test@example.test")
				before, stamp, _ := storedEmailSettings(t, e)
				adminOnly(t, e, http.MethodPost, "/api/emails/test", nil)
				wantNoSMTPConnection(t, server, "refused test email")
				after, afterStamp, _ := storedEmailSettings(t, e)
				if !sameJSON(before, after) || afterStamp != stamp {
					t.Fatalf("a refused test email changed the settings row")
				}
			}),
		scenario(req, "INV-ROUTE-R172-ROUTE", "error", "initial", "test-email-unreachable-smtp-fixed-error",
			"When the SMTP host refuses the connection, POST /api/emails/test answers 400 with the fixed verification message and does not expose the host or port.",
			[]string{testMailManager, "server/controllers/EmailController.js:43-48"},
			func(t *testing.T, e *harness.Env) {
				port := closedPort(t)
				useSMTP(t, e, port, "library@example.test", "test@example.test")
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "POST /api/emails/test with SMTP down", resp, http.StatusBadRequest)
				if got := strings.TrimSpace(string(resp.Body)); got != "Failed to verify SMTP connection configuration" {
					t.Fatalf("error body = %q, want the fixed verification message", got)
				}
				if strings.Contains(string(resp.Body), "127.0.0.1") || strings.Contains(string(resp.Body), strconv.Itoa(port)) {
					t.Fatalf("error body leaks the SMTP host or port: %q", resp.Body)
				}
			}),
		scenario(req, "INV-ROUTE-R172-REPEAT", "edge", "repeat", "test-email-repeat-sends-again",
			"Sending the test email twice delivers one message per request, leaves the settings row unchanged and answers 200 both times.",
			[]string{testMailManager},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "test@example.test")
				_, stamp, _ := storedEmailSettings(t, e)
				for i := 0; i < 2; i++ {
					wantStatus(t, "POST /api/emails/test", e.Post(t, e.Token(t, "root"), "/api/emails/test", nil), http.StatusOK)
				}
				waitForMessages(t, server, 2)
				messages := server.Messages()
				if len(messages) != 2 {
					t.Fatalf("two requests delivered %d messages, want 2", len(messages))
				}
				if messages[0].From != messages[1].From || !sameJSON(messages[0].To, messages[1].To) {
					t.Fatalf("repeat envelopes differ: %v vs %v", messages[0].To, messages[1].To)
				}
				_, afterStamp, _ := storedEmailSettings(t, e)
				if afterStamp != stamp {
					t.Fatalf("sending email rewrote the settings row: %q -> %q", stamp, afterStamp)
				}
			}),
		scenario(req, "INV-ROUTE-R172-ROUTE", "edge", "changed", "test-email-follows-changed-test-address",
			"After the admin changes testAddress, the next POST /api/emails/test delivers to the new address.",
			[]string{testMailManager, emailSettingsObject},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "first@example.test")
				patchEmailSettings(t, e, map[string]any{"testAddress": "second@example.test"})
				wantStatus(t, "POST /api/emails/test", e.Post(t, e.Token(t, "root"), "/api/emails/test", nil), http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				if len(message.To) != 1 || message.To[0] != "second@example.test" {
					t.Fatalf("repeat envelope to = %v, want the changed test address", message.To)
				}
			}),
	}
}

// closedPort returns a port nothing listens on; a dial is refused immediately.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a closed port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the closed port: %v", err)
	}
	return port
}
