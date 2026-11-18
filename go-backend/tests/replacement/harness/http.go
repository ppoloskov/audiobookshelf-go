package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

const (
	maxResponseBytes = 64 << 20
	requestTimeout   = 60 * time.Second
)

// Request describes one HTTP call.
type Request struct {
	Method string            // default GET
	Path   string            // relative to Env.Base ("/api/me"); if Raw is true, relative to Env.URL
	Raw    bool              // Path is relative to Env.URL, outside the router base path
	Token  string            // sent as "Authorization: Bearer <token>" when non-empty
	Header map[string]string // extra request headers
	Query  url.Values        // appended to the URL
	JSON   any               // marshalled as the body with Content-Type application/json (a json.RawMessage, []byte or string is sent verbatim)
	Body   io.Reader         // raw body (set Content-Type yourself in Header); mutually exclusive with JSON
}

// Response is a fully read HTTP response.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte // whole body; reading more than 64 MiB fails the test
	Cookies []*http.Cookie
}

// newClient returns a client that never follows redirects, keeps no cookies and
// no idle connections (a restarted server must not meet stale ones).
func newClient() *http.Client {
	return &http.Client{
		Timeout:       requestTimeout,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Do performs the request and reads the whole response.
func (e *Env) Do(t *testing.T, r Request) Response {
	t.Helper()
	if r.JSON != nil && r.Body != nil {
		t.Fatal("Request.JSON and Request.Body are mutually exclusive")
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	target := e.Base + r.Path
	if r.Raw {
		target = e.URL + r.Path
	}
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	body := r.Body
	contentType := ""
	if r.JSON != nil {
		var raw []byte
		switch value := r.JSON.(type) {
		case json.RawMessage:
			raw = value
		case []byte:
			raw = value
		case string:
			raw = []byte(value)
		default:
			var err error
			if raw, err = json.Marshal(r.JSON); err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
		}
		body, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	for key, value := range r.Header {
		req.Header.Set(key, value)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close() // fully read below
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, target, err)
	}
	if len(raw) > maxResponseBytes {
		t.Fatalf("%s %s: response exceeds %d bytes", method, target, maxResponseBytes)
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: raw, Cookies: resp.Cookies()}
}

// Get performs an authenticated GET.
func (e *Env) Get(t *testing.T, token, path string) Response {
	t.Helper()
	return e.Do(t, Request{Path: path, Token: token})
}

// Post performs an authenticated POST with body as JSON (nil sends no body).
func (e *Env) Post(t *testing.T, token, path string, body any) Response {
	t.Helper()
	return e.Do(t, Request{Method: http.MethodPost, Path: path, Token: token, JSON: body})
}

// Patch performs an authenticated PATCH with body as JSON (nil sends no body).
func (e *Env) Patch(t *testing.T, token, path string, body any) Response {
	t.Helper()
	return e.Do(t, Request{Method: http.MethodPatch, Path: path, Token: token, JSON: body})
}

// Delete performs an authenticated DELETE.
func (e *Env) Delete(t *testing.T, token, path string) Response {
	t.Helper()
	return e.Do(t, Request{Method: http.MethodDelete, Path: path, Token: token})
}

// excerpt shortens a body for failure messages.
func excerpt(body []byte) string {
	if len(body) > 300 {
		return string(body[:300]) + "..."
	}
	return string(body)
}

// JSON decodes the body into into; it fails the test with the status and a body excerpt otherwise.
func (r Response) JSON(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, into); err != nil {
		t.Fatalf("status %d: body is not valid JSON for %T: %v: %s", r.Status, into, err, excerpt(r.Body))
	}
}

// Object decodes a JSON object (numbers as float64).
func (r Response) Object(t *testing.T) map[string]any {
	t.Helper()
	var value map[string]any
	r.JSON(t, &value)
	if value == nil {
		t.Fatalf("status %d: body is not a JSON object: %s", r.Status, excerpt(r.Body))
	}
	return value
}

// Array decodes a JSON array.
func (r Response) Array(t *testing.T) []any {
	t.Helper()
	var value []any
	r.JSON(t, &value)
	if value == nil {
		t.Fatalf("status %d: body is not a JSON array: %s", r.Status, excerpt(r.Body))
	}
	return value
}

// Login posts the credentials to /login and returns the access token and user
// object. It fails the test unless the status is 200.
func (e *Env) Login(t *testing.T, username, password string) (accessToken string, user map[string]any) {
	t.Helper()
	resp := e.Post(t, "", "/login", map[string]string{"username": username, "password": password})
	if resp.Status != http.StatusOK {
		t.Fatalf("login %s: status %d: %s", username, resp.Status, excerpt(resp.Body))
	}
	user, _ = resp.Object(t)["user"].(map[string]any)
	accessToken, _ = user["accessToken"].(string)
	if accessToken == "" {
		t.Fatalf("login %s: no user.accessToken: %s", username, excerpt(resp.Body))
	}
	return accessToken, user
}

// Token returns a cached access token for a fixture user of fixture.json
// ("root", "listener" or "finisher"). The cache is cleared by Restart.
func (e *Env) Token(t *testing.T, who string) string {
	t.Helper()
	e.tokenMu.Lock()
	token, ok := e.tokens[who]
	e.tokenMu.Unlock()
	if ok {
		return token
	}
	user, ok := e.users[who]
	if !ok {
		t.Fatalf("fixture user %q is not defined for this install", who)
	}
	token, _ = e.Login(t, user.Username, user.Password)
	e.tokenMu.Lock()
	e.tokens[who] = token
	e.tokenMu.Unlock()
	return token
}

// File is one multipart file part.
type File struct {
	Field, Name string
	Content     []byte
}

// Multipart builds a multipart/form-data body with the fields (sorted by name)
// followed by the files, and returns it with its Content-Type.
func Multipart(fields map[string]string, files ...File) (body *bytes.Buffer, contentType string) {
	body = &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		// A bytes.Buffer never fails to write.
		_ = writer.WriteField(name, fields[name])
	}
	for _, file := range files {
		part, _ := writer.CreateFormFile(file.Field, file.Name) // buffer-backed: cannot fail
		_, _ = part.Write(file.Content)
	}
	_ = writer.Close() // buffer-backed: cannot fail
	return body, writer.FormDataContentType()
}

// Dig walks decoded JSON: string path elements index objects, int elements
// index arrays. It returns nil when any step is absent or of the wrong kind.
func Dig(value any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = object[key]
		case int:
			list, ok := value.([]any)
			if !ok || key < 0 || key >= len(list) {
				return nil
			}
			value = list[key]
		default:
			return nil
		}
	}
	return value
}

// Eventually polls condition every 50ms until it is true, failing the test
// with "timed out waiting for <what>" after timeout.
func Eventually(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
