package mail

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// smtpSession is a snapshot of the recorded dialog of one SMTP connection.
type smtpSession struct {
	TLS      bool     // the connection was upgraded with STARTTLS
	Commands []string // every command line as received, verbatim
	From     string   // address of MAIL FROM
	To       []string // addresses of every RCPT TO
	Message  string   // DATA body with dot-unstuffing applied; empty when none was sent
	Auth     string   // decoded AUTH PLAIN credentials, "\x00user\x00pass"
	Done     bool     // the client finished the message exchange (accepted)
}

// smtpServer is a local SMTP stub on 127.0.0.1:0 that records what the server
// under test puts on the wire. It is deliberately small: EHLO/HELO, optional
// STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, RSET, NOOP and QUIT. Implicit TLS on
// the IANA smtps port 465 cannot be exercised: binding a privileged port needs
// root, so the packet proves the port/secure rule with STARTTLS instead.
type smtpServer struct {
	listener net.Listener
	done     chan struct{}
	config   smtpConfig

	mu       sync.Mutex
	sessions []*smtpSession
	conns    []net.Conn
	closed   bool
}

// smtpConfig is read once per connection so a scenario may change it between
// requests without racing the accept goroutine.
type smtpConfig struct {
	greeting  string
	failAt    string
	failReply string
	hangAt    string
	cert      *tls.Certificate // nil: do not advertise STARTTLS
}

// startSMTP starts a plaintext SMTP stub and registers its shutdown.
func startSMTP(t *testing.T) *smtpServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start SMTP stub: %v", err)
	}
	return serveSMTP(t, listener, nil)
}

// startSTARTTLSSMTP starts an SMTP stub that advertises STARTTLS with a
// self-signed certificate for 127.0.0.1.
func startSTARTTLSSMTP(t *testing.T) *smtpServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start STARTTLS SMTP stub: %v", err)
	}
	cert := selfSignedCertificate(t)
	return serveSMTP(t, listener, &cert)
}

func serveSMTP(t *testing.T, listener net.Listener, cert *tls.Certificate) *smtpServer {
	s := &smtpServer{
		listener: listener,
		done:     make(chan struct{}),
		config:   smtpConfig{greeting: "220 localhost ESMTP ready", cert: cert},
	}
	t.Cleanup(s.Close)
	go s.accept()
	return s
}

// Fail makes the named stage answer reply instead of its normal response. The
// stages are "greeting", "ehlo", "mail", "rcpt", "data" and "end" (after the
// terminating dot).
func (s *smtpServer) Fail(stage, reply string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.failAt, s.config.failReply = stage, reply
}

// Hang makes the named stage stop answering until the stub is closed; the
// stages are "greeting" and "end".
func (s *smtpServer) Hang(stage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.hangAt = stage
}

func (s *smtpServer) settings() smtpConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

// Port returns the listening port the server under test must be configured with.
func (s *smtpServer) Port() int { return s.listener.Addr().(*net.TCPAddr).Port }

// Close stops accepting and drops every live connection; it is idempotent.
func (s *smtpServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	conns := slices.Clone(s.conns)
	s.conns = nil
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	_ = s.listener.Close()
}

func (s *smtpServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

// Sessions returns a snapshot of every recorded dialog.
func (s *smtpServer) Sessions() []smtpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]smtpSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		out = append(out, snapshotSession(session))
	}
	return out
}

// Messages returns the snapshots of sessions that carried an accepted message.
func (s *smtpServer) Messages() []smtpSession {
	var out []smtpSession
	for _, session := range s.Sessions() {
		if session.Done && session.Message != "" {
			out = append(out, session)
		}
	}
	return out
}

// Connections reports how many TCP connections the client opened.
func (s *smtpServer) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// snapshotSession copies a session; the caller must hold s.mu.
func snapshotSession(session *smtpSession) smtpSession {
	out := *session
	out.Commands = slices.Clone(session.Commands)
	out.To = slices.Clone(session.To)
	return out
}

// record mutates the session under the server lock.
func (s *smtpServer) record(session *smtpSession, change func(*smtpSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(session)
}

func (s *smtpServer) handle(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	session := &smtpSession{}
	s.mu.Lock()
	s.sessions = append(s.sessions, session)
	s.mu.Unlock()
	config := s.settings()

	conn := raw
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}

	if config.hangAt == "greeting" {
		s.wait()
		return
	}
	if config.failAt == "greeting" {
		_ = write(config.failReply)
		return
	}
	if !write(config.greeting) {
		return
	}
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				if config.failAt == "end" {
					_ = write(config.failReply)
					continue
				}
				if config.hangAt == "end" {
					s.wait()
					return
				}
				s.record(session, func(session *smtpSession) { session.Done = true })
				_ = write("250 2.0.0 queued")
				continue
			}
			// SMTP dot-stuffing: a leading ".." restores one ".".
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			s.record(session, func(session *smtpSession) { session.Message += line + "\n" })
			continue
		}
		s.record(session, func(session *smtpSession) { session.Commands = append(session.Commands, line) })
		verb, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			if config.failAt == "ehlo" {
				_ = write(config.failReply)
				continue
			}
			_ = write("250-localhost")
			_ = write("250-8BITMIME")
			if config.cert != nil && !s.upgraded(session) {
				_ = write("250-STARTTLS")
			}
			_ = write("250-AUTH PLAIN")
			_ = write("250 SIZE 10485760")
		case "HELO":
			_ = write("250 localhost")
		case "STARTTLS":
			if config.cert == nil || s.upgraded(session) {
				_ = write("502 5.5.1 STARTTLS not available")
				continue
			}
			if !write("220 2.0.0 ready to start TLS") {
				return
			}
			tlsConn := tls.Server(conn, &tls.Config{
				Certificates: []tls.Certificate{*config.cert},
				MinVersion:   tls.VersionTLS12,
			})
			if err := tlsConn.Handshake(); err != nil {
				return // the client rejected our self-signed certificate
			}
			conn = tlsConn
			reader = bufio.NewReader(tlsConn)
			writer = bufio.NewWriter(tlsConn)
			s.record(session, func(session *smtpSession) { session.TLS = true })
		case "AUTH":
			method, payload, _ := strings.Cut(rest, " ")
			if strings.ToUpper(method) != "PLAIN" {
				_ = write("504 5.5.4 unsupported authentication mechanism")
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
			if err != nil {
				_ = write("501 5.5.2 cannot decode response")
				continue
			}
			s.record(session, func(session *smtpSession) { session.Auth = string(decoded) })
			_ = write("235 2.7.0 authentication successful")
		case "MAIL":
			address := angleAddress(rest)
			s.record(session, func(session *smtpSession) { session.From = address })
			if config.failAt == "mail" {
				_ = write(config.failReply)
				continue
			}
			_ = write("250 2.1.0 sender ok")
		case "RCPT":
			if address := angleAddress(rest); address != "" {
				s.record(session, func(session *smtpSession) { session.To = append(session.To, address) })
			}
			if config.failAt == "rcpt" {
				_ = write(config.failReply)
				continue
			}
			_ = write("250 2.1.5 recipient ok")
		case "DATA":
			if config.failAt == "data" {
				_ = write(config.failReply)
				continue
			}
			if !write("354 end data with <CR><LF>.<CR><LF>") {
				return
			}
			inData = true
		case "RSET":
			_ = write("250 2.0.0 reset")
		case "NOOP":
			_ = write("250 2.0.0 ok")
		case "QUIT":
			_ = write("221 2.0.0 bye")
			return
		default:
			_ = write("502 5.5.2 command not recognized")
		}
	}
}

// upgraded reports whether the session already negotiated TLS.
func (s *smtpServer) upgraded(session *smtpSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return session.TLS
}

// wait blocks until the stub is closed; a hanging stage deliberately never
// answers so the caller can prove cancellation and timeout behaviour.
func (s *smtpServer) wait() {
	<-s.done
}

// angleAddress extracts the address of "FROM:<addr>" or "TO:<addr>".
func angleAddress(rest string) string {
	open := strings.IndexByte(rest, '<')
	closeAt := strings.LastIndexByte(rest, '>')
	if open < 0 || closeAt <= open {
		return ""
	}
	return rest[open+1 : closeAt]
}

// selfSignedCertificate returns a one-hour self-signed certificate for
// 127.0.0.1; rejectUnauthorized=false is what makes the client accept it.
func selfSignedCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate SMTP test key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create SMTP test certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// waitForMessages waits until the stub recorded want accepted messages.
func waitForMessages(t *testing.T, server *smtpServer, want int) []smtpSession {
	t.Helper()
	harness.Eventually(t, 10*time.Second, "the SMTP message exchange", func() bool {
		return len(server.Messages()) >= want
	})
	return server.Messages()
}

// waitForConnections waits until the stub accepted want connections.
func waitForConnections(t *testing.T, server *smtpServer, want int) {
	t.Helper()
	harness.Eventually(t, 10*time.Second, "the SMTP connection", func() bool {
		return server.Connections() >= want
	})
}

// wantNoSMTPConnection fails when the stub received any connection.
func wantNoSMTPConnection(t *testing.T, server *smtpServer, what string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond) // give a late dial a chance to appear
	if got := server.Connections(); got != 0 {
		t.Fatalf("%s: SMTP stub received %d connections, want none", what, got)
	}
}
