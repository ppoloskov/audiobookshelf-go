package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// ebookRelPath is the fixture's ebook file, relative to the install media
// directory; it is the attachment the send-ebook flow must deliver.
const ebookRelPath = "audiobooks/Ada Example/Ebook Only/Ebook Only.epub"

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
		t.Fatalf("%s: want status %d, got %d: %.300s", what, status, resp.Status, resp.Body)
	}
}

// sameJSON reports whether two values are equal after a JSON round trip.
func sameJSON(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

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

// wantKeys fails unless every key of want has the same JSON value in got.
func wantKeys(t *testing.T, what string, got, want map[string]any) {
	t.Helper()
	for key, value := range want {
		have, present := got[key]
		if !present || !sameJSON(have, value) {
			t.Errorf("%s: %s = %v (present %v), want %v", what, key, have, present, value)
		}
	}
}

// adminOnly asserts 401 without a token and 404 for an authenticated non-admin
// (EmailController.adminMiddleware answers 404, not 403).
func adminOnly(t *testing.T, e *harness.Env, method, path string, body any) {
	t.Helper()
	anonymous := e.Do(t, harness.Request{Method: method, Path: path, JSON: body})
	wantStatus(t, method+" "+path+" without token", anonymous, http.StatusUnauthorized)
	listener := e.Do(t, harness.Request{Method: method, Path: path, JSON: body, Token: e.Token(t, "listener")})
	wantStatus(t, method+" "+path+" as non-admin", listener, http.StatusNotFound)
}

// emailSettings reads GET /api/emails/settings as root and returns the
// settings object.
func emailSettings(t *testing.T, e *harness.Env) map[string]any {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/emails/settings")
	wantStatus(t, "GET /api/emails/settings", resp, http.StatusOK)
	settings, ok := resp.Object(t)["settings"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/emails/settings has no settings object: %.300s", resp.Body)
	}
	return settings
}

// patchEmailSettings sends PATCH /api/emails/settings as root and returns the
// settings object of the 200 response.
func patchEmailSettings(t *testing.T, e *harness.Env, body any) map[string]any {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/emails/settings", body)
	wantStatus(t, "PATCH /api/emails/settings", resp, http.StatusOK)
	settings, ok := resp.Object(t)["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH /api/emails/settings has no settings object: %.300s", resp.Body)
	}
	return settings
}

// storedEmailSettings returns the email-settings row value and its updatedAt.
// exists is false while no route has written the row.
func storedEmailSettings(t *testing.T, e *harness.Env) (value map[string]any, updatedAt string, exists bool) {
	t.Helper()
	rows := e.Rows(t, "SELECT value, updatedAt FROM settings WHERE key = 'email-settings'")
	switch len(rows) {
	case 0:
		return nil, "", false
	case 1:
	default:
		t.Fatalf("settings table holds %d email-settings rows, want at most one", len(rows))
	}
	text, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatalf("email-settings value is %T, want JSON text", rows[0]["value"])
	}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("email-settings value is not a JSON object: %v", err)
	}
	updatedAt, _ = rows[0]["updatedAt"].(string)
	return value, updatedAt, true
}

// smtpSettings is the admin payload the email settings page writes, pointed at
// a local stub. The page sends exactly these keys (client/pages/config/email.vue
// submitForm).
func smtpSettings(port int, from, testAddress string) map[string]any {
	return map[string]any{
		"host": "127.0.0.1", "port": port, "secure": false, "rejectUnauthorized": true,
		"user": nil, "pass": nil, "testAddress": testAddress, "fromAddress": from,
	}
}

// useSMTP points the stored email settings at the given stub port.
func useSMTP(t *testing.T, e *harness.Env, port int, from, testAddress string) {
	t.Helper()
	settings := patchEmailSettings(t, e, smtpSettings(port, from, testAddress))
	wantKeys(t, "stored SMTP settings", settings, map[string]any{"host": "127.0.0.1", "port": float64(port), "fromAddress": from})
}

// postWithin sends a POST with its own short client timeout so a scenario can
// prove cancellation and timeout behaviour; the harness client's 60s bound is
// too long for a hanging SMTP stage.
func postWithin(t *testing.T, e *harness.Env, token, path string, body any, timeout time.Duration) (status int, responseBody []byte, err error) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(http.MethodPost, e.Base+path, reader)
	if err != nil {
		t.Fatalf("build %s request: %v", path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close() // read below
	responseBody, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("%s: read response: %v", path, err)
	}
	return response.StatusCode, responseBody, nil
}

// mailPart is one decoded MIME entity: headers, transfer-decoded body and
// nested multipart children.
type mailPart struct {
	Header   textproto.MIMEHeader
	Body     []byte
	Children []*mailPart
}

// header decodes one RFC 2047 header value.
func (p *mailPart) header(t *testing.T, key string) string {
	t.Helper()
	raw := p.Header.Get(key)
	if raw == "" {
		return ""
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil {
		t.Fatalf("decode %s header %q: %v", key, raw, err)
	}
	return decoded
}

// mediaType returns the lower-cased media type without parameters.
func (p *mailPart) mediaType(t *testing.T) string {
	t.Helper()
	value := p.Header.Get("Content-Type")
	if value == "" {
		return "text/plain"
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		t.Fatalf("parse Content-Type %q: %v", value, err)
	}
	return strings.ToLower(mediaType)
}

// filename returns the attachment/file name of the part, if any.
func (p *mailPart) filename(t *testing.T) string {
	t.Helper()
	if _, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	if _, params, err := mime.ParseMediaType(p.Header.Get("Content-Type")); err == nil && params["name"] != "" {
		return params["name"]
	}
	return ""
}

// parseMessage decodes a captured SMTP DATA body into a MIME tree.
func parseMessage(t *testing.T, raw string) *mailPart {
	t.Helper()
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse captured message: %v\n%s", err, raw)
	}
	part := &mailPart{Header: textproto.MIMEHeader(message.Header)}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		t.Fatalf("read captured message body: %v", err)
	}
	part.Body = body
	part.Children = parseChildren(t, part)
	return part
}

// parseChildren expands multipart parts and transfer-decodes leaf bodies.
func parseChildren(t *testing.T, part *mailPart) []*mailPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse part Content-Type %q: %v", part.Header.Get("Content-Type"), err)
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		if params["boundary"] == "" {
			t.Fatalf("multipart part without boundary: %v", part.Header)
		}
		reader := multipart.NewReader(bytes.NewReader(part.Body), params["boundary"])
		var children []*mailPart
		for {
			next, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read multipart part: %v", err)
			}
			body, err := io.ReadAll(next)
			if err != nil {
				t.Fatalf("read multipart body: %v", err)
			}
			child := &mailPart{Header: textproto.MIMEHeader(next.Header), Body: body}
			child.Children = parseChildren(t, child)
			children = append(children, child)
		}
		return children
	}
	switch strings.ToLower(part.Header.Get("Content-Transfer-Encoding")) {
	case "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(part.Body)))
		if err != nil {
			t.Fatalf("decode base64 body: %v", err)
		}
		part.Body = decoded
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(part.Body)))
		if err != nil {
			t.Fatalf("decode quoted-printable body: %v", err)
		}
		part.Body = decoded
	}
	return nil
}

// walkParts visits the decoded MIME tree depth first.
func walkParts(root *mailPart, visit func(*mailPart)) {
	visit(root)
	for _, child := range root.Children {
		walkParts(child, visit)
	}
}

// partSummary lists every media type and filename for a failure message.
func describeParts(root *mailPart) string {
	var lines []string
	walkParts(root, func(part *mailPart) {
		lines = append(lines, part.Header.Get("Content-Type")+" filename="+part.Header.Get("Content-Disposition"))
	})
	return strings.Join(lines, "; ")
}

// ebookPath returns the absolute path of the fixture ebook file.
func ebookPath(e *harness.Env) string {
	return filepath.Join(e.Media, filepath.FromSlash(ebookRelPath))
}

// createRestrictedUser creates a plain user through the admin route with the
// given permission overrides, logs it in and returns its token. It is setup for
// the send-ebook access checks.
func createRestrictedUser(t *testing.T, e *harness.Env, username string, permissions map[string]any) string {
	t.Helper()
	const password = "restricted-password"
	resp := e.Post(t, e.Token(t, "root"), "/api/users", map[string]any{
		"username": username, "password": password, "type": "user", "isActive": true,
		"permissions": permissions,
	})
	wantStatus(t, "create restricted user", resp, http.StatusOK)
	token, _ := e.Login(t, username, password)
	return token
}

// legacyRowWithoutRejectUnauthorized rewrites the stored email-settings row the
// way a pre-v2.10.1 install left it: no rejectUnauthorized key. The server is
// stopped and the caller restarts it. Exec is the only way to produce this
// legacy persisted state.
func legacyRowWithoutRejectUnauthorized(t *testing.T, e *harness.Env) {
	t.Helper()
	e.Stop(t)
	value, _, exists := storedEmailSettings(t, e)
	if !exists {
		t.Fatal("precondition: the email-settings row must exist")
	}
	delete(value, "rejectUnauthorized")
	value["host"] = "legacy.example.test"
	value["port"] = 2525
	value["secure"] = true
	value["fromAddress"] = "legacy@example.test"
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode legacy settings row: %v", err)
	}
	if affected := e.Exec(t, "UPDATE settings SET value = ? WHERE key = 'email-settings'", string(raw)); affected != 1 {
		t.Fatalf("legacy rewrite affected %d rows, want 1", affected)
	}
}

// wantEbookFile fails when the fixture ebook file is missing.
func wantEbookFile(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	content, err := os.ReadFile(ebookPath(e))
	if err != nil {
		t.Fatalf("read fixture ebook: %v", err)
	}
	return content
}
