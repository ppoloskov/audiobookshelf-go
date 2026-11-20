package oracle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	maxProxyBodyBytes = 16 << 20
	proxyCloseTimeout = 5 * time.Second
)

// RecordProxy intercepts HTTP traffic between a real client and an upstream
// Audiobookshelf server and records non-static API calls as a replayable Flow.
type RecordProxy struct {
	URL string

	installRoot string
	basePath    string
	proxy       *httputil.ReverseProxy
	server      *http.Server

	mu    sync.Mutex
	steps []Step
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// StartRecordProxy starts an HTTP reverse proxy forwarding to upstreamBaseURL
// and registers t.Cleanup to shut it down cleanly.
func StartRecordProxy(t *testing.T, upstreamBaseURL, installRoot string) *RecordProxy {
	t.Helper()
	upstream, err := url.Parse(upstreamBaseURL)
	if err != nil {
		t.Fatalf("parse upstream url %q: %v", upstreamBaseURL, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen record proxy: %v", err)
	}

	p := &RecordProxy{
		URL:         "http://" + ln.Addr().String(),
		installRoot: strings.TrimRight(installRoot, "/"),
		basePath:    strings.TrimRight(upstream.Path, "/"),
		proxy:       httputil.NewSingleHostReverseProxy(upstream),
	}
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if serveErr := p.server.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			t.Logf("record proxy serve error: %v", serveErr)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), proxyCloseTimeout)
		defer cancel()
		_ = p.server.Shutdown(ctx) // best-effort cleanup at end of test
	})
	return p
}

// ServeHTTP forwards the request to the upstream server and records API calls.
func (p *RecordProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, p.basePath)
	if relPath == "" {
		relPath = "/"
	}
	record := shouldRecordAPIPath(relPath)

	var rawBody []byte
	if record && r.Body != nil {
		if r.ContentLength > maxProxyBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var err error
		rawBody, err = io.ReadAll(io.LimitReader(r.Body, maxProxyBodyBytes+1))
		_ = r.Body.Close() // body drained or oversized
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		if len(rawBody) > maxProxyBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(rawBody))
	}

	rec := &statusRecorder{ResponseWriter: w}
	p.proxy.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if !record {
		return
	}

	step := p.buildRecordedStep(r, relPath, rawBody, rec.status)
	p.mu.Lock()
	step.Name = fmt.Sprintf("step-%03d-%s-%s", len(p.steps)+1, strings.ToLower(r.Method), sanitizeStepSlug(relPath))
	p.steps = append(p.steps, step)
	p.mu.Unlock()
}

// Flow returns a sanitized replayable Flow snapshot of all recorded API steps.
func (p *RecordProxy) Flow(name string) Flow {
	p.mu.Lock()
	defer p.mu.Unlock()

	copied := make([]Step, len(p.steps))
	for i, s := range p.steps {
		s.Header = maps.Clone(s.Header)
		s.Query = maps.Clone(s.Query)
		s.Fields = maps.Clone(s.Fields)
		if s.Body != nil {
			s.Body = append(json.RawMessage(nil), s.Body...)
		}
		copied[i] = s
	}
	return Flow{Name: name, Steps: copied}
}

// WriteFlow writes the sanitized recorded Flow as formatted JSON to filePath.
func (p *RecordProxy) WriteFlow(filePath, name string) error {
	flow := p.Flow(name)
	data, err := json.MarshalIndent(flow, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal flow %q: %w", name, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return fmt.Errorf("write flow %s: %w", filePath, err)
	}
	return nil
}

func (p *RecordProxy) buildRecordedStep(r *http.Request, relPath string, rawBody []byte, status int) Step {
	step := Step{
		Kind:         "http",
		Method:       r.Method,
		Path:         p.redactText(relPath),
		Role:         inferRoleFromHeader(r.Header.Get("Authorization")),
		ExpectStatus: status,
	}
	if q := r.URL.Query(); len(q) > 0 {
		step.Query = make(map[string]string, len(q))
		for k, vals := range q {
			if len(vals) > 0 {
				step.Query[k] = p.redactText(vals[0])
			}
		}
	}
	for _, k := range []string{"Range", "If-None-Match", "Origin"} {
		if v := r.Header.Get(k); v != "" {
			if step.Header == nil {
				step.Header = make(map[string]string)
			}
			step.Header[k] = p.redactText(v)
		}
	}
	if len(rawBody) > 0 && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if sanitized, ok := p.sanitizeJSONBody(rawBody); ok {
			step.Body = sanitized
		}
	}
	return step
}

func (p *RecordProxy) sanitizeJSONBody(raw []byte) (json.RawMessage, bool) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	redacted := p.redactJSONValue(doc)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, false
	}
	return out, true
}

func (p *RecordProxy) redactJSONValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			if isSecretKey(k) {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = p.redactJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = p.redactJSONValue(item)
		}
		return out
	case string:
		return p.redactText(val)
	default:
		return v
	}
}

func (p *RecordProxy) redactText(s string) string {
	if p.installRoot == "" {
		return s
	}
	out := strings.ReplaceAll(s, p.installRoot, "{{install}}")
	slashRoot := filepath.ToSlash(p.installRoot)
	if slashRoot != p.installRoot {
		out = strings.ReplaceAll(out, slashRoot, "{{install}}")
	}
	return out
}

func inferRoleFromHeader(authHeader string) string {
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return ""
	}
	tok := strings.TrimSpace(authHeader[len("bearer "):])
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return "root"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "root"
	}
	var claims struct {
		Type     string `json:"type"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "root"
	}
	switch {
	case strings.Contains(claims.Username, "listener"):
		return "listener"
	case strings.Contains(claims.Username, "finisher"):
		return "finisher"
	case claims.Type == "root" || strings.Contains(claims.Username, "root"):
		return "root"
	case claims.Username != "":
		return claims.Username
	default:
		return "root"
	}
}

func shouldRecordAPIPath(relPath string) bool {
	if strings.HasPrefix(relPath, "/socket.io") {
		return false
	}
	for _, prefix := range []string{"/api/", "/login", "/logout", "/init", "/status", "/ping", "/healthcheck", "/auth/", "/public/", "/hls/", "/feed/"} {
		if relPath == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	return false
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"password", "secret", "token", "apikey", "smtppass"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return lower == "pass"
}

func sanitizeStepSlug(relPath string) string {
	trimmed := strings.Trim(relPath, "/")
	if trimmed == "" {
		return "root"
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
