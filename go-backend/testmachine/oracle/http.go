package oracle

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

// Request describes one HTTP request to send to a Target.
type Request struct {
	Method string            // HTTP method; defaults to GET
	Path   string            // relative to Target.Base (or Target.URL when Raw is true)
	Raw    bool              // send relative to Target.URL instead of Target.Base
	Token  string            // sent as "Authorization: Bearer <token>" when non-empty
	Header map[string]string // extra request headers
	Query  url.Values        // URL query parameters
	JSON   any               // marshalled as application/json (json.RawMessage, []byte, string sent verbatim)
	Body   []byte            // raw request body; mutually exclusive with JSON
}

// Response is a fully read HTTP response.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Cookies []*http.Cookie
}

// File is one multipart file part for Multipart.
type File struct {
	Field   string
	Name    string
	Content []byte
}

func newClient() *http.Client {
	return &http.Client{
		Timeout:       requestTimeout,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Do executes r against the target and returns the complete response.
func (tgt *Target) Do(t *testing.T, r Request) Response {
	t.Helper()
	if r.JSON != nil && r.Body != nil {
		t.Fatal("Request.JSON and Request.Body are mutually exclusive")
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	target := tgt.Base + r.Path
	if r.Raw {
		target = tgt.URL + r.Path
	}
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}

	var body io.Reader
	contentType := ""
	switch {
	case r.JSON != nil:
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
			raw, err = json.Marshal(r.JSON)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
		}
		body = bytes.NewReader(raw)
		contentType = "application/json"
	case r.Body != nil:
		body = bytes.NewReader(r.Body)
	}

	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, target, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}

	resp, err := tgt.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v", tgt.Name, method, target, err)
	}
	defer resp.Body.Close() // body drained below

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		t.Fatalf("%s %s %s: read body: %v", tgt.Name, method, target, err)
	}
	if len(raw) > maxResponseBytes {
		t.Fatalf("%s %s %s: response exceeds %d bytes", tgt.Name, method, target, maxResponseBytes)
	}
	surfaceTracker.RecordHTTP(tgt.isTS, method, req.URL.Path, resp.StatusCode)
	return Response{
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Body:    raw,
		Cookies: resp.Cookies(),
	}
}

// Get sends an authenticated GET request.
func (tgt *Target) Get(t *testing.T, token, path string) Response {
	t.Helper()
	return tgt.Do(t, Request{Method: http.MethodGet, Path: path, Token: token})
}

// Post sends an authenticated POST request with body encoded as JSON.
func (tgt *Target) Post(t *testing.T, token, path string, body any) Response {
	t.Helper()
	return tgt.Do(t, Request{Method: http.MethodPost, Path: path, Token: token, JSON: body})
}

// Patch sends an authenticated PATCH request with body encoded as JSON.
func (tgt *Target) Patch(t *testing.T, token, path string, body any) Response {
	t.Helper()
	return tgt.Do(t, Request{Method: http.MethodPatch, Path: path, Token: token, JSON: body})
}

// Delete sends an authenticated DELETE request.
func (tgt *Target) Delete(t *testing.T, token, path string) Response {
	t.Helper()
	return tgt.Do(t, Request{Method: http.MethodDelete, Path: path, Token: token})
}

// Login posts credentials to /login and returns the access token and user object.
func (tgt *Target) Login(t *testing.T, username, password string) (string, map[string]any) {
	t.Helper()
	resp := tgt.Post(t, "", "/login", map[string]string{
		"username": username,
		"password": password,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("%s login %s: status %d: %s", tgt.Name, username, resp.Status, excerpt(resp.Body))
	}
	obj := resp.Object(t)
	user, ok := obj["user"].(map[string]any)
	if !ok {
		t.Fatalf("%s login %s: missing user object: %s", tgt.Name, username, excerpt(resp.Body))
	}
	token, ok := user["accessToken"].(string)
	if !ok || token == "" {
		t.Fatalf("%s login %s: missing user.accessToken: %s", tgt.Name, username, excerpt(resp.Body))
	}
	return token, user
}

// Token returns a cached access token for a fixture user role ("root",
// "listener", or "finisher"). Cached tokens are cleared on Restart.
func (tgt *Target) Token(t *testing.T, who string) string {
	t.Helper()
	tgt.tokenMu.Lock()
	token, ok := tgt.tokens[who]
	tgt.tokenMu.Unlock()
	if ok {
		return token
	}
	user, ok := tgt.users[who]
	if !ok {
		t.Fatalf("%s fixture user %q is not defined for this install", tgt.Name, who)
	}
	token, _ = tgt.Login(t, user.Username, user.Password)
	tgt.tokenMu.Lock()
	tgt.tokens[who] = token
	tgt.tokenMu.Unlock()
	return token
}

// JSON decodes the response body into into, failing the test on decode errors.
func (r Response) JSON(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, into); err != nil {
		t.Fatalf("status %d: body is not valid JSON for %T: %v: %s", r.Status, into, err, excerpt(r.Body))
	}
}

// Object decodes the response body as a JSON object.
func (r Response) Object(t *testing.T) map[string]any {
	t.Helper()
	var obj map[string]any
	r.JSON(t, &obj)
	if obj == nil {
		t.Fatalf("status %d: body is not a JSON object: %s", r.Status, excerpt(r.Body))
	}
	return obj
}

// Array decodes the response body as a JSON array.
func (r Response) Array(t *testing.T) []any {
	t.Helper()
	var arr []any
	r.JSON(t, &arr)
	if arr == nil {
		t.Fatalf("status %d: body is not a JSON array: %s", r.Status, excerpt(r.Body))
	}
	return arr
}

// Multipart encodes form fields (sorted by key) and files into a
// multipart/form-data payload and returns the body bytes and Content-Type header.
func Multipart(fields map[string]string, files ...File) ([]byte, string) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		_ = writer.WriteField(name, fields[name]) // bytes.Buffer write cannot fail
	}
	for _, file := range files {
		part, _ := writer.CreateFormFile(file.Field, file.Name) // bytes.Buffer write cannot fail
		_, _ = part.Write(file.Content)                         // bytes.Buffer write cannot fail
	}
	_ = writer.Close() // bytes.Buffer write cannot fail
	return buf.Bytes(), writer.FormDataContentType()
}

func excerpt(body []byte) string {
	if len(body) > 300 {
		return string(body[:300]) + "..."
	}
	return string(body)
}
