package mail

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	flowManager   = "server/managers/EmailManager.js:39-63"
	flowTransport = "server/objects/settings/EmailSettings.js:115-134"
)

// flowScenarios are the cap-flow-mail-smtp-mime scenarios: the SMTP envelope,
// the MIME tree, TLS/authentication, failure reporting and cancellation of the
// delivery flows.
func flowScenarios() []harness.Scenario {
	const req = "cap-flow-mail-smtp-mime"
	return []harness.Scenario{
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME", "normal", "stateless", "flow-ebook-envelope",
			"Sending the fixture ebook verifies the connection on one connection and then delivers one message on a second connection whose MAIL FROM is the configured fromAddress and whose RCPT TO is the device email.",
			[]string{flowManager, flowTransport, "server/managers/EmailManager.js:12-37"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "FlowTarget", "email": "flow@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "FlowTarget"})
				wantStatus(t, "send ebook", resp, http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				if connections := server.Connections(); connections != 2 {
					t.Fatalf("want one verification and one delivery connection, got %d: %+v", connections, server.Sessions())
				}
				if verify := server.Sessions()[0]; verify.Message != "" || verify.From != "" {
					t.Fatalf("first connection must only verify the config: %+v", verify)
				}
				if message.From != "library@example.test" || len(message.To) != 1 || message.To[0] != "flow@example.test" {
					t.Fatalf("envelope from=%q to=%v", message.From, message.To)
				}
			}),
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME-MIME-001", "normal", "stateless", "flow-ebook-mime-attachment",
			"The ebook message is multipart/mixed with an html body part and a base64 application/epub+zip attachment named after the ebook file whose decoded bytes equal the file on disk.",
			[]string{flowManager, "server/managers/EmailManager.js:47-56"},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "MimeTarget", "email": "mime@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				resp := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "MimeTarget"})
				wantStatus(t, "send ebook", resp, http.StatusOK)
				message := waitForMessages(t, server, 1)[0]
				part := parseMessage(t, message.Message)
				if got := part.header(t, "Subject"); got != "Here is your Ebook!" {
					t.Fatalf("Subject = %q", got)
				}
				if got := part.header(t, "From"); got != "library@example.test" {
					t.Fatalf("From header = %q", got)
				}
				if got := part.header(t, "To"); got != "mime@example.test" {
					t.Fatalf("To header = %q", got)
				}
				if got := part.mediaType(t); got != "multipart/mixed" {
					t.Fatalf("root media type = %q, want multipart/mixed", got)
				}
				if len(part.Children) != 2 {
					t.Fatalf("multipart/mixed has %d parts, want 2: %s", len(part.Children), describeParts(part))
				}
				html := part.Children[0]
				if got := html.mediaType(t); got != "text/html" {
					t.Fatalf("first part media type = %q, want text/html", got)
				}
				if got := strings.TrimRight(string(html.Body), "\r\n"); got != `<div dir="auto"></div>` {
					t.Fatalf("html body = %q", got)
				}
				attachment := part.Children[1]
				if got := attachment.mediaType(t); got != "application/epub+zip" {
					t.Fatalf("attachment media type = %q, want application/epub+zip", got)
				}
				if got := attachment.filename(t); got != "Ebook Only.epub" {
					t.Fatalf("attachment filename = %q", got)
				}
				if disposition := attachment.Header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
					t.Fatalf("attachment disposition = %q", disposition)
				}
				if !bytes.Equal(attachment.Body, wantEbookFile(t, e)) {
					t.Fatalf("attachment body differs from %s (%d vs %d bytes)", ebookRelPath, len(attachment.Body), len(wantEbookFile(t, e)))
				}
			}),
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME-TRANSPORT-001", "edge", "stateless", "flow-starttls-uses-reject-unauthorized",
			"Against a server that advertises STARTTLS, rejectUnauthorized true fails verification with the fixed message, rejectUnauthorized false upgrades the connection (EHLO, STARTTLS, EHLO) and delivers the message over TLS.",
			[]string{flowTransport, "server/objects/settings/EmailSettings.js:115-134", flowManager},
			func(t *testing.T, e *harness.Env) {
				server := startSTARTTLSSMTP(t)
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				patchEmailSettings(t, e, map[string]any{"secure": true, "rejectUnauthorized": true})
				untrusted := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "test email with an untrusted certificate", untrusted, http.StatusBadRequest)
				if got := strings.TrimSpace(string(untrusted.Body)); got != "Failed to verify SMTP connection configuration" {
					t.Fatalf("untrusted-certificate error = %q", got)
				}
				patchEmailSettings(t, e, map[string]any{"secure": true, "rejectUnauthorized": false})
				trusted := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "test email with rejectUnauthorized false", trusted, http.StatusOK)
				messages := waitForMessages(t, server, 1)
				if !messages[0].TLS {
					t.Fatalf("delivered message did not use STARTTLS: %+v", messages[0])
				}
			}),
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME-TRANSPORT-001", "edge", "stateless", "flow-auth-plain-credentials",
			"With a user and password configured, both the verification and the delivery connection authenticate with AUTH PLAIN carrying exactly the stored credentials before MAIL FROM.",
			[]string{flowTransport, flowManager},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				payload := smtpSettings(server.Port(), "library@example.test", "admin@example.test")
				payload["user"], payload["pass"] = "smtp-user", "smtp-pass"
				patchEmailSettings(t, e, payload)
				wantStatus(t, "test email", e.Post(t, e.Token(t, "root"), "/api/emails/test", nil), http.StatusOK)
				waitForMessages(t, server, 1)
				const want = "\x00smtp-user\x00smtp-pass"
				for _, session := range server.Sessions() {
					if session.Auth != want {
						t.Fatalf("AUTH PLAIN credentials = %q, want the configured user/pass: %v", session.Auth, session.Commands)
					}
				}
			}),
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME-FAILURE-001", "error", "stateless", "flow-message-rejection-is-not-success",
			"When the SMTP server rejects the message after DATA, both the test route and the send-ebook route answer 400 and never the success status.",
			[]string{"server/managers/EmailManager.js:12-37", flowManager},
			func(t *testing.T, e *harness.Env) {
				server := startSMTP(t)
				server.Fail("end", "554 5.7.1 rejected by policy")
				useSMTP(t, e, server.Port(), "library@example.test", "admin@example.test")
				deviceScenario(t, e, map[string]any{"name": "Rejected", "email": "rejected@example.test", "availabilityOption": "adminOrUp", "users": []any{}})
				test := e.Post(t, e.Token(t, "root"), "/api/emails/test", nil)
				wantStatus(t, "test email rejected after DATA", test, http.StatusBadRequest)
				if string(test.Body) == "OK" {
					t.Fatalf("a rejected message was reported as sent")
				}
				send := e.Post(t, e.Token(t, "root"), "/api/emails/send-ebook-to-device",
					map[string]any{"libraryItemId": e.IDs["ebook"], "deviceName": "Rejected"})
				wantStatus(t, "ebook rejected after DATA", send, http.StatusBadRequest)
				if messages := server.Messages(); len(messages) != 0 {
					t.Fatalf("the stub accepted %d messages it rejected", len(messages))
				}
			}),
		scenario(req, "INV-CAP-FLOW-MAIL-SMTP-MIME-FAILURE-001", "error", "stateless", "flow-cancel-leaves-server-usable",
			"A client that cancels while the SMTP server never answers gets no success response, and the server still answers the settings route and a later test email.",
			[]string{flowManager, "server/controllers/EmailController.js:43-48"},
			func(t *testing.T, e *harness.Env) {
				hung := startSMTP(t)
				hung.Hang("greeting")
				useSMTP(t, e, hung.Port(), "library@example.test", "admin@example.test")
				_, _, err := postWithin(t, e, e.Token(t, "root"), "/api/emails/test", nil, 300*time.Millisecond)
				if err == nil {
					t.Fatal("a test email against a silent SMTP server returned a response, want a client timeout")
				}
				wantStatus(t, "settings after the cancelled request", e.Get(t, e.Token(t, "root"), "/api/emails/settings"), http.StatusOK)

				stopped := startSMTP(t)
				stopped.Hang("end")
				useSMTP(t, e, stopped.Port(), "library@example.test", "admin@example.test")
				_, _, err = postWithin(t, e, e.Token(t, "root"), "/api/emails/test", nil, 300*time.Millisecond)
				if err == nil {
					t.Fatal("a test email whose DATA is never acknowledged returned a response, want a client timeout")
				}
				if messages := stopped.Messages(); len(messages) != 0 {
					t.Fatalf("a cancelled request produced %d accepted messages", len(messages))
				}

				healthy := startSMTP(t)
				useSMTP(t, e, healthy.Port(), "library@example.test", "admin@example.test")
				wantStatus(t, "test email after cancellations", e.Post(t, e.Token(t, "root"), "/api/emails/test", nil), http.StatusOK)
				waitForMessages(t, healthy, 1)
			}),
	}
}
