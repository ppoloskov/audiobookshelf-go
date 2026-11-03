package contracttest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestJourneyAdmin is the admin domain's journey (ROADMAP F8a): the client
// routes for API keys, auth and server settings, backups, the cache, email,
// notifications, tasks and logs, cron validation, the filesystem, uploads,
// users, custom metadata providers and the tools. It runs on its own fresh
// install and is recorded into contracts/shapes/admin.json, the same way as
// TestJourney (journey_test.go). To record it against TS:
//
//	node go-backend/tools/contractfixture/serve-upstream.cjs   # prints ROOT and READY
//	cd go-backend && go test ./internal/contracttest -run '^TestJourneyAdmin$' -record \
//	    -upstream=<READY URL> -upstream-root=<ROOT>
//
// then stop the serve script.
const adminShapesFile = "../../contracts/shapes/admin.json"

func TestJourneyAdmin(t *testing.T) {
	smtp := startAdminSMTPServer(t)
	runJourney(t, adminShapesFile, nil, func(t *testing.T, in *Install, c *journeyCtx) []journeyStep {
		return journeyAdminSteps(t, in, c, smtp)
	})
	// The send step must have delivered the ebook, whether TS or Go answered
	// it: the local server records a message before it acknowledges DATA.
	smtp.checkEbookDelivered(t, "trial-kindle@example.com", filepath.Join(fixtureDir, "media", "audiobooks", "Ada Example", "Ebook Only", "Ebook Only.epub"))
}

// journeyAdminSteps is the admin journey, in order. Steps create what later
// steps change or delete, so the order must not change. Admin-only routes get
// a 403 step as the listener. The bodies are the ones the web client sends.
// smtp is the local SMTP server the send-ebook steps deliver to.
func journeyAdminSteps(t *testing.T, in *Install, c *journeyCtx, smtp *adminSMTP) []journeyStep {
	ids := in.IDs
	// The notification and image URLs are local servers; the steps never reach
	// the network (AGENTS.md: SSRF whitelist for 127.0.0.1).
	apprise := startAdminAppriseServer(t)
	provider := c.fixtureURL + "/search"
	upload, err := os.ReadFile(filepath.Join(fixtureDir, "media", "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3"))
	if err != nil {
		t.Fatalf("read upload sample: %v", err)
	}
	libraryFolder := func(c *journeyCtx, field string) string { return c.s("lib", "folders", 0, field) }
	rootID := func(c *journeyCtx) string { return c.find("usersList", "users", "username", "trial-root", "id") }
	createdUser := func(c *journeyCtx) string { return c.s("createdUser", "user", "id") }
	apiKeyID := func(c *journeyCtx) string { return c.s("apiKey", "apiKey", "id") }
	notificationID := func(c *journeyCtx) string {
		return c.find("notifCreated", "notifications", "titleTemplate", "Trial title", "id")
	}
	backupID := func(c *journeyCtx) string { return c.s("backupCreated", "backups", 0, "id") }
	backupFile := func(c *journeyCtx) string { return c.s("backupCreated", "backups", 0, "filename") }
	saga1, saga2, novel := ids["saga1"], ids["saga2"], ids["novel"]
	listenerID := func(c *journeyCtx) string { return c.listenerID }

	return []journeyStep{
		// Sign in. The listener is the non-admin account for the 403 steps.
		{id: "login.root", method: "POST", route: "/login", body: in.Users["root"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "root"},
		{id: "login.listener", method: "POST", route: "/login", body: in.Users["listener"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"},

		// Admin-only routes refuse the listener.
		{id: "forbidden.apikeys.list", who: "listener", route: "/api/api-keys"},
		{id: "forbidden.backups.list", who: "listener", route: "/api/backups"},
		{id: "forbidden.emails.settings", who: "listener", route: "/api/emails/settings"},
		{id: "forbidden.notifications.list", who: "listener", route: "/api/notifications"},
		{id: "forbidden.providers.list", who: "listener", route: "/api/custom-metadata-providers"},
		{id: "forbidden.settings.patch", who: "listener", method: "PATCH", route: "/api/settings", body: map[string]any{"scannerCoverProvider": "google"}},
		{id: "forbidden.embed", who: "listener", method: "POST", route: "/api/tools/item/" + saga2 + "/embed-metadata"},
		{id: "forbidden.users.create", who: "listener", method: "POST", route: "/api/users"},

		// Reads the later steps take ids from.
		{id: "lib.get", who: "root", route: "/api/libraries/" + ids["library"], capture: "lib"},
		{id: "users.list", who: "root", route: "/api/users", capture: "usersList"},

		// Authentication settings: read, then send the read back with the
		// authentication methods the client sends.
		{id: "auth.get", who: "root", route: "/api/auth-settings", capture: "authSettings"},
		{id: "auth.patch", who: "root", method: "PATCH", route: "/api/auth-settings", bodyFn: func(c *journeyCtx) any {
			body := copyCaptured(c, "authSettings")
			body["authActiveAuthMethods"] = []any{"local"}
			return body
		}},

		// API keys: create for root, change, delete.
		{id: "apikeys.list.before", who: "root", route: "/api/api-keys"},
		{id: "apikeys.create", who: "root", method: "POST", route: "/api/api-keys", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"name": "trial-admin-key", "userId": rootID(c), "isActive": true}
		}, capture: "apiKey"},
		{id: "apikeys.list.after", who: "root", route: "/api/api-keys"},
		{id: "apikeys.update", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/api-keys/" + apiKeyID(c) }, bodyFn: func(c *journeyCtx) any {
			return map[string]any{"isActive": false, "userId": rootID(c)}
		}},
		{id: "apikeys.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/api-keys/" + apiKeyID(c) }},

		// Users: create, change, read listening stats, unlink OpenID, delete.
		{id: "users.create", who: "root", method: "POST", route: "/api/users", bodyFn: func(c *journeyCtx) any {
			body := adminUserBody("trial-admin-created")
			body["password"] = "trial-created-password"
			return body
		}, capture: "createdUser"},
		{id: "forbidden.users.update", who: "listener", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/users/" + createdUser(c) }, body: adminUserBody("trial-admin-created")},
		{id: "users.update", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/users/" + createdUser(c) }, body: adminUserBody("trial-admin-renamed")},
		{id: "users.listening-stats", who: "root", routeFn: func(c *journeyCtx) string { return "/api/users/" + listenerID(c) + "/listening-stats" }},
		{id: "users.openid-unlink", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/users/" + listenerID(c) + "/openid-unlink" }},
		{id: "users.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/users/" + createdUser(c) }},

		// Server settings and sorting prefixes.
		{id: "settings.patch", who: "root", method: "PATCH", route: "/api/settings", body: map[string]any{"scannerCoverProvider": "google"}},
		{id: "sorting-prefixes.patch", who: "root", method: "PATCH", route: "/api/sorting-prefixes", body: map[string]any{"sortingPrefixes": []any{"the", "a", "an"}}},

		// Cache.
		{id: "cache.purge", who: "root", method: "POST", route: "/api/cache/purge"},
		{id: "cache.items.purge", who: "root", method: "POST", route: "/api/cache/items/purge"},

		// Email: read, save the form's fields, test with no SMTP host, and add
		// then remove an e-reader device the way the device modal does.
		{id: "emails.settings.get", who: "root", route: "/api/emails/settings", capture: "emailSettings"},
		{id: "emails.settings.patch", who: "root", method: "PATCH", route: "/api/emails/settings", bodyFn: func(c *journeyCtx) any {
			return pickCaptured(c, "emailSettings", "settings", "host", "port", "secure", "rejectUnauthorized", "user", "pass", "testAddress", "fromAddress")
		}},
		{id: "emails.test", who: "root", method: "POST", route: "/api/emails/test"},
		{id: "emails.ereader.add", who: "root", method: "POST", route: "/api/emails/ereader-devices", body: map[string]any{"ereaderDevices": []any{
			map[string]any{"name": "trial-kindle", "email": "trial-kindle@example.com", "availabilityOption": "adminAndUp", "users": []any{}},
		}}},
		{id: "emails.ereader.remove", who: "root", method: "POST", route: "/api/emails/ereader-devices", body: map[string]any{"ereaderDevices": []any{}}},

		// Send the ebook item's ebook to a device: the SMTP settings point at
		// the local server, the device is added again, and the send delivers
		// one message to it. The device is removed after.
		{id: "emails.settings.smtp", who: "root", method: "PATCH", route: "/api/emails/settings", body: map[string]any{
			"host": "127.0.0.1", "port": smtp.port, "secure": false, "rejectUnauthorized": true, "fromAddress": "abs@example.test",
		}},
		{id: "emails.ereader.add.send", who: "root", method: "POST", route: "/api/emails/ereader-devices", body: map[string]any{"ereaderDevices": []any{
			map[string]any{"name": "trial-kindle", "email": "trial-kindle@example.com", "availabilityOption": "adminOrUp", "users": []any{}},
		}}},
		{id: "emails.send-ebook", who: "root", method: "POST", route: "/api/emails/send-ebook-to-device", body: map[string]any{
			"libraryItemId": ids["ebook"], "deviceName": "trial-kindle",
		}},
		{id: "emails.ereader.remove.after-send", who: "root", method: "POST", route: "/api/emails/ereader-devices", body: map[string]any{"ereaderDevices": []any{}}},

		// Notifications: the Apprise URL and queue limits, one notification
		// created, enabled and updated, then tested (against the local server)
		// and deleted.
		{id: "notifications.get", who: "root", route: "/api/notifications"},
		{id: "notifications.settings.patch", who: "root", method: "PATCH", route: "/api/notifications", body: map[string]any{"appriseApiUrl": apprise, "maxNotificationQueue": 20, "maxFailedAttempts": 5}},
		{id: "notifications.create", who: "root", method: "POST", route: "/api/notifications", body: map[string]any{
			"libraryId": nil, "eventName": "onTest", "urls": []any{"json://127.0.0.1/hook"}, "titleTemplate": "Trial title", "bodyTemplate": "Trial body", "enabled": true, "type": nil,
		}, capture: "notifCreated"},
		{id: "notifications.enable", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/notifications/" + notificationID(c) }, bodyFn: func(c *journeyCtx) any {
			return map[string]any{"id": notificationID(c), "enabled": true}
		}},
		{id: "notifications.update", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/notifications/" + notificationID(c) }, bodyFn: func(c *journeyCtx) any {
			return map[string]any{"id": notificationID(c), "libraryId": nil, "eventName": "onTest", "urls": []any{"json://127.0.0.1/hook"},
				"titleTemplate": "Trial title updated", "bodyTemplate": "Trial body", "enabled": true, "type": nil}
		}},
		{id: "notifications.test.one", who: "root", routeFn: func(c *journeyCtx) string { return "/api/notifications/" + notificationID(c) + "/test" }},
		{id: "notifications.test.all", who: "root", route: "/api/notifications/test?fail=0"},
		{id: "notifications.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/notifications/" + notificationID(c) }},

		// Cron validation, logs and the filesystem.
		{id: "validate-cron.valid", who: "root", method: "POST", route: "/api/validate-cron", body: map[string]any{"expression": "0 3 * * 1"}},
		{id: "validate-cron.invalid", who: "root", method: "POST", route: "/api/validate-cron", body: map[string]any{"expression": "every tuesday"}},
		{id: "logger.get", who: "root", route: "/api/logger-data"},
		{id: "filesystem.get", who: "root", route: "/api/filesystem?path=" + url.QueryEscape(in.Dir+"/media") + "&level=0"},
		{id: "filesystem.pathexists", who: "root", method: "POST", route: "/api/filesystem/pathexists", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"directory": "Trial Uploader", "folderPath": libraryFolder(c, "fullPath")}
		}},

		// Upload one small audio file into the library folder.
		{id: "upload.file", who: "root", method: "POST", route: "/api/upload", settle: true, bodyFn: func(c *journeyCtx) any {
			return multipartForm(t, [][2]string{
				{"title", "Trial Upload Book"},
				{"author", "Trial Uploader"},
				{"series", ""},
				{"library", ids["library"]},
				{"folder", libraryFolder(c, "id")},
			}, formFile{field: "0", name: "Trial Upload Book.mp3", data: upload})
		}},
		{id: "filesystem.pathexists.after", who: "root", method: "POST", route: "/api/filesystem/pathexists", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"directory": "Trial Uploader/Trial Upload Book", "folderPath": libraryFolder(c, "fullPath")}
		}},

		// Custom metadata providers: list, create, delete.
		{id: "providers.list.before", who: "root", route: "/api/custom-metadata-providers"},
		{id: "providers.create", who: "root", method: "POST", route: "/api/custom-metadata-providers", body: map[string]any{
			"name": "Trial Provider", "url": provider, "mediaType": "book", "authHeaderValue": "",
		}, capture: "providerCreated"},
		{id: "providers.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string {
			return "/api/custom-metadata-providers/" + c.s("providerCreated", "provider", "id")
		}},

		// Tools: embed metadata (quick and batch) and an m4b merge, cancelled
		// while it runs. The embed steps wait for their task to finish; the
		// merge waits for its task to start.
		{id: "embed.quick", who: "root", method: "POST", route: "/api/tools/item/" + saga2 + "/embed-metadata", taskFinished: "embed-metadata",
			touches: "media/audiobooks/Terry Fixture/Long Saga/Book 2 - The Middle/The Middle.flac"},
		{id: "embed.batch", who: "root", method: "POST", route: "/api/tools/batch/embed-metadata", body: map[string]any{"libraryItemIds": []any{novel}}, taskFinished: "embed-metadata",
			touches: "media/audiobooks/Ada Example/Single File Novel/Single File Novel.m4b"},
		{id: "encode.start", who: "root", method: "POST", route: "/api/tools/item/" + saga1 + "/encode-m4b?codec=aac&bitrate=128k&channels=2", taskStarted: "encode-m4b"},
		// The queue still holds the running merge here.
		{id: "tasks.get", who: "root", route: "/api/tasks?include=queue"},
		{id: "encode.cancel", who: "root", method: "DELETE", route: "/api/tools/item/" + saga1 + "/encode-m4b", retry: 20, taskFinished: "encode-m4b"},
		// The merge runs to completion with the cover: the m4b it writes plays.
		{id: "encode.run", who: "root", method: "POST", route: "/api/tools/item/" + saga1 + "/encode-m4b?codec=aac&bitrate=128k&channels=2", taskFinished: "encode-m4b",
			produces: "media/audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/Book 1 - The Beginning.m4b"},

		// Backups: create, list, download, delete the original, upload the
		// downloaded file back under its own name (the backup id comes from the
		// file, so the original must be gone first), move the backup path and
		// back, then apply the backup. Apply is last: it restores the database.
		{id: "backups.get.before", who: "root", route: "/api/backups", capture: "backupsBefore"},
		{id: "backups.create", who: "root", method: "POST", route: "/api/backups", capture: "backupCreated"},
		{id: "backups.get.after-create", who: "root", route: "/api/backups"},
		{id: "backups.download", who: "root", routeFn: func(c *journeyCtx) string { return "/api/backups/" + backupID(c) + "/download" }, tokenQuery: true, capture: "backupZip"},
		{id: "backups.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/backups/" + backupID(c) }},
		{id: "backups.upload", who: "root", method: "POST", route: "/api/backups/upload", bodyFn: func(c *journeyCtx) any {
			return multipartForm(t, nil, formFile{field: "file", name: backupFile(c), data: []byte(c.s("backupZip"))})
		}, capture: "backupUploaded"},
		{id: "backups.path.set", who: "root", method: "PATCH", route: "/api/backups/path", body: map[string]any{"path": in.Dir + "/backups-trial"}},
		{id: "backups.path.restore", who: "root", method: "PATCH", route: "/api/backups/path", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"path": c.s("backupsBefore", "backupLocation")}
		}},
		{id: "backups.apply", who: "root", routeFn: func(c *journeyCtx) string { return "/api/backups/" + backupID(c) + "/apply" }},
	}
}

// adminPermissions is the permission set the client's new-account form starts
// from.
func adminPermissions() map[string]any {
	return map[string]any{
		"download": true, "update": false, "delete": false, "upload": false,
		"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": false,
		"selectedTagsNotAccessible": false, "createEreader": false,
	}
}

// adminUserBody is the account the client's AccountModal sends for a regular
// user with the default permissions. The create step adds a password.
func adminUserBody(username string) map[string]any {
	return map[string]any{
		"username": username, "email": nil, "type": "user", "isActive": true,
		"permissions": adminPermissions(), "librariesAccessible": []any{}, "itemTagsSelected": []any{},
	}
}

// copyCaptured returns a copy of the object captured under name, so a step can
// change fields without changing the capture.
func copyCaptured(c *journeyCtx, name string) map[string]any {
	out := map[string]any{}
	if m, ok := c.v(name).(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// pickCaptured returns the fields of the object at c.v(name, key), as the
// client's form sends them.
func pickCaptured(c *journeyCtx, name, key string, fields ...string) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		out[f] = c.v(name, key, f)
	}
	return out
}

// adminSMTP is a local SMTP server for the send-ebook steps. It takes any
// number of sessions, speaks plain SMTP (no STARTTLS, no AUTH) and records each
// message it receives after DATA. A session that only verifies the connection
// (EHLO, QUIT) records nothing.
type adminSMTP struct {
	port     int
	mu       sync.Mutex
	messages []adminSMTPMessage
}

// adminSMTPMessage is one delivered message: its envelope and raw data.
type adminSMTPMessage struct {
	from, to, data string
}

// startAdminSMTPServer listens on 127.0.0.1 until the test ends.
func startAdminSMTPServer(t testing.TB) *adminSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for SMTP: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &adminSMTP{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

// serve answers one SMTP session. A message is recorded before its DATA is
// acknowledged, so the client's success means the record exists.
func (s *adminSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 localhost ESMTP\r\n")
	var cur adminSMTPMessage
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			fmt.Fprint(conn, "250 localhost\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			cur = adminSMTPMessage{from: smtpAddress(cmd)}
			fmt.Fprint(conn, "250 OK\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			cur.to = smtpAddress(cmd)
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "DATA":
			fmt.Fprint(conn, "354 End data with <CR><LF>.<CR><LF>\r\n")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.data = data.String()
			s.mu.Lock()
			s.messages = append(s.messages, cur)
			s.mu.Unlock()
			cur = adminSMTPMessage{}
			fmt.Fprint(conn, "250 OK queued\r\n")
		case upper == "RSET":
			cur = adminSMTPMessage{}
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "NOOP":
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "QUIT":
			fmt.Fprint(conn, "221 Bye\r\n")
			return
		default:
			fmt.Fprint(conn, "502 Command not implemented\r\n")
		}
	}
}

// smtpAddress returns the address in an envelope command such as
// "RCPT TO:<a@example.com>".
func smtpAddress(cmd string) string {
	if i := strings.Index(cmd, "<"); i >= 0 {
		if j := strings.Index(cmd[i:], ">"); j >= 0 {
			return cmd[i+1 : i+j]
		}
	}
	return strings.TrimSpace(cmd[strings.Index(cmd, ":")+1:])
}

// checkEbookDelivered fails unless exactly one message went to the address and
// it carries the file's bytes as an attachment. The data is compared without
// its line breaks, so the check holds for any base64 encoder.
func (s *adminSMTP) checkEbookDelivered(t *testing.T, to, ebookPath string) {
	t.Helper()
	ebook, err := os.ReadFile(ebookPath)
	if err != nil {
		t.Fatalf("read ebook sample: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) != 1 {
		t.Fatalf("SMTP server received %d messages, want 1", len(s.messages))
	}
	m := s.messages[0]
	if m.to != to {
		t.Errorf("message to %q, want %q", m.to, to)
	}
	flat := strings.NewReplacer("\r", "", "\n", "").Replace(m.data)
	if !strings.Contains(flat, base64.StdEncoding.EncodeToString(ebook)) {
		t.Errorf("message from %q does not carry the ebook as its attachment", m.from)
	}
}

// startAdminAppriseServer answers every request 200: the Apprise API the
// notification steps send to.
func startAdminAppriseServer(t testing.TB) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// formFile is one file part of a multipart upload.
type formFile struct {
	field, name string
	data        []byte
}

// multipartForm builds a multipart/form-data body with the text fields, in
// order, and the file parts, as the web client's FormData does.
func multipartForm(t testing.TB, fields [][2]string, files ...formFile) rawBody {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			t.Fatalf("multipart field %s: %v", f[0], err)
		}
	}
	for _, f := range files {
		part, err := w.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatalf("multipart file %s: %v", f.field, err)
		}
		if _, err := part.Write(f.data); err != nil {
			t.Fatalf("multipart file %s: %v", f.field, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	return rawBody{contentType: w.FormDataContentType(), data: buf.Bytes()}
}
