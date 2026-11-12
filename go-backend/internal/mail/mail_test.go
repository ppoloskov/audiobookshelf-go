package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	stdmail "net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/settings"
)

const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
)

type env struct {
	t      *testing.T
	d      *db.DB
	store  *settings.Store
	mux    *http.ServeMux
	events []string
	itemID string
	ebook  string // path of the item's ebook
}

// newEnv is a Handler over the fixture database. The fixture's ebook-only item
// gets a real file to mail.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, d: dbtest.Fixture(t), mux: http.NewServeMux()}
	var err error
	if e.store, err = settings.NewStore(context.Background(), e.d, func(string) string { return "" }, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	New(e.d, e.store, func(audience, event string, _ any) { e.events = append(e.events, audience+" "+event) }).Routes(e.mux)
	e.ebook = filepath.Join(t.TempDir(), "Ünïcode book.epub")
	if err := os.WriteFile(e.ebook, []byte(strings.Repeat("epub data ", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Get(context.Background(), &e.itemID, `SELECT li.id FROM libraryItems li JOIN books b ON b.id = li.mediaId WHERE b.ebookFile IS NOT NULL AND b.ebookFile != 'null'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Exec(context.Background(), `UPDATE books SET ebookFile = json_set(ebookFile, '$.metadata.path', ?, '$.metadata.filename', 'Ünïcode book.epub')`, e.ebook); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) do(userID, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	var row db.User
	if err := e.d.Get(r.Context(), &row, `SELECT * FROM users WHERE id = ?`, userID); err != nil {
		e.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), auth.NewPrincipal(row))))
	return w
}

func (e *env) admin(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(rootID, method, path, body)
}

func (e *env) email() settings.Email {
	e.t.Helper()
	email, err := e.store.Email(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return email
}

// pointAt configures the SMTP settings to use the fake server.
func (e *env) pointAt(f *fakeSMTP) {
	e.t.Helper()
	host, port := f.addr()
	body := `{"host":"` + host + `","port":` + strconv.Itoa(port) + `,"secure":false,"fromAddress":"abs@example.test","testAddress":"test@example.test"`
	if f.user != "" {
		body += `,"user":"` + f.user + `","pass":"` + f.pass + `"`
	}
	if w := e.admin("PATCH", "/api/emails/settings", body+`}`); w.Code != 200 {
		e.t.Fatalf("configure SMTP: %d %s", w.Code, w.Body)
	}
}

func TestSettingsDefaultsAndPatch(t *testing.T) {
	e := newEnv(t)
	var got settingsResponse
	w := e.admin("GET", "/api/emails/settings", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("GET: %d %v %s", w.Code, err, w.Body)
	}
	if got.Settings.Port != 465 || !got.Settings.Secure || !got.Settings.RejectUnauthorized || got.Settings.EreaderDevices == nil || got.Settings.ID != "email-settings" {
		t.Fatalf("defaults: %+v", got.Settings)
	}
	// A numeric-string port is accepted (older clients), an unreadable one is 465.
	w = e.admin("PATCH", "/api/emails/settings", `{"host":"smtp.example.test","port":"587","secure":false,"user":"u","pass":"p","fromAddress":"Abs <abs@example.test>"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("PATCH: %d %v %s", w.Code, err, w.Body)
	}
	if *got.Settings.Host != "smtp.example.test" || got.Settings.Port != 587 || got.Settings.Secure || *got.Settings.Pass != "p" {
		t.Fatalf("patched: %+v", got.Settings)
	}
	e.admin("PATCH", "/api/emails/settings", `{"port":"invalid","user":null}`)
	if c := e.email(); c.Port != 465 || c.User != nil || *c.Host != "smtp.example.test" || !c.RejectUnauthorized {
		t.Fatalf("after second patch (absent keys must stay): %+v", c)
	}
}

func TestSettingsPatchValidation(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"port zero":     `{"port":0}`,
		"port too high": `{"port":70000}`,
		"secure string": `{"secure":"yes"}`,
		"malformed":     `{`,
	} {
		if w := e.admin("PATCH", "/api/emails/settings", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	if c := e.email(); c.Host != nil || c.Port != 465 {
		t.Errorf("an invalid PATCH changed the settings: %+v", c)
	}
}

func TestAdminRoutesAnswer404ToOthers(t *testing.T) {
	e := newEnv(t)
	for _, route := range [][2]string{{"GET", "/api/emails/settings"}, {"PATCH", "/api/emails/settings"}, {"POST", "/api/emails/test"}, {"POST", "/api/emails/ereader-devices"}} {
		if w := e.do(listenerID, route[0], route[1], `{}`); w.Code != http.StatusNotFound {
			t.Errorf("%v as user: %d, want 404", route, w.Code)
		}
	}
}

func TestDeviceListIsNormalizedAndEventedOnce(t *testing.T) {
	e := newEnv(t)
	body := `{"ereaderDevices":[
		{"name":"invalid option","email":"a@kindle.test","availabilityOption":"bogus","users":["x"]},
		{"name":"empty specific","email":"b@kindle.test","availabilityOption":"specificUsers","users":[]},
		{"name":"guest keeps nothing","email":"c@kindle.test","availabilityOption":"guestOrUp","users":["x"]},
		{"name":"specific","email":"d@kindle.test","availabilityOption":"specificUsers","users":["u1"]},
		{"name":"no option","email":"e@kindle.test"}]}`
	w := e.admin("POST", "/api/emails/ereader-devices", body)
	var got devicesBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("POST: %d %v %s", w.Code, err, w.Body)
	}
	want := []struct {
		option string
		users  int
	}{{"adminOrUp", 0}, {"adminOrUp", 0}, {"guestOrUp", 0}, {"specificUsers", 1}, {"adminOrUp", 0}}
	if len(got.EreaderDevices) != len(want) {
		t.Fatalf("devices: %+v", got.EreaderDevices)
	}
	for i, device := range got.EreaderDevices {
		if device.AvailabilityOption != want[i].option || len(device.Users) != want[i].users {
			t.Errorf("device %d: %+v, want %+v", i, device, want[i])
		}
	}
	for _, device := range got.EreaderDevices {
		if device.Users == nil {
			t.Errorf("%s: users must be an empty list, not absent", device.Name)
		}
	}
	if len(e.events) != 1 || e.events[0] != "@admins ereader-devices-updated" {
		t.Fatalf("events: %v", e.events)
	}

}

func TestDeviceListValidation(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"missing":      `{}`,
		"null":         `{"ereaderDevices":null}`,
		"not an array": `{"ereaderDevices":"x"}`,
		"no name":      `{"ereaderDevices":[{"email":"a@kindle.test"}]}`,
		"no email":     `{"ereaderDevices":[{"name":"a"}]}`,
	} {
		if w := e.admin("POST", "/api/emails/ereader-devices", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	w := e.admin("POST", "/api/emails/ereader-devices", `{}`)
	if w.Body.String() != "Invalid payload. ereaderDevices array required" {
		t.Errorf("message: %q", w.Body)
	}
}

func TestTestEmail(t *testing.T) {
	e := newEnv(t)
	if w := e.admin("POST", "/api/emails/test", ""); w.Code != 400 || w.Body.String() != verifyFailed {
		t.Fatalf("unconfigured: %d %q", w.Code, w.Body)
	}
	smtp := newFakeSMTP(t)
	e.pointAt(smtp)
	if w := e.admin("POST", "/api/emails/test", ""); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("test: %d %q", w.Code, w.Body)
	}
	got := smtp.received()
	if len(got) != 1 || got[0].From != "abs@example.test" || got[0].To != "test@example.test" ||
		!strings.Contains(got[0].Data, "Subject: Test email from Audiobookshelf") || !strings.HasSuffix(strings.TrimSpace(got[0].Data), "Success!") {
		t.Fatalf("delivered: %+v", got)
	}
	// The admin sees the server's refusal, as upstream.
	smtp.rejectTo = true
	if w := e.admin("POST", "/api/emails/test", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "no such user") {
		t.Fatalf("rejected: %d %q", w.Code, w.Body)
	}
}

func TestSendEbook(t *testing.T) {
	e := newEnv(t)
	smtp := newFakeSMTP(t)
	e.pointAt(smtp)
	devices := `{"ereaderDevices":[{"name":"admin only","email":"a@kindle.test"},{"name":"guests","email":"g@kindle.test","availabilityOption":"guestOrUp"}]}`
	e.admin("POST", "/api/emails/ereader-devices", devices)
	send := func(user, item, device string) *httptest.ResponseRecorder {
		return e.do(user, "POST", "/api/emails/send-ebook-to-device", `{"libraryItemId":"`+item+`","deviceName":"`+device+`"}`)
	}

	for name, c := range map[string]struct {
		user, item, device string
		status             int
	}{
		"unknown device": {rootID, e.itemID, "nope", 404},
		"device denied":  {listenerID, e.itemID, "admin only", 403},
		"unknown item":   {rootID, "00000000-0000-4000-8000-000000000000", "admin only", 404},
	} {
		if w := send(c.user, c.item, c.device); w.Code != c.status {
			t.Errorf("%s: %d, want %d (%s)", name, w.Code, c.status, w.Body)
		}
	}
	if w := e.admin("POST", "/api/emails/send-ebook-to-device", `{}`); w.Code != 400 {
		t.Errorf("empty body: %d", w.Code)
	}
	var podcastID string
	_ = e.d.Get(context.Background(), &podcastID, `SELECT id FROM libraryItems WHERE mediaType='podcast'`)
	if w := send(rootID, podcastID, "admin only"); w.Code != 404 || w.Body.String() != "Ebook file not found" {
		t.Errorf("podcast: %d %q", w.Code, w.Body)
	}
	if len(smtp.received()) != 0 {
		t.Fatal("a refused request sent mail")
	}

	if w := send(listenerID, e.itemID, "guests"); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("send: %d %q", w.Code, w.Body)
	}
	got := smtp.received()
	if len(got) != 1 || got[0].To != "g@kindle.test" {
		t.Fatalf("delivered: %+v", got)
	}
	filename, content := parseAttachment(t, got[0].Data)
	want, _ := os.ReadFile(e.ebook)
	if filename != "Ünïcode book.epub" || content != string(want) {
		t.Fatalf("attachment %q, %d bytes (want %d)", filename, len(content), len(want))
	}

	// A non-admin never sees the SMTP error text.
	smtp.rejectTo = true
	if w := send(listenerID, e.itemID, "guests"); w.Code != 400 || w.Body.String() != "Failed to send ebook to device" {
		t.Fatalf("rejected: %d %q", w.Code, w.Body)
	}
}

func TestSendEbookMissingFileAndSMTPDown(t *testing.T) {
	e := newEnv(t)
	e.admin("POST", "/api/emails/ereader-devices", `{"ereaderDevices":[{"name":"d","email":"d@kindle.test"}]}`)
	body := `{"libraryItemId":"` + e.itemID + `","deviceName":"d"}`
	// SMTP host unreachable: the verify message, nothing else.
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	e.admin("PATCH", "/api/emails/settings", `{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"secure":false,"fromAddress":"abs@example.test"}`)
	if w := e.admin("POST", "/api/emails/send-ebook-to-device", body); w.Code != 400 || w.Body.String() != verifyFailed {
		t.Fatalf("SMTP down: %d %q", w.Code, w.Body)
	}
	if err := os.Remove(e.ebook); err != nil {
		t.Fatal(err)
	}
	if w := e.admin("POST", "/api/emails/send-ebook-to-device", body); w.Code != 404 || w.Body.String() != "Ebook file not found" {
		t.Fatalf("missing file: %d %q", w.Code, w.Body)
	}
}

func TestSendAuthenticatesAndBuildsMessage(t *testing.T) {
	smtp := newFakeSMTP(t)
	smtp.user, smtp.pass = "mailer", "s3cret"
	host, port := smtp.addr()
	s := server{Host: host, Port: port, VerifyTLS: true, User: "mailer", Pass: "s3cret"}
	m := message{From: "abs@example.test", To: "Reader <reader@example.test>", Subject: "Grüße", Body: "hello"}
	if err := send(context.Background(), s, m); err != nil {
		t.Fatal(err)
	}
	got := smtp.received()
	if len(got) != 1 || got[0].To != "reader@example.test" {
		t.Fatalf("delivered: %+v", got)
	}
	parsed, err := stdmail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if subject != "Grüße" || parsed.Header.Get("Message-ID") == "" || parsed.Header.Get("Date") == "" {
		t.Fatalf("headers: %v", parsed.Header)
	}

	// Wrong password: a connect-phase failure.
	s.Pass = "wrong"
	var connect *connectError
	if err := send(context.Background(), s, m); !errors.As(err, &connect) {
		t.Fatalf("bad credentials: %v", err)
	}
	// Refused recipient: not a connect-phase failure.
	s.Pass, smtp.rejectTo = "s3cret", true
	if err := send(context.Background(), s, m); err == nil || errors.As(err, &connect) {
		t.Fatalf("rejected recipient: %v", err)
	}
}

func TestSendChecksInputsBeforeConnecting(t *testing.T) {
	var connect *connectError
	if err := send(context.Background(), server{}, message{From: "a@b.test", To: "c@d.test"}); !errors.As(err, &connect) {
		t.Errorf("no host: %v", err)
	}
	// The host does not resolve, so reaching the network would be a connect error.
	s := server{Host: "does-not-exist.invalid", Port: 25}
	for name, m := range map[string]message{
		"empty from":      {From: "", To: "c@d.test"},
		"header injected": {From: "a@b.test", To: "c@d.test\r\nBcc: evil@d.test"},
	} {
		if err := send(context.Background(), s, m); err == nil || errors.As(err, &connect) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWriteMessageStreamsAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.bin")
	content := make([]byte, 100_003)
	for i := range content {
		content[i] = byte(i * 7)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	from, _ := stdmail.ParseAddress("abs@example.test")
	to, _ := stdmail.ParseAddress("r@example.test")
	m := message{From: from.Address, To: to.Address, Subject: "Here is your Ebook!", AttachPath: path, AttachName: "../dir/ünï book.epub"}
	if err := m.writeTo(&out, from, to); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d characters", len(line))
		}
	}
	filename, got := parseAttachment(t, out.String())
	if filename != "ünï book.epub" || got != string(content) {
		t.Fatalf("filename %q, content equal %v", filename, got == string(content))
	}
}

func TestServerOf(t *testing.T) {
	host := "smtp.example.test"
	for name, c := range map[string]struct {
		email    settings.Email
		port     int
		implicit bool
	}{
		"default":           {settings.Email{Host: &host, Port: 465, Secure: true}, 465, true},
		"secure on 587":     {settings.Email{Host: &host, Port: 587, Secure: true}, 587, false},
		"port 465 insecure": {settings.Email{Host: &host, Port: 465, Secure: false}, 465, false},
		"unset port":        {settings.Email{Host: &host, Secure: true}, 465, true},
	} {
		if got := serverOf(c.email); got.Port != c.port || got.Implicit != c.implicit {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// parseAttachment returns the file name and decoded content of the single
// attachment of a multipart message.
func parseAttachment(t *testing.T, raw string) (string, string) {
	t.Helper()
	parsed, err := stdmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("content type %q: %v", parsed.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("no attachment: %v", err)
		}
		if part.FileName() == "" {
			continue
		}
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		if err != nil {
			t.Fatal(err)
		}
		return part.FileName(), string(data)
	}
}
