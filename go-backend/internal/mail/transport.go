package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/ids"
)

const (
	dialTimeout      = 10 * time.Second // TCP connect plus TLS handshake
	handshakeTimeout = 30 * time.Second // greeting, STARTTLS, AUTH and envelope
	sessionTimeout   = 10 * time.Minute // the whole session, which carries the attachment
)

// server is how to reach and log in to an SMTP server.
type server struct {
	Host      string
	Port      int
	Implicit  bool // TLS from the first byte instead of STARTTLS when offered
	VerifyTLS bool
	User      string
	Pass      string
}

// message is a plain-text mail, or an empty HTML mail with one attachment that
// is streamed from disk.
type message struct {
	From, To, Subject string
	Body              string // text/plain body, used when AttachPath is empty
	AttachPath        string
	AttachName        string // file name shown to the recipient
}

// connectError means the session failed before any message was sent (connect,
// STARTTLS or login). Handlers answer it with the "verify" message, as the
// upstream transporter.verify() step does.
type connectError struct{ err error }

func (e *connectError) Error() string { return e.err.Error() }

// send delivers one message in one SMTP session. Addresses are checked before
// any network I/O; an address with a line break is rejected by net/mail.
func send(ctx context.Context, s server, m message) error {
	if s.Host == "" {
		return &connectError{errors.New("SMTP host is required")}
	}
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("invalid recipient address: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx) // cancelling the request closes the connection
	defer cancel()
	client, conn, err := s.dial(ctx)
	if err != nil {
		return &connectError{err}
	}
	defer client.Close()
	_ = conn.SetDeadline(time.Now().Add(sessionTimeout)) // the attachment may be large; a failure shows as a write error
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	body, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := m.writeTo(body, from, to); err != nil {
		_ = body.Close() // the write error is the one to report
		return fmt.Errorf("write message: %w", err)
	}
	if err := body.Close(); err != nil {
		return fmt.Errorf("end of data: %w", err)
	}
	_ = client.Quit() // the message is accepted; a failed goodbye changes nothing
	return nil
}

// dial connects, upgrades to TLS where configured or offered, and logs in.
func (s server) dial(ctx context.Context) (*smtp.Client, net.Conn, error) {
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: !s.VerifyTLS} //nolint:gosec // only when the admin turned rejectUnauthorized off
	if !s.VerifyTLS {
		slog.Warn("SMTP certificate verification is disabled", "host", s.Host)
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dial := (&net.Dialer{}).DialContext
	if s.Implicit {
		dial = (&tls.Dialer{Config: tlsConfig}).DialContext
	}
	conn, err := dial(dialCtx, "tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", address, err)
	}
	context.AfterFunc(ctx, func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout)) // a failure here surfaces as the next read error
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("greeting: %w", err)
	}
	// Like nodemailer: unless the connection is implicit TLS, upgrade whenever
	// the server offers STARTTLS. PlainAuth refuses to send the password over
	// an unencrypted connection to a non-local host.
	if ok, _ := client.Extension("STARTTLS"); ok && !s.Implicit {
		err = client.StartTLS(tlsConfig)
	}
	if err == nil && s.User != "" {
		err = client.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host))
	}
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("login: %w", err)
	}
	return client, conn, nil
}

// writeTo writes the message in SMTP DATA form.
func (m message) writeTo(w io.Writer, from, to *mail.Address) error {
	_, domain, _ := strings.Cut(from.Address, "@")
	fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\n",
		from, to, mime.QEncoding.Encode("utf-8", m.Subject), time.Now().Format(time.RFC1123Z), ids.Must(), domain)
	if m.AttachPath == "" {
		_, err := io.WriteString(w, "Content-Type: text/plain; charset=utf-8\r\n\r\n"+m.Body)
		return err
	}
	parts := multipart.NewWriter(w)
	fmt.Fprintf(w, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": parts.Boundary()}))
	html, err := parts.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=utf-8"}})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(html, `<div dir="auto"></div>`); err != nil {
		return err
	}
	name := filepath.Base(m.AttachName)
	// FormatMediaType encodes a non-ASCII name as RFC 2231 filename*=utf-8''...
	part, err := parts.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {mime.FormatMediaType("application/octet-stream", map[string]string{"name": name})},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
	})
	if err != nil {
		return err
	}
	file, err := os.Open(m.AttachPath)
	if err != nil {
		return err
	}
	defer file.Close()
	// 57 bytes encode to one 76-character base64 line.
	raw, line := make([]byte, 57), make([]byte, 78)
	for {
		n, err := io.ReadFull(file, raw)
		if n > 0 {
			encoded := base64.StdEncoding.EncodedLen(n)
			base64.StdEncoding.Encode(line, raw[:n])
			copy(line[encoded:], "\r\n")
			if _, werr := part.Write(line[:encoded+2]); werr != nil {
				return werr
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return parts.Close()
		} else if err != nil {
			return err
		}
	}
}
