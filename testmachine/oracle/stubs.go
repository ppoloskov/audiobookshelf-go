package oracle

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	maxStubBodyBytes = 16 << 20
	smtpConnTimeout  = 30 * time.Second
)

// StubCall records one outbound HTTP or SMTP request made by TS or Go to the stubs.
type StubCall struct {
	Kind   string            // "http" or "smtp"
	Method string            // HTTP method or "DATA" for SMTP messages
	Path   string            // normalized HTTP path (without /ts or /go prefix) or SMTP RCPT TO
	Header map[string]string // flattened HTTP headers or SMTP envelope ("from", "to")
	Body   string            // request body or SMTP DATA payload
}

type stubRoute struct {
	status      int
	contentType string
	body        []byte
}

// StubSet hosts local programmable HTTP and SMTP stubs with separate endpoints
// for TS and Go so outbound calls from each server are tracked independently.
type StubSet struct {
	HTTPURL    string
	SMTPAddr   string
	TSHTTPURL  string
	GoHTTPURL  string
	TSSMTPAddr string
	GoSMTPAddr string

	server *httptest.Server
	tsSMTP net.Listener
	goSMTP net.Listener

	mu      sync.Mutex
	routes  map[string]stubRoute
	tsCalls []StubCall
	goCalls []StubCall
}

// StartStubs starts local HTTP and SMTP stubs and registers t.Cleanup to close them.
func StartStubs(t *testing.T) *StubSet {
	t.Helper()
	s := &StubSet{
		routes: make(map[string]stubRoute),
	}

	s.server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	s.HTTPURL = s.server.URL
	s.TSHTTPURL = s.server.URL + "/ts"
	s.GoHTTPURL = s.server.URL + "/go"

	tsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.server.Close()
		t.Fatalf("listen ts smtp stub: %v", err)
	}
	goLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = tsLn.Close()
		s.server.Close()
		t.Fatalf("listen go smtp stub: %v", err)
	}

	s.tsSMTP = tsLn
	s.goSMTP = goLn
	s.TSSMTPAddr = tsLn.Addr().String()
	s.GoSMTPAddr = goLn.Addr().String()
	s.SMTPAddr = s.TSSMTPAddr

	go s.acceptSMTP("ts", tsLn)
	go s.acceptSMTP("go", goLn)

	t.Cleanup(func() {
		s.server.Close()
		_ = s.tsSMTP.Close()
		_ = s.goSMTP.Close()
	})
	return s
}

// SetRoute configures a programmable HTTP response for path (matched after
// stripping any /ts or /go prefix).
func (s *StubSet) SetRoute(path string, status int, contentType string, body []byte) {
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if status == 0 {
		status = http.StatusOK
	}
	if contentType == "" {
		contentType = "application/json"
	}
	s.mu.Lock()
	s.routes[path] = stubRoute{
		status:      status,
		contentType: contentType,
		body:        slices.Clone(body),
	}
	s.mu.Unlock()
}

// Reset clears recorded stub calls for both TS and Go.
func (s *StubSet) Reset() {
	s.mu.Lock()
	s.tsCalls = nil
	s.goCalls = nil
	s.mu.Unlock()
}

// Calls returns a copy of all recorded calls for side ("ts" or "go").
func (s *StubSet) Calls(side string) []StubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var src []StubCall
	switch strings.ToLower(side) {
	case "ts":
		src = s.tsCalls
	case "go":
		src = s.goCalls
	default:
		return nil
	}
	out := make([]StubCall, len(src))
	for i, c := range src {
		c.Header = maps.Clone(c.Header)
		out[i] = c
	}
	return out
}

func (s *StubSet) record(side string, call StubCall) {
	s.mu.Lock()
	switch side {
	case "ts":
		s.tsCalls = append(s.tsCalls, call)
	case "go":
		s.goCalls = append(s.goCalls, call)
	}
	s.mu.Unlock()
}

func (s *StubSet) serveHTTP(w http.ResponseWriter, r *http.Request) {
	side, trimmed := splitStubSide(r.URL.Path)
	fullPath := trimmed
	if r.URL.RawQuery != "" {
		fullPath = trimmed + "?" + r.URL.RawQuery
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxStubBodyBytes+1))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	if len(raw) > maxStubBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	headers := make(map[string]string, len(r.Header))
	for k, vals := range r.Header {
		headers[k] = strings.Join(vals, ", ")
	}
	if side != "" {
		s.record(side, StubCall{
			Kind:   "http",
			Method: r.Method,
			Path:   fullPath,
			Header: headers,
			Body:   string(raw),
		})
	}

	s.mu.Lock()
	route, ok := s.routes[fullPath]
	if !ok {
		route, ok = s.routes[trimmed]
	}
	if !ok {
		route, ok = s.routes[r.URL.Path]
	}
	s.mu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}")) // client disconnect on response write is ignored
		return
	}
	w.Header().Set("Content-Type", route.contentType)
	w.WriteHeader(route.status)
	_, _ = w.Write(route.body) // client disconnect on response write is ignored
}

func splitStubSide(urlPath string) (side, trimmed string) {
	switch {
	case urlPath == "/ts" || strings.HasPrefix(urlPath, "/ts/"):
		side = "ts"
		trimmed = strings.TrimPrefix(urlPath, "/ts")
	case urlPath == "/go" || strings.HasPrefix(urlPath, "/go/"):
		side = "go"
		trimmed = strings.TrimPrefix(urlPath, "/go")
	default:
		trimmed = urlPath
	}
	if trimmed == "" {
		trimmed = "/"
	}
	return side, trimmed
}

func (s *StubSet) acceptSMTP(side string, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serveSMTP(side, conn)
	}
}

func (s *StubSet) serveSMTP(side string, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(smtpConnTimeout))

	reply := func(line string) bool {
		_, err := fmt.Fprintf(conn, "%s\r\n", line)
		return err == nil
	}
	reader := bufio.NewReader(conn)
	if !reply("220 localhost ESMTP stub") {
		return
	}

	var mailFrom, rcptTo string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		upper := strings.ToUpper(cmd)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if !reply("250-localhost") || !reply("250 AUTH PLAIN LOGIN") {
				return
			}
		case strings.HasPrefix(upper, "HELO"):
			if !reply("250 localhost") {
				return
			}
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if len(strings.Fields(cmd)) < 3 {
				if !reply("334 ") {
					return
				}
				if _, err := reader.ReadString('\n'); err != nil {
					return
				}
			}
			if !reply("235 2.7.0 Authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "AUTH LOGIN"):
			if !reply("334 VXNlcm5hbWU6") {
				return
			}
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
			if !reply("334 UGFzc3dvcmQ6") {
				return
			}
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
			if !reply("235 2.7.0 Authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			mailFrom = extractSMTPAddr(cmd[len("MAIL FROM:"):])
			rcptTo = ""
			if !reply("250 2.1.0 Ok") {
				return
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			rcptTo = extractSMTPAddr(cmd[len("RCPT TO:"):])
			if !reply("250 2.1.5 Ok") {
				return
			}
		case upper == "DATA":
			if !reply("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var body strings.Builder
			for {
				part, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if part == ".\r\n" || part == ".\n" {
					break
				}
				body.WriteString(strings.TrimPrefix(part, "."))
			}
			s.record(side, StubCall{
				Kind:   "smtp",
				Method: "DATA",
				Path:   rcptTo,
				Header: map[string]string{
					"from": mailFrom,
					"to":   rcptTo,
				},
				Body: body.String(),
			})
			if !reply("250 2.0.0 Ok") {
				return
			}
		case upper == "RSET":
			mailFrom = ""
			rcptTo = ""
			if !reply("250 2.0.0 Ok") {
				return
			}
		case upper == "NOOP":
			if !reply("250 2.0.0 Ok") {
				return
			}
		case upper == "STARTTLS":
			if !reply("502 Command not implemented") {
				return
			}
		case upper == "QUIT":
			_ = reply("221 2.0.0 Bye")
			return
		default:
			if !reply("250 2.0.0 Ok") {
				return
			}
		}
	}
}

func extractSMTPAddr(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if start := strings.IndexByte(trimmed, '<'); start >= 0 {
		if end := strings.IndexByte(trimmed[start+1:], '>'); end >= 0 {
			return trimmed[start+1 : start+1+end]
		}
	}
	return strings.Trim(trimmed, "<>")
}
