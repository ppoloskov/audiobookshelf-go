package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxBodyBytes = 16 << 20

type flowDoc struct {
	Name  string     `json:"name"`
	Steps []flowStep `json:"steps"`
}

type flowStep struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind,omitempty"`
	Method       string            `json:"method,omitempty"`
	Path         string            `json:"path,omitempty"`
	Role         string            `json:"role,omitempty"`
	Header       map[string]string `json:"header,omitempty"`
	Query        map[string]string `json:"query,omitempty"`
	Body         json.RawMessage   `json:"body,omitempty"`
	ExpectStatus int               `json:"expect_status,omitempty"`
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

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type recorder struct {
	installRoot string
	basePath    string
	proxy       *httputil.ReverseProxy
	mu          sync.Mutex
	steps       []flowStep
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, rec.basePath)
	if relPath == "" {
		relPath = "/"
	}
	capture := shouldRecordPath(relPath)
	var rawBody []byte
	if capture && r.Body != nil {
		if r.ContentLength > maxBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var err error
		rawBody, err = io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		_ = r.Body.Close() // body drained or oversized
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		if len(rawBody) > maxBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(rawBody))
	}

	sw := &statusRecorder{ResponseWriter: w}
	rec.proxy.ServeHTTP(sw, r)
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	if !capture {
		return
	}

	step := rec.buildStep(r, relPath, rawBody, sw.status)
	rec.mu.Lock()
	step.Name = fmt.Sprintf("step-%03d-%s-%s", len(rec.steps)+1, strings.ToLower(r.Method), sanitizeSlug(relPath))
	rec.steps = append(rec.steps, step)
	rec.mu.Unlock()
	log.Printf("recorded %s %s -> %d (role=%q)", r.Method, relPath, sw.status, step.Role)
}

func (rec *recorder) buildStep(r *http.Request, relPath string, rawBody []byte, status int) flowStep {
	step := flowStep{
		Kind:         "http",
		Method:       r.Method,
		Path:         rec.redactText(relPath),
		Role:         inferRole(r.Header.Get("Authorization")),
		ExpectStatus: status,
	}
	if q := r.URL.Query(); len(q) > 0 {
		step.Query = make(map[string]string, len(q))
		for k, vals := range q {
			if len(vals) > 0 {
				step.Query[k] = rec.redactText(vals[0])
			}
		}
	}
	for _, k := range []string{"Range", "If-None-Match", "Origin", "x-return-tokens"} {
		if v := r.Header.Get(k); v != "" {
			if step.Header == nil {
				step.Header = make(map[string]string)
			}
			step.Header[k] = rec.redactText(v)
		}
	}
	if len(rawBody) > 0 && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var doc any
		if err := json.Unmarshal(rawBody, &doc); err == nil {
			if data, err := json.Marshal(rec.redactJSON(doc)); err == nil {
				step.Body = data
			}
		}
	}
	return step
}

func (rec *recorder) redactJSON(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			if isSecretKey(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = rec.redactJSON(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = rec.redactJSON(item)
		}
		return out
	case string:
		return rec.redactText(val)
	default:
		return v
	}
}

func (rec *recorder) redactText(s string) string {
	if rec.installRoot == "" {
		return s
	}
	out := strings.ReplaceAll(s, rec.installRoot, "{{install}}")
	if slash := filepath.ToSlash(rec.installRoot); slash != rec.installRoot {
		out = strings.ReplaceAll(out, slash, "{{install}}")
	}
	return out
}

func (rec *recorder) writeFlow(outPath, name string) (int, error) {
	rec.mu.Lock()
	steps := append([]flowStep(nil), rec.steps...)
	rec.mu.Unlock()
	data, err := json.MarshalIndent(flowDoc{Name: name, Steps: steps}, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal flow: %w", err)
	}
	if err := os.WriteFile(outPath, append(data, '\n'), 0o600); err != nil {
		return 0, fmt.Errorf("write %s: %w", outPath, err)
	}
	return len(steps), nil
}

func inferRole(authHeader string) string {
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(authHeader[len("bearer "):]), ".")
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

func shouldRecordPath(relPath string) bool {
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

func sanitizeSlug(relPath string) string {
	var b strings.Builder
	for _, r := range strings.Trim(relPath, "/") {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if s := strings.Trim(b.String(), "-"); s != "" {
		return s
	}
	return "root"
}

func run() error {
	listenAddr := flag.String("listen", "127.0.0.1:13379", "proxy listen address")
	upstreamRaw := flag.String("upstream", "http://127.0.0.1:13378", "upstream Audiobookshelf server URL")
	installRoot := flag.String("install", "", "install root path to redact as {{install}}")
	flowName := flag.String("name", "mobile-captured", "recorded flow name")
	outPath := flag.String("out", "captured-flow.json", "output flow JSON file")
	flag.Parse()

	if strings.TrimSpace(*listenAddr) == "" || strings.TrimSpace(*flowName) == "" || strings.TrimSpace(*outPath) == "" {
		return errors.New("listen, name, and out flags must be non-empty")
	}
	upstream, err := url.Parse(*upstreamRaw)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
		return fmt.Errorf("invalid upstream URL %q", *upstreamRaw)
	}
	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listenAddr, err)
	}

	rec := &recorder{
		installRoot: strings.TrimRight(*installRoot, "/"),
		basePath:    strings.TrimRight(upstream.Path, "/"),
		proxy:       httputil.NewSingleHostReverseProxy(upstream),
	}
	srv := &http.Server{Handler: rec, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx) // best-effort graceful shutdown on signal
	}()

	log.Printf("abs-record listening on http://%s -> %s (Ctrl+C to write %s)", ln.Addr().String(), upstream.String(), *outPath)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve proxy: %w", err)
	}
	count, err := rec.writeFlow(*outPath, *flowName)
	if err != nil {
		return err
	}
	log.Printf("wrote %d recorded steps to %s", count, *outPath)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("abs-record: %v", err)
	}
}
